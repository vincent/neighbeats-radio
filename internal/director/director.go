// Package director runs the station: it keeps Liquidsoap's queue short and the
// planner's look-ahead long. It never touches audio; it is the single writer of
// what is queued.
package director

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vincent/neighbeats-radio/internal/liq"
	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/state"
)

// Liquidsoap is the part of internal/liq the director uses.
type Liquidsoap interface {
	Status(ctx context.Context) (liq.Status, error)
	Push(ctx context.Context, path string) (int, error)
	Flush(ctx context.Context) error
}

// Store is the part of internal/state the director uses.
type Store interface {
	StartPlay(ctx context.Context, p state.Play) (int64, error)
	EndPlay(ctx context.Context, id int64, outcome string) error
	ClosePlaying(ctx context.Context) error
	PlaysSince(ctx context.Context, since time.Time) ([]state.Play, error)
	SaveQueue(ctx context.Context, items []plan.Item) error
	LoadQueue(ctx context.Context) ([]plan.Item, error)
}

type Config struct {
	PollInterval  time.Duration
	PushWhenBelow int
	RepeatWindow  time.Duration
	// RetryAfter is how long to wait after a failed planning attempt (neighbeats
	// down or reloading) before asking again.
	RetryAfter time.Duration
}

type Director struct {
	cfg     Config
	lq      Liquidsoap
	planner *plan.Planner
	store   Store
	log     *slog.Logger
	now     func() time.Time

	started     bool
	lastTrackID int
	playID      int64
	retryAt     time.Time
	dirty       bool
}

func New(cfg Config, lq Liquidsoap, p *plan.Planner, st Store, log *slog.Logger) *Director {
	if cfg.RetryAfter == 0 {
		cfg.RetryAfter = 10 * time.Second
	}
	return &Director{cfg: cfg, lq: lq, planner: p, store: st, log: log, now: time.Now}
}

// Run polls until ctx ends. Errors inside a step are logged, never fatal: the
// station must keep going while neighbeats or Liquidsoap restart.
func (d *Director) Run(ctx context.Context) error {
	if err := d.init(ctx); err != nil {
		return err
	}
	t := time.NewTicker(d.cfg.PollInterval)
	defer t.Stop()
	for {
		d.Step(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// init resumes from the stored state: the repeat filter, the planned tail.
func (d *Director) init(ctx context.Context) error {
	if err := d.store.ClosePlaying(ctx); err != nil {
		return err
	}
	plays, err := d.store.PlaysSince(ctx, d.now().Add(-d.cfg.RepeatWindow))
	if err != nil {
		return err
	}
	for _, p := range plays {
		d.planner.NotePlayed(p.Path, p.StartedAt)
	}
	items, err := d.store.LoadQueue(ctx)
	if err != nil {
		return err
	}
	d.planner.Restore(items)
	d.log.Info("resumed", "planned", len(d.planner.Items()), "history", len(plays))
	return nil
}

// Step is one poll: notice a track change, repair a lost queue, plan, push.
func (d *Director) Step(ctx context.Context) {
	st, err := d.lq.Status(ctx)
	if err != nil {
		d.log.Warn("liquidsoap unreachable", "err", err)
		return
	}
	if !d.started {
		d.started = true
		d.lastTrackID = st.TrackID
		// Whatever Liquidsoap holds is unknown to us; start from a clean queue.
		if st.Queued > 0 {
			if err := d.lq.Flush(ctx); err != nil {
				d.log.Warn("flush at start", "err", err)
				d.started = false
				return
			}
			st.Queued = 0
		}
	}

	switch {
	case st.TrackID < d.lastTrackID:
		// Liquidsoap restarted: its queue is gone, ours must be pushed again.
		d.log.Warn("liquidsoap restarted", "was", d.lastTrackID, "now", st.TrackID)
		d.planner.Requeue()
		d.dirty = true
		d.lastTrackID = st.TrackID
		d.trackStarted(ctx, st.Current)
	case st.TrackID > d.lastTrackID:
		d.lastTrackID = st.TrackID
		d.trackStarted(ctx, st.Current)
	}

	if st.Queued < d.planner.Pushed() {
		d.log.Warn("liquidsoap lost queued tracks", "queued", st.Queued, "pushed", d.planner.Pushed())
		if err := d.lq.Flush(ctx); err != nil {
			d.log.Warn("flush", "err", err)
			return
		}
		d.planner.Requeue()
		st.Queued = 0
		d.dirty = true
	}

	d.fill(ctx)
	d.push(ctx, st.Queued)

	if d.dirty {
		if err := d.store.SaveQueue(ctx, d.planner.Items()); err != nil {
			d.log.Error("save queue", "err", err)
			return
		}
		d.dirty = false
	}
}

func (d *Director) trackStarted(ctx context.Context, cur liq.Track) {
	now := d.now()
	if d.playID != 0 {
		if err := d.store.EndPlay(ctx, d.playID, state.Finished); err != nil {
			d.log.Error("end play", "err", err)
		}
		d.playID = 0
	}
	it, ours := d.planner.Started(cur.Filename, now)
	if ours {
		d.dirty = true
	} else {
		// A fallback track or the ident: not planned by us, still part of history.
		it = plan.Item{Track: plan.Track{Path: cur.Filename, Artist: cur.Artist, Title: cur.Title, Reason: "fallback"}}
	}
	if it.Artist == "" {
		it.Artist = cur.Artist
	}
	if it.Title == "" {
		it.Title = cur.Title
	}
	d.log.Info("track", "artist", it.Artist, "title", it.Title, "cosine", it.Cosine, "reason", it.Reason, "leg", it.Leg)
	id, err := d.store.StartPlay(ctx, state.Play{
		StartedAt: now, Path: it.Path, ItemID: it.ItemID, Artist: it.Artist, Title: it.Title,
		Reason: it.Reason, Cosine: it.Cosine, Leg: it.Leg,
	})
	if err != nil {
		d.log.Error("record play", "err", err)
		return
	}
	d.playID = id
}

func (d *Director) fill(ctx context.Context) {
	if d.now().Before(d.retryAt) {
		return
	}
	n, err := d.planner.Fill(ctx, d.now())
	if n > 0 {
		d.dirty = true
	}
	if err != nil {
		d.log.Warn("planning failed, will retry", "err", err, "in", d.cfg.RetryAfter)
		d.retryAt = d.now().Add(d.cfg.RetryAfter)
	}
}

func (d *Director) push(ctx context.Context, queued int) {
	want := d.cfg.PushWhenBelow - queued
	if want <= 0 {
		return
	}
	items := d.planner.TakeForPush(want)
	for i, it := range items {
		_, err := d.lq.Push(ctx, it.Path)
		var le *liq.Error
		switch {
		case err == nil:
			d.dirty = true
		case errors.As(err, &le) && le.Code == 404:
			d.log.Warn("file missing, dropped from the plan", "path", it.Path)
			d.planner.Reject(it.Path)
			d.dirty = true
		default:
			d.log.Warn("push failed", "path", it.Path, "err", err)
			for _, rest := range items[i:] {
				d.planner.Unpush(rest.Path)
			}
			return
		}
	}
}
