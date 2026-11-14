#!/bin/sh
# What this machine has and lacks for the kario targets (make doctor): the
# host build and tests first, then the device image. Exit status 1 when
# something the host targets need is missing.
#   sh scripts/doctor.sh [yocto dir]
yocto=${1:-$HOME/yocto}
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

echo "host build and tests (make build, test, selftest, preview, pixel-selftest):"
need go 1.25 "go version" "the platform"
cxx=$(command -v /opt/rh/gcc-toolset-14/root/usr/bin/g++ || command -v g++-14 || command -v g++)
if [ -n "$cxx" ] && version_ge "$($cxx -dumpversion)" 14; then ok "C++23 compiler $cxx $($cxx -dumpversion)"; else miss "GCC 14 or newer (telephonyd is C++23; gcc-toolset-14 on RHEL, g++-14 on Debian / Ubuntu)"; fi
need cmake 3.20 "cmake --version" "telephonyd, the C SDK"
need ninja 1.10 "ninja --version" "telephonyd, the C SDK"
need cargo 1.92 "cargo --version" "phone-gui (rustup)"
need python3 3.11 "python3 --version" "tests and tools"
if command -v pkg-config >/dev/null 2>&1 && pkg-config --exists libsystemd; then ok "libsystemd (sd-bus)"; else warn "no libsystemd development files: telephonyd builds without its ModemManager backend here (make mm-selftest builds it in a container)"; fi
if fc-list 2>/dev/null | grep -q -i 'Noto Sans CJK' || ls /usr/share/fonts/*noto*cjk* /usr/share/fonts/opentype/noto >/dev/null 2>&1; then ok "Noto Sans CJK"; else miss "the Noto Sans CJK fonts the pixel renderers draw with (google-noto-sans-cjk-fonts / fonts-noto-cjk)"; fi
if command -v ffmpeg >/dev/null 2>&1; then ok "ffmpeg (video cases)"; else warn "no ffmpeg: the video cases are skipped"; fi
if [ -d vendor ]; then ok "vendor/ (make vendor)"; else warn "no vendor/: run make vendor for builds without the network"; fi

echo "containers (make mm-selftest, web-selftest) and the device image (make yocto-*):"
if command -v podman >/dev/null 2>&1; then
	ok "podman $(podman --version | grep -o -E '[0-9.]+' | head -1)"
	if grep -q "^$(id -un):" /etc/subuid 2>/dev/null; then ok "subordinate uids for rootless podman"; else warn "no /etc/subuid entry for $(id -un): rootless podman cannot map users"; fi
else
	warn "no podman: no container tests, no device image"
fi
kas=${KAS:-$HOME/.local/kas-venv/bin/kas-container}
if [ -x "$kas" ]; then ok "kas-container $kas"; else warn "no kas-container at $kas (python3 -m venv ~/.local/kas-venv && ~/.local/kas-venv/bin/pip install kas, or KAS=)"; fi
python3 -c 'import numpy' 2>/dev/null && ok "numpy (yocto/audio-check.py)" || warn "no numpy: make yocto-selftest cannot check the sound"
mkdir -p "$yocto" 2>/dev/null
free=$(df -Pk "$yocto" 2>/dev/null | awk 'NR == 2 {print int($4 / 1048576)}')
if [ "${free:-0}" -ge 150 ]; then ok "$free GB free under $yocto"; else warn "${free:-?} GB free under $yocto: a first image build wants about 150 GB (sources, sstate, two build directories)"; fi
cpus=$(nproc)
mem=$(awk '/MemTotal/ {print int($2 / 1048576)}' /proc/meminfo)
if [ "$cpus" -ge 8 ] && [ "$mem" -ge 16 ]; then ok "$cpus CPUs, $mem GB of memory"; else warn "$cpus CPUs, $mem GB of memory: the first image build takes hours"; fi

[ $bad = 0 ] && echo "DOCTOR: the host targets can run" || echo "DOCTOR: install what is marked MISS"
exit $bad
