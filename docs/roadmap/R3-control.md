# R3: Control CLI

Goal: drive the station from a terminal (or an SSH shortcut on the phone) without a web UI.

## Design

`neighbeats-radio run` serves a small HTTP + JSON API on a Unix socket (`./run/neighbeats-radio.sock`), the
same pattern as `neighbeats serve`. Every other command is a client of it, so the CLI works
from the host or via `docker compose exec neighbeats-radio neighbeats-radio ...`.

| Command | Does |
|---------|------|
| `now` | playing track, its reason and cosine, time left, next 3 |
| `queue` | the planned tail, with legs and destinations |
| `history [-n 30]` | recent plays with outcome (played/skipped) and reason |
| `skip` | skip now; records a skip (feeds R5); the next pushed track starts |
| `steer "text"` or `steer -path P` | make this the next destination; replans from the last *pushed* track, drops the rest of the plan |
| `theme "sea, night"` / `theme off` | pass `theme` on every leg until turned off |
| `mode comfort\|explore\|rediscover` | change taste mode from the next leg |
| `more` | "more like this": next leg uses `/v1/similar` from the playing track |
| `ban artist X` | adds to `avoid_artists` permanently (config or SQLite) |
| `pause` / `resume` | station plays the fallback/ident instead of planned music |

Replanning never touches the playing track or the one already pushed, so a steer is
heard within one track.

## Tasks

- [x] Socket API in `run` (`internal/control`); client commands `now`, `queue`, `history`,
      `skip`, `steer`, `pause`, `resume`.
- [x] Replan logic: `Planner.SteerNow` (routes from the last pushed track, replaces the planned
      tail only; no route or neighbeats down → old plan kept), tested.
- [x] Skip is recorded as `skipped` (feeds R5).
- [ ] `theme`, `mode`, `more`, `ban artist`: not built yet.
- [ ] Liquidsoap metadata: check the player shows artist/title from the stream (harbor sends
      track tags; files without tags show nothing). ICY is on (`icy-metaint: 8192`), but a
      listener joining mid-track got no title: the script now re-sends the current one every
      10 s. Untested; VLC showed nothing even when a title was sent, so check it again.
- [ ] Optional: a ntfy/Gotify notification with now-playing, for the phone without a terminal.

## Exit criteria

- Roadmap success test 3.
- Every command answers in under a second while a leg is being planned.

## Status notes

- `steer` resolves words with neighbeats `/v1/search`; several matches are listed and `steer -path`
  picks one. Reads (`now`, `queue`, `history`) never wait for a leg being planned: the director
  releases its lock while it talks to neighbeats.
- `pause` flushes the queue and skips the playing track, so the station falls to `./fallback`.
- Unit tested only (fake Liquidsoap, neighbeats and socket); not yet run on the stack.
