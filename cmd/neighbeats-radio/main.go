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
	"github.com/vincent/neighbeats-radio/internal/control"
	"github.com/vincent/neighbeats-radio/internal/director"
	"github.com/vincent/neighbeats-radio/internal/liq"
	"github.com/vincent/neighbeats-radio/internal/neighbeats"
	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/speak"
	"github.com/vincent/neighbeats-radio/internal/state"
)

var version = "dev"

const usage = `usage: neighbeats-radio [-config FILE] <command>

commands:
  run               run the director (long-running)
  now               what is playing, time left, the next three
  queue             the planned queue
  history [-n 30]   recent plays with outcome and reason
  skip              skip the current track (recorded as a skip)
  steer TEXT        head for a track found by words (or: steer -path PATH)
  pause | resume    play the fallback instead of the plan, and back
  push PATH         push a file into Liquidsoap's music queue (debug)
  liq-skip          skip in Liquidsoap directly, the director is not told (debug)
  status            print what Liquidsoap is playing (debug)
  version           print the version

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
	case "now", "queue", "history", "skip", "steer", "pause", "resume":
		return remote(ctx, cfg, cmd, rest)
	case "push":
		if len(rest) != 1 {
			return fmt.Errorf("push: want exactly one PATH")
		}
		res, err := lq.Push(ctx, rest[0], nil)
		if err != nil {
			return err
		}
		if res.Duplicate != "" {
			fmt.Printf("refused, already %s\n", res.Duplicate)
			return nil
		}
		fmt.Printf("pushed, %d queued\n", res.Queued)
		return nil
	case "liq-skip":
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
	nb := neighbeats.New(cfg.Neighbeats.Socket, cfg.Neighbeats.Timeout)
	router := &director.NeighbeatsRouter{
		Client: nb,
		Mode:   d.Mode, User: cfg.Neighbeats.User, MaxMinutes: d.MaxMinutes,
	}
	picker := director.NeighbeatsPicker{
		Client: nb, Mode: d.Mode, User: cfg.Neighbeats.User, MaxMinutes: d.MaxMinutes,
		Fallback: director.SeedPicker{Paths: d.SeedPaths}, Log: log,
	}
	pl := plan.New(plan.Config{
		LegMin: d.LegMin, LegMax: d.LegMax, LookAhead: d.LookAhead,
		RepeatWindow: d.RepeatWindow, AssumedTrack: d.AssumedTrack,
	}, router, picker, log, nil)

	voice, err := newVoice(cfg.Voice, log)
	if err != nil {
		return err
	}
	dir := director.New(director.Config{
		Voice:        voice,
		PollInterval: d.PollInterval, PushWhenBelow: d.PushWhenBelow, RepeatWindow: d.RepeatWindow,
		Fades: plan.Fades{Close: d.Fades.Close, Mid: d.Fades.Mid, Far: d.Fades.Far, CloseAt: d.Fades.CloseAt, FarAt: d.Fades.FarAt},
	}, lq, pl, db, log)
	log.Info("director started", "version", version, "liquidsoap", cfg.Liquidsoap.ControlURL, "neighbeats", cfg.Neighbeats.Socket)

	// The command socket is a convenience: if it cannot start, the station still runs.
	srv := &control.Server{B: dir, R: director.NeighbeatsResolver{Client: nb}}
	go func() {
		if err := srv.Serve(ctx, cfg.Control.Socket); err != nil {
			log.Error("control socket", "err", err)
		}
	}()
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

// newVoice sets up spoken breaks, or returns nil when they are off. A voice that
// cannot start (bad template folder) is a config error; Piper itself failing later
// only costs the break.
func newVoice(v config.Voice, log *slog.Logger) (*director.Voice, error) {
	if !v.Enabled {
		return nil, nil
	}
	fsys := speak.BuiltinFS()
	if v.Templates != "" {
		fsys = os.DirFS(v.Templates)
	}
	words, err := speak.LoadTemplates(fsys, v.Say)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(v.Model); err != nil {
		log.Warn("voice model not found, breaks will be skipped", "model", v.Model, "err", err)
	}
	return &director.Voice{
		Breaks: director.NewBreaks(director.BreakConfig{
			EveryMin: v.EveryMin, EveryMax: v.EveryMax, HourMark: v.HourMark, OnSteer: v.OnSteer, OnLeg: v.OnLeg,
		}, nil),
		Speaker: &speak.Renderer{Piper: v.Piper, Model: v.Model, Dir: v.Dir, Timeout: v.Timeout, KeepFor: time.Hour},
		Words:   words,
	}, nil
}
