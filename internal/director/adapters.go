package director

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/vincent/neighbeats-radio/internal/neighbeats"
	"github.com/vincent/neighbeats-radio/internal/plan"
)

// NeighbeatsRouter adapts the neighbeats client to plan.Router.
type NeighbeatsRouter struct {
	Client     *neighbeats.Client
	Mode       string
	User       string
	MaxMinutes int
}

// Route asks /v1/path. neighbeats has no `exclude` field until N1, so the repeat
// filter runs on the answer (plan.filter) and the exclusion list is not sent.
func (r NeighbeatsRouter) Route(ctx context.Context, from, to plan.Track, length int, _ []string) (plan.Route, error) {
	res, err := r.Client.Path(ctx, neighbeats.PathRequest{
		From: ref(from), To: ref(to), Length: length,
		Mode: r.Mode, User: r.User, MaxMinutes: r.MaxMinutes,
	})
	if err != nil {
		return plan.Route{}, err
	}
	out := plan.Route{Found: res.Found, Why: res.Why}
	if res.Found {
		out.Tracks = make([]plan.Track, len(res.Tracks))
		for i, t := range res.Tracks {
			out.Tracks[i] = fromAPI(t)
		}
	}
	return out, nil
}

func ref(t plan.Track) neighbeats.Ref { return neighbeats.Ref{ItemID: t.ItemID, Path: t.Path} }

func fromAPI(t neighbeats.Track) plan.Track {
	pt := plan.Track{
		Path: t.Path, Artist: t.Artist, Title: t.Title, Cosine: t.Cosine, Reason: t.Reason,
		Duration: time.Duration(t.Duration * float64(time.Second)),
	}
	if t.ItemID != nil {
		pt.ItemID = *t.ItemID
	}
	return pt
}

// SeedPicker draws destinations from a fixed list. It is the fallback until
// neighbeats has /v1/pick (N1).
type SeedPicker struct {
	Paths []string
	Rand  func(n int) int
}

func (s SeedPicker) Pick(_ context.Context, exclude []string) (plan.Track, error) {
	if len(s.Paths) == 0 {
		return plan.Track{}, errors.New("no destinations: set director.seed_paths (neighbeats /v1/pick is not available yet)")
	}
	rnd := s.Rand
	if rnd == nil {
		rnd = rand.IntN
	}
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}
	var free []string
	for _, p := range s.Paths {
		if !skip[p] {
			free = append(free, p)
		}
	}
	if len(free) == 0 {
		// Every seed was played recently; a repeat beats silence.
		free = s.Paths
	}
	p := free[rnd(len(free))]
	return plan.Track{Path: p, Reason: "seed"}, nil
}
