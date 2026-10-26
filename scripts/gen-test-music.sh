#!/bin/sh
# Generate short tone files (accents, spaces, '#' in names) for trying the stream
# without a real library: scripts/gen-test-music.sh DIR
set -eu
dir=${1:?usage: gen-test-music.sh DIR}
mkdir -p "$dir"
i=0
for name in "one.mp3" "Café déjà vu.mp3" "with space.mp3" "track #4.mp3" "five.mp3"; do
  i=$((i + 1))
  ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=$((220 * i)):duration=20" \
    -metadata artist="Tone $i" -metadata title="${name%.mp3}" -b:a 128k "$dir/$name"
done
ls "$dir"
