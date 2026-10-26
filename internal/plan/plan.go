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
	"strings"
	"sync"
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

// Planner is safe for concurrent use: the director's poll and the control socket
// both call it. Routing calls run without the lock; epoch changes whenever the
// plan is rewritten under them, and a leg planned against an older epoch is dropped.
type Planner struct {
	mu     sync.Mutex
	epoch  int
	cfg    Config
	router Router
	picker Picker
	log    *slog.Logger
	rand   func(n int) int

	items  []Item
	recent map[string]time.Time // path -> when it started playing
	// heard is the same by artist and title: one song under several paths (a copy in
	// the fallback folder, an album and a compilation) must not repeat either.
	heard   map[string]time.Time
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
	return &Planner{cfg: cfg, router: r, picker: p, log: log, rand: rnd, recent: map[string]time.Time{}, heard: map[string]time.Time{}, nextLeg: 1}
}

// Restore loads a saved queue (a restart resumes the same leg). Items that were
// pushed are planned again: Liquidsoap keeps no state that matters.
func (p *Planner) Restore(items []Item) {
	p.mu.Lock()
	defer p.mu.Unlock()
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

// NotePlayed seeds the repeat filter from stored history and from what the
// fallback played.
func (p *Planner) NotePlayed(path, artist, title string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recent[path] = at
	if k := songKey(artist, title); k != "" {
		p.heard[k] = at
	}
}

// songKey identifies a song across files. Empty when the tags are missing: untagged
// files are told apart by path only.
func songKey(artist, title string) string {
	a, t := strings.ToLower(strings.TrimSpace(artist)), strings.ToLower(strings.TrimSpace(title))
	if a == "" || t == "" {
		return ""
	}
	return a + "\x00" + t
}

// wasHeard reports whether the song was played inside the window, or is already in
// the plan under another path.
func (p *Planner) wasHeard(t Track, now time.Time, withPlanned bool) bool {
	k := songKey(t.Artist, t.Title)
	if k == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if at, ok := p.heard[k]; ok {
		if now.Sub(at) <= p.cfg.RepeatWindow {
			return true
		}
		delete(p.heard, k)
	}
	for _, it := range p.items {
		if withPlanned || it.State != Planned {
			if it.Path != t.Path && songKey(it.Artist, it.Title) == k {
				return true
			}
		}
	}
	return false
}

// Items returns a copy of the queue, oldest first.
func (p *Planner) Items() []Item {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Item(nil), p.items...)
}

// Steer makes t the next destination, ahead of the picker.
func (p *Planner) Steer(t Track) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steered = &t
}

// Lookahead is the playing time still ahead: everything not yet started, plus the
// track now playing (counted whole, which errs short on the safe side).
func (p *Planner) Lookahead() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lookahead()
}

func (p *Planner) lookahead() time.Duration {
	var d time.Duration
	for _, it := range p.items {
		if it.State == Planned || it.State == Pushed || it.State == Playing {
			d += p.duration(it.Track)
		}
	}
	return d
}

