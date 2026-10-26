package director

import (
	"math/rand/v2"
	"time"

	"github.com/vincent/neighbeats-radio/internal/speak"
)

// BreakConfig says when the station talks.
type BreakConfig struct {
	EveryMin, EveryMax int // a break after this many tracks, drawn between the two
	HourMark           bool
	OnSteer, OnLeg     bool
}

// Event describes the track about to air.
type Event struct {
	Now      time.Time
	Steer    bool // first track after a steer
	LegStart bool // first track of a new leg
}

// Breaks decides which tracks get a spoken break. It is not safe for concurrent
// use; the director calls it under its lock.
type Breaks struct {
	cfg          BreakConfig
	rand         func(n int) int
	since        int
	target       int
	lastHour     int
	hourSeen     bool
	lastWasBreak bool
}

func NewBreaks(cfg BreakConfig, rnd func(n int) int) *Breaks {
	if rnd == nil {
		rnd = rand.IntN
	}
	if cfg.EveryMin < 1 {
		cfg.EveryMin = 1
	}
	if cfg.EveryMax < cfg.EveryMin {
		cfg.EveryMax = cfg.EveryMin
	}
	b := &Breaks{cfg: cfg, rand: rnd}
	b.target = b.draw()
	return b
}

func (b *Breaks) draw() int { return b.cfg.EveryMin + b.rand(b.cfg.EveryMax-b.cfg.EveryMin+1) }

// Decide is called once per track pushed. It returns the moment to speak about, or
// "". Never two breaks in a row, except a steer: the listener asked for it. With
// EveryMin 1 the station is asked to talk after every track, so that rule is off.
func (b *Breaks) Decide(ev Event) string {
	b.since++
	if !b.hourSeen {
		// No time check just because we started at 14:59.
		b.hourSeen, b.lastHour = true, ev.Now.Hour()
	}
	moment := ""
	switch {
	case ev.Steer && b.cfg.OnSteer:
		moment = speak.Steer
	case b.lastWasBreak && b.cfg.EveryMin > 1:
	case b.cfg.HourMark && ev.Now.Hour() != b.lastHour:
		moment = speak.Hour
	case ev.LegStart && b.cfg.OnLeg:
		moment = speak.Leg
	case b.since >= b.target:
		moment = speak.Between
	}
	if moment == "" {
		b.lastWasBreak = false
		return ""
	}
	if moment == speak.Hour {
		b.lastHour = ev.Now.Hour()
	}
	b.since, b.target, b.lastWasBreak = 0, b.draw(), true
	return moment
}
