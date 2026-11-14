#!/usr/bin/env python3
"""Serves a directory over HTTP with byte ranges, for the system update test
(qemu-ota.sh): RAUC streams a bundle with range requests, which
`python3 -m http.server` does not answer. Each request is logged as
"SENT <path> <bytes> <first byte>", so the test can tell how much of a
bundle an (adaptive) install downloaded, and which parts it read twice. POST /report keeps phones' install reports in
reports.jsonl as sent (phone-ota-verify checks them); with OTA_REPORT_TO it
hands them to that update server instead.

    [OTA_REPORT_TO=http://host:port] ota-www.py PORT DIR
"""

import http.server
import os
import re
import sys
import urllib.error
import urllib.request

REPORT_TO = os.environ.get("OTA_REPORT_TO")
# The update server is on this machine; a proxy from the environment must not see it.
DIRECT = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def forward(body):
    req = urllib.request.Request(REPORT_TO + "/report", data=body, headers={"Content-Type": "application/json"})
    try:
        with DIRECT.open(req, timeout=10) as res:
            print("REPORT %d" % res.status, flush=True)
            return res.status
    except urllib.error.HTTPError as e:
        print("REPORT %d %s" % (e.code, e.read().decode(errors="replace").strip()), flush=True)
        return e.code
    except OSError as e:
        print("REPORT unreachable: %s" % e, flush=True)
        return 502


class Handler(http.server.SimpleHTTPRequestHandler):
    protocol_version = "HTTP/1.1"  # keep-alive: a streamed install makes many small requests
    left = None  # bytes of the range still to send; None: the whole body
    first = 0

    def send_head(self):
        path = self.translate_path(self.path)
        if os.path.isdir(path):
            return super().send_head()
        try:
            f = open(path, "rb")
        except OSError:
            self.send_error(404)
            return None
        size = os.fstat(f.fileno()).st_size
        m = re.fullmatch(r"bytes=(\d*)-(\d*)", self.headers.get("Range", ""))
        start, end = 0, size - 1
        if m and m.group(1):
            start = int(m.group(1))
            end = min(int(m.group(2)), size - 1) if m.group(2) else size - 1
        elif m and m.group(2):
            start = max(0, size - int(m.group(2)))
        if m and (start > end or start >= size):
            f.close()
            self.send_response(416)
            self.send_header("Content-Range", "bytes */%d" % size)
            self.end_headers()
            return None
        self.send_response(206 if m else 200)
        self.send_header("Content-Type", self.guess_type(path))
        self.send_header("Content-Length", str(end - start + 1))
        self.send_header("Accept-Ranges", "bytes")
        if m:
            self.send_header("Content-Range", "bytes %d-%d/%d" % (start, end, size))
        self.end_headers()
        f.seek(start)
        self.left, self.first = end - start + 1, start
        return f

    def copyfile(self, src, dst):
        sent = 0
        if self.left is None:
            while chunk := src.read(1 << 16):
                dst.write(chunk)
                sent += len(chunk)
        else:
            while self.left > 0:
                chunk = src.read(min(self.left, 1 << 16))
                if not chunk:
                    break
                dst.write(chunk)
                self.left -= len(chunk)
                sent += len(chunk)
        print("SENT %s %d %d" % (self.path, sent, self.first), flush=True)


class ReportHandler(Handler):
    """POST /report: a phone's outcome of an install, kept one per line in
    reports.jsonl."""

    def do_POST(self):
        if self.path.rstrip("/").endswith("/report"):
            body = self.rfile.read(min(int(self.headers.get("Content-Length", 0)), 8192))
            if REPORT_TO:
                code = forward(body)
            else:
                with open(os.path.join(self.directory, "reports.jsonl"), "ab") as fh:
                    fh.write(body.strip() + b"\n")
                code = 204
            self.send_response(code)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self.send_error(404)


def main():
    port, root = int(sys.argv[1]), sys.argv[2]
    server = http.server.ThreadingHTTPServer(("", port), lambda *a: ReportHandler(*a, directory=root))
    server.serve_forever()


if __name__ == "__main__":
    main()
