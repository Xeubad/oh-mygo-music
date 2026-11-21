#!/usr/bin/env bash
# Clears what the app keeps about the listener, so it opens as on first use:
# its settings, where playback was left, its window, its local library and its
# plugins' data. The songs it cached stay unless --songs is given.
#
#   scripts/reset.sh              the development build (mygo dev)
#   scripts/reset.sh --release    the installed build (mygo build)
#   scripts/reset.sh --songs      also clears the cached songs
#
# The app saves its settings and session as it quits, so a running one is
# quit first.
set -euo pipefail

name="MyGo Music"
display_name="MyGo Music by XEU"
id="dev.egoist.music"
release=0 songs=0
for arg in "$@"; do
	case "$arg" in
	--release) release=1 ;;
	--songs) songs=1 ;;
	-h | --help)
		sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "unknown option: $arg" >&2
		exit 2
		;;
	esac
done
if [[ $release == 0 ]]; then
	name+=" Dev"
	display_name+=" Dev"
	id+=".dev"
fi

if [[ $(uname) != Darwin ]]; then
	echo "only macOS for now" >&2
	exit 1
fi
data="$HOME/Library/Application Support/$name"
cache="$HOME/Library/Caches/$name"
process_pattern='/(MyGo Music|MyGo Music by XEU)( Dev)?\.app/Contents/MacOS/'

if pgrep -f "$process_pattern" >/dev/null; then
	echo "quitting $display_name…"
	osascript -e "tell application id \"$id\" to quit" >/dev/null 2>&1 || true
	for _ in $(seq 50); do
		pgrep -f "$process_pattern" >/dev/null || break
		sleep 0.1
	done
	if pgrep -f "$process_pattern" >/dev/null; then
		echo "$display_name is still running: quit it and try again" >&2
		exit 1
	fi
fi

if [[ -d $data ]]; then
	for f in "$data"/* "$data"/.[!.]*; do
		[[ -e $f ]] || continue
		rm -rf "$f"
		echo "removed ${f/#$HOME/~}"
	done
fi

if [[ $songs == 1 && -d $cache ]]; then
	rm -rf "$cache"
	echo "removed ${cache/#$HOME/~}"
fi
echo "$name will open as on first use"
