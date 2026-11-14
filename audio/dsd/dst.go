package dsd

import "encoding/binary"

// DSTFile wraps DST frames as a DSDIFF file for ffmpeg's DST decoder (its
// DSDIFF reader seeks, so the frames go to it as a file, a few seconds at
// a time: audio/hifi). DST frames decode independently of each other.
func DSTFile(rate, channels int, frames [][]byte) []byte {
	be := binary.BigEndian
	chunk := func(b []byte, id string, size int) []byte {
		b = append(b, id...)
		return be.AppendUint64(b, uint64(size))
	}
	ids := []string{"SLFT", "SRGT", "C   ", "LFE ", "LS  ", "RS  "}
	switch channels {
	case 1:
		ids = []string{"C   "}
	case 5:
		ids = []string{"SLFT", "SRGT", "C   ", "LS  ", "RS  "}
	}
	dst := 12 + 6
	for _, f := range frames {
		dst += 12 + len(f) + len(f)&1
	}
	chnl := 2 + 4*channels
	const cmpr = 4 + 1 + 11
	prop := 4 + (12 + 4) + (12 + chnl) + (12 + cmpr)
	total := 4 + (12 + 4) + (12 + prop) + (12 + dst)
	b := make([]byte, 0, 12+total)
	b = chunk(b, "FRM8", total)
	b = append(b, "DSD "...)
	b = chunk(b, "FVER", 4)
	b = be.AppendUint32(b, 0x01050000)
	b = chunk(b, "PROP", prop)
	b = append(b, "SND "...)
	b = chunk(b, "FS  ", 4)
	b = be.AppendUint32(b, uint32(rate))
	b = chunk(b, "CHNL", chnl)
	b = be.AppendUint16(b, uint16(channels))
	for i := 0; i < channels; i++ {
		id := "C   "
		if i < len(ids) {
			id = ids[i]
		}
		b = append(b, id...)
	}
	b = chunk(b, "CMPR", cmpr)
	b = append(b, "DST \x0bDST Encoded"...)
	b = chunk(b, "DST ", dst)
	b = chunk(b, "FRTE", 6)
	b = be.AppendUint32(b, uint32(len(frames)))
	b = be.AppendUint16(b, FramesPerSecond)
	for _, f := range frames {
		b = chunk(b, "DSTF", len(f))
		b = append(b, f...)
		if len(f)%2 == 1 {
			b = append(b, 0)
		}
	}
	return b
}
