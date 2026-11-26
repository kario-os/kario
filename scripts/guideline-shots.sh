#!/bin/sh
# The do and don't pictures of the app guidelines (docs/33-app-guidelines.md
# §1.3): the pages of examples/guidelines, run as the developer's app in the
# Shell, drawn by phone-paint headless as the pixel selftest draws its
# snapshots (examples/guidelines/shots.script), once in Chinese and once in
# English, then made small (tools/pngshrink) into docs/img/33/.
#   scripts/guideline-shots.sh      (make guideline-shots: after make build paint)
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
prefix=${PREFIX:-$here/build/usr}
paint=${PAINT:-$here/paint/target/release/phone-paint}
out=$here/docs/img/33
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
"$here/build/bin/kario" build -q -o "$tmp/app" "$here/examples/guidelines"
(cd "$here" && go build -o "$tmp/pngshrink" ./tools/pngshrink)
mkdir -p "$out"
for lang in zh-Hans en; do
	PHONE_LANG=$lang "$prefix/bin/phone-shell" --prefix "$prefix" --script "$here/examples/guidelines/shots.script" \
		--dev-app "$tmp/app" --gui "$paint" --device bar-t9 --pixel-dir "$tmp/$lang" --snap-dir "$tmp/$lang.text" >"$tmp/$lang.log" 2>&1 ||
		{ cat "$tmp/$lang.log"; exit 1; }
	suffix=
	[ "$lang" = zh-Hans ] || suffix=.$lang
	"$tmp/pngshrink" -scale 2 -colors 16 -suffix "$suffix" -o "$out" "$tmp/$lang"/*.png
done
ls -l "$out"
