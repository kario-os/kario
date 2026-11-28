# Monkey's Audio samples

`tone16.ape` and `tone24.ape` are decoded by the APE tests of `audio/hifi` (`ape_test.go`) through ffmpeg, as the player does, and compared sample for sample with the signal they were made from; `audio/musicfile` (`TestAPEFiles`) reads their headers and tags. Nothing here is a recording: the signal is `apeSample.source` in `ape_test.go`.

| File | Format | Length | APE frames | Level | Size |
|---|---|---|---|---|---|
| tone16.ape | 16-bit, 44.1 kHz, stereo | 13230 frames (0.3 s) | 1 | `-c4000` (extra high) | 16749 bytes |
| tone24.ape | 24-bit, 192 kHz, stereo | 76800 frames (0.4 s) | 2 (73728 + 3072) | `-c2000` (normal) | 56275 bytes |

Both are file version 3.99 (3990), the only layout the encoder writes. The layout before 3.98 has no real file here, only hand-written headers (`TestAPEHeader` in `audio/musicfile`).

The signal, in integers: on the left a 1 kHz parabola wave at -12 dBFS with a full-scale click at frames 100 and 101 (the largest and the smallest value), on the right a sawtooth at -12 dBFS rising by 7 a frame. `tone24.ape` is silent from frame 4800 to 71040: that stretch only carries it into its second APE frame, where the tests seek (APE spends about 1.8 bits a sample even on 24-bit silence, so sound there would make the file three times larger).

## Encoder

Monkey's Audio 13.50, the SDK from <https://monkeysaudio.com/developers.html> (`MAC_1350_SDK.zip`, SHA-256 `64a347668211efbc564a8543b28be612895caddf0a5f317b9404255fcffc0be1`), under the 3-clause BSD licence (`License.txt` in the SDK, <https://monkeysaudio.com/license.html>). It is built from source outside the tree; neither the SDK nor the program is in git or in an image:

```sh
cd /tmp && curl -LO https://monkeysaudio.com/files/MAC_1350_SDK.zip
unzip -q MAC_1350_SDK.zip -d mac-sdk
cmake -S mac-sdk -B mac-build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED=OFF -DCMAKE_EXE_LINKER_FLAGS=-pthread
cmake --build mac-build
```

`-pthread`: glibc before 2.34 keeps `pthread_create` out of libc, and the SDK's CMakeLists.txt does not link it.

## Making them again

```sh
KARIO_UPDATE_TESTDATA=1 MAC=/tmp/mac-build/mac go test -run TestUpdateAPESamples ./audio/hifi
```

writes each signal as a WAV file and runs:

```sh
mac src.wav tone16.ape -c4000 -t "Title=空山新雨|Artist=Kario|Album=APE samples|Track=2/9|Year=2026"
mac src.wav tone24.ape -c2000 -t "Title=APE 24/192|Artist=Kario|Album=APE samples|Track=3/9|Year=2026"
```

Monkey's Audio 13.50 makes the same bytes every time:

```
475acfda76c60d23f73425eb73ab342a1d3c6196545e68b7acb21e6fa0e33c5b  tone16.ape
2e2848a8f3005dfeca5c19fd34e4740f6952ef75ba38176d00270b8c49da5f49  tone24.ape
```
