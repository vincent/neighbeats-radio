// Package control is the director's command socket: a small HTTP + JSON API on a
// Unix socket, with a client the CLI commands use. Same pattern as `neighbeats serve`.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/vincent/neighbeats-radio/internal/director"
	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/state"
)

// Backend is the part of the director the socket drives.
type Backend interface {
	Now() director.Snapshot
	Queue() []plan.Item
	History(ctx context.Context, n int) ([]state.Play, error)
	Skip(ctx context.Context) error
	Steer(ctx context.Context, dest plan.Track) (plan.SteerResult, error)
	Pause(ctx context.Context) error
	Resume()
}

// Resolver turns what the user typed into analysed tracks.
type Resolver interface {
	// Resolve returns the candidates for a query (words, MBID or path).
	// ambiguous means several plausible matches; none means an empty result.
	Resolve(ctx context.Context, query string) (tracks []plan.Track, ambiguous bool, err error)
}

// Wire types.

type TrackView struct {
	Path     string  `json:"path"`
	Artist   string  `json:"artist"`
	Title    string  `json:"title"`
	Reason   string  `json:"reason,omitempty"`
	Cosine   float64 `json:"cosine,omitempty"`
	Leg      int     `json:"leg,omitempty"`
	State    string  `json:"state,omitempty"`
	Duration float64 `json:"duration,omitempty"` // seconds
}

type NowResponse struct {
	Playing   *TrackView  `json:"playing"`
	Elapsed   float64     `json:"elapsed"`   // seconds
	Remaining float64     `json:"remaining"` // seconds, 0 = unknown
	Next      []TrackView `json:"next"`
	Ahead     float64     `json:"ahead"` // seconds planned
	Paused    bool        `json:"paused"`
}

type QueueResponse struct {
	Items []TrackView `json:"items"`
	Ahead float64     `json:"ahead"`
}

type PlayView struct {
	At      time.Time `json:"at"`
	Artist  string    `json:"artist"`
	Title   string    `json:"title"`
	Reason  string    `json:"reason"`
	Cosine  float64   `json:"cosine"`
	Leg     int       `json:"leg"`
	Outcome string    `json:"outcome"`
}

type HistoryResponse struct {
	Plays []PlayView `json:"plays"`
}

