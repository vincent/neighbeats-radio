package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vincent/neighbeats-radio/internal/director"
	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/state"
)

type fakeBackend struct {
	skips, pauses, resumes int
	steered                []plan.Track
	steerRes               plan.SteerResult
	snap                   director.Snapshot
}

func (f *fakeBackend) Now() director.Snapshot { return f.snap }
func (f *fakeBackend) Queue() []plan.Item {
	return []plan.Item{{Track: plan.Track{Path: "/m/a", Artist: "A", Title: "a"}, Leg: 1, State: plan.Planned}}
}
func (f *fakeBackend) History(_ context.Context, n int) ([]state.Play, error) {
	return []state.Play{{Artist: "H", Title: "h", Outcome: state.Skipped, StartedAt: time.Unix(1, 0)}}, nil
}
func (f *fakeBackend) Skip(context.Context) error  { f.skips++; return nil }
func (f *fakeBackend) Pause(context.Context) error { f.pauses++; return nil }
func (f *fakeBackend) Resume()                     { f.resumes++ }
func (f *fakeBackend) Steer(_ context.Context, d plan.Track) (plan.SteerResult, error) {
	f.steered = append(f.steered, d)
	return f.steerRes, nil
}

type fakeResolver struct {
	tracks    []plan.Track
	ambiguous bool
}

func (f fakeResolver) Resolve(context.Context, string) ([]plan.Track, bool, error) {
	return f.tracks, f.ambiguous, nil
}

func start(t *testing.T, b *fakeBackend, r fakeResolver) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "nr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := &Server{B: b, R: r, Now: func() time.Time { return time.Unix(100, 0) }}
	go srv.Serve(ctx, sock)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return NewClient(sock, 2*time.Second)
}

func TestNowQueueHistoryOverSocket(t *testing.T) {
	b := &fakeBackend{snap: director.Snapshot{
		Playing:   &state.Play{Artist: "P", Title: "p", Reason: "similarity", Cosine: 0.7, StartedAt: time.Unix(40, 0)},
		Remaining: 90 * time.Second, Ahead: 20 * time.Minute,
		Next: []plan.Item{{Track: plan.Track{Artist: "N", Title: "n"}, State: plan.Pushed}},
	}}
	c := start(t, b, fakeResolver{})
	now, err := c.Now(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if now.Playing == nil || now.Playing.Artist != "P" || now.Elapsed != 60 || now.Remaining != 90 || len(now.Next) != 1 {
		t.Fatalf("now %+v", now)
	}
	q, err := c.Queue(context.Background())
	if err != nil || len(q.Items) != 1 || q.Items[0].State != "planned" || q.Ahead != 1200 {
		t.Fatalf("queue %+v err %v", q, err)
	}
	h, err := c.History(context.Background(), 5)
	if err != nil || len(h.Plays) != 1 || h.Plays[0].Outcome != state.Skipped {
		t.Fatalf("history %+v err %v", h, err)
	}
}

func TestSkipPauseResume(t *testing.T) {
	b := &fakeBackend{}
	c := start(t, b, fakeResolver{})
	for _, f := range []func(context.Context) error{c.Skip, c.Pause, c.Resume} {
		if err := f(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if b.skips != 1 || b.pauses != 1 || b.resumes != 1 {
		t.Fatalf("%+v", b)
	}
}

func TestSteerOutcomes(t *testing.T) {
	one := []plan.Track{{Path: "/m/sea.mp3", Artist: "S", Title: "Sea"}}
	two := append(append([]plan.Track(nil), one...), plan.Track{Path: "/m/sea2.mp3", Artist: "S", Title: "Sea 2"})
	ctx := context.Background()

	b := &fakeBackend{steerRes: plan.SteerResult{Found: true, Tracks: 9}}
	r, err := start(t, b, fakeResolver{tracks: one}).Steer(ctx, SteerRequest{Text: "sea"})
	if err != nil || !r.Found || r.Tracks != 9 || len(b.steered) != 1 || b.steered[0].Path != "/m/sea.mp3" {
		t.Fatalf("found: %+v err %v steered %v", r, err, b.steered)
	}

	b = &fakeBackend{steerRes: plan.SteerResult{Why: "broke at X"}}
	r, err = start(t, b, fakeResolver{tracks: one}).Steer(ctx, SteerRequest{Path: "/m/sea.mp3"})
	if err != nil || r.Found || r.Why != "broke at X" || r.Target == nil {
		t.Fatalf("no route: %+v err %v", r, err)
	}

	b = &fakeBackend{}
	r, err = start(t, b, fakeResolver{tracks: two, ambiguous: true}).Steer(ctx, SteerRequest{Text: "sea"})
	if err != nil || r.Found || len(r.Candidates) != 2 || len(b.steered) != 0 {
		t.Fatalf("ambiguous: %+v err %v", r, err)
	}

	r, err = start(t, &fakeBackend{}, fakeResolver{}).Steer(ctx, SteerRequest{Text: "nothing"})
	if err != nil || r.Found || r.Why == "" {
		t.Fatalf("no match: %+v err %v", r, err)
	}

	if _, err = start(t, &fakeBackend{}, fakeResolver{}).Steer(ctx, SteerRequest{}); err == nil {
		t.Fatal("empty steer must fail")
	}
}
