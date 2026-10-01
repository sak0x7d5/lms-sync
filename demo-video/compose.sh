#!/usr/bin/env bash
# Build the README demo from two screen recordings.
#
#   bash compose.sh LEFT.mp4 RIGHT.mp4 [options]
#
# LEFT is "the usual way" (LMS -> download -> upload -> ask), RIGHT is
# "with lms-sync" (just ask). Start both recordings at the same moment: the
# instant you begin, with the question already decided.
#
# Options
#   --speed N        play the left side N times faster (default 4). Its clock
#                    still shows real elapsed time, and the bar says "N x speed".
#   --half-width W   width of each half in pixels (default 960; height is 16:9)
#   --left-subs F    .srt burned into the left half, timed against the ORIGINAL
#                    left recording (so you can note times while watching it)
#   --right-subs F   .srt burned into the right half
#   --font FILE      a bold .ttf (default: Segoe UI Bold, Arial Bold or DejaVu)
#   --tagline TEXT   line under the times on the end card and social preview
#                    (default: "Ask AI about your lectures. It answers from your
#                    own course files.")
#   --out DIR        where to write the results (default: ./out)
#
# Writes
#   demo.mp4             split screen + end card, H.264, no audio, aimed at
#                        GitHub's 10 MB limit for a video in a README
#   hero.gif             the right half only, looping, aimed under 5 MB
#   social-preview.png   1280x640 end card for the repository's social preview
set -euo pipefail

die() { echo "compose.sh: $*" >&2; exit 1; }
command -v ffmpeg >/dev/null || die "ffmpeg is not on PATH"
command -v ffprobe >/dev/null || die "ffprobe is not on PATH"

[ $# -ge 2 ] || die "usage: bash compose.sh LEFT.mp4 RIGHT.mp4 [--speed N] [--half-width W] [--left-subs F] [--right-subs F] [--font F] [--out DIR]"
LEFT=$1; RIGHT=$2; shift 2
SPEED=4; HALF_W=960; LEFT_SUBS=""; RIGHT_SUBS=""; FONT=""; OUT="out"
TAGLINE="Ask AI about your lectures. It answers from your own course files."
while [ $# -gt 0 ]; do
  case "$1" in
    --speed) SPEED=$2; shift 2 ;;
    --half-width) HALF_W=$2; shift 2 ;;
    --left-subs) LEFT_SUBS=$2; shift 2 ;;
    --right-subs) RIGHT_SUBS=$2; shift 2 ;;
    --font) FONT=$2; shift 2 ;;
    --tagline) TAGLINE=$2; shift 2 ;;
    --out) OUT=$2; shift 2 ;;
    *) die "unknown option $1" ;;
  esac
done
[ -f "$LEFT" ] || die "no such file: $LEFT"
[ -f "$RIGHT" ] || die "no such file: $RIGHT"

# Absolute paths, because ffmpeg runs from a scratch folder below (the
# subtitles filter is far happier with a bare relative file name).
abspath() { (cd "$(dirname "$1")" && printf '%s/%s' "$(pwd)" "$(basename "$1")"); }
LEFT=$(abspath "$LEFT"); RIGHT=$(abspath "$RIGHT")
[ -z "$LEFT_SUBS" ] || LEFT_SUBS=$(abspath "$LEFT_SUBS")
[ -z "$RIGHT_SUBS" ] || RIGHT_SUBS=$(abspath "$RIGHT_SUBS")
mkdir -p "$OUT"; OUT=$(cd "$OUT" && pwd)

if [ -z "$FONT" ]; then
  for f in /c/Windows/Fonts/segoeuib.ttf "/System/Library/Fonts/Supplemental/Arial Bold.ttf" \
           /usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf /usr/share/fonts/TTF/DejaVuSans-Bold.ttf; do
    [ -f "$f" ] && { FONT=$f; break; }
  done
fi
[ -n "$FONT" ] && [ -f "$FONT" ] || die "no bold font found; pass --font /path/to/font.ttf"
# drawtext wants C\:/Windows/... on Windows; cygpath turns /c/... into C:/...
command -v cygpath >/dev/null && FONT=$(cygpath -m "$FONT")
FONT_ESC=$(printf '%s' "$FONT" | sed 's/:/\\:/g')

dur() { ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 "$1"; }
calc() { awk "BEGIN { printf \"%.3f\", $1 }"; }
mmss() { awk -v s="$1" 'BEGIN { s = int(s); printf "%d:%02d", int(s / 60), s % 60 }'; }

