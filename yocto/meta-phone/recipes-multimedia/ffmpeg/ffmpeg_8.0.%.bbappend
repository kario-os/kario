# ffmpeg 8.0's Opus parser rejects the empty flush at the end of every
# Opus-in-Matroska/WebM file and logs "Error parsing Opus packet header"
# after a good decode, which mediad then writes to its log and failure
# dumps. FFmpeg 8.1 has the fix: this append goes with OE-core's move to it.
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"
SRC_URI:append = " file://0001-libavcodec-opus-parser-Fix-spurious-Error-parsing-Op.patch"
