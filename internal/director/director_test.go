package director

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincent/neighbeats-radio/internal/liq"
	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/speak"
	"github.com/vincent/neighbeats-radio/internal/state"
)

var ctx = context.Background()

// fakeLiq simulates Liquidsoap: a current track, a queue, a track counter.
type fakeLiq struct {
	trackID   int
	current   string
	queue     []string
	missing   map[string]bool
	down      error
	pushes    []string
	metas     []map[string]string
	flushes   int
	skips     int
	voices    []string
	voiceErr  error
	lostReply bool // a push is queued but its answer is lost
}

func (f *fakeLiq) Status(context.Context) (liq.Status, error) {
	if f.down != nil {
		return liq.Status{}, f.down
	}
	return liq.Status{TrackID: f.trackID, Queued: len(f.queue), Current: liq.Track{Filename: f.current, Artist: "liq-artist", Title: "liq-title"}}, nil
}

func (f *fakeLiq) Push(_ context.Context, path string, meta map[string]string) (int, error) {
	if f.missing[path] {
		return 0, &liq.Error{Path: "/control/push", Code: 404, Msg: "no such file"}
	}
	f.pushes = append(f.pushes, path)
	f.metas = append(f.metas, meta)
	f.queue = append(f.queue, path)
	if f.lostReply {
		return 0, errors.New("connection reset by peer")
	}
	return len(f.queue), nil
}

func (f *fakeLiq) Flush(context.Context) error { f.queue = nil; f.flushes++; return nil }

func (f *fakeLiq) PushVoice(_ context.Context, path string) error {
	f.voices = append(f.voices, path)
	return f.voiceErr
}

func (f *fakeLiq) Skip(context.Context) error { f.skips++; f.advance(); return nil }

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
	h.pl.Requeue(0)
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

// The crossfade length Liquidsoap reads comes from the track that is ending, so each
// push carries the fade of the hop that follows it. The route cosine is 0.6: mid.
func TestPushCarriesFadeOfFollowingHop(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	if len(h.lq.metas) != 2 {
		t.Fatalf("metas %v", h.lq.metas)
	}
	for i, m := range h.lq.metas {
		if m["liq_cross_duration"] != "5.00" {
			t.Errorf("push %d meta %v", i, m)
		}
	}
}

