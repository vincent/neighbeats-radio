package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vincent/neighbeats-radio/internal/config"
	"github.com/vincent/neighbeats-radio/internal/control"
)

// remote runs a command against the director's socket.
func remote(ctx context.Context, cfg config.Config, cmd string, args []string) error {
	c := control.NewClient(cfg.Control.Socket, cfg.Control.Timeout)
	switch cmd {
	case "now":
		r, err := c.Now(ctx)
		if err != nil {
			return err
		}
		printNow(r)
	case "queue":
		r, err := c.Queue(ctx)
		if err != nil {
			// The daemon is down: the stored plan is still readable.
			fmt.Fprintln(os.Stderr, "director not reachable, showing the stored plan")
			return printQueue(ctx, cfg)
		}
		for _, it := range r.Items {
			fmt.Printf("%-8s leg %-3d %s - %s  [%s]\n", it.State, it.Leg, it.Artist, it.Title, it.Reason)
		}
		fmt.Printf("%d tracks, about %s ahead\n", len(r.Items), secs(r.Ahead))
	case "history":
		fs := flag.NewFlagSet("history", flag.ContinueOnError)
		n := fs.Int("n", 30, "how many plays")
		if err := fs.Parse(args); err != nil {
			return err
		}
		r, err := c.History(ctx, *n)
		if err != nil {
			return err
		}
		for _, p := range r.Plays {
			cos := ""
			if p.Cosine != 0 {
				cos = fmt.Sprintf(" cos %.2f", p.Cosine)
			}
			fmt.Printf("%s  %-8s %s - %s  [%s%s]\n", p.At.Local().Format("15:04:05"), p.Outcome, p.Artist, p.Title, p.Reason, cos)
		}
	case "skip":
		return c.Skip(ctx)
	case "pause":
		if err := c.Pause(ctx); err != nil {
			return err
		}
		fmt.Println("paused: playing the fallback")
	case "resume":
		if err := c.Resume(ctx); err != nil {
			return err
		}
		fmt.Println("resumed")
	case "steer":
		return steer(ctx, c, args)
	}
	return nil
}

func steer(ctx context.Context, c *control.Client, args []string) error {
	fs := flag.NewFlagSet("steer", flag.ContinueOnError)
	path := fs.String("path", "", "steer to this exact file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), " ")
	if (*path == "") == (text == "") {
		return fmt.Errorf("steer: give either words or -path PATH")
	}
	r, err := c.Steer(ctx, control.SteerRequest{Text: text, Path: *path})
	if err != nil {
		return err
	}
	switch {
	case r.Found:
		fmt.Printf("steering to %s - %s (%d tracks planned)\n", r.Target.Artist, r.Target.Title, r.Tracks)
		return nil
	case len(r.Candidates) > 0:
		fmt.Println(r.Why)
		for _, t := range r.Candidates {
			fmt.Printf("  %s - %s\n    %s\n", t.Artist, t.Title, t.Path)
		}
	case r.Target != nil:
		fmt.Printf("no route to %s - %s: %s\nkeeping the current plan\n", r.Target.Artist, r.Target.Title, r.Why)
	default:
		fmt.Println(r.Why)
	}
	return fmt.Errorf("not steered")
}

func printNow(r control.NowResponse) {
	if r.Playing == nil {
		fmt.Println("nothing from the plan is playing (fallback)")
	} else {
		p := r.Playing
		fmt.Printf("%s - %s\n  why: %s", p.Artist, p.Title, p.Reason)
		if p.Cosine != 0 {
			fmt.Printf(" (cos %.2f)", p.Cosine)
		}
		fmt.Printf("  leg %d\n", p.Leg)
		if r.Remaining > 0 {
			fmt.Printf("  %s left\n", secs(r.Remaining))
		}
	}
	if r.Paused {
		fmt.Println("PAUSED")
	}
	for i, t := range r.Next {
		fmt.Printf("  next %d: %s - %s  [%s]\n", i+1, t.Artist, t.Title, t.Reason)
	}
}

func secs(s float64) time.Duration {
	return (time.Duration(s * float64(time.Second))).Round(time.Second)
}
