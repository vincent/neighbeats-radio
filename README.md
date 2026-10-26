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
