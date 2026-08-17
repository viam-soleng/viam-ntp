// Command cli exercises the NTP service without a running viam-server.
//
// Usage:
//
//	go run ./cmd/cli            # query only, does not touch the clock
//	sudo go run ./cmd/cli -set  # query and step the system clock
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"viamntp"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	genericservice "go.viam.com/rdk/services/generic"
)

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func realMain() error {
	set := flag.Bool("set", false, "actually step the system clock (needs root)")
	flag.Parse()

	ctx := context.Background()
	logger := logging.NewLogger("cli")

	deps := resource.Dependencies{}
	// can load these from a remote machine if you need

	cfg := viamntp.Config{}

	thing, err := viamntp.NewUpdate(ctx, deps, genericservice.Named("ntp"), &cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = thing.Close(ctx) }()

	cmd := map[string]interface{}{"query": map[string]interface{}{}}
	if *set {
		cmd = map[string]interface{}{"update": map[string]interface{}{}}
	}

	resp, err := thing.DoCommand(ctx, cmd)
	if err != nil {
		return err
	}

	out, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}
