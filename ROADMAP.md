# Roadmap

Working name: **neighbeats-radio** (rename freely). A personal radio station that never ends:
one MP3 stream, the music chosen by `neighbeats serve`, short spoken announcements
between some tracks. CLI only, no web UI, no database server.

Listener: one person (me), from anywhere, through the existing Traefik.

Constraints: Go, separate repository, Docker on the same NAS as neighbeats (Debian,
i7-3770K without AVX2, GTX 1060). neighbeats is reached only through its Unix socket
API, never its SQLite file. No PostgreSQL, Redis, Node or web stack.

neighbeats source is in `/home/vincent/Workspace/Perso/neighbeats`.

| # | Milestone | Status | Detail |
|---|-----------|--------|--------|
| R0 | Audio spine: Liquidsoap, one mount behind Traefik, push a file by hand | in progress | [R0](docs/roadmap/R0-audio-spine.md) |
| R1 | Endless director: routes from neighbeats keep the queue full | in progress | [R1](docs/roadmap/R1-director.md) |
| N1 | neighbeats API additions the director needs | built, untested on real data (in neighbeats) | [N1](docs/roadmap/N1-neighbeats-api.md) |
| R2 | Transitions: crossfade, loudness, silence trimming | todo | [R2](docs/roadmap/R2-transitions.md) |
| R3 | Control CLI: now, next, skip, steer, history | todo | [R3](docs/roadmap/R3-control.md) |
| R4 | Spoken announcements (Piper TTS, templates, ducking) | todo | [R4](docs/roadmap/R4-announcements.md) |
| R5 | Feedback: plays and skips reach neighbeats' taste | todo | [R5](docs/roadmap/R5-feedback.md) |
| R6 | Upkeep: health, silence alarm, packaging | todo | [R6](docs/roadmap/R6-upkeep.md) |

Order: R0 → R1 (with N1 alongside) gives a usable station. R2-R4 make it pleasant;
R5 closes the loop; R6 makes it boring to run.

Later (not scheduled): [docs/roadmap/later.md](docs/roadmap/later.md).

## Architecture

```
 phone/car/laptop ──https──► Traefik (basic auth) ──► Liquidsoap :8000 /live.mp3
                                                          ▲  harbor HTTP :8005 (internal)
                                                          │  /control/push /skip /status ...
 neighbeats-radio CLI ──unix socket──► neighbeats-radio director ───────┘
                                   │   │
                                   │   └── piper (TTS) → /spool/voice/*.wav
                                   │
                                   └──unix socket──► neighbeats serve  (/v1/path, /v1/pick ...)
```

Three long-running containers: `liquidsoap`, `neighbeats-radio` (director), and the existing
`neighbeats-serve`. Shared volumes: the music (read-only), `./run` for sockets,
`./spool` for rendered speech, `./data` for the director's state.

- **Liquidsoap only plays.** It holds two request queues (music, voice), a fallback,
  the crossfade and the output. It decides nothing.
- **The director only decides.** It never decodes or mixes audio. It is the single
  writer of what is queued (lesson from deadair: one owner of the running order).
- **neighbeats only routes.** The radio adds no audio analysis of its own.

No Icecast at first: Liquidsoap's `output.harbor` serves the mount directly, which is
enough for one listener. Icecast comes back only if several mounts or many listeners
are wanted (see later).

## Success test

1. **Never silent (after R1):** 48 hours of continuous play with the director restarted,
   neighbeats restarted and the NAS under `update` load; no gap longer than 3 seconds.
2. **Coherent (after R2):** an evening of listening where no transition feels like a
   channel change, judged by ear, with the hop cosines logged for the bad ones.
3. **Steerable (after R3):** "steer toward X" audibly lands on X within 30 minutes.

## Principles

- **Never silent.** Every failure falls back to something playable: the next queued
  track, then a fallback folder, then a short station ident on loop. Never dead air.
- **Liquidsoap restart is normal.** The director re-pushes the queue it believes in;
  Liquidsoap keeps no state that matters.
- **Every track carries its reason** (the hop `reason` from neighbeats, or "steered",
  "fallback"), logged and shown by `now`.
- **Honest failure.** If neighbeats finds no route, log `why`, pick another destination,
  and say so in `history`.
- **No repeats within a window** (default 8 hours, per recording), enforced by the director.

## Decisions taken

| Question | Choice |
|----------|--------|
| Repository | separate Go repo, talks to neighbeats over its socket only |
| Listener | one person, remote, behind the existing Traefik |
| Stream server | Liquidsoap `output.harbor`, no Icecast |
| First voice | simple TTS announcements from templates, no LLM |
| TTS engine | Piper (CPU, small, many languages) |
| Director state | one SQLite file (modernc.org/sqlite, as in neighbeats) |
| Control | CLI over a Unix socket, same pattern as `neighbeats serve` |

## Open questions

| Question | Where | Default until answered |
|----------|-------|------------------------|
| Announcement language: French, English, or both? | R4 | French Piper voice |
| Which Jellyfin user's taste steers the station? | R1 | `jellyfin.user` from neighbeats' config |
| How do radio plays and skips reach neighbeats' taste? | R5 | see R5: Jellyfin playback reports first |
| Stream bitrate on mobile data | R0 | MP3 192 kbps (one mount) |

## Open risks

| Risk | Where | How it gets closed |
|------|-------|--------------------|
| Liquidsoap 2.4 learning curve (request queues, crossfade on queues, metadata) | R0, R2 | Keep `radio.liq` small; read the commented subwave/deadair scripts for traps before writing each part |
| neighbeats returns paths as Jellyfin sees them, not as Liquidsoap sees them | R0 | Mount the music in the Liquidsoap container at exactly the Jellyfin path (`path_map.remote`), so no translation exists |
| Traefik or a proxy timeout cuts a long-lived stream | R0 | 2-hour listening test through Traefik over mobile data; adjust entrypoint timeouts if it drops |
| Players that do not send basic auth from `https://user:pass@host/...` | R0 | Test the actual players used (phone app, car, VLC); fall back to a secret path or Liquidsoap's own mount auth |
| neighbeats answers one request at a time; `update` reloads the index | R1 | Director keeps ≥ 20 minutes queued, so a slow answer never matters |
| Plays from the radio are invisible to Jellyfin, so taste goes stale | R5 | Milestone R5 |
