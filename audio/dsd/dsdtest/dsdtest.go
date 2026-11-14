// Package dsdtest makes DSD for tests: a sigma-delta modulator and
// writers for DSF, DSDIFF (plain and DST) and SACD images. The SACD
// writer follows the same public description of the disc layout as the
// reader in audio/dsd; it is not a mastering tool.
package dsdtest

import (
	"bytes"
	"encoding/binary"
	"math"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/kario-os/kario/audio/dsd"
)

// Tone is byte frames of DSD at rate: channel c a sine of freqs[c] (the
// last one repeated) at amplitude amp (0.5 is DSD's 0 dB), from a
// second-order sigma-delta modulator.
func Tone(rate, channels int, seconds float64, freqs []float64, amp float64) []byte {
	n := int(seconds*float64(rate)) / 8
	out := make([]byte, n*channels)
	for c := 0; c < channels; c++ {
		f := freqs[min(c, len(freqs)-1)]
		var i1, i2 float64
		y := -1.0
		for k := 0; k < n; k++ {
			var b byte
			for bit := 0; bit < 8; bit++ {
				x := amp * math.Sin(2*math.Pi*f*float64(8*k+bit)/float64(rate))
				i1 += x - y
				i2 += i1 - y
				if i2 >= 0 {
					y = 1
					b |= 0x80 >> bit
				} else {
					y = -1
				}
			}
			out[k*channels+c] = b
		}
	}
	return out
}

var rev [256]byte

func init() {
	for i := range rev {
		var r byte
		for b := 0; b < 8; b++ {
			if i&(1<<b) != 0 {
				r |= 0x80 >> b
			}
		}
		rev[i] = r
	}
}

// DSF is a DSF file of the byte frames (LSB first, blocks of 4096), with
// id3 as its tag.
func DSF(rate, channels int, frames []byte, id3 []byte) []byte {
	const bs = 4096
	nf := len(frames) / channels
	blocks := (nf + bs - 1) / bs
	data := blocks * bs * channels
	le := binary.LittleEndian
	h := make([]byte, 92, 92+data+len(id3))
	copy(h, "DSD ")
	le.PutUint64(h[4:], 28)
	le.PutUint64(h[12:], uint64(92+data+len(id3)))
	if len(id3) > 0 {
		le.PutUint64(h[20:], uint64(92+data))
	}
	copy(h[28:], "fmt ")
	le.PutUint64(h[32:], 52)
	le.PutUint32(h[40:], 1)
	le.PutUint32(h[48:], uint32(channels))
	le.PutUint32(h[52:], uint32(channels))
	le.PutUint32(h[56:], uint32(rate))
	le.PutUint32(h[60:], 1)
	le.PutUint64(h[64:], uint64(nf*8))
	le.PutUint32(h[72:], bs)
	copy(h[80:], "data")
	le.PutUint64(h[84:], uint64(12+data))
	block := make([]byte, bs)
	for b := 0; b < blocks; b++ {
		for c := 0; c < channels; c++ {
			clear(block)
			for i := 0; i < bs && b*bs+i < nf; i++ {
				block[i] = rev[frames[(b*bs+i)*channels+c]]
			}
			h = append(h, block...)
		}
	}
	return append(h, id3...)
}

