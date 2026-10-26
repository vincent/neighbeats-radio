# N1: neighbeats API additions (done in the neighbeats repo)

Goal: give the radio what it needs from `neighbeats serve` without it touching the SQLite file.
Each addition is small and useful to the Jellyfin plugin too. Version the API additively
(`/v1` stays compatible).

| Addition | Why the radio needs it | Size |
|----------|------------------------|------|
| `duration` (seconds) on every track in answers | look-ahead in minutes, announcements ("a long one") | tiny: already in `embeddings.duration` |
| `exclude: [trackRef]` on `POST /v1/path` | no repeats within the window | small: add to the route's forbidden set (destination still allowed) |
| `POST /v1/pick` | choose a destination: `{"pool": "taste" \| "any", "mode", "user", "theme", "exclude": [...], "count": 1}` → tracks | small-medium: random draw from the taste pool or the whole index |
| `POST /v1/search` | `neighbeats-radio steer "yann tiersen valse"` without the CLI's text resolution being duplicated | small: expose the existing text resolution; ambiguous → list of candidates |
| (R5) `POST /v1/feedback` | record radio plays and skips if the Jellyfin route in R5 is not taken | medium: new table, merged into taste |

## Tasks

- [ ] `duration` in `trackOut`; doc in `docs/api.md`.
- [ ] `exclude` on `/v1/path` (and `/v1/similar` for symmetry); test that an excluded destination
      is still allowed, as for skips.
- [ ] `/v1/pick` with tests: respects `exclude`, `max_minutes`, artist spread across `count`.
- [ ] `/v1/search`.
- [ ] Bump the version in `/v1/health` so the director can check what the server supports and
      fall back (R1 already has fallbacks for each).

## Exit criteria

- The director runs without its fallbacks (no assumed durations, no client-side repeat filter,
  no seed list).
