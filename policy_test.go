// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package ra

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"github.com/stretchr/testify/require"
)

// newPolicyTestDaemon starts a daemon with a single unicast instance on net0
// and returns it with the fake socket of the instance.
func newPolicyTestDaemon(t *testing.T, iface *InterfaceConfig) (*Daemon, *fakeSock) {
	t.Helper()

	reg := newFakeSockRegistry()
	devWatcher := newFakeDeviceWatcher("net0")
	devWatcher.update("net0", deviceState{isUp: true, addr: net.HardwareAddr{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}})

	d, err := NewDaemon(
		&Config{Interfaces: []*InterfaceConfig{iface}},
		withSocketConstructor(reg.newSock),
		withDeviceWatcher(devWatcher),
	)
	require.NoError(t, err)

	go d.Run(t.Context())

	var sock *fakeSock
	eventully(t, func() bool {
		sock, err = reg.getSock("net0")
		return err == nil
	})
	return d, sock
}

func nextUnicastRA(t *testing.T, sock *fakeSock, within time.Duration) fakeRA {
	t.Helper()
	select {
	case ra := <-sock.txLLUnicastCh():
		return ra
	case <-time.After(within):
		t.Fatalf("no unicast RA within %s", within)
		return fakeRA{}
	}
}

func drainUnicastRAs(sock *fakeSock) []fakeRA {
	ras := []fakeRA{}
	for {
		select {
		case ra := <-sock.txLLUnicastCh():
			ras = append(ras, ra)
		default:
			return ras
		}
	}
}

func routeLifetimes(msg *ndp.RouterAdvertisement) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, opt := range msg.Options {
		if ri, ok := opt.(*ndp.RouteInformation); ok {
			out[netip.PrefixFrom(ri.Prefix, int(ri.PrefixLength)).String()] = ri.RouteLifetime
		}
	}
	return out
}

func TestSourceAddress(t *testing.T) {
	t.Run("Configured source address is used for unicast RAs", func(t *testing.T) {
		_, sock := newPolicyTestDaemon(t, &InterfaceConfig{
			ID:                     1,
			Name:                   "net0",
			RAIntervalMilliseconds: 100,
			RouterLifetimeSeconds:  10,
			SourceAddress:          "fe80::face:1",
			Clients:                []string{"fe80::1"},
		})
		ra := nextUnicastRA(t, sock, time.Second)
		require.Equal(t, netip.MustParseAddr("fe80::face:1"), ra.from)
		require.Equal(t, netip.MustParseAddr("fe80::1"), ra.to)
	})

	t.Run("Without a source address the primary address is used", func(t *testing.T) {
		_, sock := newPolicyTestDaemon(t, &InterfaceConfig{
			ID:                     1,
			Name:                   "net0",
			RAIntervalMilliseconds: 100,
			RouterLifetimeSeconds:  10,
			Clients:                []string{"fe80::1"},
		})
		ra := nextUnicastRA(t, sock, time.Second)
		require.False(t, ra.from.IsValid())
	})

	t.Run("A non link-local source address is rejected", func(t *testing.T) {
		_, err := NewDaemon(&Config{Interfaces: []*InterfaceConfig{{
			ID:                     1,
			Name:                   "net0",
			RAIntervalMilliseconds: 100,
			SourceAddress:          "2001:db8::1",
		}}})
		var verrs ValidationErrors
		require.ErrorAs(t, err, &verrs)
	})
}

func TestUnicastRAIsSentImmediately(t *testing.T) {
	// The interval is far longer than the wait below, so an RA can only
	// arrive in time if it is sent right after the (re)load.
	iface := &InterfaceConfig{
		ID:                     1,
		Name:                   "net0",
		RAIntervalMilliseconds: 60000,
		RouterLifetimeSeconds:  10,
		Clients:                []string{"fe80::1"},
	}
	d, sock := newPolicyTestDaemon(t, iface)

	ra := nextUnicastRA(t, sock, time.Second)
	require.Equal(t, netip.MustParseAddr("fe80::1"), ra.to)

	updated := iface.deepCopy()
	updated.Clients = []string{"fe80::2"}
	require.NoError(t, d.UpdateInterface(t.Context(), updated))

	ra = nextUnicastRA(t, sock, time.Second)
	require.Equal(t, netip.MustParseAddr("fe80::2"), ra.to)
}

