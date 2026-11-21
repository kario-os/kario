#!/usr/bin/env python3
"""Check what the device played during the boot selftest.

qemu-selftest.sh records the virtio sound card into a WAV file. This finds the
dominant frequency of every ~46 ms window, names the system tones phone-audiod
synthesises (audio/sources.go) and fails unless the ringtone, alarm and
ringback were heard.

    audio-check.py qemu-audio.wav [--plot spectrogram.png]
"""
import sys
import wave

import numpy as np

# name -> frequencies (Hz) whose presence as the window's peak names it.
TONES = {
    "ringtone": (659.25, 783.99, 987.77),
    "alarm": (880.0,),
    "ringback/busy": (450.0,),
    "notify": (1046.5, 1318.5),
    "call voice": (280.0, 300.0, 330.0, 560.0, 600.0, 660.0),
    # The simulated microphone (audio/ring.go); heard when a recorded voice
    # message plays. Not notes of the demo tracks, so music cannot pass for it.
    "voice message": (365.0, 475.0),
}
# Seconds a tone must be heard; a voice message is longer than a blip.
REQUIRED = {"ringtone": 0.3, "alarm": 0.3, "ringback/busy": 0.3, "voice message": 0.6}
TOLERANCE = 6.0  # Hz
WINDOW_S = 0.046


def load(path):
    with wave.open(path, "rb") as w:
        rate, ch, width, n = w.getframerate(), w.getnchannels(), w.getsampwidth(), w.getnframes()
        raw = w.readframes(n)
    if width != 2:
        sys.exit(f"{path}: {width * 8}-bit samples, expected 16-bit")
    x = np.frombuffer(raw, dtype="<i2").astype(np.float64) / 32768.0
    if ch > 1:
        x = x.reshape(-1, ch).mean(axis=1)
    return rate, x


def classify(freq):
    for name, fs in TONES.items():
        if any(abs(freq - f) <= TOLERANCE for f in fs):
            return name
    return "other"


def plot(path, times, freqs, labels):
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    colors = {"ringtone": "#e4572e", "alarm": "#f3a712", "ringback/busy": "#29335c",
              "notify": "#669bbc", "call voice": "#8a9a5b", "voice message": "#9b5de5", "other": "#bbbbbb"}
    fig, ax = plt.subplots(figsize=(10, 3.4), dpi=110)
    for name, c in colors.items():
        pts = [(t, f) for t, f, l in zip(times, freqs, labels) if l == name]
        if pts:
            ax.scatter([p[0] for p in pts], [p[1] for p in pts], s=9, color=c, label=name)
    for name, fs in TONES.items():
        for f in fs:
            ax.axhline(f, color=colors[name], lw=0.5, alpha=0.3)
    ax.set_ylim(200, 1450)
    ax.set_xlabel("time since the recording started (s)")
    ax.set_ylabel("dominant frequency (Hz)")
    ax.set_title("What the device played: virtio sound card, recorded by QEMU, one dot per 46 ms window")
    ax.legend(loc="upper left", fontsize=7, ncol=6, frameon=False)
    fig.tight_layout()
    fig.savefig(path)


def main():
    args = sys.argv[1:]
    out = None
    if "--plot" in args:
        i = args.index("--plot")
        out = args[i + 1] if i + 1 < len(args) else sys.exit(__doc__)
        del args[i : i + 2]
    if len(args) != 1:
        sys.exit(__doc__)
    rate, x = load(args[0])
    win = int(rate * WINDOW_S)
    pad = 1 << 16
    hann = np.hanning(win)
    labels, heard = [], ([], [], [])
    for start in range(0, len(x) - win, win):
        seg = x[start : start + win]
        if np.sqrt(np.mean(seg**2)) < 0.01:
            labels.append(None)
            continue
        spec = np.abs(np.fft.rfft(seg * hann, pad))
        freq = np.argmax(spec) * rate / pad
        labels.append(classify(freq))
        for lst, v in zip(heard, (start / rate, freq, labels[-1])):
            lst.append(v)
    if out:
        plot(out, *heard)

    total = {}
    for label in labels:
        if label:
            total[label] = total.get(label, 0) + 1
    print(f"audio-check: {len(x) / rate:.1f} s at {rate} Hz, {sum(total.values()) * WINDOW_S:.1f} s audible")
    for name in list(TONES) + ["other"]:
        print(f"  {name:14s} {total.get(name, 0) * WINDOW_S:6.1f} s")

    # Runs of the same tone, so the order of events is visible.
    runs, prev, begin = [], None, 0
    for i, label in enumerate(labels + [None]):
        if label != prev:
            if prev and prev != "other" and i - begin >= 3:
                runs.append(f"{begin * WINDOW_S:.1f}s {prev}")
            prev, begin = label, i
    print("  timeline: " + ", ".join(runs[:40]))

    missing = [n for n, secs in REQUIRED.items() if total.get(n, 0) * WINDOW_S < secs]
    if missing:
        print("AUDIO-CHECK: FAIL (not heard: " + ", ".join(missing) + ")")
        sys.exit(1)
    print("AUDIO-CHECK: PASS")


if __name__ == "__main__":
    main()
