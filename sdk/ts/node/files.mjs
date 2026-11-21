// Files that come with a capability's reply under Node.js (kario run, kario
// test), as kario-jsrt hands them to JavaScript (jsrt/src/files.c), with
// its limits and messages. A file handle (io's) is { stat() -> {size,
// regular}, read(buf, position | null) -> count, write(buf, position) ->
// count, close() }: an fd under kario run, bytes under kario test.
const FILE_DEFAULT_READ = 64 << 10;
const FILE_MAX_READ = 1 << 20;
const FILE_MAX_ALL = 16 << 20;
const FILES_MAX_OPEN = 64;
const RING_HEADER = 64;
const RING_MAGIC = 0x474e5250;
const RING_RATE = 48000;
const RING_MAX_SECS = 10;
const AUDIO_CAPS = new Set(["audio.stream", "audio.record"]);

class KarioFile {
  #h;
  #files;
  constructor(h, files) {
    this.#h = h;
    this.#files = files;
  }
  #open(what) {
    if (!this.#h) throw new TypeError(`KarioFile.${what}: the file is closed`);
    return this.#h;
  }
  read(max) {
    const h = this.#open("read");
    let n = FILE_DEFAULT_READ;
    if (max !== undefined) {
      n = Number(max);
      if (!(n >= 1 && n <= FILE_MAX_READ)) throw new RangeError(`KarioFile.read: from 1 to ${FILE_MAX_READ} bytes at a time`);
      n = Math.trunc(n);
    }
    const buf = new Uint8Array(n);
    return buf.slice(0, h.read(buf, null));
  }
  readAll() {
    const h = this.#open("readAll");
    const over = () => new RangeError(`KarioFile.readAll: the file is over ${FILE_MAX_ALL >> 20} MB; read it in parts (read())`);
    if (h.stat().size > FILE_MAX_ALL) throw over();
    const chunks = [];
    let total = 0;
    for (;;) {
      const buf = new Uint8Array(Math.min(FILE_MAX_ALL + 1 - total, 1 << 20) || 1);
      const got = h.read(buf, total);
      if (got === 0) break;
      total += got;
      if (total > FILE_MAX_ALL) throw over();
      chunks.push(buf.subarray(0, got));
    }
    const all = new Uint8Array(total);
    let at = 0;
    for (const c of chunks) {
      all.set(c, at);
      at += c.byteLength;
    }
    return all;
  }
  get size() {
    return this.#open("size").stat().size;
  }
  get closed() {
    return !this.#h;
  }
  close() {
    if (!this.#h) return;
    const h = this.#h;
    this.#h = undefined;
    this.#files.closed(this, h);
  }
  get [Symbol.toStringTag]() {
    return "KarioFile";
  }
}

// AudioRing over the ring's file: the positions and samples are read and
// written through the file (pread, pwrite) rather than a mapping, which
// Node.js has none of; kario-jsrt maps it as the Go SDK does.
class AudioRing {
  #h;
  #cap;
  constructor(h, cap) {
    this.#h = h;
    this.#cap = cap;
  }
  #open(what) {
    if (!this.#h) throw new TypeError(`AudioRing.${what}: the ring is closed`);
    return this.#h;
  }
  #u64(h, at) {
    const b = new Uint8Array(8);
    h.read(b, at);
    return new DataView(b.buffer).getBigUint64(0, true);
  }
  #put64(h, at, v) {
    const b = new Uint8Array(8);
    new DataView(b.buffer).setBigUint64(0, v, true);
    h.write(b, at);
  }
  #samples(v, what) {
    if (!(v instanceof Int16Array)) throw new TypeError(`AudioRing.${what}: the samples are an Int16Array`);
    return v;
  }
  #positions(h) {
    return [this.#u64(h, 16), this.#u64(h, 24)];
  }
  // copy moves n samples between p and the ring from position pos
  // (modulo the capacity, in at most two runs).
  #copy(h, p, pos, n, write) {
    const cap = BigInt(this.#cap);
    let done = 0;
    while (done < n) {
      const at = Number((pos + BigInt(done)) % cap);
      const run = Math.min(n - done, this.#cap - at);
      const bytes = new Uint8Array(p.buffer, p.byteOffset + done * 2, run * 2);
      if (write) h.write(bytes, RING_HEADER + at * 2);
      else h.read(bytes, RING_HEADER + at * 2);
      done += run;
    }
  }
  write(samples) {
    const h = this.#open("write");
    const p = this.#samples(samples, "write");
    const [w, rd] = this.#positions(h);
    const cap = BigInt(this.#cap);
    const used = w - rd < cap ? w - rd : cap;
    const n = Math.min(p.length, Number(cap - used));
    this.#copy(h, p, w, n, true);
    this.#put64(h, 16, w + BigInt(n));
    return n;
  }
  read(samples) {
    const h = this.#open("read");
    const p = this.#samples(samples, "read");
    let [w, rd] = this.#positions(h);
    const cap = BigInt(this.#cap);
    if (w - rd > cap) rd = w - cap;
    const n = Math.min(p.length, Number(w - rd));
    this.#copy(h, p, rd, n, false);
    this.#put64(h, 24, rd + BigInt(n));
    return n;
  }
  finish() {
    const h = this.#open("finish");
    const b = new Uint8Array(4);
    new DataView(b.buffer).setUint32(0, 1, true);
    h.write(b, 32);
  }
  close() {
    if (!this.#h) return;
    const h = this.#h;
    this.#h = undefined;
    h.close();
  }
  get capacity() {
    this.#open("capacity");
    return this.#cap;
  }
  get buffered() {
    const [w, rd] = this.#positions(this.#open("buffered"));
    const cap = BigInt(this.#cap);
    return Number(w - rd < cap ? w - rd : cap);
  }
  get finished() {
    const b = new Uint8Array(4);
    this.#open("finished").read(b, 32);
    return (new DataView(b.buffer).getUint32(0, true) & 1) !== 0;
  }
  get closed() {
    return !this.#h;
  }
  get rate() {
    return RING_RATE;
  }
  get [Symbol.toStringTag]() {
    return "AudioRing";
  }
}

// ringOf checks a ring's layout as kario-jsrt does; of its seals Node.js
// sees only whether it can have any.
function ringOf(h) {
  if (h.sealable && !h.sealable()) return "the audio ring is not sealed against shrinking";
  const { size } = h.stat();
  if (size < RING_HEADER + 2 || size > RING_HEADER + 2 * RING_RATE * RING_MAX_SECS) return "the audio ring has a size no ring has";
  const head = new Uint8Array(16);
  h.read(head, 0);
  const v = new DataView(head.buffer);
  const frames = v.getUint32(12, true);
  if (v.getUint32(0, true) !== RING_MAGIC || v.getUint32(4, true) !== 1 || RING_HEADER + 2 * frames !== size || frames === 0) return "not an audio ring";
  return new AudioRing(h, frames);
}

// replyFiles is the runtime's side of the files: how many are open, and
// what a reply's become (jsrt_reply_files).
export function replyFiles(log) {
  let open = 0;
  const close = (h) => {
    try {
      h.close();
    } catch {
      // Already gone: nothing to give back.
    }
  };
  const files = {
    closed(_f, h) {
      open--;
      registry.unregister(_f);
      close(h);
    },
  };
  const registry = new FinalizationRegistry((h) => {
    open--;
    close(h);
  });
  return {
    closeAll(hs) {
      for (const h of hs) close(h);
    },
    // attach returns the reason the call fails instead, or undefined.
    attach(cap, data, hs) {
      if (data === null || typeof data !== "object") {
        log(`kario-node: ${cap}: ${hs.length} files with a result that is not an object, closed`);
        this.closeAll(hs);
        return undefined;
      }
      if (AUDIO_CAPS.has(cap)) {
        let ring = hs.length === 1 ? undefined : "the audio stream came without its ring";
        if (!ring) {
          try {
            ring = ringOf(hs[0]);
          } catch {
            ring = "the audio ring could not be mapped";
          }
        }
        if (typeof ring === "string") {
          this.closeAll(hs);
          return ring;
        }
        Object.defineProperty(data, "ring", { value: ring, writable: true, configurable: true });
        return undefined;
      }
      if (open + hs.length > FILES_MAX_OPEN) {
        this.closeAll(hs);
        return "too many files open from replies (at most 64): close() the ones done with";
      }
      const out = [];
      for (const h of hs) {
        let regular = false;
        try {
          regular = h.stat().regular;
        } catch {}
        if (!regular) {
          log(`kario-node: ${cap}: a file that is not a regular file, closed`);
          close(h);
          continue;
        }
        const f = new KarioFile(h, files);
        open++;
        registry.register(f, h, f);
        out.push(f);
      }
      Object.defineProperty(data, "files", { value: out, writable: true, configurable: true });
      return undefined;
    },
  };
}