func TestSendRA(t *testing.T) {
	iface := &InterfaceConfig{
		ID:                     1,
		Name:                   "net0",
		RAIntervalMilliseconds: 60000,
		RouterLifetimeSeconds:  10,
		Clients:                []string{"fe80::1"},
	}
	d, sock := newPolicyTestDaemon(t, iface)
	nextUnicastRA(t, sock, time.Second) // the initial RA of the instance

	t.Run("Withdrawn routes are advertised with a zero lifetime", func(t *testing.T) {
		err := d.SendRA(t.Context(), &InterfaceConfig{
			Name:                   "net0",
			RAIntervalMilliseconds: 60000,
			SourceAddress:          "fe80::face:1",
			Clients:                []string{"fe80::2", "fe80::3"},
			Routes: []*RouteConfig{
				{Prefix: "2001:db8:1::/48", LifetimeSeconds: 1800},
			},
		}, []string{"2001:db8:2::/48", "2001:db8:1::/48"})
		require.NoError(t, err)

		ras := drainUnicastRAs(sock)
		require.Len(t, ras, 2)
		for _, ra := range ras {
			require.Equal(t, netip.MustParseAddr("fe80::face:1"), ra.from)
			require.Equal(t, time.Duration(0), ra.msg.RouterLifetime)
			// A route that is still advertised must not be withdrawn.
			require.Equal(t, map[string]time.Duration{
				"2001:db8:1::/48": 1800 * time.Second,
				"2001:db8:2::/48": 0,
			}, routeLifetimes(ra.msg))
		}
		require.ElementsMatch(t,
			[]netip.Addr{netip.MustParseAddr("fe80::2"), netip.MustParseAddr("fe80::3")},
			[]netip.Addr{ras[0].to, ras[1].to},
		)
	})

	t.Run("The running configuration is not changed", func(t *testing.T) {
		ifaces := d.Interfaces()
		require.Len(t, ifaces, 1)
		require.Equal(t, []string{"fe80::1"}, ifaces[0].Clients)
	})

	t.Run("An invalid withdrawn route is rejected", func(t *testing.T) {
		err := d.SendRA(t.Context(), &InterfaceConfig{
			Name:                   "net0",
			RAIntervalMilliseconds: 60000,
			Clients:                []string{"fe80::2"},
		}, []string{"192.0.2.0/24"})
		require.Error(t, err)
		require.Empty(t, drainUnicastRAs(sock))
	})
}

func TestDeleteInterfaceSkipGoodbye(t *testing.T) {
	makeIface := func() *InterfaceConfig {
		return &InterfaceConfig{
			ID:                     1,
			Name:                   "net0",
			RAIntervalMilliseconds: 60000,
			RouterLifetimeSeconds:  10,
			Preference:             "high",
			Clients:                []string{"fe80::1"},
		}
	}

	t.Run("A goodbye RA with medium preference is sent by default", func(t *testing.T) {
		d, sock := newPolicyTestDaemon(t, makeIface())
		nextUnicastRA(t, sock, time.Second)

		require.NoError(t, d.DeleteInterface(t.Context(), 1))
		eventully(t, func() bool { return sock.isClosed() })

		ras := drainUnicastRAs(sock)
		require.Len(t, ras, 1)
		require.Equal(t, time.Duration(0), ras[0].msg.RouterLifetime)
		require.Equal(t, ndp.Medium, ras[0].msg.RouterSelectionPreference)
	})

	t.Run("No goodbye RA is sent with SkipGoodbye", func(t *testing.T) {
		d, sock := newPolicyTestDaemon(t, makeIface())
		nextUnicastRA(t, sock, time.Second)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		require.NoError(t, d.DeleteInterfaceWithOptions(ctx, 1, DeleteOptions{SkipGoodbye: true}))
		// The call returns only after the instance has stopped.
		require.True(t, sock.isClosed())

		require.Empty(t, drainUnicastRAs(sock))
	})
}
