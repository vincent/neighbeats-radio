// Package plan is the director's brain: it owns the ordered list of planned
// tracks and decides which leg to route next. It does no I/O; routing and
// destination picking are interfaces so tests use fakes.
//
// The station is a path that never ends: route from the last queued track to a
// destination; when the look-ahead runs low, pick a new destination and route on.
package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"time"
)

type State string

const (
	Planned State = "planned"
	Pushed  State = "pushed"
	Playing State = "playing"
	Played  State = "played"
	Skipped State = "skipped"
)

// Track is what the radio keeps of a neighbeats answer.
type Track struct {
	ItemID   string
	Path     string
	Artist   string
	Title    string
	Cosine   float64
	Reason   string
	Duration time.Duration // zero = unknown, AssumedTrack is used
}

type Item struct {
	Track
	Leg   int
	State State
}

// Route is the answer to a routing request. Found=false is a normal outcome;
// Why says where the search broke.
type Route struct {
	Found  bool
	Tracks []Track
	Why    string
}

type Router interface {
	// Route returns a path from one track to another of about length tracks,
	// avoiding the excluded paths (the destination itself is still allowed).
	Route(ctx context.Context, from, to Track, length int, exclude []string) (Route, error)
}

type Picker interface {
	// Pick chooses a destination that is not in exclude.
	Pick(ctx context.Context, exclude []string) (Track, error)
}

type Config struct {
	LegMin       int
	LegMax       int
	LookAhead    time.Duration
	RepeatWindow time.Duration
	AssumedTrack time.Duration
	// MaxAttempts bounds destinations tried per leg before jumping.
	MaxAttempts int
}

type Planner struct {
	cfg    Config
	router Router
	picker Picker
	log    *slog.Logger
	rand   func(n int) int

	items   []Item
	recent  map[string]time.Time // path -> when it started playing
	steered *Track
	nextLeg int
}

// New returns an empty planner. rnd(n) returns 0..n-1; nil uses math/rand.
func New(cfg Config, r Router, p Picker, log *slog.Logger, rnd func(n int) int) *Planner {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if rnd == nil {
		rnd = rand.IntN
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 5
	}
	return &Planner{cfg: cfg, router: r, picker: p, log: log, rand: rnd, recent: map[string]time.Time{}, nextLeg: 1}
}

// Restore loads a saved queue (a restart resumes the same leg). Items that were
// pushed are planned again: Liquidsoap keeps no state that matters.
func (p *Planner) Restore(items []Item) {
	p.items = nil
	for _, it := range items {
		switch it.State {
		case Played, Skipped:
			continue
		case Pushed:
			it.State = Planned
		}
		p.items = append(p.items, it)
		if it.Leg >= p.nextLeg {
			p.nextLeg = it.Leg + 1
		}
	}
}

// NotePlayed seeds the repeat filter from stored history.
func (p *Planner) NotePlayed(path string, at time.Time) { p.recent[path] = at }

// Items returns a copy of the queue, oldest first.
func (p *Planner) Items() []Item { return append([]Item(nil), p.items...) }

// Steer makes t the next destination, ahead of the picker.
func (p *Planner) Steer(t Track) { p.steered = &t }

// Lookahead is the playing time still ahead: everything not yet started, plus the
// track now playing (counted whole, which errs short on the safe side).
func (p *Planner) Lookahead() time.Duration {
	var d time.Duration
	for _, it := range p.items {
		if it.State == Planned || it.State == Pushed || it.State == Playing {
			d += p.duration(it.Track)
		}
	}
	return d
}

func (p *Planner) duration(t Track) time.Duration {
	if t.Duration > 0 {
		return t.Duration
	}
	return p.cfg.AssumedTrack
}

// Fill plans legs until the look-ahead is reached. It returns how many tracks it
// added. On error the tracks added so far stay planned.
func (p *Planner) Fill(ctx context.Context, now time.Time) (int, error) {
	added := 0
	for legs := 0; p.Lookahead() < p.cfg.LookAhead; legs++ {
		if legs >= 20 {
			return added, errors.New("plan: look-ahead not reached after 20 legs")
		}
		n, err := p.planLeg(ctx, now)
		added += n
		if err != nil {
			return added, err
		}
	}
	return added, nil
}

