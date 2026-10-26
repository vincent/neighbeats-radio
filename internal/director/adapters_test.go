package director

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

	r := NeighbeatsRouter{Client: neighbeats.New(sock, 5*time.Second), Mode: "comfort", User: "v", MaxMinutes: 10}
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
