// Command neighbeats-radio runs and controls the station director.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vincent/neighbeats-radio/internal/config"
	"github.com/vincent/neighbeats-radio/internal/director"
	"github.com/vincent/neighbeats-radio/internal/liq"
	"github.com/vincent/neighbeats-radio/internal/neighbeats"
	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/state"
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
	case "run":
		return runDirector(ctx, cfg, lq)
	case "queue":
		return printQueue(ctx, cfg)
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

func dbPath(cfg config.Config) string { return filepath.Join(cfg.Data, "neighbeats-radio.sqlite") }

func runDirector(ctx context.Context, cfg config.Config, lq *liq.Client) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	db, err := state.Open(dbPath(cfg))
	if err != nil {
		return err
	}
	defer db.Close()

	d := cfg.Director
	router := director.NeighbeatsRouter{
		Client: neighbeats.New(cfg.Neighbeats.Socket, cfg.Neighbeats.Timeout),
		Mode:   d.Mode, User: cfg.Neighbeats.User, MaxMinutes: d.MaxMinutes,
	}
	pl := plan.New(plan.Config{
		LegMin: d.LegMin, LegMax: d.LegMax, LookAhead: d.LookAhead,
		RepeatWindow: d.RepeatWindow, AssumedTrack: d.AssumedTrack,
	}, router, director.SeedPicker{Paths: d.SeedPaths}, log, nil)

	dir := director.New(director.Config{
		PollInterval: d.PollInterval, PushWhenBelow: d.PushWhenBelow, RepeatWindow: d.RepeatWindow,
	}, lq, pl, db, log)
	log.Info("director started", "version", version, "liquidsoap", cfg.Liquidsoap.ControlURL, "neighbeats", cfg.Neighbeats.Socket)
	return dir.Run(ctx)
}

// printQueue shows the stored plan. It reads the director's SQLite file, so it
// works while the director runs (WAL) or after it stopped.
func printQueue(ctx context.Context, cfg config.Config) error {
	db, err := state.Open(dbPath(cfg))
	if err != nil {
		return err
	}
	defer db.Close()
	items, err := db.LoadQueue(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("queue is empty")
		return nil
	}
	var ahead time.Duration
	for _, it := range items {
		dur := it.Duration
		if dur == 0 {
			dur = cfg.Director.AssumedTrack
		}
		ahead += dur
		fmt.Printf("%-8s leg %-3d %s - %s  [%s]\n", it.State, it.Leg, it.Artist, it.Title, it.Reason)
	}
	fmt.Printf("%d tracks, about %s ahead\n", len(items), ahead.Round(time.Minute))
	return nil
}