func playOutcomes(t *testing.T, st *state.DB) map[string]string {
	t.Helper()
	plays, err := st.History(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range plays {
		out[p.Path] = p.Outcome
	}
	return out
}

func TestSkipRecordsSkippedNotFinished(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step() // pushes dest001, t001
	h.lq.advance()
	h.step() // dest001 plays
	if err := h.d.Skip(ctx); err != nil {
		t.Fatal(err)
	}
	h.step() // t001 starts
	out := playOutcomes(t, h.st)
	if out["/m/dest001.mp3"] != state.Skipped {
		t.Fatalf("outcomes %v", out)
	}
	if h.lq.skips != 1 {
		t.Fatalf("skips %d", h.lq.skips)
	}
	now := h.d.Now()
	if now.Playing == nil || now.Playing.Path != "/m/t001.mp3" {
		t.Fatalf("now %+v", now.Playing)
	}
}

func TestPauseStopsPushingResumePushesAgain(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.lq.advance()
	h.step()
	if err := h.d.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	pushed := len(h.lq.pushes)
	for i := 0; i < 3; i++ {
		h.step()
	}
	if len(h.lq.pushes) != pushed || len(h.lq.queue) != 0 {
		t.Fatalf("pushed while paused: %v", h.lq.pushes[pushed:])
	}
	if !h.d.Now().Paused {
		t.Fatal("not paused")
	}
	h.d.Resume()
	h.step()
	if len(h.lq.queue) == 0 {
		t.Fatal("nothing pushed after resume")
	}
}

func TestSteerReplacesTailAndNeverTouchesPushed(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	pushed := append([]string(nil), h.lq.queue...)
	res, err := h.d.Steer(ctx, plan.Track{Path: "/m/target.mp3", Artist: "T"})
	if err != nil || !res.Found {
		t.Fatalf("res %+v err %v", res, err)
	}
	for _, it := range h.d.Queue() {
		if it.State == plan.Pushed && it.Path != pushed[0] && it.Path != pushed[1] {
			t.Fatalf("pushed set changed: %+v", it)
		}
	}
	last := h.d.Queue()
	if last[len(last)-1].Path != "/m/target.mp3" {
		t.Fatalf("tail %v", last)
	}
}

type fakeSpeaker struct {
	err   error
	gate  chan struct{} // when set, Render blocks until closed
	texts []string
	mu    sync.Mutex
}

func (f *fakeSpeaker) said() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func (f *fakeSpeaker) Render(_ context.Context, text string) (string, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return "/spool/voice/" + fmt.Sprint(len(text)) + ".wav", nil
}

type echoWords struct{}

func (echoWords) Text(m string, d speak.Data) (string, error) {
	return m + ": " + d.Prev.Artist + " > " + d.Next.Artist + " / " + d.Dest.Artist, nil
}

// voiceHarness is a harness where every track is followed by a break.
func voiceHarness(t *testing.T, sp Speaker) *harness {
	t.Helper()
	h := newHarness(t, nil, nil)
	h.d.cfg.Voice = &Voice{
		Breaks:  NewBreaks(BreakConfig{EveryMin: 1, EveryMax: 1, OnSteer: true}, func(int) int { return 0 }),
		Speaker: sp, Words: echoWords{},
	}
	return h
}

func waitJobs(h *harness) {
	for _, j := range h.d.voices {
		<-j.done
	}
}

func TestBreakIsQueuedWhenItsTrackStarts(t *testing.T) {
	sp := &fakeSpeaker{}
	h := voiceHarness(t, sp)
	h.step() // pushes dest001 (no predecessor) and t001 (break due)
	waitJobs(h)
	if len(h.lq.voices) != 0 {
		t.Fatalf("voice pushed before its track started: %v", h.lq.voices)
	}
	h.lq.advance() // dest001 starts
	h.step()
	if len(h.lq.voices) != 0 {
		t.Fatalf("break for t001 played over dest001: %v", h.lq.voices)
	}
	h.lq.advance() // t001 starts
	h.step()
	if len(h.lq.voices) != 1 || len(sp.said()) == 0 || !strings.Contains(sp.said()[0], "D > A") {
		t.Fatalf("voices %v texts %v", h.lq.voices, sp.said())
	}
}

func TestBreakDroppedWhenItsTrackIsSkippedOver(t *testing.T) {
	h := voiceHarness(t, &fakeSpeaker{})
	h.step()
	waitJobs(h)
	h.lq.advance()
	h.step()
	// dest001 is skipped past: whatever starts next is not t001, so t001's break must go.
	h.lq.queue = nil
	h.d.planner.Requeue(0)
	h.lq.advance() // fallback ident starts
	h.step()
	if len(h.lq.voices) != 0 {
		t.Fatalf("voices %v", h.lq.voices)
	}
}

func TestRenderFailureLeavesMusicUnaffected(t *testing.T) {
	h := voiceHarness(t, &fakeSpeaker{err: errors.New("piper missing")})
	h.step()
	waitJobs(h)
	for i := 0; i < 4; i++ {
		h.lq.advance()
		h.step()
	}
	if len(h.lq.voices) != 0 || len(h.lq.pushes) < 4 {
		t.Fatalf("voices %v pushes %v", h.lq.voices, h.lq.pushes)
	}
}

func TestSlowRenderIsDroppedNotWaitedFor(t *testing.T) {
	gate := make(chan struct{})
	h := voiceHarness(t, &fakeSpeaker{gate: gate})
	done := make(chan struct{})
	go func() { h.step(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Step waited for the renderer")
	}
	h.lq.advance()
	h.step()
	h.lq.advance() // t001 starts while piper is still busy
	h.step()
	if len(h.lq.voices) != 0 {
		t.Fatalf("late break played: %v", h.lq.voices)
	}
	close(gate)
}

func TestSteerBreakNamesDestination(t *testing.T) {
	sp := &fakeSpeaker{}
	h := voiceHarness(t, sp)
	h.d.cfg.Voice.Breaks = NewBreaks(BreakConfig{EveryMin: 100, EveryMax: 100, OnSteer: true}, func(int) int { return 0 })
	h.step()
	h.lq.advance()
	h.step()
	if _, err := h.d.Steer(ctx, plan.Track{Path: "/m/target.mp3", Artist: "Target"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		h.step()
		waitJobs(h)
		h.lq.advance()
	}
	h.step()
	var steered bool
	for _, s := range sp.said() {
		steered = steered || (strings.HasPrefix(s, "steer:") && strings.HasSuffix(s, "/ Target"))
	}
	if !steered {
		t.Fatalf("texts %v", sp.said())
	}
}

// Two requests missing from Liquidsoap's queue are lost ones: one fewer than pushed
// is only the crossfade holding the next request.
func TestLostRequestsDetected(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	if len(h.lq.queue) != 2 {
		t.Fatalf("queue %v", h.lq.queue)
	}
	held := h.lq.pushes[0]
	h.lq.queue = nil // Liquidsoap lost both
	h.step()
	if h.lq.flushes != 1 {
		t.Fatalf("flushes %d: lost requests not noticed", h.lq.flushes)
	}
	// The first may be held by the crossfade: it is not pushed again.
	n := 0
	for _, p := range h.lq.pushes {
		if p == held {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s pushed %d times: %v", held, n, h.lq.pushes)
	}
}

// The crossfade holds the next request, so the queue is one short of pushed. Over a
// track the planner does not know (a repeat) that must not flush and re-push it.
func TestHeldRequestIsNotLostOverUnknownTrack(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.lq.advance() // dest001 starts
	h.step()
	h.lq.current = "/fallback/other.mp3"
	h.lq.trackID++
	h.lq.queue = h.lq.queue[1:] // the crossfade pulled the next request
	h.step()
	h.step()
	if h.lq.flushes != 0 {
		t.Fatalf("flushes %d", h.lq.flushes)
	}
	seen := map[string]int{}
	for _, p := range h.lq.pushes {
		seen[p]++
		if seen[p] > 1 {
			t.Fatalf("%s pushed twice: %v", p, h.lq.pushes)
		}
	}
}

// What the fallback played counts for the repeat filter.
func TestFallbackPlayNotedForRepeatFilter(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	h.lq.queue = nil
	h.lq.advance() // ident starts; /fallback/ident.mp3 is a fallback track
	h.lq.current = "/fallback/moby.mp3"
	h.step()
	for i := 0; i < 12; i++ { // play on until the planner routes a new leg
		h.lq.advance()
		h.step()
	}
	found := false
	for _, e := range h.r.lastExclude {
		if e == "/fallback/moby.mp3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("exclude %v lacks the fallback track", h.r.lastExclude)
	}
}

func TestLostPushReplyIsNotPushedTwiceNorCalledFallback(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.lq.lostReply = true
	h.step()
	if len(h.lq.pushes) != 1 {
		t.Fatalf("pushes %v", h.lq.pushes)
	}
	h.lq.lostReply = false
	h.step()
	for _, p := range h.lq.pushes[1:] {
		if p == h.lq.pushes[0] {
			t.Fatalf("pushed twice: %v", h.lq.pushes)
		}
	}
	h.lq.advance() // dest001 starts
	h.step()
	hist, _ := h.st.History(ctx, 10)
	if len(hist) != 1 || hist[0].Reason != "start" {
		t.Fatalf("history %+v", hist)
	}
}

// The track mark can come without metadata; the filename shows up on a later poll.
func TestLateMetadataMatchesPlannedTrack(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.step()
	want := h.lq.queue[0]
	h.lq.advance()
	h.lq.current = "" // the mark carried no metadata
	h.step()
	h.lq.current = want // metadata arrived, same track id
	h.step()
	if now := h.d.Now(); now.Playing.Path != want || now.Playing.Reason == "fallback" {
		t.Fatalf("now %+v", now.Playing)
	}
}
