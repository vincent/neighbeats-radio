# R1: Endless director

Goal: the station runs on its own. The director keeps Liquidsoap's music queue full with
routes from neighbeats, forever.

## Design

The station is a path that never reaches its end: route from the last queued track to a
destination; when the route is nearly used up, pick a new destination and route on.

- **Queue model.** The director owns an ordered list of planned tracks with state
  `planned → pushed → playing → played | skipped`. Liquidsoap only ever holds the next
  1-2 pushed items; the director pushes the next one when `status` shows the queue below 2.
  This keeps steering and skips cheap (nothing deep to flush in Liquidsoap).
- **Look-ahead.** Keep ≥ 20 minutes planned (sum of durations), so neighbeats being slow,
  reloading after `update`, or restarting never matters.
- **Legs.** One leg = one `POST /v1/path` call, `length` 8-15 (configurable, randomised a
  little), `mode` from config (`comfort` by default), `max_minutes` 10.
- **Destination picker** (in order, configurable weights):
  1. a steered target (R3), if any;
  2. a random track from neighbeats' taste pool (favourites / often played) - needs `/v1/pick` (N1);
  3. a random analysed track - also `/v1/pick`;
  4. fallback before N1 exists: a list of seed paths in the config.
- **No repeats.** Tracks played in the last 8 hours are sent as an exclusion list (needs
  `exclude` on `/v1/path`, N1). Until then: drop repeats from the answer and accept a
  shorter leg.
- **Durations** come from neighbeats (N1 adds `duration` to answers). Until then: assume
  4 minutes per track for the look-ahead.
- **State**: SQLite in `./data/neighbeats-radio.sqlite`: `plays` (time, path, item_id, artist, title,
  reason, leg id, outcome) and `queue` (the planned tail, so a restart resumes the same leg).
- **Restart.** On start: read `/control/status`; if Liquidsoap is empty, push the first two
  planned tracks; if it is playing something the director planned, carry on from there.

## Tasks

- [x] `internal/neighbeats`: client for `/v1/health`, `/v1/path`, `/v1/similar` over the Unix
      socket; types copied from `docs/api.md` (no shared module, the API is the contract).
- [ ] `internal/plan`: the queue model and the leg logic as pure functions over an interface
      (`Route(from, to, n)`, `Pick()`), tested with a fake router: leg chaining, repeat filter,
      look-ahead, behaviour when no route is found (`found: false` → new destination, logged).
- [ ] `internal/state`: SQLite schema + migrations for `plays` and `queue`.
- [ ] Director loop: poll `/control/status` every 2 s; push when low; plan a leg when look-ahead
      drops below the threshold. Detect track change from Liquidsoap metadata (on_track) and
      mark `playing` / `played`.
- [ ] `neighbeats-radio run` (the long-running command) and `neighbeats-radio queue` (print the plan).
- [ ] Logs: one line per track start with artist, title, cosine, reason, leg.

## Exit criteria

- Roadmap success test 1: 48 hours, no gap over 3 seconds, through restarts of each container.
- `history` of a full day shows no repeat inside 8 hours and the reason of every track.
- Note: average hop cosine across leg boundaries vs inside legs (the seam between two legs
  is the risky hop: the new leg starts from the last queued track, so it should be fine).
