package integration_tests

import (
	"context"
	"testing"
	"time"

	"github.com/YutaroHayakawa/go-radv"
	"github.com/lorenzosaino/go-sysctl"

	apipb "github.com/osrg/gobgp/v3/api"
	"github.com/osrg/gobgp/v3/pkg/config/oc"
	"github.com/osrg/gobgp/v3/pkg/server"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

func TestGoBGPUnnumbered(t *testing.T) {
	// Create veth pair
	err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name:      "go-radv0",
			OperState: netlink.OperUp,
		},
		PeerName: "go-radv1",
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		t.Log("Cleaning up veth pair")
		netlink.LinkDel(&netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{
				Name: "go-radv0",
			},
		})
	})

	link0, err := netlink.LinkByName("go-radv0")
	require.NoError(t, err)

	link1, err := netlink.LinkByName("go-radv1")
	require.NoError(t, err)

	err = netlink.LinkSetUp(link0)
	require.NoError(t, err)

	err = netlink.LinkSetUp(link1)
	require.NoError(t, err)

	t.Log("Created veth pair. Setting sysctl.")

	sysctlClient, err := sysctl.NewClient(sysctl.DefaultPath)
	require.NoError(t, err)

	sysctlClient.Set("net.ipv6.conf.go-radv0.forwarding", "1")
	require.NoError(t, err)

	sysctlClient.Set("net.ipv6.conf.go-radv0.accept_ra", "2")
	require.NoError(t, err)

	sysctlClient.Set("net.ipv6.conf.go-radv1.forwarding", "1")
	require.NoError(t, err)

	sysctlClient.Set("net.ipv6.conf.go-radv1.accept_ra", "2")
	require.NoError(t, err)

	t.Log("Sysctl set. Starting radvd.")

	ctx := context.Background()

	// Start radvd
	radvd0, err := radv.NewDaemon(&radv.Config{
		Interfaces: []*radv.InterfaceConfig{
			{
				Name:                   "go-radv0",
				RAIntervalMilliseconds: 1000,
			},
		},
	})
	require.NoError(t, err)

	radvd1, err := radv.NewDaemon(&radv.Config{
		Interfaces: []*radv.InterfaceConfig{
			{
				Name:                   "go-radv1",
				RAIntervalMilliseconds: 1000,
			},
		},
	})
	require.NoError(t, err)

	go radvd0.Run(ctx)
	go radvd1.Run(ctx)

	t.Log("Started radvd. Waiting for RAs to be sent.")

	// Wait at least for 2 RAs to be sent
	require.Eventually(t, func() bool {
		status0 := radvd0.Status()
		status1 := radvd1.Status()
		return status0 != nil && status1 != nil &&
			status0.Interfaces[0].State == radv.Running &&
			status1.Interfaces[0].State == radv.Running
	}, time.Second*10, time.Millisecond*500)

	t.Log("RAs are being sent. Starting BGP.")

	// Start bgpd
	timeout, cancel := context.WithTimeout(ctx, time.Second*1)
	bgpd0 := server.NewBgpServer()
	go bgpd0.Serve()

	err = bgpd0.StartBgp(timeout, &apipb.StartBgpRequest{
		Global: &apipb.Global{
			Asn:        64512,
			RouterId:   "10.0.0.0",
			ListenPort: 10179,
		},
	})
	require.NoError(t, err)
	cancel()

	timeout, cancel = context.WithTimeout(ctx, time.Second*1)
	bgpd1 := server.NewBgpServer()
	go bgpd1.Serve()

	err = bgpd1.StartBgp(timeout, &apipb.StartBgpRequest{
		Global: &apipb.Global{
			Asn:        64512,
			RouterId:   "10.0.0.1",
			ListenPort: 11179,
		},
	})
	require.NoError(t, err)
	cancel()

	t.Log("Started BGP. Adding peers.")

	lladdr0, err := oc.GetIPv6LinkLocalNeighborAddress("go-radv0")
	require.NoError(t, err)

	lladdr1, err := oc.GetIPv6LinkLocalNeighborAddress("go-radv1")
	require.NoError(t, err)

	// Set up unnumbered peer
	err = bgpd0.AddPeer(ctx, &apipb.AddPeerRequest{
		Peer: &apipb.Peer{
			Conf: &apipb.PeerConf{
				PeerAsn:           64512,
				NeighborAddress:   lladdr0,
				NeighborInterface: "go-radv0",
			},
			Transport: &apipb.Transport{
				RemotePort: 11179,
			},
			Timers: &apipb.Timers{
				Config: &apipb.TimersConfig{
					ConnectRetry: 1,
				},
			},
		},
	})
	require.NoError(t, err)

	err = bgpd1.AddPeer(ctx, &apipb.AddPeerRequest{
		Peer: &apipb.Peer{
			Conf: &apipb.PeerConf{
				PeerAsn:           64512,
				NeighborAddress:   lladdr1,
				NeighborInterface: "go-radv1",
			},
			Transport: &apipb.Transport{
				RemotePort: 10179,
			},
			Timers: &apipb.Timers{
				Config: &apipb.TimersConfig{
					ConnectRetry: 1,
				},
			},
		},
	})
	require.NoError(t, err)

	t.Log("Peers added. Waiting for session to be established.")

	require.Eventually(t, func() bool {
		var peer0, peer1 *apipb.Peer

		if err := bgpd0.ListPeer(ctx, &apipb.ListPeerRequest{}, func(p *apipb.Peer) {
			if p.Conf.NeighborInterface == "go-radv0" {
				peer0 = p
			}
		}); err != nil {
			return false
		}

		if err := bgpd1.ListPeer(ctx, &apipb.ListPeerRequest{}, func(p *apipb.Peer) {
			if p.Conf.NeighborInterface == "go-radv1" {
				peer1 = p
			}
		}); err != nil {
			return false
		}

		return peer0 != nil && peer1 != nil &&
			peer0.State.SessionState == apipb.PeerState_ESTABLISHED &&
			peer1.State.SessionState == apipb.PeerState_ESTABLISHED
	}, time.Second*10, time.Millisecond*500)

	t.Log("Session established. All done.")
}
