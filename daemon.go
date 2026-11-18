// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package ra

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/mdlayher/ndp"
)

// Daemon is the main struct for the ra daemon
type Daemon struct {
	initialConfig     *Config
	reloadCh          chan *Config
	logger            *slog.Logger
	socketConstructor socketCtor
	deviceWatcher     deviceWatcher

	advertisers     map[int]*advertiser
	advertisersLock sync.RWMutex

	currentConfig   *Config
	currentConfigMu sync.RWMutex

	// IDs of the instances to be removed without a goodbye RA on the next
	// reload. Protected by advertisersLock.
	silentDeletes map[int]struct{}
}

// NewDaemon creates a new Daemon instance with the provided configuration and
// options. It returns ValidationErrors if the configuration is invalid.
func NewDaemon(config *Config, opts ...DaemonOption) (*Daemon, error) {
	// Take a copy of the new configuration. c.validate() will modify it to
	// set default values.
	c := config.deepCopy()

	// Validate the configuration first
	if err := c.defaultAndValidate(); err != nil {
		return nil, err
	}

	d := &Daemon{
		initialConfig:     c,
		reloadCh:          make(chan *Config),
		logger:            slog.Default(),
		socketConstructor: newSocket,
		deviceWatcher:     newDeviceWatcher(),
		advertisers:       map[int]*advertiser{},
		silentDeletes:     map[int]struct{}{},
		currentConfig:     c,
	}

	for _, opt := range opts {
		opt(d)
	}

	return d, nil
}

// Run starts the daemon and blocks until the context is cancelled
func (d *Daemon) Run(ctx context.Context) {
	d.logger.Info("Starting daemon")

	// Current desired configuration
	config := d.initialConfig

reload:
	// Main loop
	for {
		var (
			toAdd    []*InterfaceConfig
			toUpdate []*advertiser
			toRemove []*advertiser
		)

		// We may modify the advertiser map from now
		d.advertisersLock.Lock()

		// Cache the ID => config mapping for later use
		ifaceConfigs := map[int]*InterfaceConfig{}

		// Find out which advertiser to add, update and remove
		for _, c := range config.Interfaces {
			if advertiser, ok := d.advertisers[c.ID]; !ok {
				toAdd = append(toAdd, c)
			} else {
				toUpdate = append(toUpdate, advertiser)
			}
			ifaceConfigs[c.ID] = c
		}
		for id, advertiser := range d.advertisers {
			if _, ok := ifaceConfigs[id]; !ok {
				toRemove = append(toRemove, advertiser)
			}
		}

		// Add new per-interface jobs
		for _, c := range toAdd {
			d.logger.Info("Adding new RA sender", slog.Int("id", c.ID), slog.String("interface", c.Name))
			advertiser := newAdvertiser(c, d.socketConstructor, d.deviceWatcher, d.logger)
			go advertiser.run(ctx)
			d.advertisers[c.ID] = advertiser
		}

		// Update (reload) existing workers
		for _, advertiser := range toUpdate {
			id := advertiser.initialConfig.ID
			d.logger.Info("Updating RA sender", slog.Int("id", id), slog.String("interface", advertiser.initialConfig.Name))
			// Set timeout to guarantee progress
			timeout, cancelTimeout := context.WithTimeout(ctx, time.Second*3)
			advertiser.reload(timeout, ifaceConfigs[id])
			cancelTimeout()
		}

		// Remove unnecessary workers
		for _, advertiser := range toRemove {
			id := advertiser.initialConfig.ID
			d.logger.Info("Deleting RA sender", slog.Int("id", id), slog.String("interface", advertiser.initialConfig.Name))
			_, silent := d.silentDeletes[id]
			advertiser.stop(silent)
			delete(d.advertisers, id)
			delete(d.silentDeletes, id)
		}

		d.advertisersLock.Unlock()

		// Wait for the events
		for {
			select {
			case newConfig := <-d.reloadCh:
				d.logger.Info("Reloading configuration")
				config = newConfig
				d.currentConfigMu.Lock()
				d.currentConfig = newConfig
				d.currentConfigMu.Unlock()
				continue reload
			case <-ctx.Done():
				d.logger.Info("Shutting down daemon")
				return
			}
		}
	}
}

