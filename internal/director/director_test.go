package director

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vincent/neighbeats-radio/internal/liq"
	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/state"
)

var ctx = context.Background()

// fakeLiq simulates Liquidsoap: a current track, a queue, a track counter.
type fakeLiq struct {
	trackID int
	current string
	queue   []string
	missing map[string]bool
	down    error
	pushes  []string
	flushes int
}

func (f *fakeLiq) Status(context.Context) (liq.Status, error) {
	if f.down != nil {
		return liq.Status{}, f.down
	}
	return liq.Status{TrackID: f.trackID, Queued: len(f.queue), Current: liq.Track{Filename: f.current, Artist: "liq-artist", Title: "liq-title"}}, nil
}

func (f *fakeLiq) Push(_ context.Context, path string) (int, error) {
	if f.missing[path] {
		return 0, &liq.Error{Path: "/control/push", Code: 404, Msg: "no such file"}
	}
	f.pushes = append(f.pushes, path)
	f.queue = append(f.queue, path)
	return len(f.queue), nil
}

func (f *fakeLiq) Flush(context.Context) error { f.queue = nil; f.flushes++; return nil }

// advance ends the current track: the next queued one starts, or a fallback.
func (f *fakeLiq) advance() {
	f.trackID++
	if len(f.queue) == 0 {
		f.current = "/fallback/ident.mp3"
		return
	}
	f.current, f.queue = f.queue[0], f.queue[1:]
}

func (f *fakeLiq) restart() { *f = fakeLiq{missing: f.missing, current: "/fallback/ident.mp3"} }

type seqRouter struct {
	n           int
	err         error
	lastExclude []string
}

func (r *seqRouter) Route(_ context.Context, from, to plan.Track, length int, exclude []string) (plan.Route, error) {
	r.lastExclude = exclude
	if r.err != nil {
		return plan.Route{}, r.err
	}
	out := []plan.Track{from}
	for i := 1; i < length; i++ {
		r.n++
		out = append(out, plan.Track{Path: fmt.Sprintf("/m/t%03d.mp3", r.n), Artist: "A", Title: "T", Cosine: 0.6, Reason: "similarity 0.6"})
	}
	return plan.Route{Found: true, Tracks: append(out, to)}, nil
}

type seqPicker struct{ n int }

func (p *seqPicker) Pick(context.Context, []string) (plan.Track, error) {
	p.n++
	return plan.Track{Path: fmt.Sprintf("/m/dest%03d.mp3", p.n), Artist: "D"}, nil
}

type harness struct {
	d  *Director
	lq *fakeLiq
	st *state.DB
	r  *seqRouter
	pl *plan.Planner
	at time.Time
}

