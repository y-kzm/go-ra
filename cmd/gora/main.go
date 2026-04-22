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

	ra "github.com/YutaroHayakawa/go-ra"
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
	fmt.Println("  status\t\tGet the status of the service")
	fmt.Println("  add-interface\t\tAdd an interface configuration")
	fmt.Println("  update-interface\tUpdate an existing interface configuration")
	fmt.Println("  delete-interface\tDelete an interface configuration")
	fmt.Println("  help\t\t\tShow this message")
	fmt.Println("  version\t\tShow the version information")
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

	if os.Args[1] == "add-interface" {
		var (
			serverAddr string
			configFile string
		)
		command := flag.NewFlagSet("add-interface", flag.ExitOnError)
		command.StringVar(&serverAddr, "s", "localhost:50051", "gRPC server address")
		command.StringVar(&configFile, "f", "", "interface config file path (YAML)")
		command.Parse(os.Args[2:])

		if configFile == "" {
			fmt.Println("Interface config file path is required (-f)")
			os.Exit(1)
		}

		client, err := internal.NewClient(serverAddr)
		if err != nil {
			fmt.Printf("Failed to connect to server: %s\n", err.Error())
			os.Exit(1)
		}
		defer client.Close()

		addInterface(client, configFile)
		return
	}

	if os.Args[1] == "update-interface" {
		var (
			serverAddr string
			configFile string
		)
		command := flag.NewFlagSet("update-interface", flag.ExitOnError)
		command.StringVar(&serverAddr, "s", "localhost:50051", "gRPC server address")
		command.StringVar(&configFile, "f", "", "interface config file path (YAML)")
		command.Parse(os.Args[2:])

		if configFile == "" {
			fmt.Println("Interface config file path is required (-f)")
			os.Exit(1)
		}

		client, err := internal.NewClient(serverAddr)
		if err != nil {
			fmt.Printf("Failed to connect to server: %s\n", err.Error())
			os.Exit(1)
		}
		defer client.Close()

		updateInterface(client, configFile)
		return
	}

	if os.Args[1] == "delete-interface" {
		var (
			serverAddr string
			id         int
		)
		command := flag.NewFlagSet("delete-interface", flag.ExitOnError)
		command.StringVar(&serverAddr, "s", "localhost:50051", "gRPC server address")
		command.IntVar(&id, "id", 0, "interface ID to delete")
		command.Parse(os.Args[2:])

		if id == 0 {
			fmt.Println("Interface ID is required (--id)")
			os.Exit(1)
		}

		client, err := internal.NewClient(serverAddr)
		if err != nil {
			fmt.Printf("Failed to connect to server: %s\n", err.Error())
			os.Exit(1)
		}
		defer client.Close()

		deleteInterface(client, id)
		return
	}

	usageRoot()
	os.Exit(1)
}

func addInterface(client *internal.Client, configFile string) {
	f, err := os.Open(configFile)
	if err != nil {
		fmt.Printf("Failed to open config file: %s\n", err.Error())
		os.Exit(1)
	}
	defer f.Close()

	var ifaceConfig ra.InterfaceConfig
	if err := yaml.NewDecoder(f).Decode(&ifaceConfig); err != nil {
		fmt.Printf("Failed to parse config file: %s\n", err.Error())
		os.Exit(1)
	}

	_, err = client.AddInterface(context.Background(), &gorav1.AddInterfaceRequest{
		Interface: internal.InterfaceConfigToProto(&ifaceConfig),
	})
	if err != nil {
		fmt.Printf("Failed to add interface: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Println("Successfully added interface.")
}

func updateInterface(client *internal.Client, configFile string) {
	f, err := os.Open(configFile)
	if err != nil {
		fmt.Printf("Failed to open config file: %s\n", err.Error())
		os.Exit(1)
	}
	defer f.Close()

	var ifaceConfig ra.InterfaceConfig
	if err := yaml.NewDecoder(f).Decode(&ifaceConfig); err != nil {
		fmt.Printf("Failed to parse config file: %s\n", err.Error())
		os.Exit(1)
	}

	_, err = client.UpdateInterface(context.Background(), &gorav1.UpdateInterfaceRequest{
		Interface: internal.InterfaceConfigToProto(&ifaceConfig),
	})
	if err != nil {
		fmt.Printf("Failed to update interface: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Println("Successfully updated interface.")
}

func deleteInterface(client *internal.Client, id int) {
	_, err := client.DeleteInterface(context.Background(), &gorav1.DeleteInterfaceRequest{
		Id: int32(id),
	})
	if err != nil {
		fmt.Printf("Failed to delete interface: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Println("Successfully deleted interface.")
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
