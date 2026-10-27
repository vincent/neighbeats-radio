# Standalone experiment: does an HLS output carry per-track ID3 (title, artist, cover) that
# Aerial (Media3) picks up? Not part of the radio. Untested: written from the Liquidsoap docs
# (savonet/liquidsoap v2.4.6), adjust the encoder record fields if the script refuses to start.
#
#   make a few tracks:  ../../scripts/gen-test-music.sh /tmp/hls-music   (add a cover.jpg next to them)
#   run:                see README.md

settings.log.level.set(3)

music_dir = environment.get(default="/music", "MUSIC_DIR")
out_dir = environment.get(default="/out", "HLS_DIR")

tracks = playlist(mode="randomize", reload_mode="watch", music_dir)

# Same cover lookup as radio.liq: a picture file next to the track.
def with_cover(m)
  dir = path.dirname(m["filename"])
  jpg = "#{dir}/cover.jpg"
  if m["coverart"] == "" and file.exists(jpg) then
    [("coverart", file.contents(jpg)), ("coverart_mime", "image/jpeg")]
  else
    []
  end
end
tracks = map_metadata(with_cover, tracks)

radio = mksafe(tracks)

variant = environment.get(default="mp3", "HLS_VARIANT")

# Variant "mp3": packed MP3 segments. Each segment starts with an ID3v2 tag (replay_id3 repeats
# the current track's tag at the head of every segment, so a late joiner still gets it).
mp3_enc = %mp3(bitrate=128, id3v2=true).{id3_version = 4, replay_id3 = true}

# Variant "ts": MPEG-TS with ID3 as a timed metadata stream (what Media3 HLS reads as `Metadata`).
ts_enc = %ffmpeg(format="mpegts", %audio(codec="aac", channels=2, ar=44100, b="128k"))

enc = if variant == "ts" then ts_enc else mp3_enc end

output.file.hls(
  playlist="live.m3u8",
  segment_duration=4.,
  segments=8,
  segments_overhead=4,
  persist_at="#{out_dir}/state.config",
  out_dir,
  [(variant, enc)],
  radio
)
