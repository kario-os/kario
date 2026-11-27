/*
 * native:imgproc, the sample app's native module (22-developer-api.md §9.3):
 * a box blur of RGBA pixels, what QuickJS does slowly, and the same
 * checksum as src/blur.ts, so the two can be compared. The TypeScript
 * fallback (src/blur.ts) computes exactly the same pixels.
 *
 * Built by the developer, not by kario build, once per architecture:
 *
 *	cc -shared -fPIC -O2 -I<kario>/sdk/c/include -o native/x86_64/imgproc.so native/imgproc.c
 *	aarch64-linux-gnu-gcc -shared -fPIC -O2 -I<kario>/sdk/c/include -o native/arm64/imgproc.so native/imgproc.c
 *
 * (make native-example in kario builds this machine's into build/native.)
 */
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "kario_native.h"

#define MAX_RADIUS 64

static int clamp(int v, int lo, int hi) { return v < lo ? lo : v > hi ? hi : v; }

/* blur_pass blurs one direction: count lines of len pixels, step bytes
 * between pixels of a line and stride between lines. */
static void blur_pass(const uint8_t *src, uint8_t *dst, int count, int len, size_t step, size_t stride, int r)
{
	unsigned n = 2u * (unsigned)r + 1;
	for (int l = 0; l < count; l++) {
		const uint8_t *s = src + (size_t)l * stride;
		uint8_t *d = dst + (size_t)l * stride;
		for (int c = 0; c < 4; c++) {
			unsigned sum = 0;
			for (int k = -r; k <= r; k++)
				sum += s[(size_t)clamp(k, 0, len - 1) * step + c];
			for (int i = 0; i < len; i++) {
				d[(size_t)i * step + c] = (uint8_t)((sum * 2 + n) / (2 * n));
				sum += s[(size_t)clamp(i + r + 1, 0, len - 1) * step + c];
				sum -= s[(size_t)clamp(i - r, 0, len - 1) * step + c];
			}
		}
	}
}

/* blur_rgba blurs w×h RGBA pixels in place: rows, then columns. */
static int blur_rgba(uint8_t *px, int w, int h, int r)
{
	uint8_t *tmp = malloc((size_t)w * h * 4);
	if (!tmp)
		return -1;
	blur_pass(px, tmp, h, w, 4, (size_t)w * 4, r);
	blur_pass(tmp, px, w, h, (size_t)w * 4, 4, r);
	free(tmp);
	return 0;
}

/* args reads (pixels, width, height, radius), or says what is wrong. */
static uint8_t *args(const kario_api *api, kario_call *c, size_t *len, int *w, int *h, int *r)
{
	uint8_t *px = api->bytes(c, 0, len);
	double dw = api->number(c, 1), dh = api->number(c, 2), dr = api->number(c, 3);
	if (!px || !(dw >= 1 && dh >= 1 && dw <= 8192 && dh <= 8192) || !(dr >= 0 && dr <= MAX_RADIUS)) {
		api->fail(c, "blur: (pixels, width, height, radius): RGBA bytes, a size up to 8192, a radius up to 64");
		return NULL;
	}
	*w = (int)dw, *h = (int)dh, *r = (int)dr;
	if (*len < (size_t)*w * *h * 4) {
		api->fail(c, "blur: fewer bytes than width × height × 4");
		return NULL;
	}
	return px;
}

static void blur(const kario_api *api, kario_call *c, void *data)
{
	size_t len;
	int w, h, r;
	uint8_t *px = args(api, c, &len, &w, &h, &r);
	if (px && blur_rgba(px, w, h, r) < 0)
		api->fail(c, "blur: out of memory");
}

/* blur_async runs on a worker: its argument is a copy, the result a new
 * buffer. */
static void blur_async(const kario_api *api, kario_call *c, void *data)
{
	size_t len;
	int w, h, r;
	uint8_t *px = args(api, c, &len, &w, &h, &r);
	if (!px)
		return;
	uint8_t *out = api->ret_bytes(c, (size_t)w * h * 4);
	if (!out) {
		api->fail(c, "blur: out of memory");
		return;
	}
	memcpy(out, px, (size_t)w * h * 4);
	if (blur_rgba(out, w, h, r) < 0)
		api->fail(c, "blur: out of memory");
}

/* checksum is FNV-1a over the bytes, as 8 hex digits. */
static void checksum(const kario_api *api, kario_call *c, void *data)
{
	size_t len;
	const uint8_t *p = api->bytes(c, 0, &len);
	if (!p) {
		api->fail(c, "checksum: the argument is bytes");
		return;
	}
	uint32_t x = 2166136261u;
	for (size_t i = 0; i < len; i++)
		x = (x ^ p[i]) * 16777619u;
	static const char hex[] = "0123456789abcdef";
	char out[8];
	for (int i = 0; i < 8; i++)
		out[i] = hex[x >> (28 - 4 * i) & 15];
	api->ret_string(c, out, 8);
}

static int init(const kario_api *api, kario_module *m)
{
	return api->export_fn(m, "blur", blur, NULL, 0) || api->export_fn(m, "blurAsync", blur_async, NULL, KARIO_ASYNC) ||
	       api->export_fn(m, "checksum", checksum, NULL, 0) || api->export_string(m, "version", "imgproc 1.0");
}

KARIO_NATIVE_MODULE(init)
