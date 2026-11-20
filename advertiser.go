// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package ra

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdlayher/ndp"
	"golang.org/x/sys/unix"
)

type advertiser struct {
	logger *slog.Logger

	initialConfig *InterfaceConfig

	// We use mutex-based synchronization instead of channels because
	// ifaceStatus must be reported even when the main loop is hanging.
	ifaceStatus     *InterfaceStatus
	ifaceStatusLock sync.RWMutex

	reloadCh chan *InterfaceConfig
	stopCh   chan any
	// skipGoodbye suppresses the goodbye RA when the instance is stopped
	// by stop(). It doesn't affect the daemon shutdown.
	skipGoodbye atomic.Bool
	// stopped is closed when run returns. After that the instance sends
	// no more RAs.
	stopped       chan struct{}
	socketCtor    socketCtor
	deviceWatcher deviceWatcher
}

// An internal structure to represent RS
type rsMsg struct {
	rs   *ndp.RouterSolicitation
	from netip.Addr
}

func newAdvertiser(initialConfig *InterfaceConfig, ctor socketCtor, devWatcher deviceWatcher, logger *slog.Logger) *advertiser {
	return &advertiser{
		logger:        logger.With(slog.Int("id", initialConfig.ID), slog.String("interface", initialConfig.Name)),
		initialConfig: initialConfig,
		ifaceStatus:   &InterfaceStatus{ID: initialConfig.ID, Name: initialConfig.Name, State: "Unknown"},
		reloadCh:      make(chan *InterfaceConfig),
		stopCh:        make(chan any),
		stopped:       make(chan struct{}),
		socketCtor:    ctor,
		deviceWatcher: devWatcher,
	}
}

func (s *advertiser) createRAMsg(config *InterfaceConfig, deviceState *deviceState) *ndp.RouterAdvertisement {
	return buildRAMsg(config, deviceState.addr)
}

// buildRAMsg builds the RA message advertised for config. hwAddr is put in
// the Source Link-Layer Address option. The configuration must have been
// validated.
func buildRAMsg(config *InterfaceConfig, hwAddr net.HardwareAddr) *ndp.RouterAdvertisement {
	return &ndp.RouterAdvertisement{
		CurrentHopLimit:           uint8(config.CurrentHopLimit),
		ManagedConfiguration:      config.Managed,
		OtherConfiguration:        config.Other,
		RouterSelectionPreference: toNDPPreference(config.Preference),
		RouterLifetime:            time.Duration(config.RouterLifetimeSeconds) * time.Second,
		ReachableTime:             time.Duration(config.ReachableTimeMilliseconds) * time.Millisecond,
		RetransmitTimer:           time.Duration(config.RetransmitTimeMilliseconds) * time.Millisecond,
		Options:                   buildOptions(config, hwAddr),
	}
}

// raTargets returns the destinations of the unsolicited RAs of config: its
// clients, or the all-nodes multicast address if it has none.
func raTargets(config *InterfaceConfig) []netip.Addr {
	if len(config.Clients) == 0 {
		return []netip.Addr{netip.IPv6LinkLocalAllNodes()}
	}
	targets := make([]netip.Addr, 0, len(config.Clients))
	for _, client := range config.Clients {
		// Validated already. An invalid address here is a bug.
		targets = append(targets, netip.MustParseAddr(client))
	}
	return targets
}

// raSource returns the configured source address of config, or the zero
// Addr if the interface's primary link-local address should be used.
func raSource(config *InterfaceConfig) netip.Addr {
	if config.SourceAddress == "" {
		return netip.Addr{}
	}
	// Validated already. An invalid address here is a bug.
	return netip.MustParseAddr(config.SourceAddress)
}

