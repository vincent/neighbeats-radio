// Package liq is the client for the control endpoints of radio.liq.
package liq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Track struct {
	Filename string `json:"filename"`
	Artist   string `json:"artist"`
	Title    string `json:"title"`
}

// Status is Liquidsoap's view. TrackID increments on every track start, so a
// change means a new track began. Queued counts requests waiting behind the
// one playing.
type Status struct {
	TrackID int   `json:"track_id"`
	Queued  int   `json:"queued"`
	Current Track `json:"current"`
}

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout}}
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, "/control/status", "", &s)
	return s, err
}

// Push queues an absolute path. It returns the queue length after the push.
func (c *Client) Push(ctx context.Context, path string) (int, error) {
	var out struct {
		Queued int `json:"queued"`
	}
	err := c.do(ctx, http.MethodPost, "/control/push", path, &out)
	return out.Queued, err
}

func (c *Client) Skip(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/control/skip", "", nil)
}

func (c *Client) Flush(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/control/flush", "", nil)
}

func (c *Client) do(ctx context.Context, method, path, body string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	if body != "" {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("liquidsoap %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("liquidsoap %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("liquidsoap %s: %d: %s", path, resp.StatusCode, e.Error)
		}
		return fmt.Errorf("liquidsoap %s: %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("liquidsoap %s: bad answer: %w", path, err)
	}
	return nil
}
