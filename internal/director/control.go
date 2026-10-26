package director

import (
	"context"
	"fmt"
	"time"

	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/state"
)

// Snapshot is what `now` shows: the track on air, how long it has left, and what
// comes next.
type Snapshot struct {
	Playing   *state.Play
	Duration  time.Duration // zero = unknown
	Remaining time.Duration
	Next      []plan.Item
	Ahead     time.Duration // planned playing time still ahead
	Paused    bool
}

// Now returns the on-air track and the next three planned ones.
func (d *Director) Now() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Snapshot{Paused: d.paused, Ahead: d.planner.Lookahead()}
	items := d.planner.Items()
	if d.playID != 0 {
		p := d.playing
		s.Playing = &p
		for _, it := range items {
			if it.State == plan.Playing && it.Path == p.Path {
				s.Duration = it.Duration
				if s.Duration == 0 {
					s.Duration = d.planner.Assumed()
				}
			}
		}
		if s.Duration > 0 {
			if left := s.Duration - d.now().Sub(p.StartedAt); left > 0 {
				s.Remaining = left
			}
		}
	}
	for _, it := range items {
		if it.State == plan.Pushed || it.State == plan.Planned {
			s.Next = append(s.Next, it)
			if len(s.Next) == 3 {
				break
			}
		}
	}
	return s
}

// Queue returns the planned tail (playing, pushed, planned).
func (d *Director) Queue() []plan.Item { return d.planner.Items() }

// History returns the last n plays, newest first.
func (d *Director) History(ctx context.Context, n int) ([]state.Play, error) {
	return d.store.History(ctx, n)
}

// Skip ends the track on air. It records a skip (not a finish), then lets the next
// pushed track start.
func (d *Director) Skip(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.skip(ctx)
}

func (d *Director) skip(ctx context.Context) error {
	if err := d.lq.Skip(ctx); err != nil {
		return err
	}
	if d.playID != 0 {
		if err := d.store.EndPlay(ctx, d.playID, state.Skipped); err != nil {
			d.log.Error("end play", "err", err)
		}
		d.playID = 0
		d.playing = state.Play{}
	}
	if _, ok := d.planner.SkipPlaying(); ok {
		d.dirty = true
	}
	return nil
}

// Steer makes dest the next destination. It routes outside the lock: neighbeats can
// be slow, and a skip must not wait for it.
func (d *Director) Steer(ctx context.Context, dest plan.Track) (plan.SteerResult, error) {
	res, err := d.planner.SteerNow(ctx, dest, d.now())
	if err != nil {
		return res, fmt.Errorf("steer: %w", err)
	}
	if res.Found {
		d.mu.Lock()
		d.dirty = true
		d.steerLeg = res.Leg
		d.mu.Unlock()
	}
	return res, nil
}

// Pause stops the music: the queue is dropped and the current track skipped, so
// the station falls back to the safe folder or the ident. Planning goes on.
func (d *Director) Pause(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.paused {
		return nil
	}
	if err := d.lq.Flush(ctx); err != nil {
		return err
	}
	d.planner.Requeue()
	d.voices = map[string]*voiceJob{}
	if err := d.skip(ctx); err != nil {
		return err
	}
	d.paused = true
	d.dirty = true
	return nil
}

// Resume pushes the plan again from the next poll.
func (d *Director) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.paused = false
}
