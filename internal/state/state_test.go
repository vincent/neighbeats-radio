package state

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vincent/neighbeats-radio/internal/plan"
)

var ctx = context.Background()

func TestPlaysRoundTrip(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	base := time.Unix(1_700_000_000, 0)
	id1, err := d.StartPlay(ctx, Play{StartedAt: base, Path: "/a é #.mp3", Artist: "A", Title: "T", Reason: "similarity 0.7", Cosine: 0.7, Leg: 1})
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := d.StartPlay(ctx, Play{StartedAt: base.Add(time.Hour), Path: "/b.mp3", Leg: 2})
	if err := d.EndPlay(ctx, id1, Skipped); err != nil {
		t.Fatal(err)
	}
	got, err := d.PlaysSince(ctx, base.Add(time.Minute))
	if err != nil || len(got) != 1 || got[0].ID != id2 || got[0].Outcome != Playing {
		t.Fatalf("since: %+v %v", got, err)
	}
	all, _ := d.PlaysSince(ctx, base)
	if len(all) != 2 || all[0].Path != "/a é #.mp3" || all[0].Outcome != Skipped || all[0].Cosine != 0.7 || !all[0].StartedAt.Equal(base) {
		t.Fatalf("all: %+v", all)
	}
	hist, _ := d.History(ctx, 1)
	if len(hist) != 1 || hist[0].ID != id2 {
		t.Fatalf("history: %+v", hist)
	}
	if err := d.ClosePlaying(ctx); err != nil {
		t.Fatal(err)
	}
	hist, _ = d.History(ctx, 1)
	if hist[0].Outcome != Finished {
		t.Fatalf("not closed: %+v", hist[0])
	}
}

func TestQueueRoundTripAndReplace(t *testing.T) {
	d, _ := Open(":memory:")
	defer d.Close()
	items := []plan.Item{
		{Track: plan.Track{Path: "/a.mp3", ItemID: "x", Artist: "A", Title: "T", Cosine: 0.5, Reason: "r", Duration: 201500 * time.Millisecond}, Leg: 3, State: plan.Pushed},
		{Track: plan.Track{Path: "/b.mp3"}, Leg: 3, State: plan.Planned},
	}
	if err := d.SaveQueue(ctx, items); err != nil {
		t.Fatal(err)
	}
	got, err := d.LoadQueue(ctx)
	if err != nil || !reflect.DeepEqual(got, items) {
		t.Fatalf("got %+v err %v", got, err)
	}
	if err := d.SaveQueue(ctx, items[1:]); err != nil {
		t.Fatal(err)
	}
	got, _ = d.LoadQueue(ctx)
	if len(got) != 1 || got[0].Path != "/b.mp3" {
		t.Fatalf("replace: %+v", got)
	}
}

func TestReopenKeepsDataAndMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.StartPlay(ctx, Play{StartedAt: time.Now(), Path: "/a.mp3"})
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h, _ := d.History(ctx, 5)
	if len(h) != 1 {
		t.Fatalf("lost data: %+v", h)
	}
	var v int
	d.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != len(migrations) {
		t.Fatalf("version %d", v)
	}
}

func TestNewerDatabaseRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.sqlite")
	d, _ := Open(path)
	d.db.Exec("PRAGMA user_version = 99")
	d.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("want error for newer schema")
	}
}