L_RAW=$(dur "$LEFT"); R_RAW=$(dur "$RIGHT")
L_OUT=$(calc "$L_RAW / $SPEED")                 # left half's length on screen
HOLD=2                                          # both halves hold their last frame this long
T=$(calc "($L_OUT > $R_RAW ? $L_OUT : $R_RAW) + $HOLD")
H=$(( HALF_W * 9 / 16 )); H=$(( H - H % 2 ))
BAR=$(( H / 10 )); FS=$(( BAR * 55 / 100 )); PADX=$(( BAR / 2 )); TY=$(( (BAR - FS) / 2 ))
L_REAL=$(mmss "$L_RAW"); R_REAL=$(mmss "$R_RAW")

echo "left:  $(mmss "$L_RAW") real, $(mmss "$L_OUT") on screen at ${SPEED}x"
echo "right: $R_REAL real"

WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
cd "$WORK"
# drawtext reads the tagline from a file, so no quote, colon or % in it
# can break the filter (expansion=none keeps % literal).
printf '%s' "$TAGLINE" > tagline.txt
TAGTXT="textfile=tagline.txt:expansion=none"
LSUB=""; RSUB=""
STYLE="force_style='FontName=Segoe UI,Bold=1,FontSize=20,PrimaryColour=&H00FFFFFF,BackColour=&H99000000,BorderStyle=4,Outline=0,Shadow=0,MarginV=28'"
[ -z "$LEFT_SUBS" ] || { cp "$LEFT_SUBS" left.srt; LSUB="subtitles=left.srt:$STYLE,"; }
[ -z "$RIGHT_SUBS" ] || { cp "$RIGHT_SUBS" right.srt; RSUB="subtitles=right.srt:$STYLE,"; }

FIT="scale=${HALF_W}:${H}:force_original_aspect_ratio=decrease,pad=${HALF_W}:${H}:(ow-iw)/2:(oh-ih)/2:color=0x0d1117,setsar=1,fps=30"
BARBOX="drawbox=x=0:y=0:w=iw:h=${BAR}:color=black@0.72:t=fill"
TXT="fontfile='${FONT_ESC}':fontsize=${FS}:y=${TY}"
# Real elapsed time, frozen once that side is done: min(t, done) * speed.
clock() { # $1 = seconds on screen when done, $2 = speed
  printf "%%{eif\\\\:trunc(min(t\\\\,%s)*%s/60)\\\\:d}\\\\:%%{eif\\\\:mod(trunc(min(t\\\\,%s)*%s)\\\\,60)\\\\:d\\\\:2}" "$1" "$2" "$1" "$2"
}
SPEEDTAG=""
[ "$SPEED" = "1" ] || SPEEDTAG="  ·  ${SPEED}× speed"

FILTER="\
[0:v]${LSUB}setpts=PTS/${SPEED},${FIT},tpad=stop_mode=clone:stop_duration=$(calc "$T - $L_OUT"),${BARBOX},\
drawtext=${TXT}:x=${PADX}:text='The usual way${SPEEDTAG}':fontcolor=white,\
drawtext=${TXT}:x=w-tw-${PADX}:text='$(clock "$L_OUT" "$SPEED")':fontcolor=0xFF6B6B[l];\
[1:v]${RSUB}${FIT},tpad=stop_mode=clone:stop_duration=$(calc "$T - $R_RAW"),${BARBOX},\
drawtext=${TXT}:x=${PADX}:text='With lms-sync':fontcolor=white,\
drawtext=${TXT}:x=w-tw-${PADX}:text='$(clock "$R_RAW" 1)':fontcolor=0x51CF66,\
drawtext=${TXT}:x=(w-tw)/2:y=${BAR}*1.5:text='Answered':fontcolor=0x51CF66:box=1:boxcolor=black@0.72:boxborderw=14:enable='gte(t\\,${R_RAW})'[r];\
[l][r]hstack=inputs=2[stack];\
color=c=0x0d1117:s=$(( HALF_W * 2 ))x${H}:r=30:d=4,setsar=1,\
drawtext=fontfile='${FONT_ESC}':fontsize=$(( FS * 2 )):fontcolor=0xFF6B6B:x=w/4-tw/2:y=h*0.30:text='${L_REAL/:/\\:}',\
drawtext=fontfile='${FONT_ESC}':fontsize=$(( FS * 2 )):fontcolor=0x51CF66:x=3*w/4-tw/2:y=h*0.30:text='${R_REAL/:/\\:}',\
drawtext=fontfile='${FONT_ESC}':fontsize=${FS}:fontcolor=white@0.8:x=w/4-tw/2:y=h*0.30+${FS}*2.6:text='The usual way',\
drawtext=fontfile='${FONT_ESC}':fontsize=${FS}:fontcolor=white@0.8:x=3*w/4-tw/2:y=h*0.30+${FS}*2.6:text='With lms-sync',\
drawtext=fontfile='${FONT_ESC}':fontsize=${FS}:fontcolor=white:x=(w-tw)/2:y=h*0.70:${TAGTXT},\
drawtext=fontfile='${FONT_ESC}':fontsize=$(( FS * 8 / 10 )):fontcolor=white@0.7:x=(w-tw)/2:y=h*0.70+${FS}*1.7:text='github.com/sak0x7d5/lms-sync'[card];\
[stack][card]concat=n=2:v=1:a=0,format=yuv420p[v]"