func buildOptions(config *InterfaceConfig, hwAddr net.HardwareAddr) []ndp.Option {
	options := []ndp.Option{
		&ndp.LinkLayerAddress{
			Direction: ndp.Source,
			Addr:      hwAddr,
		},
	}

	if config.MTU > 0 {
		options = append(options, &ndp.MTU{
			MTU: uint32(config.MTU),
		})
	}

	for _, prefix := range config.Prefixes {
		// At this point, we should have validated the
		// configuration. If we haven't, it's a bug.
		p := netip.MustParsePrefix(prefix.Prefix)
		options = append(options, &ndp.PrefixInformation{
			PrefixLength:                   uint8(p.Bits()),
			OnLink:                         prefix.OnLink,
			AutonomousAddressConfiguration: prefix.Autonomous,
			ValidLifetime:                  time.Second * time.Duration(*prefix.ValidLifetimeSeconds),
			PreferredLifetime:              time.Second * time.Duration(*prefix.PreferredLifetimeSeconds),
			Prefix:                         p.Addr(),
		})
	}

	for _, route := range config.Routes {
		// At this point, we should have validated the
		// configuration. If we haven't, it's a bug.
		p := netip.MustParsePrefix(route.Prefix)
		options = append(options, routeOption(p, toNDPPreference(route.Preference),
			time.Second*time.Duration(route.LifetimeSeconds), route.OptionLength))
	}

	for _, rdnss := range config.RDNSSes {
		addresses := []netip.Addr{}
		for _, addr := range rdnss.Addresses {
			// At this point, we should have validated the
			// configuration. If we haven't, it's a bug.
			addresses = append(addresses, netip.MustParseAddr(addr))
		}
		options = append(options, &ndp.RecursiveDNSServer{
			Lifetime: time.Second * time.Duration(rdnss.LifetimeSeconds),
			Servers:  addresses,
		})
	}

	for _, dnssl := range config.DNSSLs {
		options = append(options, &ndp.DNSSearchList{
			Lifetime:    time.Second * time.Duration(dnssl.LifetimeSeconds),
			DomainNames: dnssl.DomainNames,
		})
	}

	for _, nat64prefix := range config.NAT64Prefixes {
		options = append(options, &ndp.PREF64{
			Lifetime: time.Second * time.Duration(*nat64prefix.LifetimeSeconds),
			Prefix:   netip.MustParsePrefix(nat64prefix.Prefix),
		})
	}

	return options
}

// optRouteInformation is the type of the Route Information Option (RFC 4191).
const optRouteInformation = 24

// routeOption builds a Route Information Option. optionLength is the Length
// field in units of 8 octets; 0 selects the shortest length that fits the
// prefix, which is what ndp.RouteInformation encodes. A longer option is
// built as a raw option, with the unused prefix octets set to zero.
//
// The caller must have validated that optionLength fits the prefix.
func routeOption(p netip.Prefix, preference ndp.Preference, lifetime time.Duration, optionLength int) ndp.Option {
	if optionLength <= minRouteOptionLength(p.Bits()) {
		return &ndp.RouteInformation{
			PrefixLength:  uint8(p.Bits()),
			Preference:    preference,
			RouteLifetime: lifetime,
			Prefix:        p.Addr(),
		}
	}

	// Prefix length, flags, route lifetime, then (Length-1)*8 prefix octets.
	value := make([]byte, 6+(optionLength-1)*8)
	value[0] = uint8(p.Bits())
	value[1] = uint8(preference) << 3
	binary.BigEndian.PutUint32(value[2:6], uint32(lifetime.Seconds()))
	copy(value[6:], p.Masked().Addr().AsSlice())
	return &ndp.RawOption{Type: optRouteInformation, Length: uint8(optionLength), Value: value}
}

// toNDPPreference converts a validated preference string. Unknown values
// map to medium.
func toNDPPreference(preference string) ndp.Preference {
	switch preference {
	case "low":
		return ndp.Low
	case "high":
		return ndp.High
	default:
		return ndp.Medium
	}
}

func (s *advertiser) reportRunning() {
	s.ifaceStatusLock.Lock()
	defer s.ifaceStatusLock.Unlock()
	s.ifaceStatus.State = Running
	s.ifaceStatus.Message = ""
}

func (s *advertiser) reportReloading() {
	s.ifaceStatusLock.Lock()
	defer s.ifaceStatusLock.Unlock()
	s.ifaceStatus.State = Reloading
	s.ifaceStatus.Message = ""
}

