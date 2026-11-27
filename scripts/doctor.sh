#!/bin/sh
# What this machine has and lacks for the kario targets (make doctor): the
# host build and tests, writing apps, the device image, the containers of
# two host tests. MISS marks what its part cannot do without, warn what only
# some checks want. Exit status 1 when the host targets miss something, or,
# with --image (make yocto-doctor: that part alone), the device image.
#   sh scripts/doctor.sh [--image] [yocto dir]
# The Makefile passes what its targets use: GO, CXX23, KAS,
# KAS_CONTAINER_ENGINE, WORK, BB_NUMBER_THREADS and PARALLEL_MAKE.
part=all
if [ "${1:-}" = --image ]; then
	part=image
	shift
fi
yocto=${1:-$HOME/yocto}
here=$(cd "$(dirname "$0")/.." && pwd)
GO=${GO:-go}
CXX23=${CXX23:-$(command -v /opt/rh/gcc-toolset-14/root/usr/bin/g++ || command -v g++-14 || command -v g++)}
KAS=${KAS:-$(command -v "$HOME/.local/kas-venv/bin/kas-container" || command -v kas-container || echo "$HOME/.local/kas-venv/bin/kas-container")}
if [ -z "${KAS_CONTAINER_ENGINE:-}" ]; then
	KAS_CONTAINER_ENGINE=docker
	command -v podman >/dev/null 2>&1 && KAS_CONTAINER_ENGINE=podman
fi
WORK=${WORK:-$yocto/kario-work}
user=$(id -un)
bad=0
ok() { printf '  ok    %s\n' "$*"; }
miss() { printf '  MISS  %s\n' "$*"; bad=1; }
warn() { printf '  warn  %s\n' "$*"; }

# version_ge 1.25.3 1.25: the first is at least the second.
version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]; }

