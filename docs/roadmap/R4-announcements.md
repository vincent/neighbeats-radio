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
- **What to say:** Go `text/template` files in `./templates/<lang>/*.tmpl`, several variants
  per moment, picked at random, never the same twice in a row. Inputs: previous and next
  track (artist, title, album, year if neighbeats exposes it), hop reason, leg destination,
  time, duration. Example: `C'était {{.Prev.Artist}}. On enchaîne avec {{.Next.Title}}.`
- **Pronunciation:** a small substitution table (artist names Piper mangles) in config.
- **Station ident:** a few fixed phrases rendered once at start and cached; also used by the
  fallback chain in R0.

## Tasks

- [ ] Piper in the image (or a sidecar binary in `./models`); pick and test a French voice
      on 20 real artist names.
- [ ] `internal/speak`: render text → WAV, cache by text hash, timeout, failure = skip the break
      silently (never delay music for a voice).
- [ ] Voice queue + ducking in `radio.liq`; `/control/voice`.
- [ ] Break scheduler in the director with tests (frequency, no two in a row, hour mark).
- [ ] Templates (FR, maybe EN) and the substitution table.

## Exit criteria

- A day of listening: announcements are on the right tracks (name the *next* track correctly
  after skips and steers), never over a sung intro by more than a couple of words, never cut.
- Piper failure (remove the voice file) leaves music unaffected.
