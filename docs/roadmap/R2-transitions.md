# R2: Transitions

Goal: it sounds like a station, not a playlist: no silences between tracks, no level jumps,
fades that suit the music.

## Design

- **Crossfade** on the music source only (never across the voice queue). Start with
  Liquidsoap's stock `crossfade()` and fixed durations; read subwave's comments first
  (it moved to the lower-level `cross` because the wrapper misbehaved with request queues -
  check whether that still applies to 2.4.x before copying it).
- **Silence at head and tail.** Many files have seconds of silence. First try Liquidsoap's own
  blank handling on the music source; if that is unreliable, measure cue points once with
  `ffmpeg silencedetect` in the director and pass them as `liq_cue_in` / `liq_cue_out`
  annotations on push (`annotate:liq_cue_in=...:/path`).
- **Loudness.** Use ReplayGain tags where present (check how many files have them; neighbeats'
  `stats` could report it). Otherwise measure `ffmpeg ebur128` once per track, cache in the
  director's SQLite, pass as `liq_amplify` annotation. A gentle compressor/limiter on the
  output as a safety net.
- **Hop-aware fades (optional).** Use the neighbeats cosine: a close hop gets a long blend, a
  distant hop a short fade and a clean start. This is the station's one "smart" transition and
  it costs nothing.

## Tasks

- [x] Crossfade on music (`crossfade`, 5 s default) in `radio.liq`.
- [x] Silence at head/tail: `blank.eat(threshold=-50.)`, Liquidsoap only, no ffmpeg.
- [x] ReplayGain tags applied where present (`replaygain`); output `limit()` as safety net.
- [x] Hop-aware fades: the director pushes each track with `liq_cross_duration` (and
      `liq_fade_in/out`) set from the cosine of the hop that follows it (`plan.Fades`,
      config `director.fades`). Push is `POST /control/push?liq_...=...`, body still the path.
- [ ] Count files with ReplayGain tags on the real library; if coverage is poor, bring back
      `internal/measure` (ffmpeg ebur128 + silencedetect, cached by path + mtime, run ahead
      of time) - needs ffmpeg in the director image and the music mounted there.
- [ ] Listen: check the script starts (`liquidsoap --check`), that fade length follows the
      cosine in the logs, and tune thresholds.

## Exit criteria

- Roadmap success test 2: an evening of listening; any bad transition logged with its cosine,
  durations and gain.
- No track more than ~2 LU from its neighbours by ear.

## Status notes

Written without a local Liquidsoap, so the script is unchecked: run it first
(`docker compose up liquidsoap`, look for syntax errors in the log).

- Liquidsoap reads `liq_cross_duration` from the track that is *ending*, so the director sets it
  on a track from the hop to the next planned one (`Planner.After`). Verify by ear/log that the
  long blends land on close hops; if the incoming track's metadata wins instead, push the fade
  with the incoming track.
- The crossfade takes the next request off the queue a few seconds early, so `queued` drops by
  one before the track change. The director's "lost queue" check tolerates one missing.
- Tracks pushed with no planned successor keep the 5 s default.