need() { # need <command> <min version> <version command> <what for>
	if ! command -v "$1" >/dev/null 2>&1; then
		miss "$1 ($4)"
		return
	fi
	v=$(sh -c "$3" 2>/dev/null | grep -o -E '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1)
	if [ -n "$2" ] && ! version_ge "${v:-0}" "$2"; then
		miss "$1 $v, need $2 or newer ($4)"
	else
		ok "$1 $v"
	fi
}

gcc14() { # gcc14 <what for>
	if [ -n "$CXX23" ] && command -v "$CXX23" >/dev/null 2>&1 && version_ge "$("$CXX23" -dumpversion)" 14; then
		ok "C++23 compiler $CXX23 $("$CXX23" -dumpversion)"
	else
		miss "GCC 14 or newer, ${CXX23:-g++} is not ($1; gcc-toolset-14 on RHEL, g++-14 on Debian / Ubuntu)"
	fi
}

subids() { # subids u|g: how many subordinate uids or gids this user has
	if command -v getsubids >/dev/null 2>&1; then
		if [ "$1" = g ]; then n=$(getsubids -g "$user" 2>/dev/null); else n=$(getsubids "$user" 2>/dev/null); fi
		n=$(printf '%s\n' "$n" | awk 'NF {s += $NF} END {print s + 0}')
	else
		n=$(awk -F: -v u="$user" -v i="$(id -u)" '$1 == u || $1 == i {s += $3} END {print s + 0}' "/etc/sub${1}id" 2>/dev/null)
	fi
	echo "${n:-0}"
}

gomin=$(awk '$1 == "go" {print $2; exit}' "$here/go.mod")

if [ $part = all ]; then
	echo "host build and tests (make build, test, selftest, preview, pixel-selftest):"
	need "$GO" "$gomin" "$GO version" "the platform: go.mod's go line"
	gcc14 "telephonyd is C++23"
	need cmake 3.20 "cmake --version" "telephonyd, the C SDK"
	need ninja 1.10 "ninja --version" "telephonyd, the C SDK"
	need cargo 1.92 "cargo --version" "phone-gui (rustup)"
	need python3 3.11 "python3 --version" "tests and tools"
	if command -v pkg-config >/dev/null 2>&1 && pkg-config --exists libcurl; then ok "libcurl $(pkg-config --modversion libcurl) (kario-jsrt's fetch)"; else miss "the libcurl development files kario-jsrt's fetch builds with (libcurl4-openssl-dev on Debian / Ubuntu, libcurl-devel on RHEL)"; fi
	if command -v pkg-config >/dev/null 2>&1 && pkg-config --exists libsystemd; then ok "libsystemd (sd-bus)"; else warn "no libsystemd development files: telephonyd builds without its ModemManager backend here (make mm-selftest builds it in a container)"; fi
	if fc-list 2>/dev/null | grep -q -i 'Noto Sans CJK' || ls /usr/share/fonts/*noto*cjk* /usr/share/fonts/opentype/noto >/dev/null 2>&1; then ok "Noto Sans CJK"; else miss "the Noto Sans CJK fonts the pixel renderers draw with (google-noto-sans-cjk-fonts / fonts-noto-cjk)"; fi
	if command -v ffmpeg >/dev/null 2>&1; then ok "ffmpeg (video cases)"; else warn "no ffmpeg: the video cases are skipped"; fi
	if [ -d "$here/vendor" ]; then ok "vendor/ (make vendor)"; else warn "no vendor/: run make vendor for builds without the network"; fi
	host=$bad

	echo "writing apps (kario run, test, preview; Kario Studio; docs/35-quickstart.md):"
	nv=$(node --version 2>/dev/null)
	if [ -z "$nv" ]; then
		warn "no node on PATH: kario run, test and preview need Node.js 22.15 or newer"
	elif version_ge "${nv#v}" 22.15; then
		ok "node $nv"
	else
		warn "node $nv ($(command -v node)): kario run, test and preview need Node.js 22.15 or newer (kario -node PATH, or KARIO_NODE)"
	fi
	if [ -x "$here/paint/target/release/phone-paint" ]; then ok "phone-paint (kario run -preview, kario preview)"; else warn "no phone-paint: make paint (kario run -preview, kario preview)"; fi
	k=$(command -v kario 2>/dev/null)
	if [ -z "$k" ]; then
		warn "no kario on PATH: make install-kario (a link in ~/.local/bin to build/bin/kario)"
	elif [ "$(readlink -f "$k")" = "$here/build/bin/kario" ]; then
		ok "kario $k (this tree's build/bin/kario)"
	else
		warn "kario on PATH is $k, not this tree's build/bin/kario (make install-kario)"
	fi
fi

echo "device image (make yocto-*; yocto-selftest and the update tests build the host targets first):"
bad=0
case $KAS_CONTAINER_ENGINE in
podman | docker)
	if ! command -v "$KAS_CONTAINER_ENGINE" >/dev/null 2>&1; then
		miss "podman or docker: kas-container builds in a container (KAS_CONTAINER_ENGINE chooses; $KAS_CONTAINER_ENGINE is not on PATH)"
	elif [ "$KAS_CONTAINER_ENGINE" = docker ] && ! docker info >/dev/null 2>&1; then
		miss "docker cannot reach its daemon (is $user in the docker group?)"
	else
		ok "$KAS_CONTAINER_ENGINE $($KAS_CONTAINER_ENGINE --version | grep -o -E '[0-9]+\.[0-9.]+' | head -1) (kas-container's engine)"
	fi
	;;
*)
	miss "KAS_CONTAINER_ENGINE=$KAS_CONTAINER_ENGINE: kas-container runs podman or docker"
	;;
esac
if [ "$KAS_CONTAINER_ENGINE" = podman ] && [ "$(id -u)" != 0 ]; then
	su=$(subids u)
	sg=$(subids g)
	if [ "$su" -ge 65536 ] && [ "$sg" -ge 65536 ]; then
		ok "$su subordinate uids and $sg gids for $user (rootless podman)"
	else
		miss "$su subordinate uids and $sg gids for $user: rootless podman maps the container's ids 0 to 65535 onto them (as root: usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $user)"
	fi
	if command -v newuidmap >/dev/null 2>&1 && command -v newgidmap >/dev/null 2>&1; then
		ok "newuidmap and newgidmap (rootless podman)"
	else
		miss "newuidmap and newgidmap, which rootless podman maps the ids with (uidmap on Debian / Ubuntu, shadow-utils on RHEL)"
	fi
	if [ "$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo 1)" = 0 ]; then
		miss "user namespaces are off (user.max_user_namespaces = 0); rootless podman needs them"
	fi
fi
if [ -x "$KAS" ]; then
	kv=$(sed -n 's/^KAS_CONTAINER_SCRIPT_VERSION="\(.*\)"$/\1/p' "$KAS" | head -1)
	ok "kas-container ${kv:-?} $KAS"
else
	miss "kas-container at $KAS, the one the Makefile runs (python3 -m venv ~/.local/kas-venv && ~/.local/kas-venv/bin/pip install kas; or KAS=<path>)"
fi
need "$GO" "$gomin" "GOTOOLCHAIN=local $GO version" "make gomods, which the build runs when go.mod or go.sum changed, with this Go alone: go.mod's go line"
gcc14 "make build, before the boot and update tests and to check their reports"
need cargo 1.92 "cargo --version" "make paint: a release puts phone-paint for development machines beside its images"
need openssl "" "openssl version" "make ota-keys: the development update keys the image trusts"
need python3 3.11 "python3 --version" "tools/sku.py writes the SKU's files and kas overlay"
d=$WORK
while [ ! -d "$d" ]; do d=$(dirname "$d"); done
free=$(df -Pk "$d" 2>/dev/null | awk 'NR == 2 {print int($4 / 1048576)}')
if [ -d "$WORK/build" ]; then
	want=20
	why="a build in it rewrites the recipes that changed, the root file system and the disk images"
else
	want=150
	why="a first build fetches the sources and fills the sstate cache and the build directory"
fi
if [ "${free:-0}" -ge $want ]; then
	ok "$free GB free for $WORK (at least $want)"
else
	miss "${free:-?} GB free for $WORK: $why, about $want GB"
fi
jobs=$(python3 "$here/scripts/yocto-jobs.py" --explain 2>/dev/null)
n=${jobs%% *}
ok "bitbake runs ${BB_NUMBER_THREADS:-$n} tasks at once, make ${PARALLEL_MAKE:--j $n} in each (YOCTO_JOBS= sets both; here: $jobs)"
cpus=$(nproc)
mem=$(awk '/MemTotal/ {print int($2 / 1048576)}' /proc/meminfo)
if [ "$cpus" -lt 8 ] || [ "$mem" -lt 16 ]; then warn "$cpus CPUs, $mem GB of memory: the first image build takes many hours"; fi
if python3 -c 'import numpy' 2>/dev/null; then ok "numpy (yocto/audio-check.py)"; else warn "no numpy: make yocto-selftest cannot check the sound"; fi
image=$bad

if [ $part = all ]; then
	echo "containers (make mm-selftest, web-selftest):"
	if command -v podman >/dev/null 2>&1; then ok "podman"; else warn "no podman: make mm-selftest and web-selftest do not run here"; fi
	if [ "$host" = 0 ]; then echo "DOCTOR: the host targets can run"; else echo "DOCTOR: the host targets miss what is marked MISS under them"; fi
fi
if [ $image = 0 ]; then
	echo "DOCTOR: the device image can be built"
elif [ $part = all ]; then
	echo "DOCTOR: the device image cannot be built here: install what is marked MISS under it (make yocto-doctor checks that part alone)"
else
	echo "DOCTOR: the device image cannot be built here: install what is marked MISS"
fi
if [ $part = all ]; then exit "$host"; fi
exit $image
