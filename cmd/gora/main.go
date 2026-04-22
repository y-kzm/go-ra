// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of go-ra

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	gorav1 "github.com/YutaroHayakawa/go-ra/api/gora/v1"
	"github.com/YutaroHayakawa/go-ra/cmd/internal"
	"gopkg.in/yaml.v3"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func usageRoot() {
	fmt.Printf("Usage: %s <subcommand> [options]\n", os.Args[0])
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  status\tGet the status of the service")
	fmt.Println("  help\t\tShow this message")
	fmt.Println("  version\tShow the version information")
}

func main() {
	if len(os.Args) < 2 {
		usageRoot()
		os.Exit(1)
	}

	if os.Args[1] == "version" {
		fmt.Printf("Version: %s, Commit: %s, Date: %s", version, commit, date)
		return
	}

	if os.Args[1] == "help" {
		usageRoot()
		os.Exit(0)
	}

	if os.Args[1] == "status" {
		var (
			serverAddr string
			output     string
		)
		command := flag.NewFlagSet("status", flag.ExitOnError)
		command.StringVar(&serverAddr, "s", "localhost:50051", "gRPC server address")
		command.StringVar(&output, "o", "table", "Output format (table, json, or yaml)")
		command.Parse(os.Args[2:])

		client, err := internal.NewClient(serverAddr)
		if err != nil {
			fmt.Printf("Failed to connect to server: %s\n", err.Error())
			os.Exit(1)
		}
		defer client.Close()

		status(client, output)
		return
	}

	usageRoot()
	os.Exit(1)
}

func status(client *internal.Client, output string) {
	resp, err := client.GetStatus(context.Background(), &gorav1.GetStatusRequest{})
	if err != nil {
		fmt.Printf("Failed to get daemon status: %s\n", err.Error())
		os.Exit(1)
	}

	switch output {
	case "table":
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)
		fmt.Fprintln(w, "ID\tName\tAge\tTxUnsolicited\tTxSolicited\tState\tMessage")
		for _, iface := range resp.Interfaces {
			age := time.Duration(time.Now().Unix()-iface.LastUpdate) * time.Second
			age = age.Round(time.Second)
			fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%d\t%s\t%s\n",
				iface.Id, iface.Name, age.String(),
				iface.TxUnsolicitedRa, iface.TxSolicitedRa,
				iface.State, iface.Message)
		}
		w.Flush()

	case "json":
		j, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			fmt.Printf("Failed to indent the JSON: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Print(string(j))

	case "yaml":
		out, err := yaml.Marshal(resp)
		if err != nil {
			fmt.Printf("Failed to marshal the status: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Print(string(out))

	default:
		fmt.Printf("Invalid output format: %s\n", output)
		os.Exit(1)
	}
}