// DFF is a DSDIFF file of the byte frames: plain, or DST with every frame
// stored uncompressed (DST's own escape for frames it does not code,
// which real decoders accept), optionally with a DSTI index.
func DFF(rate, channels int, frames []byte, dst, index bool, title, artist string) []byte {
	be := binary.BigEndian
	chunk := func(id string, body []byte) []byte {
		b := append([]byte(id), be.AppendUint64(nil, uint64(len(body)))...)
		b = append(b, body...)
		if len(body)%2 == 1 {
			b = append(b, 0)
		}
		return b
	}
	ids := []string{"SLFT", "SRGT", "C   ", "LFE ", "LS  ", "RS  "}
	chnl := be.AppendUint16(nil, uint16(channels))
	for i := 0; i < channels; i++ {
		chnl = append(chnl, ids[i]...)
	}
	cmpr := []byte("DSD \x0enot compressed\x00")
	if dst {
		cmpr = []byte("DST \x0bDST Encoded")
	}
	prop := append([]byte("SND "), chunk("FS  ", be.AppendUint32(nil, uint32(rate)))...)
	prop = append(prop, chunk("CHNL", chnl)...)
	prop = append(prop, chunk("CMPR", cmpr)...)
	body := []byte("DSD ")
	body = append(body, chunk("FVER", be.AppendUint32(nil, 0x01050000))...)
	body = append(body, chunk("PROP", prop)...)
	var diin []byte
	if title != "" {
		diin = append(diin, chunk("DITI", append(be.AppendUint32(nil, uint32(len(title))), title...))...)
	}
	if artist != "" {
		diin = append(diin, chunk("DIAR", append(be.AppendUint32(nil, uint32(len(artist))), artist...))...)
	}
	if !dst {
		body = append(body, chunk("DSD ", frames)...)
	} else {
		per := rate / 8 / dsd.FramesPerSecond * channels
		n := (len(frames) + per - 1) / per
		dstBody := chunk("FRTE", append(be.AppendUint32(nil, uint32(n)), 0, dsd.FramesPerSecond))
		var offsets []int
		for i := 0; i < n; i++ {
			f := make([]byte, 1+per)
			copy(f[1:], frames[i*per:min(len(frames), (i+1)*per)])
			for k := 1 + len(frames[i*per:min(len(frames), (i+1)*per)]); k < len(f); k++ {
				f[k] = dsd.Silence
			}
			offsets = append(offsets, len(dstBody)+12)
			dstBody = append(dstBody, chunk("DSTF", f)...)
		}
		start := 12 + len(body) + 12
		body = append(body, chunk("DST ", dstBody)...)
		if index {
			var idx []byte
			for _, o := range offsets {
				idx = be.AppendUint64(idx, uint64(start+o))
				idx = be.AppendUint32(idx, uint32(1+per))
			}
			body = append(body, chunk("DSTI", idx)...)
		}
	}
	if diin != nil {
		body = append(body, chunk("DIIN", diin)...)
	}
	return append(append([]byte("FRM8"), be.AppendUint64(nil, uint64(len(body)))...), body...)
}

// Track and Area describe an SACD image to write.
type Track struct {
	Title, Performer string
	Frames           []byte // byte frames, DSD64
}

type Area struct {
	Channels int
	Speakers int
	DST      bool
	Tracks   []Track
}

// SACD writes a disc image: Areas[0] as the stereo area (if it has two
// channels), the next as the multichannel one. Text is GB 2312.
func SACD(title, artist string, areas ...Area) []byte {
	be := binary.BigEndian
	const (
		stereoTOC = 540
		multiTOC  = 560
		audioAt   = 600
	)
	img := make([]byte, audioAt*dsd.SectorSize)
	sector := func(lsn int) []byte {
		for len(img) < (lsn+1)*dsd.SectorSize {
			img = append(img, make([]byte, dsd.SectorSize)...)
		}
		return img[lsn*dsd.SectorSize : (lsn+1)*dsd.SectorSize]
	}
	gb := func(s string) []byte {
		b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
		if err != nil {
			return []byte(s)
		}
		return b
	}
	m := sector(510)
	copy(m, "SACDMTOC")
	m[8], m[9] = 1, 20
	be.PutUint16(m[120:], 2004)
	m[138] = 5
	t := sector(511)
	copy(t, "SACDText")
	p := 64
	put := func(at int, s string) {
		be.PutUint16(t[at:], uint16(p))
		p += copy(t[p:], gb(s)) + 1
	}
	put(16, title)
	put(18, artist)
	put(32, title)
	next := audioAt
	for ai, a := range areas {
		toc := stereoTOC
		if a.Channels > 2 || ai > 0 {
			toc = multiTOC
			be.PutUint32(sector(510)[72:], uint32(toc))
			be.PutUint16(sector(510)[86:], 4)
		} else {
			be.PutUint32(sector(510)[64:], uint32(toc))
			be.PutUint16(sector(510)[84:], 4)
		}
		first := next
		lsns, sectors, starts, lengths := writeAudio(sector, &next, a)
		h := sector(toc)
		if toc == stereoTOC {
			copy(h, "TWOCHTOC")
		} else {
			copy(h, "MULCHTOC")
		}
		h[8], h[9] = 1, 20
		be.PutUint16(h[10:], 4)
		h[20] = 4
		h[21] = 2
		if a.DST {
			h[21] = 0
		}
		h[32], h[33] = byte(a.Channels), byte(a.Speakers<<3)
		total := starts[len(starts)-1] + lengths[len(lengths)-1]
		putTime(h[64:], total)
		h[68], h[69] = 0, byte(len(a.Tracks))
		be.PutUint32(h[72:], uint32(first))
		be.PutUint32(h[76:], uint32(next-1))
		h[90] = 5
		l1, l2, tx := sector(toc+1), sector(toc+2), sector(toc+3)
		copy(l1, "SACDTRL1")
		copy(l2, "SACDTRL2")
		copy(tx, "SACDTTxt")
		tp := 528
		for i, tr := range a.Tracks {
			be.PutUint32(l1[8+4*i:], uint32(lsns[i]))
			be.PutUint32(l1[8+4*255+4*i:], uint32(sectors[i]))
			putTime(l2[8+4*i:], starts[i])
			putTime(l2[8+4*255+4*i:], lengths[i])
			be.PutUint16(tx[16+2*i:], uint16(tp))
			tx[tp] = 2
			tp += 4
			for _, it := range []struct {
				typ byte
				s   string
			}{{1, tr.Title}, {2, tr.Performer}} {
				tx[tp] = it.typ
				tp += 2
				tp += copy(tx[tp:], gb(it.s)) + 1
				for tp%4 != 0 {
					tp++
				}
			}
		}
	}
	return img
}

