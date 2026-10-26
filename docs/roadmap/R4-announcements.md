# R4: Spoken announcements

Goal: a short voice between some tracks, from templates. No LLM.

## Design

- **Engine: Piper**, run by the director as a subprocess (binary + one `.onnx` voice in the
  image or in `./models`). CPU only; a sentence renders in well under a second on the i7.
  Output WAV to `./spool/voice/`, shared read-only with Liquidsoap. Delete files once played.
- **Liquidsoap side:** a second `request.queue(id="voice")`, mixed over the music with
  ducking (music down ~10-12 dB while voice plays, smooth release). Control endpoint
  `POST /control/voice` pushes a file. Voice is placed at a track boundary: pushed while the
  outgoing track's last seconds play, so it lands over the crossfade or the intro of the next one.
  This is the fiddliest Liquidsoap part; read both reference scripts' voice/duck sections first.
- **When to talk** (config, all optional):
  - every N tracks (default 4-6, randomised), never twice in a row;
  - at the top of the hour: time check;
  - on a steer: "on change de cap, direction X";
  - on a leg start: name where the route is heading ("on part vers Yann Tiersen").
- **What to say:** Go `text/template` files (`<moment>.tmpl`, variants separated by a `---` line), several variants
  per moment, picked at random, never the same twice in a row. Inputs: previous and next
  track (artist, title, album, year if neighbeats exposes it), hop reason, leg destination,
  time, duration. Example: `C'était {{.Prev.Artist}}. On enchaîne avec {{.Next.Title}}.`
- **Pronunciation:** a small substitution table (artist names Piper mangles) in config.
- **Station ident:** a few fixed phrases rendered once at start and cached; also used by the
  fallback chain in R0.

## Tasks

- [x] Piper in the image (pinned release + `en_US-lessac-medium`, `/opt/piper`); other voices via
      `voice.model` and `./models`.
- [x] `internal/speak`: render text → WAV, cache by text hash, timeout, failure = no break
      (never delay music for a voice).
- [x] Voice queue + ducking in `radio.liq` (`smooth_add`, `p=0.25`); `POST /control/voice`;
      `/control/flush` clears it too.
- [x] Break scheduler (`internal/director/breaks.go`) with tests: every 4-6 tracks, never twice in
      a row, hour mark, steer, leg start.
- [x] English templates (`internal/speak/templates/en`, several variants per moment) and the
      `voice.say` substitution table.
- [ ] Run Piper on the NAS CPU (no AVX2): check it starts and renders in well under a second.
- [ ] Try 20 real artist names, fill `voice.say`.
- [ ] Listen: ducking depth, break placement, wording.

## Exit criteria

- A day of listening: announcements are on the right tracks (name the *next* track correctly
  after skips and steers), never over a sung intro by more than a couple of words, never cut.
- Piper failure (remove the voice file) leaves music unaffected.

## Status notes

Language is English (the open question is answered). Unit tested with a fake Piper and fake
Liquidsoap; the new Liquidsoap lines and the image build have not been run.

- **Timing.** A break is decided and rendered in the background when its track is pushed, and
  queued into Liquidsoap when that track *starts* (the poll is 2 s), so it lands over the first
  seconds of the track it names, not over the crossfade. A render that is not finished by then is
  dropped. The text names the track before it and the one starting, taken from the plan at push time.
- Breaks are discarded when their track is skipped over, on pause, steer replans, and when
  Liquidsoap restarts.
- Spoken breaks are not tracks: `on_track` and the ids are counted before the voice is mixed in.
- `voice.enabled` is off by default. Idents and the cached "station ident" phrases are not built yet.
- Rendered files live in `./spool/voice` and are deleted an hour after their last use.
