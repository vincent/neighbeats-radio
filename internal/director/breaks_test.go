package director

import (
	"testing"
	"time"

	"github.com/vincent/neighbeats-radio/internal/speak"
)

var noon = time.Date(2026, 10, 26, 12, 10, 0, 0, time.UTC)

func newBreaks(cfg BreakConfig) *Breaks { return NewBreaks(cfg, func(int) int { return 0 }) }

func TestBreaksEveryNTracksNeverTwiceInARow(t *testing.T) {
	b := newBreaks(BreakConfig{EveryMin: 3, EveryMax: 3})
	var got []string
	for i := 0; i < 8; i++ {
		got = append(got, b.Decide(Event{Now: noon}))
	}
	want := []string{"", "", speak.Between, "", "", speak.Between, "", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	b = newBreaks(BreakConfig{EveryMin: 1, EveryMax: 1})
	if b.Decide(Event{Now: noon}) != speak.Between || b.Decide(Event{Now: noon}) != "" || b.Decide(Event{Now: noon}) != speak.Between {
		t.Fatal("every track: want a break, a gap, a break")
	}
}

func TestBreaksRandomisedEvery(t *testing.T) {
	picks := []int{2, 0}
	i := 0
	b := NewBreaks(BreakConfig{EveryMin: 4, EveryMax: 6}, func(n int) int { v := picks[i%2]; i++; return v })
	// first target 4+2=6
	for k := 1; k < 6; k++ {
		if m := b.Decide(Event{Now: noon}); m != "" {
			t.Fatalf("track %d: %q", k, m)
		}
	}
	if m := b.Decide(Event{Now: noon}); m != speak.Between {
		t.Fatalf("got %q", m)
	}
}

func TestBreaksHourMarkOncePerHourNotAtStart(t *testing.T) {
	b := newBreaks(BreakConfig{EveryMin: 100, EveryMax: 100, HourMark: true})
	at := time.Date(2026, 10, 26, 14, 59, 0, 0, time.UTC)
	if m := b.Decide(Event{Now: at}); m != "" {
		t.Fatalf("start of day should not announce: %q", m)
	}
	three := at.Add(2 * time.Minute)
	if m := b.Decide(Event{Now: three}); m != speak.Hour {
		t.Fatalf("got %q", m)
	}
	if m := b.Decide(Event{Now: three.Add(time.Minute)}); m != "" {
		t.Fatalf("second time check in the same hour: %q", m)
	}
}

func TestBreaksSteerAndLegGating(t *testing.T) {
	b := newBreaks(BreakConfig{EveryMin: 100, EveryMax: 100, OnSteer: true, OnLeg: true})
	if m := b.Decide(Event{Now: noon, LegStart: true}); m != speak.Leg {
		t.Fatalf("leg: %q", m)
	}
	// right after a break the steer still talks, a leg start does not.
	if m := b.Decide(Event{Now: noon, LegStart: true}); m != "" {
		t.Fatalf("leg twice in a row: %q", m)
	}
	if m := b.Decide(Event{Now: noon, Steer: true, LegStart: true}); m != speak.Steer {
		t.Fatalf("steer: %q", m)
	}
	off := newBreaks(BreakConfig{EveryMin: 100, EveryMax: 100})
	if m := off.Decide(Event{Now: noon, Steer: true, LegStart: true}); m != "" {
		t.Fatalf("disabled moments spoke: %q", m)
	}
}
