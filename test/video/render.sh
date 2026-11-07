#!/bin/bash
# Renders a recording of test/video/tour.js into the videos: no audio,
# 1920×1080, H.264.
#
#   NAME.mp4             the subtitles burned in, in a band below the pages
#   NAME-subtitles.mp4   the pages full size, the subtitles as a track a
#                        player can switch on and off (mov_text)
#
# NAME.srt and NAME.vtt stay next to them, for players and translations.
#
# Usage: render.sh DIR NAME   (reads DIR/NAME.webm, .ass and .srt)
set -euo pipefail
dir=${1:?usage: render.sh DIR NAME}
name=${2:?usage: render.sh DIR NAME}
in=$dir/$name.webm
for f in "$in" "$dir/$name.ass" "$dir/$name.srt"; do
	[ -s "$f" ] || { echo "render: $f is missing" >&2; exit 1; }
done
enc=(-an -r 25 -c:v libx264 -preset slow -crf 20 -pix_fmt yuv420p -movflags +faststart)
# The pages (2:1) scaled to 1920×960; below them a 120-pixel band in the
# pages' dark green, where the subtitles are.
ffmpeg -nostdin -loglevel error -y -i "$in" \
	-vf "scale=1920:960:flags=lanczos,pad=1920:1080:0:0:color=0x0c322c,ass=$dir/$name.ass" \
	"${enc[@]}" "$dir/$name.mp4"
# Full size (letterboxed to 16:9), with a subtitle track.
ffmpeg -nostdin -loglevel error -y -i "$in" -i "$dir/$name.srt" \
	-vf "scale=1920:960:flags=lanczos,pad=1920:1080:0:60:color=0x0c322c" \
	-map 0:v -map 1:s "${enc[@]}" -c:s mov_text -metadata:s:s:0 language=eng "$dir/$name-subtitles.mp4"
for f in "$dir/$name.mp4" "$dir/$name-subtitles.mp4"; do
	d=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$f")
	a=$(ffprobe -v error -select_streams a -show_entries stream=index -of csv=p=0 "$f")
	[ -z "$a" ] || { echo "render: $f has an audio stream" >&2; exit 1; }
	echo "render: $f, ${d%.*} s, no audio"
done