// Assumed is the length used for tracks neighbeats reports no duration for.
func (p *Planner) Assumed() time.Duration { return p.cfg.AssumedTrack }

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
	p.mu.Lock()
	exclude := p.exclusions(now, false)
	leg := p.nextLeg
	p.nextLeg++
	epoch := p.epoch
	var last *Item
	if l := p.last(); l != nil {
		cp := *l
		last = &cp
	}
	p.mu.Unlock()

	// stale reports that the plan was rewritten (a steer) while we were routing.
	stale := func() bool { return p.epoch != epoch }

	var to Track
	for attempt := 1; attempt <= p.cfg.MaxAttempts; attempt++ {
		var err error
		to, err = p.destination(ctx, exclude)
		if err != nil {
			return 0, err
		}
		if to.Reason != "steered" && p.wasHeard(to, now, true) {
			p.log.Warn("destination already heard under another path, picking again", "leg", leg, "to", to.Path)
			continue
		}
		if last == nil {
			to.Reason = "start"
			p.mu.Lock()
			defer p.mu.Unlock()
			if stale() {
				return 0, nil
			}
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
		tracks := p.filter(r.Tracks, last.Track, exclude, now, true)
		if len(tracks) == 0 {
			p.log.Warn("route empty after repeat filter", "leg", leg, "attempt", attempt, "to", to.Path)
			continue
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if stale() {
			return 0, nil
		}
		for _, t := range tracks {
			p.items = append(p.items, Item{Track: t, Leg: leg, State: Planned})
		}
		p.log.Info("leg", "leg", leg, "from", last.Path, "to", to.Path, "tracks", len(tracks))
		return len(tracks), nil
	}
	// Every destination failed: jump to the last one so the station never stalls.
	to.Reason = "fallback"
	p.mu.Lock()
	defer p.mu.Unlock()
	if stale() {
		return 0, nil
	}
	p.items = append(p.items, Item{Track: to, Leg: leg, State: Planned})
	p.log.Warn("jump", "leg", leg, "to", to.Path, "attempts", p.cfg.MaxAttempts)
	return 1, nil
}

func (p *Planner) destination(ctx context.Context, exclude []string) (Track, error) {
	p.mu.Lock()
	if p.steered != nil {
		t := *p.steered
		p.steered = nil
		p.mu.Unlock()
		if t.Reason == "" {
			t.Reason = "steered"
		}
		return t, nil
	}
	p.mu.Unlock()
	t, err := p.picker.Pick(ctx, exclude)
	if err != nil {
		return Track{}, fmt.Errorf("plan: pick destination: %w", err)
	}
	return t, nil
}

// filter drops the start of the answer (the track we route from), anything played
// inside the window, anything already queued, and duplicates within the answer.
func (p *Planner) filter(in []Track, from Track, exclude []string, now time.Time, withPlanned bool) []Track {
	skip := map[string]bool{from.Path: true}
	songs := map[string]bool{songKey(from.Artist, from.Title): true}
	for _, e := range exclude {
		skip[e] = true
	}
	var out []Track
	for _, t := range in {
		if skip[t.Path] {
			continue
		}
		k := songKey(t.Artist, t.Title)
		if k != "" && (songs[k] || p.wasHeard(t, now, withPlanned)) {
			continue
		}
		skip[t.Path] = true
		songs[k] = true
		out = append(out, t)
	}
	return out
}

// exclusions lists every path the next leg must not repeat: played inside the
// window and still in the queue. withoutPlanned leaves out items not yet pushed,
// for a steer that is about to drop them.
func (p *Planner) exclusions(now time.Time, withoutPlanned bool) []string {
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
		if withoutPlanned && it.State == Planned {
			continue
		}
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
	p.mu.Lock()
	defer p.mu.Unlock()
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
	p.mu.Lock()
	defer p.mu.Unlock()
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
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.items {
		if p.items[i].State == Pushed {
			p.items[i].State = Planned
		}
	}
}

// Unpush puts a pushed item back to planned, for a push that did not reach Liquidsoap.
func (p *Planner) Unpush(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.items {
		if p.items[i].State == Pushed && p.items[i].Path == path {
			p.items[i].State = Planned
			return
		}
	}
}

// Reject drops a pushed item Liquidsoap refused for good (file missing).
func (p *Planner) Reject(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	p.mu.Lock()
	defer p.mu.Unlock()
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
	if k := songKey(p.items[idx].Artist, p.items[idx].Title); k != "" {
		p.heard[k] = at
	}
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

// Fades maps the cosine of a hop to the crossfade length used at that seam: a close
// hop blends long, a distant one gets a short fade and a clean start.
type Fades struct {
	Close, Mid, Far time.Duration
	// CloseAt and FarAt are the cosine bounds: >= CloseAt is close, < FarAt is far.
	CloseAt, FarAt float64
}

// DefaultFades are used when the config leaves the fades out.
var DefaultFades = Fades{Close: 8 * time.Second, Mid: 5 * time.Second, Far: 1500 * time.Millisecond, CloseAt: 0.8, FarAt: 0.5}

// For returns the crossfade length for a hop with the given cosine. A zero cosine
// means unknown (a start or fallback track) and gets the middle length.
func (f Fades) For(cosine float64) time.Duration {
	switch {
	case cosine == 0:
		return f.Mid
	case cosine >= f.CloseAt:
		return f.Close
	case cosine < f.FarAt:
		return f.Far
	}
	return f.Mid
}

// After returns the item planned right after the one with this path, if any.
// Liquidsoap reads a crossfade length from the track that is ending, so the
// director sets it when pushing a track, from the hop that follows it.
func (p *Planner) After(path string) (Item, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, it := range p.items {
		if it.Path == path && i+1 < len(p.items) {
			return p.items[i+1], true
		}
	}
	return Item{}, false
}

// SteerResult is the outcome of SteerNow. Found=false leaves the plan untouched.
type SteerResult struct {
	Found  bool
	Why    string
	Tracks int
	Leg    int // the leg the steer created
}

// SteerNow reroutes the station to dest. It routes from the last track already
// pushed to Liquidsoap (or the one playing), then replaces everything planned after
// it, so the change is heard within one track. If neighbeats finds no route, or is
// down, the old plan stays.
func (p *Planner) SteerNow(ctx context.Context, dest Track, now time.Time) (SteerResult, error) {
	p.mu.Lock()
	var anchor *Item
	for i := len(p.items) - 1; i >= 0; i-- {
		if st := p.items[i].State; st == Pushed || st == Playing {
			cp := p.items[i]
			anchor = &cp
			break
		}
	}
	exclude := p.exclusions(now, true)
	p.mu.Unlock()

	dest.Reason = "steered"
	var tracks []Track
	if anchor == nil {
		// Nothing pushed yet: there is no seam to blend, start at the destination.
		tracks = []Track{dest}
	} else {
		length := p.cfg.LegMin + p.rand(p.cfg.LegMax-p.cfg.LegMin+1)
		r, err := p.router.Route(ctx, anchor.Track, dest, length, exclude)
		if err != nil {
			return SteerResult{}, err
		}
		if !r.Found {
			return SteerResult{Why: r.Why}, nil
		}
		tracks = p.filter(r.Tracks, anchor.Track, exclude, now, false)
		if len(tracks) == 0 {
			return SteerResult{Why: "every track on the route was played recently"}, nil
		}
		last := &tracks[len(tracks)-1]
		last.Reason = "steered: " + last.Reason
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	keep := p.items[:0]
	for _, it := range p.items {
		if it.State != Planned {
			keep = append(keep, it)
		}
	}
	p.items = keep
	leg := p.nextLeg
	p.nextLeg++
	for _, t := range tracks {
		p.items = append(p.items, Item{Track: t, Leg: leg, State: Planned})
	}
	p.steered = nil
	p.epoch++
	p.log.Info("steered", "leg", leg, "to", dest.Path, "tracks", len(tracks))
	return SteerResult{Found: true, Tracks: len(tracks), Leg: leg}, nil
}

// SkipPlaying marks the playing item skipped and returns it.
func (p *Planner) SkipPlaying() (Item, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.items {
		if p.items[i].State == Playing {
			p.items[i].State = Skipped
			it := p.items[i]
			p.compact()
			return it, true
		}
	}
	return Item{}, false
}

// Before returns the item queued right before the one with this path, if any.
func (p *Planner) Before(path string) (Item, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, it := range p.items {
		if it.Path == path && i > 0 {
			return p.items[i-1], true
		}
	}
	return Item{}, false
}

// LegEnd returns the last item of a leg: where that leg is heading.
func (p *Planner) LegEnd(leg int) (Item, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.items) - 1; i >= 0; i-- {
		if p.items[i].Leg == leg {
			return p.items[i], true
		}
	}
	return Item{}, false
}
