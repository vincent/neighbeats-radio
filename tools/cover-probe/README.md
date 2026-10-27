# cover-probe

Tools to find out why Android players (Aerial, Transistor) show no cover for the stream.
Findings so far are in the commit message / session notes; these tools produce the evidence.

## probe.py: what does the live stream send?

    ./probe.py --stop-after 2                     # wait for two ICY titles / ID3 tags
    ./probe.py --no-icy --save-cover /tmp/covers  # in-stream ID3 only, keep any cover found
    ./probe.py --url http://localhost:8000/live.mp3

The server sends metadata only when it changes, so a run needs a track change (minutes).
Exit status 1 when nothing was seen. Prints each ICY title and each ID3v2 tag with its frames
(`TIT2`, `TPE1`, `APIC <mime> <bytes>`).

## hls-test.liq: can an HLS output carry the cover?

Standalone Liquidsoap script, not wired into compose. Untested: written from the docs.

    ../../scripts/gen-test-music.sh /tmp/hls-music && cp some-cover.jpg /tmp/hls-music/cover.jpg
    mkdir -p /tmp/hls-out
    docker run --rm -v $PWD/hls-test.liq:/t.liq:ro -v /tmp/hls-music:/music:ro -v /tmp/hls-out:/out \
      -e HLS_VARIANT=mp3 savonet/liquidsoap:v2.4.6 /t.liq      # or HLS_VARIANT=ts
    python3 -m http.server -d /tmp/hls-out 8080                 # then add http://HOST:8080/live.m3u8 in Aerial

Check the segments first: `ffprobe -show_streams /tmp/hls-out/*/seg*` should list an attached
picture (mp3 variant) or a timed_id3 data stream (ts variant). Then see whether Aerial shows
the cover. Decide on adding HLS to compose only after that.
