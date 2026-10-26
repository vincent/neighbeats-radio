package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func tr(path string) Track { return Track{Path: path, Artist: "A", Title: path, Reason: "similarity"} }

// fakeRouter answers legs with n tracks named after the destination, starting with
// the from track (as neighbeats does).
type fakeRouter struct {
	calls    []string
	exclude  [][]string
	lengths  []int
	notFound map[string]bool // destination path -> found:false
	err      error
	extra    map[string][]Track // destination path -> tracks replacing the generated ones
}

func (f *fakeRouter) Route(_ context.Context, from, to Track, length int, exclude []string) (Route, error) {
	f.calls = append(f.calls, from.Path+"->"+to.Path)
	f.exclude = append(f.exclude, exclude)
	f.lengths = append(f.lengths, length)
	if f.err != nil {
		return Route{}, f.err
	}
	if f.notFound[to.Path] {
		return Route{Found: false, Why: "broke at X", Tracks: []Track{from}}, nil
	}
	if t, ok := f.extra[to.Path]; ok {
		return Route{Found: true, Tracks: t}, nil
	}
	out := []Track{from}
	for i := 1; i < length; i++ {
		out = append(out, tr(fmt.Sprintf("%s#%d", to.Path, i)))
	}
	to.Reason = "similarity"
	return Route{Found: true, Tracks: append(out, to)}, nil
}

type fakePicker struct {
	paths []string
	n     int
	err   error
	seen  [][]string
}

func (f *fakePicker) Pick(_ context.Context, exclude []string) (Track, error) {
	f.seen = append(f.seen, exclude)
	if f.err != nil {
		return Track{}, f.err
	}
	p := f.paths[f.n%len(f.paths)]
	f.n++
	return Track{Path: p, Artist: "D", Title: p}, nil
}

func cfg() Config {
	return Config{LegMin: 3, LegMax: 3, LookAhead: 20 * time.Minute, RepeatWindow: 8 * time.Hour, AssumedTrack: 4 * time.Minute}
}

func paths(items []Item) string {
	var s []string
	for _, it := range items {
		s = append(s, it.Path)
	}
	return strings.Join(s, " ")
}

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func TestFillStartsThenChainsLegs(t *testing.T) {
	r := &fakeRouter{}
	pk := &fakePicker{paths: []string{"/s", "/d1", "/d2", "/d3"}}
	p := New(cfg(), r, pk, nil, func(int) int { return 0 })
	n, err := p.Fill(context.Background(), t0)
	if err != nil {
		t.Fatal(err)
	}
	// start (1) + legs of 3 tracks, from track dropped = 2 new each; 5 tracks needed for 20 min.
	if p.Lookahead() < 20*time.Minute {
		t.Fatalf("lookahead %v", p.Lookahead())
	}
	items := p.Items()
	if len(items) != n {
		t.Fatalf("added %d, have %d", n, len(items))
	}
	if items[0].Path != "/s" || items[0].Reason != "start" {
		t.Fatalf("first: %+v", items[0])
	}
	// second leg routes from the last track of the first leg.
	if r.calls[0] != "/s->/d1" || r.calls[1] != "/d1->/d2" {
		t.Fatalf("calls %v", r.calls)
	}
	if items[1].Leg != 2 || items[len(items)-1].Leg < 3 {
		t.Fatalf("legs: %+v", items)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Path == items[i-1].Path {
			t.Fatalf("duplicate: %s", paths(items))
		}
	}
}

