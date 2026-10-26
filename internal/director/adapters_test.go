package director

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vincent/neighbeats-radio/internal/neighbeats"
	"github.com/vincent/neighbeats-radio/internal/plan"
)

func TestSeedPicker(t *testing.T) {
	s := SeedPicker{Paths: []string{"/a", "/b", "/c"}, Rand: func(int) int { return 0 }}
	got, err := s.Pick(ctx, []string{"/a"})
	if err != nil || got.Path != "/b" {
		t.Fatalf("%+v %v", got, err)
	}
	got, _ = s.Pick(ctx, []string{"/a", "/b", "/c"})
	if got.Path != "/a" {
		t.Fatalf("all excluded should repeat rather than stall: %+v", got)
	}
	if _, err := (SeedPicker{}).Pick(ctx, nil); err == nil {
		t.Fatal("want error without seeds")
	}
}

func TestNeighbeatsRouter(t *testing.T) {
	dir, _ := os.MkdirTemp("", "nb")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got neighbeats.PathRequest
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"found":true,"tracks":[{"item_id":"i1","path":"/m/a.mp3","artist":"A","title":"T","cosine":0.7,"reason":"r","duration":90.5}]}`)
	})}
	go srv.Serve(l)
	defer srv.Close()

	r := &NeighbeatsRouter{Client: neighbeats.New(sock, 5*time.Second), Mode: "comfort", User: "v", MaxMinutes: 10}
	res, err := r.Route(ctx, plan.Track{Path: "/m/from.mp3", ItemID: "f"}, plan.Track{Path: "/m/to.mp3"}, 9, nil)
	if err != nil || !res.Found || len(res.Tracks) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	tr := res.Tracks[0]
	if tr.ItemID != "i1" || tr.Duration != 90500*time.Millisecond || tr.Cosine != 0.7 {
		t.Fatalf("%+v", tr)
	}
	if got.From.ItemID != "f" || got.To.Path != "/m/to.mp3" || got.Length != 9 || got.Mode != "comfort" || got.User != "v" || got.MaxMinutes != 10 {
		t.Fatalf("request %+v", got)
	}
}

// serveFake serves h on a Unix socket and returns a client for it.
func serveFake(t *testing.T, h http.HandlerFunc) *neighbeats.Client {
	t.Helper()
	dir, _ := os.MkdirTemp("", "nb")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return neighbeats.New(sock, 5*time.Second)
}

func TestRouterSendsExcludeAndDropsItForOldServers(t *testing.T) {
	var bodies []string
	c := serveFake(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 { // a pre-N1 server rejects the unknown field
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"request body: json: unknown field \"exclude\""}`)
			return
		}
		io.WriteString(w, `{"found":true,"tracks":[{"item_id":null,"path":"/m/a.mp3","artist":"A","title":"T"}]}`)
	})
	r := &NeighbeatsRouter{Client: c}
	for range 2 {
		res, err := r.Route(ctx, plan.Track{Path: "/f"}, plan.Track{Path: "/t"}, 5, []string{"/old1", "/old2"})
		if err != nil || !res.Found {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if len(bodies) != 3 || !strings.Contains(bodies[0], `"exclude":[{"path":"/old1"},{"path":"/old2"}]`) ||
		strings.Contains(bodies[1], "exclude") || strings.Contains(bodies[2], "exclude") {
		t.Errorf("bodies: %q", bodies)
	}
}

func TestNeighbeatsPicker(t *testing.T) {
	var got neighbeats.PickRequest
	c := serveFake(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"tracks":[{"item_id":"i9","path":"/m/p.mp3","artist":"P","title":"Q","duration":180}]}`)
	})
	p := NeighbeatsPicker{Client: c, Mode: "comfort", User: "v", MaxMinutes: 10}
	tr, err := p.Pick(ctx, []string{"/m/x.mp3"})
	if err != nil || tr.Path != "/m/p.mp3" || tr.ItemID != "i9" || tr.Duration != 180*time.Second || tr.Reason != "pick" {
		t.Fatalf("%+v %v", tr, err)
	}
	if got.Count != 1 || got.Mode != "comfort" || len(got.Exclude) != 1 || got.Exclude[0].Path != "/m/x.mp3" {
		t.Errorf("request: %+v", got)
	}

	// An old server answers 404: seeds take over, or the error shows when there are none.
	old := serveFake(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	p = NeighbeatsPicker{Client: old, Fallback: SeedPicker{Paths: []string{"/seed"}, Rand: func(int) int { return 0 }}}
	if tr, err := p.Pick(ctx, nil); err != nil || tr.Path != "/seed" {
		t.Errorf("fallback: %+v %v", tr, err)
	}
	if _, err := (NeighbeatsPicker{Client: old}).Pick(ctx, nil); err == nil {
		t.Error("want error without pick and without seeds")
	}
}
