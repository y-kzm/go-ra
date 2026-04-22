// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package internal

import (
	"context"
	"log/slog"
	"net"

	ra "github.com/YutaroHayakawa/go-ra"
	gorav1 "github.com/YutaroHayakawa/go-ra/api/gora/v1"
	"google.golang.org/grpc"
)

type goraServer struct {
	gorav1.UnimplementedGoRAServiceServer
	daemon *ra.Daemon
	logger *slog.Logger
}

// NewGRPCServer creates and registers the gRPC server, returning the server
// and the listener. The caller is responsible for calling srv.Serve(lis).
func NewGRPCServer(addr string, daemon *ra.Daemon, logger *slog.Logger) (*grpc.Server, net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}

	srv := grpc.NewServer()
	gorav1.RegisterGoRAServiceServer(srv, &goraServer{
		daemon: daemon,
		logger: logger,
	})

	return srv, lis, nil
}

func (s *goraServer) GetStatus(ctx context.Context, _ *gorav1.GetStatusRequest) (*gorav1.GetStatusResponse, error) {
	status := s.daemon.Status()

	resp := &gorav1.GetStatusResponse{}
	for _, iface := range status.Interfaces {
		resp.Interfaces = append(resp.Interfaces, &gorav1.InterfaceStatus{
			Id:              int32(iface.ID),
			Name:            iface.Name,
			State:           iface.State,
			Message:         iface.Message,
			LastUpdate:      iface.LastUpdate,
			TxSolicitedRa:   int32(iface.TxSolicitedRA),
			TxUnsolicitedRa: int32(iface.TxUnsolicitedRA),
		})
	}

	return resp, nil
}
