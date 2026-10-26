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

- [ ] Crossfade on music, fixed 5 s; listen.
- [ ] Count files with ReplayGain tags; decide tags vs measurement.
- [ ] `internal/measure`: ffmpeg loudness + silence detection, cached by path + mtime, run ahead
      of time on planned tracks (never on the playing one).
- [ ] Push with annotations (amplify, cue in/out, fade duration).
- [ ] Output limiter.

## Exit criteria

- Roadmap success test 2: an evening of listening; any bad transition logged with its cosine,
  durations and gain.
- No track more than ~2 LU from its neighbours by ear.
