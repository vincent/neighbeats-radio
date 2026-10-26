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
| `queue` | prints the planned queue (reads the director's SQLite file) |
| `status` | what Liquidsoap is playing |
| `push PATH`, `skip` | by hand, for testing |

Until neighbeats has `/v1/pick` (N1), set `director.seed_paths` in `config.yaml`: paths, as
Jellyfin sees them, the station may route toward.

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
neighbeats-radio skip
```

`push`, `skip` and `status` talk to Liquidsoap's internal control port (`liquidsoap.control_url`,
port 8005, never published). From the host, publish it temporarily or run the command in the
director container.