type SteerRequest struct {
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

// SteerResponse: Found means the plan was replaced. Otherwise Why says what stopped
// it, and Candidates lists the matches when the query was ambiguous.
type SteerResponse struct {
	Found      bool        `json:"found"`
	Why        string      `json:"why,omitempty"`
	Target     *TrackView  `json:"target,omitempty"`
	Tracks     int         `json:"tracks,omitempty"`
	Candidates []TrackView `json:"candidates,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func view(t plan.Track) TrackView {
	return TrackView{Path: t.Path, Artist: t.Artist, Title: t.Title, Reason: t.Reason, Cosine: t.Cosine, Duration: t.Duration.Seconds()}
}

func viewItem(it plan.Item) TrackView {
	v := view(it.Track)
	v.Leg, v.State = it.Leg, string(it.State)
	return v
}

// Server serves the API.
type Server struct {
	B Backend
	R Resolver
	// Now is for tests.
	Now func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/now", s.now)
	mux.HandleFunc("GET /v1/queue", s.queue)
	mux.HandleFunc("GET /v1/history", s.history)
	mux.HandleFunc("POST /v1/skip", s.skip)
	mux.HandleFunc("POST /v1/steer", s.steer)
	mux.HandleFunc("POST /v1/pause", s.pause)
	mux.HandleFunc("POST /v1/resume", s.resume)
	return mux
}

// Serve listens on the socket until ctx ends. A stale socket file from a crash is replaced.
func (s *Server) Serve(ctx context.Context, socket string) error {
	os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("control: %w", err)
	}
	// The socket's folder is shared with the host; let our group in, like neighbeats' socket.
	if err := os.Chmod(socket, 0o660); err != nil {
		ln.Close()
		return fmt.Errorf("control: %w", err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("control: %w", err)
	}
	return nil
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) { reply(w, code, errorResponse{err.Error()}) }

func (s *Server) now(w http.ResponseWriter, _ *http.Request) {
	snap := s.B.Now()
	out := NowResponse{Ahead: snap.Ahead.Seconds(), Paused: snap.Paused, Remaining: snap.Remaining.Seconds(), Next: []TrackView{}}
	if p := snap.Playing; p != nil {
		v := TrackView{Path: p.Path, Artist: p.Artist, Title: p.Title, Reason: p.Reason, Cosine: p.Cosine, Leg: p.Leg, Duration: snap.Duration.Seconds()}
		out.Playing = &v
		clock := time.Now
		if s.Now != nil {
			clock = s.Now
		}
		out.Elapsed = clock().Sub(p.StartedAt).Seconds()
	}
	for _, it := range snap.Next {
		out.Next = append(out.Next, viewItem(it))
	}
	reply(w, http.StatusOK, out)
}

func (s *Server) queue(w http.ResponseWriter, _ *http.Request) {
	items := s.B.Queue()
	out := QueueResponse{Items: []TrackView{}}
	for _, it := range items {
		out.Items = append(out.Items, viewItem(it))
	}
	out.Ahead = s.B.Now().Ahead.Seconds()
	reply(w, http.StatusOK, out)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	n := 30
	if v := r.URL.Query().Get("n"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 || n > 1000 {
			fail(w, http.StatusBadRequest, errors.New("n: want 1..1000"))
			return
		}
	}
	plays, err := s.B.History(r.Context(), n)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	out := HistoryResponse{Plays: []PlayView{}}
	for _, p := range plays {
		out.Plays = append(out.Plays, PlayView{At: p.StartedAt, Artist: p.Artist, Title: p.Title, Reason: p.Reason, Cosine: p.Cosine, Leg: p.Leg, Outcome: p.Outcome})
	}
	reply(w, http.StatusOK, out)
}

func (s *Server) skip(w http.ResponseWriter, r *http.Request) {
	if err := s.B.Skip(r.Context()); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	reply(w, http.StatusOK, map[string]bool{"skipped": true})
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	if err := s.B.Pause(r.Context()); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	reply(w, http.StatusOK, map[string]bool{"paused": true})
}

func (s *Server) resume(w http.ResponseWriter, _ *http.Request) {
	s.B.Resume()
	reply(w, http.StatusOK, map[string]bool{"paused": false})
}

func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
	var req SteerRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return
	}
	query := req.Path
	if query == "" {
		query = req.Text
	}
	if query == "" {
		fail(w, http.StatusBadRequest, errors.New("steer: give text or a path"))
		return
	}
	tracks, ambiguous, err := s.R.Resolve(r.Context(), query)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if len(tracks) == 0 {
		reply(w, http.StatusOK, SteerResponse{Why: fmt.Sprintf("nothing matches %q", query)})
		return
	}
	if ambiguous {
		out := SteerResponse{Why: "several tracks match, pick one with steer -path"}
		for _, t := range tracks {
			out.Candidates = append(out.Candidates, view(t))
		}
		reply(w, http.StatusOK, out)
		return
	}
	dest := tracks[0]
	res, err := s.B.Steer(r.Context(), dest)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	v := view(dest)
	reply(w, http.StatusOK, SteerResponse{Found: res.Found, Why: res.Why, Target: &v, Tracks: res.Tracks})
}

// Client talks to the socket.
type Client struct{ http *http.Client }

func NewClient(socket string, timeout time.Duration) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	return &Client{http: &http.Client{Transport: tr, Timeout: timeout}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://radio"+path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("is `neighbeats-radio run` up? %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e errorResponse
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) Now(ctx context.Context) (NowResponse, error) {
	var r NowResponse
	return r, c.do(ctx, http.MethodGet, "/v1/now", nil, &r)
}

func (c *Client) Queue(ctx context.Context) (QueueResponse, error) {
	var r QueueResponse
	return r, c.do(ctx, http.MethodGet, "/v1/queue", nil, &r)
}

func (c *Client) History(ctx context.Context, n int) (HistoryResponse, error) {
	var r HistoryResponse
	return r, c.do(ctx, http.MethodGet, "/v1/history?n="+strconv.Itoa(n), nil, &r)
}

func (c *Client) Skip(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/skip", nil, nil)
}

func (c *Client) Pause(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/pause", nil, nil)
}

func (c *Client) Resume(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/resume", nil, nil)
}

func (c *Client) Steer(ctx context.Context, req SteerRequest) (SteerResponse, error) {
	var r SteerResponse
	return r, c.do(ctx, http.MethodPost, "/v1/steer", req, &r)
}
