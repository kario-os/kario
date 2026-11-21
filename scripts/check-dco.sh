#!/bin/sh
# Every commit in a range must be signed off by its author: the Developer
# Certificate of Origin 1.1 (60-repositories.md §1). For CI:
#   scripts/check-dco.sh origin/main..HEAD
set -u
range=${1:-origin/main..HEAD}
bad=0
for c in $(git rev-list --no-merges "$range"); do
	author=$(git log -1 --format='%an <%ae>' "$c")
	if ! git log -1 --format='%B' "$c" | grep -qF "Signed-off-by: $author"; then
		echo "DCO: $(git log -1 --format='%h %s' "$c"): missing 'Signed-off-by: $author'"
		bad=1
	fi
done
if [ "$bad" -ne 0 ]; then
	echo "DCO-CHECK: FAIL (commit with git commit -s)"
	exit 1
fi
echo "DCO-CHECK: PASS"
