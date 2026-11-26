package hifi

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/musicfile"
)

// The codecs that are hard to get right (ALAC, Monkey's Audio, WavPack,
// DST) are ffmpeg's, run as a child process like mediad's video decoder:
// ffmpeg is LGPL and stays a separate program. It decodes to 32-bit PCM
// at the file's own rate and channels; a 16- or 24-bit source comes out
// shifted left, so the samples are the file's exactly (bit-perfect
// output stays possible). A goroutine keeps a few seconds decoded ahead,
// which also starts the next track's decoder before the current one ends.

// ErrNoFFmpeg: the device has no ffmpeg for this codec.
var ErrNoFFmpeg = errors.New("no ffmpeg on this device for this format")

// FFmpeg is the program run ("ffmpeg" on PATH; tests point it elsewhere).
var FFmpeg = "ffmpeg"

const (
	chunkFrames = 4096
	aheadChunks = 48 // about 4 s at 44.1 kHz, 1 s at 192 kHz
)

// producer decodes from a position into chunks until stop closes.
type producer struct {
	chunks chan []int32
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

func (p *producer) fail(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.mu.Unlock()
}

func (p *producer) close() {
	close(p.stop)
	for range p.chunks {
	}
	<-p.done
}

// ffPCM is a PCM decoder fed by a producer.
type ffPCM struct {
	format Format
	frames int64
	start  func(at int64, p *producer)
	p      *producer
	cur    []int32
	pos    int64
}

func newFF(format Format, frames int64, start func(at int64, p *producer)) *ffPCM {
	f := &ffPCM{format: format, frames: frames, start: start}
	f.run(0)
	return f
}

func (f *ffPCM) run(at int64) {
	if f.p != nil {
		f.p.close()
	}
	p := &producer{chunks: make(chan []int32, aheadChunks), stop: make(chan struct{}), done: make(chan struct{})}
	f.p, f.cur, f.pos = p, nil, at
	go func() {
		defer close(p.done)
		defer close(p.chunks)
		f.start(at, p)
	}()
}

func (f *ffPCM) Format() Format { return f.format }
func (f *ffPCM) Frames() int64  { return f.frames }

func (f *ffPCM) SeekFrame(n int64) error {
	if n < 0 || f.frames > 0 && n > f.frames {
		return fmt.Errorf("seek to %d of %d frames", n, f.frames)
	}
	f.run(n)
	return nil
}

func (f *ffPCM) Read(dst []int32) (int, error) {
	ch := f.format.Channels
	n := 0
	for n+ch <= len(dst) {
		if f.frames > 0 && f.pos >= f.frames {
			break
		}
		if len(f.cur) == 0 {
			c, ok := <-f.p.chunks
			if !ok {
				f.p.mu.Lock()
				err := f.p.err
				f.p.mu.Unlock()
				if n == 0 {
					if err == nil {
						err = io.EOF
					}
					return 0, err
				}
				return n, nil
			}
			f.cur = c
		}
		k := min(len(f.cur), (len(dst)-n)/ch*ch)
		if f.frames > 0 {
			k = min(k, int(f.frames-f.pos)*ch)
		}
		copy(dst[n:], f.cur[:k])
		f.cur = f.cur[k:]
		n += k
		f.pos += int64(k / ch)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (f *ffPCM) Close() error {
	if f.p != nil {
		f.p.close()
		f.p = nil
	}
	return nil
}

// runFFmpeg decodes input (a file given to ffmpeg as /dev/fd/3) from
// second at to s32le, sending chunks; it drops the first drop frames and
// stops after limit frames (0: no limit). It returns the frames sent.
func runFFmpeg(p *producer, input *os.File, at float64, ch int, extra []string, drop, limit int64) (int64, error) {
	args := []string{"-nostdin", "-v", "error"}
	if at > 0 {
		args = append(args, "-ss", strconv.FormatFloat(at, 'f', 6, 64))
	}
	args = append(args, "-i", "/dev/fd/3", "-map", "0:a:0")
	args = append(args, extra...)
	args = append(args, "-f", "s32le", "-c:a", "pcm_s32le", "pipe:1")
	cmd := exec.Command(FFmpeg, args...)
	cmd.ExtraFiles = []*os.File{input}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	var errs strings.Builder
	cmd.Stderr = &limited{w: &errs, n: 2048}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return 0, ErrNoFFmpeg
		}
		return 0, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	r := bufio.NewReaderSize(out, 64<<10)
	raw := make([]byte, chunkFrames*ch*4)
	var sent int64
	for {
		k, rerr := io.ReadFull(r, raw)
		k -= k % (4 * ch)
		frames := int64(k / (4 * ch))
		b := raw[:k]
		if drop > 0 {
			d := min(drop, frames)
			b, frames, drop = b[d*int64(4*ch):], frames-d, drop-d
		}
		if limit > 0 {
			frames = min(frames, limit-sent)
			b = b[:frames*int64(4*ch)]
		}
		if frames > 0 {
			c := make([]int32, len(b)/4)
			for i := range c {
				c[i] = int32(binary.LittleEndian.Uint32(b[4*i:]))
			}
			select {
			case p.chunks <- c:
			case <-p.stop:
				return sent, nil
			}
			sent += frames
		}
		if limit > 0 && sent >= limit {
			return sent, nil
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil && sent == 0 {
		msg := strings.TrimSpace(errs.String())
		if msg == "" {
			msg = err.Error()
		}
		return sent, fmt.Errorf("ffmpeg: %s", msg)
	}
	return sent, nil
}

type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(b []byte) (int, error) {
	if l.n > 0 {
		k := min(len(b), l.n)
		l.w.Write(b[:k])
		l.n -= k
	}
	return len(b), nil
}

// openFFmpeg decodes ALAC, APE and WavPack files.
func openFFmpeg(path string, i musicfile.Info) (PCM, error) {
	if _, err := exec.LookPath(FFmpeg); err != nil {
		return nil, fmt.Errorf("%s: %w", i.Codec, ErrNoFFmpeg)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	bits := i.Bits
	if i.Float {
		bits = 24
	}
	format := Format{Rate: i.Rate, Channels: i.Channels, Bits: bits, Float: i.Float}
	d := newFF(format, i.Frames, func(at int64, p *producer) {
		if _, err := runFFmpeg(p, f, float64(at)/float64(i.Rate), i.Channels, nil, 0, 0); err != nil {
			p.fail(err)
		}
	})
	return &fileFF{ffPCM: d, f: f}, nil
}

type fileFF struct {
	*ffPCM
	f *os.File
}

func (d *fileFF) Close() error {
	d.ffPCM.Close()
	return d.f.Close()
}

// DST: ffmpeg's DSDIFF reader needs a file it can seek in, so the frames
// go to it ten seconds at a time, each batch as a DSDIFF file in memory
// (a memfd) that starts one frame early: the decoder's DSD-to-PCM filter
// and the resampler then join the batches without a seam, and the early
// frame's output is dropped. Its output: PCM at 176.4 kHz.
const (
	dstBatch   = 10 * dsd.FramesPerSecond
	dstPCMRate = 176400
)

type dstPCM struct {
	*ffPCM
	src dsd.Frames
}

func openDST(src dsd.Frames) (PCM, error) {
	if _, err := exec.LookPath(FFmpeg); err != nil {
		return nil, fmt.Errorf("DST: %w", ErrNoFFmpeg)
	}
	ch := src.Channels()
	per := int64(dstPCMRate / dsd.FramesPerSecond)
	total := src.Count() * per
	var mu sync.Mutex
	d := &dstPCM{src: src}
	// Float: the samples are ffmpeg's conversion of DSD, not a file's own.
	d.ffPCM = newFF(Format{Rate: dstPCMRate, Channels: ch, Bits: 24, Float: true}, total, func(at int64, p *producer) {
		mu.Lock()
		defer mu.Unlock()
		frame := at / per
		skip := at - frame*per
		for frame < src.Count() {
			select {
			case <-p.stop:
				return
			default:
			}
			lead := int64(0)
			if frame > 0 {
				lead = 1
			}
			if err := src.SeekFrame(frame - lead); err != nil {
				p.fail(err)
				return
			}
			var batch [][]byte
			for len(batch) < int(dstBatch+lead) {
				f, err := src.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					p.fail(err)
					return
				}
				batch = append(batch, f)
			}
			if len(batch) <= int(lead) {
				return
			}
			n := int64(len(batch)) - lead
			mem, err := memFile(dsd.DSTFile(src.Rate(), ch, batch))
			if err != nil {
				p.fail(err)
				return
			}
			_, err = runFFmpeg(p, mem, 0, ch, []string{"-ar", strconv.Itoa(dstPCMRate)}, lead*per+skip, n*per-skip)
			mem.Close()
			if err != nil {
				p.fail(err)
				return
			}
			frame += n
			skip = 0
		}
	})
	return d, nil
}

func (d *dstPCM) Close() error {
	d.ffPCM.Close()
	return d.src.Close()
}

// memFile is a sealed in-memory file holding b.
func memFile(b []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("phone-dst", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "phone-dst")
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
