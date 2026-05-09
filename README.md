# go-ra (fork)

> **Note:** This is a fork of [YutaroHayakawa/go-ra](https://github.com/YutaroHayakawa/go-ra),
> extended for use as a proof-of-concept in research on dynamic control of Router
> Advertisement. The fork adds a gRPC API for runtime management of RA instances
> (add/delete interfaces without daemon restart), multiple RA instances per
> network interface identified by integer ID, unicast RA, and goodbye RA — with
> the goal of enabling flexible programmatic control needed for the research.

[![Go Reference](https://pkg.go.dev/badge/github.com/YutaroHayakawa/go-ra.svg)](https://pkg.go.dev/github.com/YutaroHayakawa/go-ra)

Provides the `ra` package that implements a router-side functionality of IPv6
Neighbor Discovery mechanism
([RFC4861](https://datatracker.ietf.org/doc/html/rfc4861) and related RFCs). It
also provides a stand-alone daemon `gorad` and CLI tool `gora` to interact with
it. While the existing
[mdlayher/ndp](https://pkg.go.dev/github.com/mdlayher/ndp) package provides a
low-level protocol functionalities (packet encoding, raw-socket wrapper, etc),
`go-ra` implements an unsolicited and solicited advertisement machinery and
declarative configuration interface on top of it.

## Features

- Basic RA mechanism defined in RFC4861
- Router MAC address discovery with Source Link Layer Address option
- MTU discovery with MTU option
- Prefix discovery with Prefix Information option
- DNS configuration discovery with RDNSS/DNSSL option
- Route advertisement with Route Information option
- NAT64 prefix discovery with PREF64 option
- Unicast RA to specific clients
- Goodbye RA on daemon stop (RouterLifetime=0 with zeroed option lifetimes)
- Multiple RA instances per network interface (identified by integer ID)
- gRPC API for runtime management (`gorad` as server, `gora` as client)

## Installation

- Library: Use Go Modules as usual
- Stand-alone Binary: Visit [release page](https://github.com/YutaroHayakawa/go-ra/releases) and install pre-build binaries
- Container Image: Visit [registry page](https://github.com/YutaroHayakawa/go-ra/pkgs/container/gorad) and pull images

## Basic Usage

### As a library

```go
// Build a configuration
config := ra.Config{
	Interfaces: []*ra.InterfaceConfig{
		{
			ID:   1,
			Name: "eth0",
			// Send unsolicited RA once a second
			RAIntervalMilliseconds: 1000, // 1sec
		},
	},
}

// Create an RA daemon
daemon, _ := ra.NewDaemon(&config)

// Run it
ctx, cancel := context.WithCancel(context.Background())
go daemon.Run(ctx)

// Get a running status
status := daemon.Status()
for _, iface := range status.Interfaces {
    fmt.Printf("[%d] %s: %s (%s)\n", iface.ID, iface.Name, iface.State, iface.Message)
}

// Add a new interface at runtime
err := daemon.AddInterface(ctx, &ra.InterfaceConfig{
    ID:                     2,
    Name:                   "eth1",
    RAIntervalMilliseconds: 1000,
})

// Remove an interface at runtime
err = daemon.DeleteInterface(ctx, 2)

// Change configuration and reload
config.Interfaces[0].RAIntervalMilliseconds = 2000 // 2sec
err = daemon.Reload(ctx, &config)
if err != nil {
    panic(err)
}

// Stop it (sends goodbye RA by default)
cancel()
```

### As a stand-alone daemon

Create a configuration file. Each interface requires a unique integer `id`.
Multiple instances on the same network interface are allowed as long as IDs differ.

```yaml
interfaces:
- id: 1
  name: eth0
  raIntervalMilliseconds: 1000 # 1sec
```

Start the daemon. You need root privilege to run it. Use `-a` to set the gRPC
listen address (default: `localhost:50051`).

```bash
$ sudo gorad -f config.yaml
$ sudo gorad -f config.yaml -a 0.0.0.0:50051
```

Get status. Use `-s` to specify the server address (default: `localhost:50051`).

```bash
$ gora status
ID    Name    Age    TxUnsolicited    TxSolicited    State      Message
1     eth0    22s    21               1              Running
```

List all interface configurations at runtime.

```bash
$ gora list-interfaces
```

Add an interface at runtime. The file should contain a single interface config
(without the `interfaces:` wrapper).

```yaml
# iface.yaml
id: 2
name: eth1
raIntervalMilliseconds: 1000
```

```bash
$ gora add-interface -f iface.yaml
Successfully added interface.
```

Delete an interface at runtime.

```bash
$ gora delete-interface --id 2
Successfully deleted interface.
```

## gRPC API

`gorad` exposes a gRPC server. The proto definition is at
[api/gora/v1/gora.proto](api/gora/v1/gora.proto). Generated Go client code is
available at `github.com/YutaroHayakawa/go-ra/api/gora/v1`.

### Service

```protobuf
service GoRAService {
  rpc GetStatus(GetStatusRequest) returns (GetStatusResponse);
  rpc ListInterfaces(ListInterfacesRequest) returns (ListInterfacesResponse);
  rpc AddInterface(AddInterfaceRequest) returns (AddInterfaceResponse);
  rpc UpdateInterface(UpdateInterfaceRequest) returns (UpdateInterfaceResponse);
  rpc DeleteInterface(DeleteInterfaceRequest) returns (DeleteInterfaceResponse);
}
```

### RPCs

| RPC | Description | Error codes |
|-----|-------------|-------------|
| `GetStatus` | Returns the runtime status of all RA instances | — |
| `ListInterfaces` | Returns the current configuration of all RA instances | — |
| `AddInterface` | Adds a new RA instance. Triggers config validation. | `INVALID_ARGUMENT` on validation failure |
| `UpdateInterface` | Replaces the configuration of an existing RA instance. | `INVALID_ARGUMENT` on validation failure, `NOT_FOUND` if the ID does not exist |
| `DeleteInterface` | Removes the RA instance with the given `id`. Sends a goodbye RA if `send_goodbye` is enabled. | `NOT_FOUND` if the ID does not exist |

### Using grpcurl

```bash
# Get status
$ grpcurl -plaintext localhost:50051 gora.v1.GoRAService/GetStatus

# List interface configurations
$ grpcurl -plaintext localhost:50051 gora.v1.GoRAService/ListInterfaces

# Add an interface
$ grpcurl -plaintext -d '{
  "interface": {
    "id": 2,
    "name": "eth1",
    "ra_interval_milliseconds": 600000,
    "preference": "medium",
    "router_lifetime_seconds": 1800,
    "send_goodbye": true
  }
}' localhost:50051 gora.v1.GoRAService/AddInterface

# Delete an interface
$ grpcurl -plaintext -d '{"id": 2}' localhost:50051 gora.v1.GoRAService/DeleteInterface
```

### Using the Go client

```go
import (
    gorav1 "github.com/YutaroHayakawa/go-ra/api/gora/v1"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

conn, _ := grpc.NewClient("localhost:50051",
    grpc.WithTransportCredentials(insecure.NewCredentials()))
defer conn.Close()

client := gorav1.NewGoRAServiceClient(conn)

resp, _ := client.GetStatus(ctx, &gorav1.GetStatusRequest{})
for _, iface := range resp.Interfaces {
    fmt.Printf("[%d] %s: %s\n", iface.Id, iface.Name, iface.State)
}
```

## Motivation

Our original motivation for this project was use it with
[gobgp](https://pkg.go.dev/github.com/osrg/gobgp/v3) library to do [BGP
Unnumbered](https://github.com/osrg/gobgp/blob/master/docs/sources/unnumbered-bgp.md)
(see our [integration test](integration_tests/gobgp_unnumbered_test.go)) which
for us, makes sense to reinvent the RA daemon to not introduce an external
non-go dependency.
