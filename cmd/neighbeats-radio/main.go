// Command neighbeats-radio runs and controls the station director.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vincent/neighbeats-radio/internal/config"
	"github.com/vincent/neighbeats-radio/internal/liq"
)

var version = "dev"

const usage = `usage: neighbeats-radio [-config FILE] <command>

commands:
  run          run the director (long-running)
  queue        print the planned queue
  push PATH    push a file into Liquidsoap's music queue
  skip         skip the current track
  status       print what Liquidsoap is playing
  version      print the version

-config defaults to $NEIGHBEATS_RADIO_CONFIG; without either, defaults apply.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "neighbeats-radio:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("neighbeats-radio", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfgPath := fs.String("config", os.Getenv("NEIGHBEATS_RADIO_CONFIG"), "config file")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("missing command")
	}
	cmd, rest := fs.Arg(0), fs.Args()[1:]

	if cmd == "version" {
		fmt.Println(version)
		return nil
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	lq := liq.New(cfg.Liquidsoap.ControlURL, cfg.Liquidsoap.Timeout)

	switch cmd {
	case "push":
		if len(rest) != 1 {
			return fmt.Errorf("push: want exactly one PATH")
		}
		n, err := lq.Push(ctx, rest[0])
		if err != nil {
			return err
		}
		fmt.Printf("pushed, %d queued\n", n)
		return nil
	case "skip":
		return lq.Skip(ctx)
	case "status":
		s, err := lq.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("#%d %s - %s\n  %s\n  %d queued\n", s.TrackID, s.Current.Artist, s.Current.Title, s.Current.Filename, s.Queued)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}
