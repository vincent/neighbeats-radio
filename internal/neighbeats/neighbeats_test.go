package neighbeats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fake serves h on a Unix socket in a temp dir.
func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	// Unix socket paths are short; t.TempDir can be too long on some systems.
	dir, err := os.MkdirTemp("", "nb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return New(sock, 5*time.Second)
}

func TestHealth(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"version":"1.2.0","tracks":25125,"lyric_vectors":9170,"loaded_at":"x"}`)
	})
	h, err := c.Health(context.Background())
	if err != nil || h.Version != "1.2.0" || h.Tracks != 25125 {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestPathFound(t *testing.T) {
	var got PathRequest
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/path" || r.Method != "POST" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"found":true,"cost":4.2,"notes":["n"],"tracks":[
			{"item_id":null,"path":"/m/a.mp3","artist":"A","title":"T","cosine":0.62,"reason":"similarity 0.71"},
			{"item_id":"x1","path":"/m/b.mp3","artist":"B","title":"U","cosine":0.5,"reason":"r","duration":201.5}]}`)
	})
	res, err := c.Path(context.Background(), PathRequest{
		From: Ref{Path: "/m/a.mp3"}, To: Ref{ItemID: "x1", Path: "/m/b.mp3"}, Length: 8, Mode: "comfort", MaxMinutes: 10,
	})
	if err != nil || !res.Found || len(res.Tracks) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if got.Length != 8 || got.To.ItemID != "x1" || got.From.ItemID != "" {
		t.Fatalf("request lost: %+v", got)
	}
	if res.Tracks[0].ItemID != nil || res.Tracks[1].Ref().ItemID != "x1" || res.Tracks[1].Duration != 201.5 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	if res.Tracks[0].Ref() != (Ref{Path: "/m/a.mp3"}) {
		t.Fatalf("ref: %+v", res.Tracks[0].Ref())
	}
}

func TestPathNoRouteIsNotAnError(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		io.WriteString(w, `{"found":false,"tracks":[{"path":"/m/a.mp3"}],"error":"no route found","why":"broke at X"}`)
	})
	res, err := c.Path(context.Background(), PathRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || res.Why != "broke at X" || len(res.Tracks) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestErrors(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/path":
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"unknown track"}`)
		default:
			w.WriteHeader(500)
			io.WriteString(w, `{"error":"boom"}`)
		}
	})
	if _, err := c.Path(context.Background(), PathRequest{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := c.Similar(context.Background(), SimilarRequest{}); err == nil {
		t.Fatal("want 500 error")
	}
}

func TestSocketDown(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "none.sock"), time.Second)
	if _, err := c.Health(context.Background()); err == nil {
		t.Fatal("want dial error")
	}
}

func TestPickSearchAndOldServer(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/pick":
			io.WriteString(w, `{"tracks":[{"item_id":null,"path":"/m/a.mp3","artist":"A","title":"T","duration":10}],"notes":["n"]}`)
		case "/v1/search":
			io.WriteString(w, `{"tracks":[{"item_id":"x","path":"/m/a.mp3","artist":"A","title":"T"},{"item_id":null,"path":"/m/b.mp3","artist":"B","title":"U"}],"ambiguous":true}`)
		default:
			http.NotFound(w, r)
		}
	})
	p, err := c.Pick(context.Background(), PickRequest{Count: 1})
	if err != nil || len(p.Tracks) != 1 || p.Tracks[0].Duration != 10 {
		t.Fatalf("%+v %v", p, err)
	}
	s, err := c.Search(context.Background(), "a b", 5)
	if err != nil || !s.Ambiguous || len(s.Tracks) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := c.Similar(context.Background(), SimilarRequest{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("404 maps to ErrNotFound: %v", err)
	}
}
