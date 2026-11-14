#!/usr/bin/env python3
"""Fetch the sample photos and videos the media library starts with
(examples/photos, examples/videos).

Each is the first file Wikimedia Commons finds for a subject whose licence is
public domain or CC0 (checked in the file's metadata, not assumed). Photos
are scaled to 1080 pixels on the long side; videos cut to 15 seconds at 480
pixels wide as WebM (VP9 and Opus: royalty-free, decoded by ffmpeg's own
decoders). SOURCES.md next to them records where each came from. Re-run to
choose again:

    python3 tools/samples.py          # all of them
    python3 tools/samples.py NAME...  # just these (names from samples.json)
"""
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from PIL import Image

API = "https://commons.wikimedia.org/w/api.php"
HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(HERE, "examples", "photos")
FREE = {"public domain", "cc0", "cc0 1.0", "pd"}
# samples.json: the name in the library, then what to look for; searches
# for videos return too much else (newsreels, lectures), so those name the
# file.
with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "samples.json"), encoding="utf-8") as f:
    _SAMPLES = json.load(f)
SUBJECTS = [tuple(s) for s in _SAMPLES["photos"]]
VIDEOS = [tuple(s) for s in _SAMPLES["videos"]]
VOUT = os.path.join(HERE, "examples", "videos")


UA = {"User-Agent": "Kario-phone-samples/1.0 (prototype; samples for an open-source phone OS)"}


def fetch(url):
    """The body at url; Commons limits request rates, so wait and retry."""
    for wait in (2, 5, 15, 30, 60):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers=UA), timeout=120) as r:
                body = r.read()
            time.sleep(1)
            return body
        except urllib.error.HTTPError as e:
            if e.code != 429:
                raise
            time.sleep(wait)
    raise RuntimeError("Commons keeps refusing: " + url)


def get(params):
    return json.loads(fetch(API + "?" + urllib.parse.urlencode({**params, "format": "json"})))


def painted(title, meta):
    """Scans of paintings and prints come up too; the library wants photos."""
    text = (title + " " + meta.get("Categories", {}).get("value", "")).lower()
    return any(w in text for w in ("painting", "drawing", "museum", "art gallery", "center for british art", "lithograph", "engraving"))


def free(meta):
    lic = meta.get("LicenseShortName", {}).get("value", "").strip().lower()
    return lic in FREE or lic.startswith("public domain") or lic.startswith("cc0")


def pick(query, taken, video=False):
    if query.startswith("File:"):
        titles = [query]
    else:
        found = get({"action": "query", "list": "search", "srnamespace": 6, "srlimit": 40,
                     "srsearch": query + (" filetype:video" if video else " filetype:bitmap")})
        ends = (".webm", ".ogv") if video else (".jpg", ".jpeg")
        titles = [s["title"] for s in found["query"]["search"] if s["title"].lower().endswith(ends)]
    for i in range(0, len(titles), 10):
        info = get({"action": "query", "titles": "|".join(titles[i:i + 10]), "prop": "imageinfo",
                    "iiprop": "url|size|extmetadata", "iiurlwidth": 1080})
        for page in info["query"]["pages"].values():
            ii = page.get("imageinfo", [{}])[0]
            meta = ii.get("extmetadata", {})
            if page["title"] in taken or not free(meta) or painted(page["title"], meta):
                continue
            if video:
                if ii.get("width", 0) < 640 or not 15 <= ii.get("duration", 0) <= 600 or ii.get("size", 0) > 200 << 20:
                    continue
            elif ii.get("width", 0) < 1080 or ii.get("height", 0) < 720:
                continue
            return page["title"], ii, meta
    return None


def author(meta):
    artist = meta.get("Artist", {}).get("value", "").replace("\n", " ")
    return re.sub(r"(Unknown author)+", "Unknown author", re.sub(r"<[^>]+>", "", artist)).strip()


def videos(only):
    os.makedirs(VOUT, exist_ok=True)
    rows = []
    for name, query in VIDEOS:
        if only and name not in only:
            continue
        got = pick(query, set(), video=True)
        if not got:
            print(f"{name}: no free video found for {query!r}", file=sys.stderr)
            continue
        title, ii, meta = got
        with tempfile.NamedTemporaryFile(suffix=os.path.splitext(title)[1]) as src:
            src.write(fetch(ii["url"]))
            src.flush()
            out = os.path.join(VOUT, f"{name}.webm")
            subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", src.name, "-t", "15",
                            "-vf", "scale=480:-2", "-r", "25", "-c:v", "libvpx-vp9", "-crf", "38", "-b:v", "450k", "-row-mt", "1",
                            "-c:a", "libopus", "-b:a", "64k", "-ac", "2", out], check=True)
        lic = meta.get("LicenseShortName", {}).get("value", "")
        rows.append(f"| {name}.webm | [{title[5:]}]({ii['descriptionurl']}) | {author(meta) or '—'} | {lic} |")
        print(f"{name}: {title} ({lic}) -> {os.path.getsize(out) // 1024} KB")
    if rows:
        with open(os.path.join(VOUT, "SOURCES.md"), "w", encoding="utf-8") as f:
            f.write("# Sample videos\n\nPublic-domain and CC0 videos from Wikimedia Commons (tools/samples.py), "
                    "the first 15 seconds at 480 pixels wide, VP9 and Opus in WebM.\n\n"
                    "| File | Source | Author | Licence |\n|---|---|---|---|\n")
            f.write("\n".join(rows) + "\n")


def main():
    os.makedirs(OUT, exist_ok=True)
    only = set(sys.argv[1:])
    videos(only)
    if only and not only & {n for n, _ in SUBJECTS}:
        return
    videos(only)
    if only and not only & {n for n, _ in SUBJECTS}:
        return
    sources = os.path.join(OUT, "SOURCES.md")
    kept = {}
    if only and os.path.exists(sources):
        for line in open(sources, encoding="utf-8"):
            m = re.match(r"\| (\S+)\.jpg \|", line)
            if m:
                kept[m.group(1)] = line.rstrip("\n")
    rows, taken = [], set()
    for name, query in SUBJECTS:
        if only and name not in only:
            if name in kept:
                rows.append(kept[name])
            continue
        got = pick(query, taken)
        if not got:
            print(f"{name}: nothing free found for {query!r}", file=sys.stderr)
            continue
        title, ii, meta = got
        taken.add(title)
        img = Image.open(io.BytesIO(fetch(ii["thumburl"]))).convert("RGB")
        img.thumbnail((1080, 1080), Image.LANCZOS)
        path = os.path.join(OUT, f"{name}.jpg")
        img.save(path, "JPEG", quality=80, optimize=True, progressive=True)
        artist = author(meta)
        lic = meta.get("LicenseShortName", {}).get("value", "")
        rows.append(f"| {name}.jpg | [{title[5:]}]({ii['descriptionurl']}) | {artist or '—'} | {lic} |")
        print(f"{name}: {title} ({lic}) -> {os.path.getsize(path) // 1024} KB")
    with open(sources, "w", encoding="utf-8") as f:
        f.write("# Sample photos\n\nPublic-domain and CC0 photos from Wikimedia Commons (tools/samples.py), "
                "scaled to 1080 pixels on the long side.\n\n| File | Source | Author | Licence |\n|---|---|---|---|\n")
        f.write("\n".join(rows) + "\n")


if __name__ == "__main__":
    main()