func putTime(b []byte, frames int64) {
	b[0] = byte(frames / dsd.FramesPerSecond / 60)
	b[1] = byte(frames / dsd.FramesPerSecond % 60)
	b[2] = byte(frames % dsd.FramesPerSecond)
}

// writeAudio lays the area's frames into audio sectors from *next on and
// returns each track's first sector, sectors, start time and length.
func writeAudio(sector func(int) []byte, next *int, a Area) (lsns, sectors []int, starts, lengths []int64) {
	per := dsd.FrameBytes * a.Channels
	infoSize := 3
	if a.DST {
		infoSize = 4
	}
	type pk struct {
		start bool
		data  []byte
		time  int64
	}
	var cur []pk
	used := 1
	flush := func() {
		if len(cur) == 0 {
			return
		}
		s := sector(*next)
		*next++
		nframes := 0
		for _, p := range cur {
			if p.start {
				nframes++
			}
		}
		h := byte(nframes<<3 | len(cur))
		if a.DST {
			h |= 0x80
		}
		s[0] = h
		o := 1
		for _, p := range cur {
			v := uint16(2<<11 | len(p.data))
			if p.start {
				v |= 0x8000
			}
			binary.BigEndian.PutUint16(s[o:], v)
			o += 2
		}
		for _, p := range cur {
			if p.start {
				putTime(s[o:], p.time)
				o += infoSize
			}
		}
		for _, p := range cur {
			o += copy(s[o:], p.data)
		}
		cur, used = nil, 1
	}
	var at int64
	for _, tr := range a.Tracks {
		nf := (len(tr.Frames) + per - 1) / per
		starts = append(starts, at)
		lengths = append(lengths, int64(nf))
		trackLSN := -1
		for i := 0; i < nf; i++ {
			raw := bytes.Repeat([]byte{dsd.Silence}, per)
			copy(raw, tr.Frames[i*per:min(len(tr.Frames), (i+1)*per)])
			data := raw
			if a.DST {
				data = append([]byte{0}, raw...)
			}
			start := true
			for len(data) > 0 {
				need := 2 + 1
				if start {
					need += infoSize
				}
				nstart := 0
				for _, p := range cur {
					if p.start {
						nstart++
					}
				}
				if used+need > dsd.SectorSize || len(cur) == 7 || start && nstart == 7 {
					flush()
				}
				room := dsd.SectorSize - used - 2
				if start {
					room -= infoSize
				}
				take := min(len(data), room, 2047)
				if start && trackLSN < 0 {
					trackLSN = *next
				}
				cur = append(cur, pk{start: start, data: data[:take], time: at})
				used += 2 + take
				if start {
					used += infoSize
				}
				data = data[take:]
				start = false
			}
			at++
		}
		lsns = append(lsns, trackLSN)
	}
	flush()
	for i := range lsns {
		end := *next
		if i+1 < len(lsns) {
			end = lsns[i+1] + 1
		}
		sectors = append(sectors, end-lsns[i])
	}
	return
}