encode() { # $1 = crf
  ffmpeg -hide_banner -loglevel error -y -i "$LEFT" -i "$RIGHT" -filter_complex "$FILTER" \
    -map "[v]" -an -c:v libx264 -preset slow -crf "$1" -movflags +faststart "$OUT/demo.mp4"
}
size() { wc -c < "$1" | tr -d ' '; }

LIMIT=$(( 95 * 1024 * 1024 / 10 ))              # 9.5 MB, under GitHub's 10 MB free-plan cap
for CRF in 26 29 32 35; do
  encode "$CRF"
  S=$(size "$OUT/demo.mp4")
  echo "demo.mp4 at crf $CRF: $(( S / 1024 )) KB"
  [ "$S" -le "$LIMIT" ] && break
done
[ "$S" -le "$LIMIT" ] || echo "warning: demo.mp4 is still over 9.5 MB — try --half-width 800 or a shorter left recording" >&2

# hero.gif: the right half of the finished video, until it has answered.
GIF_T=$(calc "$R_RAW + $HOLD")
for SPEC in "12 800" "10 720" "8 640"; do
  set -- $SPEC
  ffmpeg -hide_banner -loglevel error -y -t "$GIF_T" -i "$OUT/demo.mp4" \
    -vf "crop=${HALF_W}:${H}:${HALF_W}:0,fps=$1,scale=$2:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
    -loop 0 "$OUT/hero.gif"
  S=$(size "$OUT/hero.gif")
  echo "hero.gif at ${1} fps, ${2}px: $(( S / 1024 )) KB"
  [ "$S" -le $(( 5 * 1024 * 1024 )) ] && break
done

# social-preview.png: its own 1280x640 layout (Settings -> General -> Social preview).
# The tagline is sized from its length so a long one still fits the 1280px width.
SFS=$(( 2100 / (${#TAGLINE} > 1 ? ${#TAGLINE} : 1) )); [ "$SFS" -gt 44 ] && SFS=44; [ "$SFS" -lt 22 ] && SFS=22
ffmpeg -hide_banner -loglevel error -y -f lavfi -i "color=c=0x0d1117:s=1280x640:d=1" -frames:v 1 -vf "\
drawtext=fontfile='${FONT_ESC}':fontsize=64:fontcolor=white:x=(w-tw)/2:y=90:text='lms-sync',\
drawtext=fontfile='${FONT_ESC}':fontsize=${SFS}:fontcolor=white@0.85:x=(w-tw)/2:y=190:${TAGTXT},\
drawtext=fontfile='${FONT_ESC}':fontsize=96:fontcolor=0xFF6B6B:x=w/4-tw/2:y=310:text='${L_REAL/:/\\:}',\
drawtext=fontfile='${FONT_ESC}':fontsize=96:fontcolor=0x51CF66:x=3*w/4-tw/2:y=310:text='${R_REAL/:/\\:}',\
drawtext=fontfile='${FONT_ESC}':fontsize=30:fontcolor=white@0.75:x=w/4-tw/2:y=430:text='The usual way',\
drawtext=fontfile='${FONT_ESC}':fontsize=30:fontcolor=white@0.75:x=3*w/4-tw/2:y=430:text='With lms-sync',\
drawtext=fontfile='${FONT_ESC}':fontsize=28:fontcolor=white@0.6:x=(w-tw)/2:y=560:text='github.com/sak0x7d5/lms-sync'" \
  "$OUT/social-preview.png"

echo
echo "Done ($(mmss "$(dur "$OUT/demo.mp4")") long):"
ls -l "$OUT/demo.mp4" "$OUT/hero.gif" "$OUT/social-preview.png"
