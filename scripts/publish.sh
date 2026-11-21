#!/bin/sh
# Pushes the repository it runs in as one root commit over the remote's
# (git push --force-with-lease against the commit the remote has, so a push
# someone made in between is not lost): until the first release the
# history is a single complete commit.
#   sh scripts/publish.sh "<first line of the commit message>"
set -eu
msg=${1:?the first line of the commit message}
was=$(git ls-remote origin refs/heads/main)
was=${was%%	*}
git add -A
commit=$(printf '%s\n\nOne commit, replaced as the project changes.\n' "$msg" | git commit-tree "$(git write-tree)")
git update-ref refs/heads/main "$commit"
git symbolic-ref HEAD refs/heads/main
git push --force-with-lease=main:"$was" origin main
echo "published $(git rev-parse --short HEAD): $(git ls-files | wc -l) files"
