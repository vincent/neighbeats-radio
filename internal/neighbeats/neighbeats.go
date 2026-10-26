// Package neighbeats is the client for `neighbeats serve` (docs/api.md in the
// neighbeats repo). Types are copied, not shared: the API is the contract.
package neighbeats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Ref names a track. Send both fields when both are known.
type Ref struct {
	ItemID string `json:"item_id,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Track is a track in an answer. ItemID is nil when neighbeats does not know the
// Jellyfin item. Cosine and Reason describe the hop from the previous track.
// Duration (seconds) is zero until neighbeats adds it (N1).
type Track struct {
	ItemID   *string `json:"item_id"`
	Path     string  `json:"path"`
	Artist   string  `json:"artist"`
	Title    string  `json:"title"`
	Cosine   float64 `json:"cosine"`
	Reason   string  `json:"reason"`
	Duration float64 `json:"duration,omitempty"`
}

func (t Track) Ref() Ref {
	r := Ref{Path: t.Path}
	if t.ItemID != nil {
		r.ItemID = *t.ItemID
	}
	return r
}

type Health struct {
	Version      string `json:"version"`
	Tracks       int    `json:"tracks"`
	LyricVectors int    `json:"lyric_vectors"`
	LoadedAt     string `json:"loaded_at"`
}

type PathRequest struct {
	From         Ref      `json:"from"`
	To           Ref      `json:"to"`
	Length       int      `json:"length,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	User         string   `json:"user,omitempty"`
	Theme        string   `json:"theme,omitempty"`
	AvoidArtists []string `json:"avoid_artists,omitempty"`
	MaxMinutes   int      `json:"max_minutes,omitempty"`
}

// PathResult is an answer to /v1/path. When Found is false, Tracks holds the
// route as far as it got and Why says where the search broke; this is a normal
// outcome, not an error.
type PathResult struct {
	Found  bool     `json:"found"`
	Tracks []Track  `json:"tracks"`
	Cost   float64  `json:"cost"`
	Notes  []string `json:"notes"`
	Error  string   `json:"error"`
	Why    string   `json:"why"`
}

type SimilarRequest struct {
	Item         Ref `json:"item"`
	Count        int `json:"count,omitempty"`
	MaxPerArtist int `json:"max_per_artist,omitempty"`
	MaxMinutes   int `json:"max_minutes,omitempty"`
}

type SimilarResult struct {
	Tracks []Track  `json:"tracks"`
	Notes  []string `json:"notes"`
}

// ErrNotFound is returned for 404: track unknown or not analysed.
var ErrNotFound = errors.New("neighbeats: track unknown or not analysed")

type Client struct {
	http *http.Client
}

// New dials the Unix socket for every request. neighbeats answers one request at
// a time, so connections are not kept.
func New(socket string, timeout time.Duration) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	return &Client{http: &http.Client{Transport: tr, Timeout: timeout}}
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	_, err := c.do(ctx, http.MethodGet, "/v1/health", nil, &h)
	return h, err
}

// Path asks for a route. A 422 "no route" answer is returned with Found=false and
// a nil error.
func (c *Client) Path(ctx context.Context, req PathRequest) (PathResult, error) {
	var r PathResult
	code, err := c.do(ctx, http.MethodPost, "/v1/path", req, &r)
	if err != nil {
		return r, err
	}
	if code == http.StatusUnprocessableEntity {
		r.Found = false
	}
	return r, nil
}

func (c *Client) Similar(ctx context.Context, req SimilarRequest) (SimilarResult, error) {
	var r SimilarResult
	_, err := c.do(ctx, http.MethodPost, "/v1/similar", req, &r)
	return r, err
}

// do returns the status code. 200 and 422 decode into out; anything else is an error.
func (c *Client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://neighbeats"+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("neighbeats %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("neighbeats %s: %w", path, err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusUnprocessableEntity:
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("neighbeats %s: bad answer: %w", path, err)
		}
		return resp.StatusCode, nil
	case http.StatusNotFound:
		return resp.StatusCode, fmt.Errorf("%w (%s)", ErrNotFound, apiError(raw))
	}
	return resp.StatusCode, fmt.Errorf("neighbeats %s: %d: %s", path, resp.StatusCode, apiError(raw))
}

func apiError(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	return string(bytes.TrimSpace(raw))
}