func newHarness(t *testing.T, st *state.DB, lq *fakeLiq) *harness {
	t.Helper()
	if st == nil {
		var err error
		if st, err = state.Open(":memory:"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
	}
	if lq == nil {
		lq = &fakeLiq{current: "/fallback/ident.mp3", missing: map[string]bool{}}
	}
	r := &seqRouter{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pl := plan.New(plan.Config{LegMin: 4, LegMax: 4, LookAhead: 20 * time.Minute, RepeatWindow: 8 * time.Hour, AssumedTrack: 4 * time.Minute},
		r, &seqPicker{}, log, func(int) int { return 0 })
	d := New(Config{PollInterval: time.Second, PushWhenBelow: 2, RepeatWindow: 8 * time.Hour}, lq, pl, st, log)
	h := &harness{d: d, lq: lq, st: st, r: r, pl: pl, at: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	d.now = func() time.Time { return h.at }
	if err := d.init(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) step() { h.at = h.at.Add(2 * time.Second); h.d.Step(ctx) }

func TestFirstStepFillsAndPushesTwo(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	if len(h.lq.pushes) != 2 || h.lq.pushes[0] != "/m/dest001.mp3" {
		t.Fatalf("pushes %v", h.lq.pushes)
	}
	if h.pl.Lookahead() < 20*time.Minute {
		t.Fatalf("lookahead %v", h.pl.Lookahead())
	}
	q, _ := h.st.LoadQueue(ctx)
	if len(q) != len(h.pl.Items()) {
		t.Fatalf("queue not saved: %d vs %d", len(q), len(h.pl.Items()))
	}
	h.step() // nothing changes: no extra pushes
	if len(h.lq.pushes) != 2 {
		t.Fatalf("pushes %v", h.lq.pushes)
	}
}

func TestTrackChangeRecordsPlayAndRefillsQueue(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.lq.advance() // dest001 starts
	h.step()
	hist, _ := h.st.History(ctx, 10)
	if len(hist) != 1 || hist[0].Path != "/m/dest001.mp3" || hist[0].Reason != "start" || hist[0].Outcome != state.Playing {
		t.Fatalf("history %+v", hist)
	}
	if len(h.lq.queue) != 2 {
		t.Fatalf("queue should be topped up to 2, got %v", h.lq.queue)
	}
	h.lq.advance()
	h.step()
	hist, _ = h.st.History(ctx, 10)
	if len(hist) != 2 || hist[0].Reason != "similarity 0.6" || hist[1].Outcome != state.Finished || hist[0].Leg == 0 {
		t.Fatalf("history %+v", hist)
	}
}

func TestFallbackTrackIsHistoryNotPlan(t *testing.T) {
	lq := &fakeLiq{current: "/fallback/a.mp3", missing: map[string]bool{}}
	h := newHarness(t, nil, lq)
	h.step()
	lq.queue = nil // the queue ran dry
	h.pl.Requeue()
	lq.advance()
	h.step()
	hist, _ := h.st.History(ctx, 10)
	if len(hist) != 1 || hist[0].Reason != "fallback" || hist[0].Artist != "liq-artist" {
		t.Fatalf("history %+v", hist)
	}
}

func TestLiquidsoapRestartRepushes(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.lq.advance()
	h.step()
	h.lq.advance()
	h.step()
	h.lq.restart() // forgets its queue and its push log
	h.step()
	if len(h.lq.queue) != 2 || len(h.lq.pushes) != 2 {
		t.Fatalf("queue after restart %v, pushes %v", h.lq.queue, h.lq.pushes)
	}
	// What was pushed again is what the plan believes in, in order.
	var planned []string
	for _, it := range h.pl.Items() {
		if it.State == plan.Pushed {
			planned = append(planned, it.Path)
		}
	}
	if fmt.Sprint(planned) != fmt.Sprint(h.lq.queue) {
		t.Fatalf("pushed %v vs queue %v", planned, h.lq.queue)
	}
}

func TestLostQueueWithoutRestartIsRepaired(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.lq.queue = nil // e.g. a flush by hand
	h.step()
	if len(h.lq.queue) != 2 {
		t.Fatalf("queue %v", h.lq.queue)
	}
}

func TestDirectorRestartResumesQueue(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.lq.advance()
	h.step()
	planned := h.pl.Items()
	playing := h.lq.current

	// New process, same database and same Liquidsoap (holding 2 stale queued tracks).
	h2 := newHarness(t, h.st, h.lq)
	h2.step()
	if h.lq.flushes != 1 {
		t.Fatalf("flushes %d", h.lq.flushes)
	}
	if h.lq.current != playing {
		t.Fatal("current track disturbed")
	}
	items := h2.pl.Items()
	if items[0].Path != playing || items[0].State != plan.Playing {
		t.Fatalf("resumed head %+v", items[0])
	}
	var want []string
	for _, it := range planned[1:3] {
		want = append(want, it.Path)
	}
	if fmt.Sprint(h.lq.queue) != fmt.Sprint(want) {
		t.Fatalf("re-pushed %v want %v (same leg)", h.lq.queue, want)
	}
}

func TestRepeatFilterSeededFromHistory(t *testing.T) {
	st, _ := state.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	st.StartPlay(ctx, state.Play{StartedAt: now.Add(-time.Hour), Path: "/m/heard.mp3"})
	st.StartPlay(ctx, state.Play{StartedAt: now.Add(-9 * time.Hour), Path: "/m/ancient.mp3"})
	h := newHarness(t, st, nil)
	h.step()
	h.step()
	ex := h.r.lastExclude
	if !contains(ex, "/m/heard.mp3") || contains(ex, "/m/ancient.mp3") {
		t.Fatalf("exclude %v", ex)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestMissingFileIsDroppedNotRetried(t *testing.T) {
	lq := &fakeLiq{current: "/fallback/ident.mp3", missing: map[string]bool{"/m/dest001.mp3": true}}
	h := newHarness(t, nil, lq)
	h.step()
	for _, p := range lq.pushes {
		if p == "/m/dest001.mp3" {
			t.Fatal("missing file queued")
		}
	}
	h.step()
	if len(lq.queue) != 2 {
		t.Fatalf("queue %v", lq.queue)
	}
	for _, it := range h.pl.Items() {
		if it.Path == "/m/dest001.mp3" {
			t.Fatal("missing file still planned")
		}
	}
}

func TestNeighbeatsDownDoesNotStopTheStationAndRetriesLater(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.r.err = errors.New("socket down")
	for i := 0; h.pl.Lookahead() >= 20*time.Minute; i++ {
		if i > 20 {
			t.Fatal("lookahead never dropped")
		}
		h.lq.advance()
		h.step()
	}
	if len(h.lq.queue) != 2 {
		t.Fatalf("queue must stay fed from the plan while neighbeats is down: %v", h.lq.queue)
	}
	h.r.err = nil
	h.step() // inside the retry delay: no new attempt
	if h.pl.Lookahead() >= 20*time.Minute {
		t.Fatal("retried inside the delay")
	}
	h.at = h.at.Add(time.Minute)
	h.step()
	if h.pl.Lookahead() < 20*time.Minute {
		t.Fatalf("did not recover: %v", h.pl.Lookahead())
	}
}

func TestLiquidsoapDownIsSurvived(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.lq.down = errors.New("connection refused")
	h.step()
	h.lq.down = nil
	h.step()
	if len(h.lq.queue) != 2 {
		t.Fatalf("queue %v", h.lq.queue)
	}
}
