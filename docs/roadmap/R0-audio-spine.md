# R0: Audio spine

Goal: a stream reachable from my phone over mobile data, playing files I push by hand.
Nothing chooses music yet.

## Design

- `liquidsoap/radio.liq`, pinned to `savonet/liquidsoap:v2.4.x` (both reference projects
  run 2.4.5-2.4.6). Target well under 200 lines at this stage.
  - `music = request.queue(id="music")`
  - `safe = playlist(mode="randomize", reload_mode="watch", "/fallback")`: a small
    folder of known-good tracks.
  - `radio = fallback(track_sensitive=false, [music, safe, ident_loop])`
  - `output.harbor(%mp3(bitrate=192), mount="live", port=8000, radio)`
- Control surface on a second, internal-only harbor port (8005), the deadair pattern:
  `GET /control/status`, `POST /control/push` (body: a path), `POST /control/skip`,
  `POST /control/flush`. Plain HTTP + JSON, so the Go side needs no telnet client.
- Paths: the Liquidsoap container mounts the music read-only at the **same path
  Jellyfin uses** (`path_map.remote` in neighbeats' config). neighbeats' answers are
  then playable as-is.
- Traefik: a router on the stream port only (never 8005), with a `basicauth`
  middleware. Credentials in `.env`.

## Tasks

- [~] Repo skeleton (C1 done: go.mod, cmd, internal/config, config.example.yaml, .env.example, Makefile, CI; compose.yaml follows in C2): `go.mod`, `cmd/neighbeats-radio`, `internal/`, `compose.yaml`, `.env.example`,
      `config.example.yaml` (unknown keys rejected, as in neighbeats), Makefile, CI with `go test -race`.
- [ ] `compose.yaml`: `liquidsoap` + `neighbeats-radio`; join the network where Traefik lives;
      mount neighbeats' `./run` read-write for the socket; `read_only: true` and a user like neighbeats.
- [ ] `radio.liq` as above, with `ident_loop` = one short generated tone or ident, so the
      chain is never empty.
- [ ] `internal/liq`: Go client for the control endpoints (status, push, skip, flush), with a
      fake HTTP server in tests.
- [ ] `neighbeats-radio push <path>` and `neighbeats-radio skip`: thin commands over `internal/liq`, to test by hand.
- [ ] Traefik labels + basic auth; document the stream URL form for players.
- [ ] Check Liquidsoap resolves a path with accents, spaces and `#` in it (push one of each).

## Exit criteria

- From a phone on 4G: open the URL in the player app actually used, push 3 tracks by hand,
  hear them in order, then hear the fallback folder take over.
- 2 hours connected through Traefik without a drop.
- `docker compose restart liquidsoap` while listening: the player reconnects (or note that it
  does not, and which player).