func (s *advertiser) reportFailing(err error) {
	s.ifaceStatusLock.Lock()
	defer s.ifaceStatusLock.Unlock()
	s.ifaceStatus.State = Failing
	if err == nil {
		s.ifaceStatus.Message = ""
	} else {
		s.ifaceStatus.Message = err.Error()
	}
}

func (s *advertiser) reportStopped(err error) {
	s.ifaceStatusLock.Lock()
	defer s.ifaceStatusLock.Unlock()
	s.ifaceStatus.State = Stopped
	if err == nil {
		s.ifaceStatus.Message = ""
	} else {
		s.ifaceStatus.Message = err.Error()
	}
}

func (s *advertiser) incTxStat(solicited bool) {
	s.ifaceStatusLock.Lock()
	defer s.ifaceStatusLock.Unlock()
	if solicited {
		s.ifaceStatus.TxSolicitedRA++
	} else {
		s.ifaceStatus.TxUnsolicitedRA++
	}
}

func (s *advertiser) setLastUpdate() {
	s.ifaceStatusLock.Lock()
	defer s.ifaceStatusLock.Unlock()
	s.ifaceStatus.LastUpdate = time.Now().Unix()
}

func (s *advertiser) run(ctx context.Context) {
	defer close(s.stopped)

	// The current desired configuration
	config := s.initialConfig

	// The current device state
	devState := deviceState{}

	// Set a timestamp for the first "update"
	s.setLastUpdate()

	// Watch the device state
	devCh, err := s.deviceWatcher.watch(ctx, config.Name)
	if err != nil {
		s.reportStopped(err)
		return
	}

waitDevice:
	// Wait for the device to be present and up and the addresses are assigned
	for {
		select {
		case <-ctx.Done():
			s.reportStopped(ctx.Err())
			return
		case dev := <-devCh:
			// Update the device state
			devState = dev

			// If the device is up, mac and link-local address are
			// assigned, we can proceed with the socket creation
			if dev.isUp || len(dev.addr) > 0 || dev.v6LLAddrAssigned {
				break waitDevice
			}
		}
	}

	// Create the socket
	sock, err := s.socketCtor(config.Name)
	if err != nil {
		// These are the unrecoverable errors we're aware of now.
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EINVAL) {
			s.reportStopped(fmt.Errorf("cannot create socket: %w", err))
			return
		}
		// Otherwise, we'll retry
		s.reportFailing(err)
		goto waitDevice
	}

	// Launch the RS receiver
	rsCh := make(chan *rsMsg)
	receiverCtx, cancelReceiver := context.WithCancel(ctx)
	go func() {
		for {
			rs, addr, err := sock.recvRS(receiverCtx)
			if err != nil {
				if receiverCtx.Err() != nil {
					return
				}
				s.reportFailing(err)
				continue
			}
			rsCh <- &rsMsg{rs: rs, from: addr}
		}
	}()

	s.reportRunning()

