#!/usr/bin/env python3
"""Minimal phone.Ui app in Python: the protocol is JSON lines on fd 3, so any
language works without an SDK."""
import json
import os

fd = int(os.environ["PHONE_UI_FD"])
rx = os.fdopen(fd, "r", encoding="utf-8")
tx = os.fdopen(os.dup(fd), "w", encoding="utf-8")


# Where a message a language lacks comes from before English, the source
# (the system's fallback chains: zh-Hant falls back to zh-Hans).
FALLBACKS = {"zh-Hant": ["zh-Hans"]}


def catalog():
    """The translations into the device's language (PHONE_LANG, default
    zh-Hans) from locale/<lang>.json beside this file, over those of the
    languages it falls back to."""
    lang = os.environ.get("PHONE_LANG", "").strip() or "zh-Hans"
    msgs = {}
    for l in reversed([lang] + FALLBACKS.get(lang, [])):
        path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "locale", l + ".json")
        try:
            with open(path, encoding="utf-8") as f:
                msgs.update(json.load(f))
        except FileNotFoundError:
            pass
    return msgs


MESSAGES = catalog()


def T(msg, *args):
    """msg in the device's language, formatted with args (%s, %d) when there
    are any; a message without a translation shows as written."""
    msg = MESSAGES.get(msg, msg)
    return msg % args if args else msg


def send(msg):
    tx.write(json.dumps(msg, ensure_ascii=False) + "\n")
    tx.flush()


def screen(count):
    return {
        "id": "main",
        "title": T("Python sample"),
        "body": [
            {"kind": "text", "style": "big", "text": "Hello from Python"},
            {"kind": "text", "style": "dim", "text": T("The same phone.Ui protocol, no SDK needed")},
            {"kind": "text", "text": T("Clicked %d times", count)},
            {"kind": "button", "id": "inc", "text": T("Tap me"),
             "actions": [{"id": "inc", "label": T("Tap me"), "primary": True}]},
        ],
    }


count = 0
send({"t": "hello", "app": os.environ.get("PHONE_APP_ID", ""), "proto": "phone.ui/0"})
for line in rx:
    msg = json.loads(line)
    if msg["t"] == "ping":
        send({"t": "pong", "value": msg.get("value", "")})
    elif msg["t"] == "start":
        send({"t": "reset", "screen": screen(count)})
    elif msg["t"] == "action" and msg.get("action") == "inc":
        count += 1
        send({"t": "update", "screen": screen(count)})
