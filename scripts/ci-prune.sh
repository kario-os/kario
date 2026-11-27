#!/bin/sh
# Deletes the GitHub Actions runs of a repository past the newest KEEP of
# each workflow (pages deployments, AI review and CodeQL included), with
# their logs and artifacts; runs not finished stay. CI's last job runs it
# (38-contributing.md); by hand it needs gh logged in with the repository's
# admin rights.
#
#   scripts/ci-prune.sh [-n] OWNER/REPO [KEEP]    KEEP defaults to 3; -n lists only
set -eu

dry=0
if [ "${1:-}" = -n ]; then
	dry=1
	shift
fi
[ $# -ge 1 ] || { sed -n '2,8s/^# \{0,1\}//p' "$0" >&2; exit 2; }
repo=$1 keep=${2:-3}
case $keep in '' | *[!0-9]*) echo "ci-prune: KEEP is a number" >&2; exit 2 ;; esac

# Newest first, as the API lists them: a run is old when KEEP of its
# workflow came before it.
old=$(gh api --paginate "repos/$repo/actions/runs?per_page=100" \
	-q '.workflow_runs[] | "\(.workflow_id) \(.id) \(.status) \(.name)"' |
	awk -v keep="$keep" '{ n[$1]++ } n[$1] > keep && $3 == "completed" { print $2 }')
count=0
for id in $old; do
	if [ "$dry" = 1 ]; then
		echo "would delete run $id"
	else
		gh api -X DELETE "repos/$repo/actions/runs/$id" >/dev/null
	fi
	count=$((count + 1))
done
echo "ci-prune: $repo: $count runs $([ "$dry" = 1 ] && echo to delete || echo deleted), the newest $keep of each workflow kept"
