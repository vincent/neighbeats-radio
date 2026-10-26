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

- [x] `duration` in `trackOut`; doc in `docs/api.md`.
- [x] `exclude` on `/v1/path` (and `/v1/similar` for symmetry); test that an excluded destination
      is still allowed, as for skips.
- [x] `/v1/pick` with tests: respects `exclude`, `max_minutes`, artist spread across `count`.
      `theme` is answered 400 for now (not implemented).
- [x] `/v1/search`. The radio client has `Search`; nothing calls it before R3 `steer`.
- [x] Bump the version in `/v1/health`: new `api` field (2); absent = 0 on older servers. The
      director does not read it yet: it falls back on errors (400 on `exclude`, 404 on `/v1/pick`). so the director can check what the server supports and
      fall back (R1 already has fallbacks for each).

## Status notes

Implemented in the neighbeats working tree (`cmd/neighbeats/serve.go`, `serve_pick.go`,
`docs/api.md`), unit tested, **not committed there** and not yet run on the NAS library.
The radio side (`internal/neighbeats`, `internal/director/adapters.go`) uses it and still
falls back against an older server.

## Exit criteria

- [ ] Checked on the real library. The director runs without its fallbacks (no assumed durations, no client-side repeat filter,
  no seed list).