func (p *Planner) planLeg(ctx context.Context, now time.Time) (int, error) {
	exclude := p.exclusions(now)
	leg := p.nextLeg
	p.nextLeg++

	last := p.last()
	var to Track
	for attempt := 1; attempt <= p.cfg.MaxAttempts; attempt++ {
		var err error
		to, err = p.destination(ctx, exclude)
		if err != nil {
			return 0, err
		}
		if last == nil {
			to.Reason = "start"
			p.items = append(p.items, Item{Track: to, Leg: leg, State: Planned})
			p.log.Info("leg", "leg", leg, "start", to.Path)
			return 1, nil
		}
		length := p.cfg.LegMin + p.rand(p.cfg.LegMax-p.cfg.LegMin+1)
		r, err := p.router.Route(ctx, last.Track, to, length, exclude)
		if err != nil {
			return 0, err
		}
		if !r.Found {
			p.log.Warn("no route", "leg", leg, "attempt", attempt, "from", last.Path, "to", to.Path, "why", r.Why)
			continue
		}
		tracks := p.filter(r.Tracks, last.Track, exclude)
		if len(tracks) == 0 {
			p.log.Warn("route empty after repeat filter", "leg", leg, "attempt", attempt, "to", to.Path)
			continue
		}
		for _, t := range tracks {
			p.items = append(p.items, Item{Track: t, Leg: leg, State: Planned})
		}
		p.log.Info("leg", "leg", leg, "from", last.Path, "to", to.Path, "tracks", len(tracks))
		return len(tracks), nil
	}
	// Every destination failed: jump to the last one so the station never stalls.
	to.Reason = "fallback"
	p.items = append(p.items, Item{Track: to, Leg: leg, State: Planned})
	p.log.Warn("jump", "leg", leg, "to", to.Path, "attempts", p.cfg.MaxAttempts)
	return 1, nil
}

func (p *Planner) destination(ctx context.Context, exclude []string) (Track, error) {
	if p.steered != nil {
		t := *p.steered
		p.steered = nil
		if t.Reason == "" {
			t.Reason = "steered"
		}
		return t, nil
	}
	t, err := p.picker.Pick(ctx, exclude)
	if err != nil {
		return Track{}, fmt.Errorf("plan: pick destination: %w", err)
	}
	return t, nil
}

// filter drops the start of the answer (the track we route from), anything played
// inside the window, anything already queued, and duplicates within the answer.
func (p *Planner) filter(in []Track, from Track, exclude []string) []Track {
	skip := map[string]bool{from.Path: true}
	for _, e := range exclude {
		skip[e] = true
	}
	var out []Track
	for _, t := range in {
		if skip[t.Path] {
			continue
		}
		skip[t.Path] = true
		out = append(out, t)
	}
	return out
}

// exclusions lists every path the next leg must not repeat: played inside the
// window and still in the queue.
func (p *Planner) exclusions(now time.Time) []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	for path, at := range p.recent {
		if now.Sub(at) > p.cfg.RepeatWindow {
			delete(p.recent, path)
			continue
		}
		add(path)
	}
	for _, it := range p.items {
		add(it.Path)
	}
	return out
}

func (p *Planner) last() *Item {
	for i := len(p.items) - 1; i >= 0; i-- {
		if p.items[i].State != Skipped {
			return &p.items[i]
		}
	}
	return nil
}

// TakeForPush marks up to n planned items as pushed, in order, and returns them.
func (p *Planner) TakeForPush(n int) []Item {
	var out []Item
	for i := range p.items {
		if len(out) >= n {
			break
		}
		if p.items[i].State == Planned {
			p.items[i].State = Pushed
			out = append(out, p.items[i])
		}
	}
	return out
}

// Pushed counts items handed to Liquidsoap and not yet started.
func (p *Planner) Pushed() int {
	n := 0
	for _, it := range p.items {
		if it.State == Pushed {
			n++
		}
	}
	return n
}

// Requeue turns pushed items back into planned ones, for when Liquidsoap lost its
// queue (restart, flush).
func (p *Planner) Requeue() {
	for i := range p.items {
		if p.items[i].State == Pushed {
			p.items[i].State = Planned
		}
	}
}

// Unpush puts a pushed item back to planned, for a push that did not reach Liquidsoap.
func (p *Planner) Unpush(path string) {
	for i := range p.items {
		if p.items[i].State == Pushed && p.items[i].Path == path {
			p.items[i].State = Planned
			return
		}
	}
}

// Reject drops a pushed item Liquidsoap refused for good (file missing).
func (p *Planner) Reject(path string) {
	for i := range p.items {
		if p.items[i].State == Pushed && p.items[i].Path == path {
			p.items[i].State = Skipped
		}
	}
	p.compact()
}

// Started records that path began playing. The previous track becomes played,
// pushed items ahead of it in the queue are marked skipped. It returns the item
// now playing, or false when the path is not one the planner pushed (a fallback
// track or an ident).
func (p *Planner) Started(path string, at time.Time) (Item, bool) {
	idx := -1
	for i, it := range p.items {
		if it.State == Pushed && it.Path == path {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Item{}, false
	}
	for i := 0; i < idx; i++ {
		switch p.items[i].State {
		case Playing:
			p.items[i].State = Played
		case Pushed:
			p.items[i].State = Skipped
		}
	}
	p.items[idx].State = Playing
	p.recent[path] = at
	playing := p.items[idx]
	p.compact()
	return playing, true
}

// compact forgets finished items. The playing item is always live, so the next
// leg can route from the end of the queue.
func (p *Planner) compact() {
	keep := p.items[:0]
	for _, it := range p.items {
		if it.State != Played && it.State != Skipped {
			keep = append(keep, it)
		}
	}
	p.items = keep
}
