// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package internal

import (
	gorav1 "github.com/YutaroHayakawa/go-ra/api/gora/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	gorav1.GoRAServiceClient
	conn *grpc.ClientConn
}

func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Client{
		GoRAServiceClient: gorav1.NewGoRAServiceClient(conn),
		conn:              conn,
	}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
