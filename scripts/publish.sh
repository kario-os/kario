#!/bin/sh
# Pushes the repository it runs in as one root commit over the remote's
# copy of the branch checked out here (git push --force-with-lease against
# the commit the remote has, so a push someone made in between is not
# lost): until the first release each branch's history is a single
# complete commit. main is what phones are tested on; dev is where new
# work lands before it goes to main (docs/38-contributing.md §10). It
# commits every file git does not ignore, so it refuses while one of them
# is a program or too big (scripts/binary-check.py).
#   sh scripts/publish.sh "<first line of the commit message>"
set -eu
msg=${1:?the first line of the commit message}
branch=$(git symbolic-ref --short -q HEAD) || {
	echo "publish: no branch checked out (detached HEAD)" >&2
	exit 1
}
case $branch in
main | dev) ;;
*)
	echo "publish: $branch is neither main nor dev" >&2
	exit 1
	;;
esac
python3 "$(dirname "$0")/binary-check.py" "$(git rev-parse --show-toplevel)" || {
	echo "publish: not published: the files above would go out with it (scripts/binary-check.py)" >&2
	exit 1
}
was=$(git ls-remote origin "refs/heads/$branch")
was=${was%%	*}
git add -A
commit=$(printf '%s\n\nOne commit, replaced as the project changes.\n' "$msg" | git commit-tree "$(git write-tree)")
git update-ref "refs/heads/$branch" "$commit"
git push --force-with-lease="$branch:$was" origin "$branch"
echo "published $branch $(git rev-parse --short HEAD): $(git ls-files | wc -l) files"