func TestFillIsIdempotentWhenFull(t *testing.T) {
	r := &fakeRouter{}
	p := New(cfg(), r, &fakePicker{paths: []string{"/s", "/d1", "/d2", "/d3"}}, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	calls := len(r.calls)
	n, err := p.Fill(context.Background(), t0)
	if err != nil || n != 0 || len(r.calls) != calls {
		t.Fatalf("n=%d err=%v calls=%d->%d", n, err, calls, len(r.calls))
	}
}

func TestDurationsCountForLookahead(t *testing.T) {
	r := &fakeRouter{extra: map[string][]Track{"/d1": {tr("/s"), {Path: "/long", Duration: 30 * time.Minute}}}}
	p := New(cfg(), r, &fakePicker{paths: []string{"/s", "/d1"}}, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	if got := paths(p.Items()); got != "/s /long" {
		t.Fatalf("got %s", got)
	}
	if len(r.calls) != 1 {
		t.Fatalf("a 30 minute track should fill the look-ahead, calls=%v", r.calls)
	}
}

func TestRepeatFilterAndExclusions(t *testing.T) {
	r := &fakeRouter{extra: map[string][]Track{
		"/d1": {tr("/s"), tr("/old"), tr("/a"), tr("/a"), tr("/d1")},
	}}
	c := cfg()
	c.LookAhead = 10 * time.Minute
	p := New(c, r, &fakePicker{paths: []string{"/s", "/d1"}}, nil, func(int) int { return 0 })
	p.NotePlayed("/old", "", "", t0.Add(-time.Hour))
	p.NotePlayed("/ancient", "", "", t0.Add(-9*time.Hour))
	p.Fill(context.Background(), t0)
	if got := paths(p.Items()); got != "/s /a /d1" {
		t.Fatalf("got %q", got)
	}
	ex := strings.Join(r.exclude[0], " ")
	if !strings.Contains(ex, "/old") || strings.Contains(ex, "/ancient") || !strings.Contains(ex, "/s") {
		t.Fatalf("exclude %q", ex)
	}
}

func TestNoRouteTriesAnotherDestination(t *testing.T) {
	r := &fakeRouter{notFound: map[string]bool{"/bad": true}}
	pk := &fakePicker{paths: []string{"/s", "/bad", "/good"}}
	p := New(cfg(), r, pk, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	if r.calls[0] != "/s->/bad" || r.calls[1] != "/s->/good" {
		t.Fatalf("calls %v", r.calls)
	}
	for _, it := range p.Items() {
		if strings.HasPrefix(it.Path, "/bad") {
			t.Fatalf("bad route used: %s", paths(p.Items()))
		}
	}
}

func TestEveryDestinationFailsJumps(t *testing.T) {
	r := &fakeRouter{notFound: map[string]bool{"/x": true, "/y": true}}
	c := cfg()
	c.MaxAttempts = 2
	c.LookAhead = 8 * time.Minute
	pk := &fakePicker{paths: []string{"/s", "/x", "/y"}}
	p := New(c, r, pk, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	items := p.Items()
	if len(items) != 2 || items[1].Path != "/y" || items[1].Reason != "fallback" {
		t.Fatalf("got %+v", items)
	}
}

func TestSteeredFirstWithReason(t *testing.T) {
	r := &fakeRouter{}
	p := New(cfg(), r, &fakePicker{paths: []string{"/s", "/d1"}}, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	p.TakeForPush(100)
	p.Started("/s", t0)
	p.items = p.items[:1] // only /s left, playing
	before := len(r.calls)
	p.Steer(Track{Path: "/target"})
	p.Fill(context.Background(), t0)
	if r.calls[before] != "/s->/target" {
		t.Fatalf("calls %v", r.calls[before:])
	}
	var found bool
	for _, it := range p.Items() {
		found = found || it.Path == "/target"
	}
	if !found {
		t.Fatalf("target not queued: %s", paths(p.Items()))
	}
}

func TestErrorsPropagate(t *testing.T) {
	boom := errors.New("socket down")
	p := New(cfg(), &fakeRouter{err: boom}, &fakePicker{paths: []string{"/s", "/d"}}, nil, func(int) int { return 0 })
	if _, err := p.Fill(context.Background(), t0); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if len(p.Items()) != 1 {
		t.Fatalf("start track should stay planned: %v", p.Items())
	}
	p2 := New(cfg(), &fakeRouter{}, &fakePicker{err: boom}, nil, nil)
	if _, err := p2.Fill(context.Background(), t0); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestPushStartedAndSkips(t *testing.T) {
	p := New(cfg(), &fakeRouter{}, &fakePicker{paths: []string{"/s", "/d1", "/d2"}}, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	took := p.TakeForPush(3)
	if len(took) != 3 || p.Pushed() != 3 {
		t.Fatalf("took %d pushed %d", len(took), p.Pushed())
	}
	if again := p.TakeForPush(1); again[0].Path == took[0].Path {
		t.Fatal("pushed twice")
	}
	// The second pushed track starts: the first was skipped.
	second := took[1].Path
	it, ok := p.Started(second, t0.Add(time.Minute))
	if !ok || it.State != Playing || it.Path != second {
		t.Fatalf("%+v %v", it, ok)
	}
	for _, i := range p.Items() {
		if i.Path == took[0].Path {
			t.Fatal("skipped item not compacted")
		}
	}
	if _, ok := p.Started("/not/ours.mp3", t0); ok {
		t.Fatal("unknown path must not match")
	}
	// next track starts: previous becomes played and leaves the queue.
	p.Started(took[2].Path, t0.Add(2*time.Minute))
	if p.Items()[0].Path != took[2].Path {
		t.Fatalf("head %+v", p.Items()[0])
	}
	// repeat filter now knows both.
	ex := p.exclusions(t0.Add(3*time.Minute), false)
	if !strings.Contains(strings.Join(ex, " "), second) {
		t.Fatalf("exclusions %v", ex)
	}
}

func TestRequeueAndRestore(t *testing.T) {
	p := New(cfg(), &fakeRouter{}, &fakePicker{paths: []string{"/s", "/d1"}}, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	p.TakeForPush(2)
	p.Requeue()
	if p.Pushed() != 0 {
		t.Fatal("requeue left pushed items")
	}
	saved := p.Items()
	saved[0].State = Played
	saved[1].State = Pushed
	q := New(cfg(), &fakeRouter{}, &fakePicker{paths: []string{"/s", "/d1"}}, nil, func(int) int { return 0 })
	q.Restore(saved)
	if len(q.Items()) != len(saved)-1 || q.Items()[0].State != Planned {
		t.Fatalf("restore: %+v", q.Items())
	}
	if q.nextLeg <= saved[len(saved)-1].Leg {
		t.Fatalf("nextLeg %d", q.nextLeg)
	}
}

func TestFadesFor(t *testing.T) {
	f := DefaultFades
	for _, c := range []struct {
		cos  float64
		want time.Duration
	}{
		{0, f.Mid}, {0.95, f.Close}, {0.8, f.Close}, {0.79, f.Mid}, {0.5, f.Mid}, {0.49, f.Far}, {-0.2, f.Far},
	} {
		if got := f.For(c.cos); got != c.want {
			t.Errorf("For(%v) = %v, want %v", c.cos, got, c.want)
		}
	}
}

// planWithPushed returns a planner whose first two items are pushed (the first playing).
func planWithPushed(t *testing.T, r *fakeRouter) *Planner {
	t.Helper()
	p := New(cfg(), r, &fakePicker{paths: []string{"/s", "/d1", "/d2"}}, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	took := p.TakeForPush(2)
	p.Started(took[0].Path, t0)
	return p
}

func TestSteerNowReplacesPlannedTailOnly(t *testing.T) {
	r := &fakeRouter{}
	p := planWithPushed(t, r)
	before := p.Items()
	var pushed []string
	for _, it := range before {
		if it.State == Playing || it.State == Pushed {
			pushed = append(pushed, it.Path)
		}
	}
	res, err := p.SteerNow(context.Background(), Track{Path: "/target"}, t0)
	if err != nil || !res.Found {
		t.Fatalf("res %+v err %v", res, err)
	}
	after := p.Items()
	var live, tail []Item
	for _, it := range after {
		if it.State == Planned {
			tail = append(tail, it)
		} else {
			live = append(live, it)
		}
	}
	if len(live) != len(pushed) {
		t.Fatalf("playing/pushed items changed: %s", paths(after))
	}
	for i, it := range live {
		if it.Path != pushed[i] {
			t.Fatalf("live %d is %s, want %s", i, it.Path, pushed[i])
		}
	}
	if len(tail) == 0 || tail[len(tail)-1].Path != "/target" || !strings.HasPrefix(tail[len(tail)-1].Reason, "steered") {
		t.Fatalf("tail %+v", tail)
	}
	if !strings.HasSuffix(r.calls[len(r.calls)-1], "->/target") || !strings.HasPrefix(r.calls[len(r.calls)-1], pushed[len(pushed)-1]) {
		t.Fatalf("routed %v, want from last pushed %s", r.calls, pushed[len(pushed)-1])
	}
}

func TestSteerNowNotFoundKeepsPlan(t *testing.T) {
	r := &fakeRouter{notFound: map[string]bool{"/target": true}}
	p := planWithPushed(t, r)
	before := paths(p.Items())
	res, err := p.SteerNow(context.Background(), Track{Path: "/target"}, t0)
	if err != nil || res.Found || res.Why == "" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if got := paths(p.Items()); got != before {
		t.Fatalf("plan changed:\n%s\n%s", before, got)
	}
}

func TestSteerNowNeighbeatsDownKeepsPlan(t *testing.T) {
	r := &fakeRouter{}
	p := planWithPushed(t, r)
	before := paths(p.Items())
	r.err = errors.New("socket down")
	if _, err := p.SteerNow(context.Background(), Track{Path: "/target"}, t0); err == nil {
		t.Fatal("want error")
	}
	if got := paths(p.Items()); got != before {
		t.Fatalf("plan changed:\n%s\n%s", before, got)
	}
}

func TestSteerNowWithNothingPushedStartsAtTarget(t *testing.T) {
	p := New(cfg(), &fakeRouter{}, &fakePicker{paths: []string{"/s"}}, nil, nil)
	p.Fill(context.Background(), t0)
	res, err := p.SteerNow(context.Background(), Track{Path: "/target"}, t0)
	if err != nil || !res.Found {
		t.Fatalf("res %+v err %v", res, err)
	}
	if got := paths(p.Items()); got != "/target" {
		t.Fatalf("items %s", got)
	}
}

func TestSkipPlaying(t *testing.T) {
	p := planWithPushed(t, &fakeRouter{})
	playing := ""
	for _, it := range p.Items() {
		if it.State == Playing {
			playing = it.Path
		}
	}
	it, ok := p.SkipPlaying()
	if !ok || it.Path != playing {
		t.Fatalf("%+v %v", it, ok)
	}
	for _, i := range p.Items() {
		if i.State == Playing || i.Path == playing {
			t.Fatalf("still there: %+v", i)
		}
	}
	if _, ok := p.SkipPlaying(); ok {
		t.Fatal("nothing playing now")
	}
}

// The same song under another path (a copy in the fallback folder) counts as heard.
func TestSameSongOtherPathIsNotRepeated(t *testing.T) {
	r := &fakeRouter{}
	pk := &fakePicker{paths: []string{"/s", "/dup", "/d2", "/d3", "/d4"}}
	p := New(cfg(), r, pk, nil, func(int) int { return 0 })
	// "/dup" is titled "/dup" by the fake picker: say its twin under /fallback was heard.
	p.NotePlayed("/fallback/x.mp3", "D", "/dup", t0.Add(-time.Minute))
	if _, err := p.Fill(context.Background(), t0); err != nil {
		t.Fatal(err)
	}
	for _, it := range p.Items() {
		if strings.HasPrefix(it.Path, "/dup") && it.Title == "/dup" {
			t.Fatalf("heard song planned again: %s", paths(p.Items()))
		}
	}
}

func TestStartedMatchesPlannedItem(t *testing.T) {
	p := New(cfg(), &fakeRouter{}, &fakePicker{paths: []string{"/s", "/d1", "/d2"}}, nil, func(int) int { return 0 })
	p.Fill(context.Background(), t0)
	head := p.Items()[0].Path // planned, never marked pushed: its push reply was lost
	it, ok := p.Started(head, t0)
	if !ok || it.State != Playing {
		t.Fatalf("%+v %v", it, ok)
	}
}
