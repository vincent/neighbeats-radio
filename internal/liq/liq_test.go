package liq

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/control/status" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"track_id":7,"queued":1,"current":{"filename":"/m/a.mp3","artist":"A","title":"T"}}`)
	}))
	defer srv.Close()
	s, err := New(srv.URL+"/", time.Second).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.TrackID != 7 || s.Queued != 1 || s.Current.Artist != "A" || s.Current.Filename != "/m/a.mp3" {
		t.Fatalf("got %+v", s)
	}
}

func TestPushSendsRawPath(t *testing.T) {
	const path = "/media/music/Café déjà vu/track #4 (live).mp3"
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		if r.Method != "POST" || r.URL.Path != "/control/push" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"pushed":"x","queued":2}`)
	}))
	defer srv.Close()
	res, err := New(srv.URL, time.Second).Push(context.Background(), path, nil)
	if err != nil || res.Queued != 2 || res.Duplicate != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got != path {
		t.Fatalf("body %q", got)
	}
}

func TestSkipAndFlush(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second)
	if err := c.Skip(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "POST /control/skip,POST /control/flush" {
		t.Fatalf("got %v", paths)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/control/push" {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"no such file: /x"}`)
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second)
	if _, err := c.Push(context.Background(), "/x", nil); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("got %v", err)
	}
	srv.Close()
	if err := c.Skip(context.Background()); err == nil {
		t.Fatal("want connection error")
	}
}

func TestPushSendsMetadataAsQuery(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		io.WriteString(w, `{"pushed":"x","queued":1}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL, time.Second).Push(context.Background(), "/m/a.mp3", map[string]string{"liq_cross_duration": "6.5"})
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("liq_cross_duration") != "6.5" {
		t.Fatalf("query %v", query)
	}
}

func TestPushReportsDuplicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"pushed":"/m/a.mp3","queued":1,"duplicate":"lookahead"}`)
	}))
	defer srv.Close()
	res, err := New(srv.URL, time.Second).Push(context.Background(), "/m/a.mp3", nil)
	if err != nil || res.Duplicate != DupLookahead {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestStatusReadsLookahead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"track_id":3,"queued":0,"lookahead":"/m/b.mp3","current":{"filename":"/m/a.mp3","artist":"A","title":"T"}}`)
	}))
	defer srv.Close()
	s, err := New(srv.URL, time.Second).Status(context.Background())
	if err != nil || s.Lookahead != "/m/b.mp3" {
		t.Fatalf("s=%+v err=%v", s, err)
	}
}
