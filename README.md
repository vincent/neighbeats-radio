# neighbeats-radio

A personal radio station that never ends: one MP3 stream, music chosen by
[neighbeats](../neighbeats) (`neighbeats serve`), played by Liquidsoap, steered by a Go
director. See [ROADMAP.md](ROADMAP.md).

## Develop

```sh
make test   # go test -race ./...
make build  # ./neighbeats-radio
```

Configuration: `cp config.example.yaml config.yaml`, `cp .env.example .env`.

## Commands

| Command | Does |
|---------|------|
| `run` | the director: keeps Liquidsoap's queue full from neighbeats routes |
| `now` | what is playing, why, time left, the next three |
| `queue` | the planned queue (falls back to the stored plan when the director is down) |
| `history [-n 30]` | recent plays with outcome (finished/skipped) and reason |
| `skip` | skip the track on air; recorded as a skip |
| `steer "words"` / `steer -path P` | head for that track; the plan after the pushed tracks is replaced. No route: the old plan stays |
| `pause` / `resume` | play the fallback folder instead of the plan, and back |
| `status`, `push PATH`, `liq-skip` | straight to Liquidsoap, for testing |

`now`, `queue`, `history`, `skip`, `steer`, `pause` and `resume` talk to the director over a Unix
socket (`control.socket`, `./run` in the compose file), so from the host or a container alike:
`docker compose exec neighbeats-radio /neighbeats-radio now`.

Crossfades are 5 s by default; the director lengthens them for close hops and shortens them for
distant ones (`director.fades`).

Destinations come from neighbeats' `/v1/pick` (API 2, N1). `director.seed_paths` is an optional
fallback for a neighbeats without it: paths, as Jellyfin sees them, the station may route toward.

## Listening

Stream: `https://HOST/live.mp3`, MP3 192 kbps, behind Traefik with basic auth.

| Player | How |
|--------|-----|
| VLC, most desktop players | `https://USER:PASS@HOST/live.mp3` |
| Browser | open the URL, answer the auth prompt |
| Phone / car apps | many ignore `user:pass@` in URLs; if yours does, enter the credentials in its auth fields, else see the fallback in ROADMAP "Open risks" |

Which players actually work is still to be tested (R0 exit criteria).

## Try it without a library

```sh
scripts/gen-test-music.sh /tmp/tones     # names with accents, spaces and '#'
# in .env: MUSIC_PATH=/tmp/tones ; in ./fallback: copy a tone or two
docker compose up -d liquidsoap
neighbeats-radio push "/tmp/tones/track #4.mp3"
neighbeats-radio status
neighbeats-radio liq-skip
```

`push`, `liq-skip` and `status` talk to Liquidsoap's internal control port (`liquidsoap.control_url`,
port 8005, never published). From the host, publish it temporarily or run the command in the
director container.
