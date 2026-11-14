#!/bin/sh
# Development machines only (make web-selftest): phone-webview from
# build/webview in its container, the shell's control socket (fd 3) passed
# through. The shell gives the web view a minimal environment pointing into
# the app's data, which the container sees at the same path (downloads and
# uploads pass through it); podman itself needs the user's own environment.
data=$HOME
unset XDG_CACHE_HOME XDG_DATA_HOME XDG_CONFIG_HOME TMPDIR
HOME=$(getent passwd "$(id -u)" | cut -d: -f6)
XDG_RUNTIME_DIR=/run/user/$(id -u)
export HOME XDG_RUNTIME_DIR
dir=$(cd "$(dirname "$0")/../build/webview" && pwd) || exit 1
mkdir -p "$data"
exec podman run --rm -i --network host --http-proxy=false --preserve-fds 1 \
    -v "$dir:/w:ro,Z" -v "$data:$data:Z" -e HOME="$data" -e XDG_RUNTIME_DIR=/tmp \
    localhost/phone-webview-dev /w/phone-webview "$@"
