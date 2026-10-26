# R6: Upkeep and packaging

Goal: it runs for months without attention.

## Tasks

- [ ] `neighbeats-radio doctor`: one line per check: Liquidsoap control reachable, mount serving,
      neighbeats socket and version (which N1 features exist), music path readable as
      Liquidsoap sees it, Piper renders, spool writable, SQLite ok.
- [ ] Silence alarm: Liquidsoap's blank detection on the output calls the director; the director
      logs it, flushes, and pushes from the fallback. Optional notification (ntfy/Gotify).
- [ ] Healthchecks in `compose.yaml` for both containers; `restart: unless-stopped`.
- [ ] Spool cleanup and SQLite pruning (keep plays for a year, they are small).
- [ ] Published image `ghcr.io/vincent/neighbeats-radio` (goreleaser, as neighbeats), amd64 + arm64.
- [ ] `docs/operations.md`: first run, Traefik labels, player URLs, updating, what to do when silent.
- [ ] Behaviour during neighbeats `update` (index reload): measured, documented.

## Exit criteria

- A month of running with no manual intervention; `doctor` green; logs explain every fallback.