// Reload reloads the configuration of the daemon. The context passed to this
// function is used to cancel the potentially long-running operations during
// the reload process. Currently, the result of the unsucecssful or cancelled
// reload is undefined and the daemon may be running with either the old or the
// new configuration or both. It returns ValidationErrors if the configuration
// is invalid.
func (d *Daemon) Reload(ctx context.Context, newConfig *Config) error {
	// Take a copy of the new configuration. c.validate() will modify it to
	// set default values.
	c := newConfig.deepCopy()

	if err := c.defaultAndValidate(); err != nil {
		return err
	}

	select {
	case d.reloadCh <- c:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// AddInterface adds a new interface configuration to the daemon. It returns
// ValidationErrors if the resulting configuration is invalid.
func (d *Daemon) AddInterface(ctx context.Context, ifaceConfig *InterfaceConfig) error {
	d.currentConfigMu.RLock()
	newConfig := d.currentConfig.deepCopy()
	d.currentConfigMu.RUnlock()

	newConfig.Interfaces = append(newConfig.Interfaces, ifaceConfig)
	return d.Reload(ctx, newConfig)
}

// UpdateInterface replaces the configuration of an existing interface. It
// returns an error if no interface with the given ID exists, or ValidationErrors
// if the resulting configuration is invalid.
func (d *Daemon) UpdateInterface(ctx context.Context, ifaceConfig *InterfaceConfig) error {
	d.currentConfigMu.RLock()
	newConfig := d.currentConfig.deepCopy()
	d.currentConfigMu.RUnlock()

	found := false
	for i, iface := range newConfig.Interfaces {
		if iface.ID == ifaceConfig.ID {
			newConfig.Interfaces[i] = ifaceConfig
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("interface with id %d not found", ifaceConfig.ID)
	}
	return d.Reload(ctx, newConfig)
}

// DeleteOptions are the options of DeleteInterfaceWithOptions.
type DeleteOptions struct {
	// SkipGoodbye removes the instance without sending a goodbye RA, even
	// if its SendGoodbye is true. Use it when the caller withdraws the
	// advertised state itself (see SendRA), for example because some of the
	// clients are now served by another instance.
	SkipGoodbye bool
}

// DeleteInterface removes the interface configuration with the given ID from
// the daemon. It returns an error if no interface with the given ID exists.
func (d *Daemon) DeleteInterface(ctx context.Context, id int) error {
	return d.DeleteInterfaceWithOptions(ctx, id, DeleteOptions{})
}

// DeleteInterfaceWithOptions is DeleteInterface with options.
func (d *Daemon) DeleteInterfaceWithOptions(ctx context.Context, id int, opts DeleteOptions) error {
	d.currentConfigMu.RLock()
	newConfig := d.currentConfig.deepCopy()
	d.currentConfigMu.RUnlock()

	found := false
	interfaces := make([]*InterfaceConfig, 0, len(newConfig.Interfaces))
	for _, iface := range newConfig.Interfaces {
		if iface.ID == id {
			found = true
			continue
		}
		interfaces = append(interfaces, iface)
	}
	if !found {
		return fmt.Errorf("interface with id %d not found", id)
	}
	newConfig.Interfaces = interfaces

	d.advertisersLock.Lock()
	adv := d.advertisers[id]
	if opts.SkipGoodbye {
		d.silentDeletes[id] = struct{}{}
	}
	d.advertisersLock.Unlock()

	if err := d.Reload(ctx, newConfig); err != nil {
		d.advertisersLock.Lock()
		delete(d.silentDeletes, id)
		d.advertisersLock.Unlock()
		return err
	}

	// The reload is applied asynchronously. Wait until the instance has
	// really stopped, so that a caller that withdraws its state right after
	// this call cannot be overtaken by one more periodic RA.
	if adv != nil {
		select {
		case <-adv.stopped:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// SendRA sends a single RA built from ifaceConfig to its clients (or to the
// all-nodes multicast address if it has none) without installing the
// configuration. For every prefix in withdrawnRoutes that is not among the
// routes of ifaceConfig, a Route Information Option with a zero lifetime is
// appended, which makes the receivers remove that route.
//
// It is meant for withdrawing state that a previous configuration installed
// on specific hosts: RAs are soft state, so without an explicit withdrawal a
// host keeps a route or a default router until its lifetime expires. It
// returns ValidationErrors if the configuration is invalid.
func (d *Daemon) SendRA(ctx context.Context, ifaceConfig *InterfaceConfig, withdrawnRoutes []string) error {
	c := &Config{Interfaces: []*InterfaceConfig{ifaceConfig.deepCopy()}}
	if err := c.defaultAndValidate(); err != nil {
		return err
	}
	config := c.Interfaces[0]

	advertised := map[netip.Prefix]struct{}{}
	for _, route := range config.Routes {
		advertised[netip.MustParsePrefix(route.Prefix).Masked()] = struct{}{}
	}
	withdrawn := []netip.Prefix{}
	for _, r := range withdrawnRoutes {
		p, err := netip.ParsePrefix(r)
		if err != nil || !p.Addr().Is6() || p.Addr().Is4In6() {
			return fmt.Errorf("invalid withdrawn route %q: must be an IPv6 prefix", r)
		}
		p = p.Masked()
		if _, ok := advertised[p]; ok {
			continue
		}
		advertised[p] = struct{}{}
		withdrawn = append(withdrawn, p)
	}

	sock, err := d.socketConstructor(config.Name)
	if err != nil {
		return fmt.Errorf("cannot create socket on %s: %w", config.Name, err)
	}
	defer sock.close()

	msg := buildRAMsg(config, sock.hardwareAddr())
	for _, p := range withdrawn {
		msg.Options = append(msg.Options, &ndp.RouteInformation{
			PrefixLength:  uint8(p.Bits()),
			Preference:    ndp.Medium,
			RouteLifetime: 0,
			Prefix:        p.Addr(),
		})
	}

	src := raSource(config)
	var errs []error
	for _, dst := range raTargets(config) {
		if err := sock.sendRA(ctx, src, dst, msg); err != nil {
			errs = append(errs, fmt.Errorf("send RA to %s: %w", dst, err))
		}
	}
	return errors.Join(errs...)
}

// Interfaces returns a snapshot of the currently active interface configurations.
func (d *Daemon) Interfaces() []*InterfaceConfig {
	d.currentConfigMu.RLock()
	defer d.currentConfigMu.RUnlock()
	result := make([]*InterfaceConfig, len(d.currentConfig.Interfaces))
	for i, iface := range d.currentConfig.Interfaces {
		result[i] = iface.deepCopy()
	}
	return result
}

// Status returns the current status of the daemon
func (d *Daemon) Status() *Status {
	d.advertisersLock.RLock()

	ifaceStatus := []*InterfaceStatus{}
	for _, advertiser := range d.advertisers {
		ifaceStatus = append(ifaceStatus, advertiser.status())
	}

	d.advertisersLock.RUnlock()

	sort.Slice(ifaceStatus, func(i, j int) bool {
		return ifaceStatus[i].ID < ifaceStatus[j].ID
	})

	return &Status{Interfaces: ifaceStatus}
}

// DaemonOption is an optional parameter for the Daemon constructor
type DaemonOption func(*Daemon)

// WithLogger overrides the default logger with the provided one.
func WithLogger(l *slog.Logger) DaemonOption {
	return func(d *Daemon) {
		d.logger = l
	}
}

// withSocketConstructor overrides the default socket constructor with the
// provided one. For testing purposes only.
func withSocketConstructor(c socketCtor) DaemonOption {
	return func(d *Daemon) {
		d.socketConstructor = c
	}
}

// withDeviceWatcher overrides the default device watcher with the provided
// one. For testing purposes only.
func withDeviceWatcher(w deviceWatcher) DaemonOption {
	return func(d *Daemon) {
		d.deviceWatcher = w
	}
}
