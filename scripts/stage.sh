#!/bin/sh
# Lay out built binaries the way the device image does.
# Usage: stage.sh <dir with go build output> <prefix>   (dev: build/bin build/usr, Yocto: ${B}/bin ${D}${prefix})
set -eu
src=$1
prefix=$2
here=$(cd "$(dirname "$0")/.." && pwd)

install -d "$prefix/bin" "$prefix/lib/phone/bin" "$prefix/share/phone/apps"
install -m 0644 "$here/brand.json" "$prefix/share/phone/brand.json"
# The composition layers (R11): products and OEMs add theirs next to it.
# System icons the pixel renderer draws (main menu folders).
install -d "$prefix/share/phone/icons"
install -m 0644 "$here"/icons/*.png "$prefix/share/phone/icons/"
install -d "$prefix/share/phone/profile.d"
install -m 0644 "$here"/profile.d/*.json "$prefix/share/phone/profile.d/"
# The fd-store crash case, run as a systemd service by the boot selftest.
install -m 0644 "$here/shell/fdstore.script" "$prefix/share/phone/fdstore.script"
# The system services are one program (kario-system, R45), linked by each
# service's name; it must sit in bin/ (the services find the prefix from
# where it is). The simulated cloud servers stay programs of their own.
install -m 0755 "$src/kario-system" "$prefix/bin/kario-system"
for b in phone-shell phone-mediad phone-keystored phone-audiod phone-imed phone-pushd phone-nfcd phone-smsd phone-cts-verify; do
	ln -sf kario-system "$prefix/bin/$b"
done
ln -sf ../../../bin/kario-system "$prefix/lib/phone/bin/phone-sandbox"
# USB file sharing's sandboxed helper: only the Shell starts it.
ln -sf ../../../bin/kario-system "$prefix/lib/phone/bin/phone-usbshared"
for b in phone-pushgw phone-chatsrv phone-storesrv; do
	install -m 0755 "$src/$b" "$prefix/bin/$b"
done
# The applier of incremental system updates, a program of its own (it
# links the update code only, so a release seldom changes it): their
# install hook prefers it to the bundle's copy when it applies the
# payload's format (25-system-update.md §5.3).
install -m 0755 "$src/phone-ota-delta" "$prefix/bin/phone-ota-delta"
for a in chat wallet settings miniapp dialer messages clock music camera gallery weather store browser snake cards; do
	install -m 0755 "$src/$a" "$prefix/lib/phone/bin/phone-app-$a"
done
# The input method's dictionary, compiled at build time; Yocto builds
# phone-imedict for the build host and passes the result in IME_DICT.
if [ -n "${IME_DICT:-}" ]; then
	install -m 0644 "$IME_DICT" "$prefix/share/phone/ime.dict"
else
	"$src/phone-imedict" -o "$prefix/share/phone/ime.dict" >/dev/null
fi
# The compatibility probe: only phone-shell --cts loads share/phone/cts.
install -m 0755 "$src/probe" "$prefix/lib/phone/bin/phone-app-ctsprobe"
install -d "$prefix/share/phone/cts/probe"
install -m 0644 "$here/cts/probe/manifest.json" "$prefix/share/phone/cts/probe/"
# Sample third-party apps: package sources (not installed); the selftest signs
# them with the development store and installs them through the store app.
install -d "$prefix/share/phone/examples/notes"
install -m 0755 "$src/notes" "$prefix/share/phone/examples/notes/phone-app-notes"
install -m 0644 "$here/examples/notes/manifest.json" "$here/examples/notes/icon.png" "$prefix/share/phone/examples/notes/"
# Tones generated for the test suite (ffmpeg's sine source): music files
# the selftest imports into the media library, which shows a file's name as
# its title when its tags have none. audio/testdata/titles.tsv names them:
# file, tab, title. A DSD file is kept as its header and one period of its
# tone (1-bit samples do not compress), repeated here 2048 times.
install -d "$prefix/share/phone/examples/music"
tab=$(printf '\t')
while IFS=$tab read -r file title; do
	dst="$prefix/share/phone/examples/music/$title.${file##*.}"
	if [ -e "$here/audio/testdata/$file" ]; then
		install -m 0644 "$here/audio/testdata/$file" "$dst"
		continue
	fi
	cp "$here/audio/testdata/$file.period" "$dst.part"
	for _ in 1 2 3 4 5 6 7 8 9 10 11; do
		cat "$dst.part" "$dst.part" > "$dst.part2" && mv "$dst.part2" "$dst.part"
	done
	cat "$here/audio/testdata/$file.head" "$dst.part" > "$dst"
	rm -f "$dst.part"
	chmod 0644 "$dst"
done < "$here/audio/testdata/titles.tsv"
# Public-domain and CC0 photos and videos the media library starts with
# (SOURCES.md).
install -d "$prefix/share/phone/examples/photos"
install -m 0644 "$here"/examples/photos/*.jpg "$here/examples/photos/SOURCES.md" "$prefix/share/phone/examples/photos/"
install -d "$prefix/share/phone/examples/videos"
install -m 0644 "$here"/examples/videos/*.webm "$here/examples/videos/SOURCES.md" "$prefix/share/phone/examples/videos/"
(cd "$here/examples/mp-counter" && find . -type f) | while read -r f; do
	install -D -m 0644 "$here/examples/mp-counter/$f" "$prefix/share/phone/examples/mp-counter/$f" || exit 1
done
# telephonyd and the C SDK's sample app are C++ (CMake); Yocto recipes
# install them on their own.
if [ -x "$here/build/telephonyd/phone-telephonyd" ] && [ -z "${PHONE_NO_TELEPHONYD:-}" ]; then
	install -m 0755 "$here/build/telephonyd/phone-telephonyd" "$prefix/bin/phone-telephonyd"
	install -m 0644 "$here/telephonyd/sim-network.json" "$prefix/share/phone/sim-network.json"
fi
if [ -x "$here/build/capi/phone-app-hellocpp" ]; then
	install -m 0755 "$here/build/capi/phone-app-hellocpp" "$prefix/lib/phone/bin/phone-app-hellocpp"
	install -d "$prefix/share/phone/apps/hello-cpp/locale"
	install -m 0644 "$here"/sdk/c/examples/hello-cpp/locale/*.json "$prefix/share/phone/apps/hello-cpp/locale/"
fi
# TypeScript apps run on kario-jsrt (29-app-development.md), which make jsrt
# builds here: the calculator (a system app) and the sample are compiled
# into their app directories, with bytecode the runtime precompiles. Yocto
# installs the runtime from its own recipe and gives KARIO_CLI, a kario for
# the build host, to compile them (not KARIO: kario-internal's Makefile
# names the kario checkout so, and make hands it down to this script).
# Without either the calculator has no module and does not start.
jsrt=${KARIO_JSRT-$here/build/jsrt/kario-jsrt}
for app in calc:apps/calc hello-ts:examples/hello-ts; do
	dst="$prefix/share/phone/apps/${app%%:*}"
	rm -rf "$dst"
	if [ -x "$jsrt" ]; then
		"$src/kario" build -q -bytecode -jsrt "$jsrt" -o "$dst" "$here/${app#*:}"
	elif [ -n "${KARIO_CLI:-}" ]; then
		"$KARIO_CLI" build -q -o "$dst" "$here/${app#*:}"
	fi
done
if [ -x "$jsrt" ]; then
	install -m 0755 "$jsrt" "$prefix/lib/phone/bin/kario-jsrt"
fi
install -d "$prefix/share/phone/web/chat" "$prefix/share/phone/bench"
install -m 0644 "$here"/web/chat/* "$prefix/share/phone/web/chat/"
# The web view's content blocker rules (the browser's "block ads and
# trackers" setting).
install -m 0644 "$here/web/blocklist.json" "$prefix/share/phone/web/blocklist.json"
install -m 0755 "$here"/bench/bench.py "$prefix/share/phone/bench/"
for d in "$here"/apps/*/; do
	n=$(basename "$d")
	install -d "$prefix/share/phone/apps/$n"
	install -m 0644 "$d/manifest.json" "$prefix/share/phone/apps/$n/"
	if [ -e "$d/icon.png" ]; then install -m 0644 "$d/icon.png" "$prefix/share/phone/apps/$n/"; fi
	for s in "$d"/*.py; do
		if [ -e "$s" ]; then install -m 0755 "$s" "$prefix/share/phone/apps/$n/"; fi
	done
	# A script reads its translations beside it; compiled apps embed theirs.
	if ls "$d"/*.py >/dev/null 2>&1 && [ -d "$d/locale" ]; then
		install -d "$prefix/share/phone/apps/$n/locale"
		install -m 0644 "$d"/locale/*.json "$prefix/share/phone/apps/$n/locale/"
	fi
done
