#!/bin/sh
# On the Mac or Linux (Go >= 1.23, ffmpeg): build the Bad Apple!! YAM plugin from
# your copy of the PV (the song and frames are not in the repo).
#   sh build.sh "<Bad Apple PV.mp4>"   -> out/badapple.yap
set -eu
R=$(cd "$(dirname "$0")" && pwd)
V=$1
P="$R/out/badapple"
rm -rf "$P" && mkdir -p "$P/bin" "$P/data"
(cd "$R/gen" && go run . data "$V" "$P/data")
(cd "$R/player" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$P/bin/badapple" .)
cp "$R/plugin.toml" "$P/"
(cd "$P" && COPYFILE_DISABLE=1 tar czf "$R/out/badapple.yap" plugin.toml bin/badapple data/frames.bin data/audio.mp3)
echo "built: $R/out/badapple.yap"
