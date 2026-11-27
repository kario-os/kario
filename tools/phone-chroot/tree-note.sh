#!/bin/sh
# What this tree is, for the record of what was tested (the kit's kit.txt)
# and of what an image was built from (tools/sku.py gen writes it into the
# image as share/phone/source.txt): its commit and whether it has changes
# (git status --short, the first 50; a new file .gitignore does not name
# counts, whatever status.showUntrackedFiles says), or the commit REV
# names, which has none. scripts/coverage.py and yocto/qemu-ota-delta.sh
# label what they report with its first line.
#
#   tree-note.sh [REV]
set -u
fw=$(cd "$(dirname "$0")/../.." && pwd)
if [ $# -ge 1 ]; then
	if c=$(git -C "$fw" rev-parse --verify "$1^{commit}" 2> /dev/null); then
		echo "$c, no changes"
	else
		echo "not a commit of $fw: $1"
	fi
	exit 0
fi
if ! c=$(git -C "$fw" rev-parse HEAD 2> /dev/null); then
	echo "not a git tree ($fw)"
	exit 0
fi
changes=$(git -C "$fw" status --short --untracked-files=normal 2> /dev/null)
if [ -z "$changes" ]; then
	echo "$c, no changes"
else
	echo "$c, with changes:"
	printf '%s\n' "$changes" | head -n 50 | sed 's/^/  /'
fi
