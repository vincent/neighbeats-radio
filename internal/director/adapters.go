package director

import (
	"context"
	"errors"
	"log/slog"
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

	noExclude bool // the server does not know `exclude`
}

func refs(paths []string) []neighbeats.Ref {
	out := make([]neighbeats.Ref, len(paths))
	for i, p := range paths {
		out[i] = neighbeats.Ref{Path: p}
	}
	return out
}

// Route asks /v1/path with the recent plays as `exclude` (API 2). An older server
// rejects the unknown field with 400, so the router retries once without it and
// stops sending it; the repeat filter on the answer (plan.filter) covers that case.
// It stays on with API 2 too, as a safety net.
func (r *NeighbeatsRouter) Route(ctx context.Context, from, to plan.Track, length int, exclude []string) (plan.Route, error) {
	req := neighbeats.PathRequest{
		From: ref(from), To: ref(to), Length: length,
		Mode: r.Mode, User: r.User, MaxMinutes: r.MaxMinutes,
	}
	if !r.noExclude {
		req.Exclude = refs(exclude)
	}
	res, err := r.Client.Path(ctx, req)
	if err != nil && len(req.Exclude) > 0 && neighbeats.IsBadRequest(err) {
		r.noExclude = true
		req.Exclude = nil
		res, err = r.Client.Path(ctx, req)
	}
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

// NeighbeatsPicker draws destinations with /v1/pick. When that fails (an older
// server, or neighbeats down) it falls back to the seed list if there is one.
type NeighbeatsPicker struct {
	Client     *neighbeats.Client
	Mode       string
	User       string
	MaxMinutes int
	Pool       string // "taste" (default) or "any"
	Fallback   SeedPicker
	Log        *slog.Logger
}

func (p NeighbeatsPicker) Pick(ctx context.Context, exclude []string) (plan.Track, error) {
	res, err := p.Client.Pick(ctx, neighbeats.PickRequest{
		Pool: p.Pool, Mode: p.Mode, User: p.User, Exclude: refs(exclude), Count: 1, MaxMinutes: p.MaxMinutes,
	})
	if err == nil && len(res.Tracks) == 0 {
		err = errors.New("neighbeats /v1/pick returned no track")
	}
	if err != nil {
		if len(p.Fallback.Paths) == 0 {
			return plan.Track{}, err
		}
		if p.Log != nil {
			p.Log.Warn("pick failed, using seed_paths", "err", err)
		}
		return p.Fallback.Pick(ctx, exclude)
	}
	t := fromAPI(res.Tracks[0])
	t.Reason = "pick"
	return t, nil
}

// SeedPicker draws destinations from a fixed list: the fallback for servers
// without /v1/pick.
type SeedPicker struct {
	Paths []string
	Rand  func(n int) int
}

func (s SeedPicker) Pick(_ context.Context, exclude []string) (plan.Track, error) {
	if len(s.Paths) == 0 {
		return plan.Track{}, errors.New("no destinations: set director.seed_paths (neighbeats /v1/pick is not available)")
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
