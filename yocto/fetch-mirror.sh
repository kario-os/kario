#!/bin/sh
# Parallel range download of a Yocto source-mirror tarball (the corporate proxy
# limits throughput per connection, not in aggregate).
# Usage: fetch-mirror.sh <url> <dest> [parts]
set -eu
url=$1
dest=$2
parts=${3:-24}
size=$(curl -sI "$url" | awk 'tolower($1)=="content-length:" {print $2}' | tr -d '\r' | tail -1)
chunk=$(( (size + parts - 1) / parts ))
work="$dest.parts"
mkdir -p "$work"
i=0
while [ $i -lt $parts ]; do
	start=$((i * chunk))
	end=$((start + chunk - 1))
	[ $end -ge $size ] && end=$((size - 1))
	(curl -s --retry 10 --retry-delay 2 -C - -r "$start-$end" -o "$work/$(printf %03d $i)" "$url" || echo "part $i failed") &
	i=$((i + 1))
done
wait
cat "$work"/* > "$dest.tmp"
got=$(stat -c %s "$dest.tmp")
if [ "$got" != "$size" ]; then
	echo "size mismatch: got $got want $size" >&2
	exit 1
fi
mv "$dest.tmp" "$dest"
rm -rf "$work"
echo "ok $dest ($size bytes)"