reload:
	for {
		// RA message
		msg := s.createRAMsg(config, &devState)

		// For unsolicited RA
		ticker := time.NewTicker(time.Duration(config.RAIntervalMilliseconds) * time.Millisecond)

		// Pre-parse the source and target addresses once per config load
		src := raSource(config)
		targets := raTargets(config)

		// sendUnsolicitedRA sends the current RA to every target.
		sendUnsolicitedRA := func() {
			failed := false
			for _, addr := range targets {
				if err := sock.sendRA(ctx, src, addr, msg); err != nil {
					s.reportFailing(err)
					failed = true
					continue
				}
				s.incTxStat(false)
			}
			if !failed {
				s.reportRunning()
			}
		}

		// A unicast instance does not rely on RS to deliver its content
		// quickly, so advertise right after every (re)load instead of
		// waiting for the first interval to elapse.
		if len(config.Clients) > 0 {
			sendUnsolicitedRA()
		}

		// sendGoodbyeRA sends a final RA with RouterLifetime=0 and all option
		// lifetimes zeroed to notify hosts that this router is no longer available.
		// Uses a fresh context because the original ctx may already be cancelled at call time.
		sendGoodbyeRA := func() {
			goodbyeMsg := *msg
			goodbyeMsg.RouterLifetime = 0
			// RFC 4191 Section 2.2: the preference must be medium when the
			// Router Lifetime is zero.
			goodbyeMsg.RouterSelectionPreference = ndp.Medium
			goodbyeMsg.Options = zeroOptionLifetimes(msg.Options)
			sendCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for _, addr := range targets {
				if err := sock.sendRA(sendCtx, src, addr, &goodbyeMsg); err != nil {
					s.logger.Error("Failed to send goodbye RA", "error", err)
				}
			}
		}

		for {
			select {
			case rs := <-rsCh:
				if config.DisableRSReply {
					continue
				}
				// Reply to RS
				//
				// TODO: Rate limit this to mitigate RS flooding attack
				err := sock.sendRA(ctx, src, rs.from, msg)
				if err != nil {
					s.reportFailing(err)
					continue
				}
				s.incTxStat(true)
				s.reportRunning()
			case <-ticker.C:
				sendUnsolicitedRA()
			case newConfig := <-s.reloadCh:
				if reflect.DeepEqual(config, newConfig) {
					s.logger.Info("No configuration change. Skip reloading.")
					continue
				}
				config = newConfig
				s.reportReloading()
				s.setLastUpdate()
				continue reload
			case dev := <-devCh:
				// Save the old address for comparison
				oldAddr := devState.addr

				// Update the device state
				devState = dev

				// Device is stopped. Stop the advertisement
				// and wait for the device to be up again.
				if !devState.isUp {
					cancelReceiver()
					s.reportFailing(fmt.Errorf("device is down"))
					goto waitDevice
				}

				// Device address has changed. We need to
				// change the Link Layer Address option in the
				// RA message. Reload internally.
				if !slices.Equal(oldAddr, dev.addr) {
					s.reportReloading()
					continue reload
				}
			case <-ctx.Done():
				if *config.SendGoodbye {
					sendGoodbyeRA()
				}
				s.reportStopped(ctx.Err())
				break reload
			case <-s.stopCh:
				if *config.SendGoodbye && !s.skipGoodbye.Load() {
					sendGoodbyeRA()
				}
				s.reportStopped(nil)
				break reload
			}
		}
	}

	cancelReceiver()
	sock.close()
}

func (s *advertiser) status() *InterfaceStatus {
	s.ifaceStatusLock.RLock()
	defer s.ifaceStatusLock.RUnlock()
	return s.ifaceStatus.deepCopy()
}

func (s *advertiser) reload(ctx context.Context, newConfig *InterfaceConfig) error {
	select {
	case s.reloadCh <- newConfig:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// stop stops the advertiser. If skipGoodbye is true, no goodbye RA is sent
// regardless of the SendGoodbye configuration.
func (s *advertiser) stop(skipGoodbye bool) {
	s.skipGoodbye.Store(skipGoodbye)
	close(s.stopCh)
}

// zeroOptionLifetimes returns a copy of opts with all lifetime fields set to 0.
func zeroOptionLifetimes(opts []ndp.Option) []ndp.Option {
	result := make([]ndp.Option, len(opts))
	for i, opt := range opts {
		switch o := opt.(type) {
		// case *ndp.PrefixInformation:
		// 	p := *o
		// 	p.ValidLifetime = 0
		// 	p.PreferredLifetime = 0
		// 	result[i] = &p
		case *ndp.RouteInformation:
			r := *o
			r.RouteLifetime = 0
			result[i] = &r
		case *ndp.RawOption:
			// A Route Information Option with a configured length.
			r := *o
			r.Value = append([]byte(nil), o.Value...)
			if r.Type == optRouteInformation && len(r.Value) >= 6 {
				binary.BigEndian.PutUint32(r.Value[2:6], 0)
			}
			result[i] = &r
		case *ndp.RecursiveDNSServer:
			s := *o
			s.Lifetime = 0
			result[i] = &s
		case *ndp.DNSSearchList:
			d := *o
			d.Lifetime = 0
			result[i] = &d
		case *ndp.PREF64:
			p := *o
			p.Lifetime = 0
			result[i] = &p
		default:
			result[i] = opt
		}
	}
	return result
}
