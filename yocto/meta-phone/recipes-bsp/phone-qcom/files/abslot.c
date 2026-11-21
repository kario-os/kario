// phone-abslot: the Android A/B boot slots of a Qualcomm phone, as its
// bootloader keeps them in the GPT attributes of boot_a and boot_b.
//
//	phone-abslot [--disk FILE] [--sector-size N] status
//	phone-abslot [--disk FILE] [--sector-size N] mark-good [--dry-run]
//
// mark-good marks the slot the phone booted from as successful, so the
// bootloader stops counting its tries down and never falls back to the
// other slot. It writes nothing when the slot is marked already; otherwise
// the primary GPT and then the backup, each its entries before its header,
// so that one of the two is whole at any moment. --disk names a disk image
// instead of the disk holding boot_a (for tests).
#define _GNU_SOURCE
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <libgen.h>
#include <limits.h>
#include <linux/fs.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

// Byte 6 of an entry's attributes (bits 48-55), as ABL and Qualcomm's boot
// control HAL define it.
#define ATTR_OFFSET (48 + 6)
#define PRIORITY 0x03
#define ACTIVE 0x04
#define RETRIES 0x38
#define SUCCESSFUL 0x40
#define UNBOOTABLE 0x80

struct gpt {
	uint64_t lba, alt_lba, entries_lba;
	uint32_t hdr_size, n_entries, entry_size;
	uint8_t *hdr;
	uint8_t *entries;
	size_t entries_len;
};

static uint32_t crc32(const uint8_t *p, size_t n)
{
	uint32_t c = 0xffffffff;
	while (n--) {
		c ^= *p++;
		for (int k = 0; k < 8; k++)
			c = (c >> 1) ^ (0xedb88320 & -(c & 1));
	}
	return ~c;
}

static uint32_t le32(const uint8_t *p) { return p[0] | p[1] << 8 | p[2] << 16 | (uint32_t)p[3] << 24; }
static uint64_t le64(const uint8_t *p) { return le32(p) | (uint64_t)le32(p + 4) << 32; }
static void put32(uint8_t *p, uint32_t v) { p[0] = v; p[1] = v >> 8; p[2] = v >> 16; p[3] = v >> 24; }

static int fail(const char *what)
{
	fprintf(stderr, "phone-abslot: %s\n", what);
	return -1;
}

static int read_at(int fd, void *buf, size_t n, uint64_t off)
{
	ssize_t r = pread(fd, buf, n, off);
	if (r != (ssize_t)n)
		return fail(r < 0 ? strerror(errno) : "short read");
	return 0;
}

static int write_at(int fd, const void *buf, size_t n, uint64_t off)
{
	ssize_t r = pwrite(fd, buf, n, off);
	if (r != (ssize_t)n)
		return fail(r < 0 ? strerror(errno) : "short write");
	if (fsync(fd) < 0)
		return fail(strerror(errno));
	return 0;
}

// A GPT header at lba with its entries, both checked against their CRCs.
static int load(int fd, unsigned ss, uint64_t lba, struct gpt *g)
{
	memset(g, 0, sizeof(*g));
	g->hdr = malloc(ss);
	if (!g->hdr || read_at(fd, g->hdr, ss, lba * ss) < 0)
		return -1;
	if (memcmp(g->hdr, "EFI PART", 8) != 0)
		return fail("no GPT header there");
	g->hdr_size = le32(g->hdr + 12);
	if (g->hdr_size < 92 || g->hdr_size > ss)
		return fail("bad GPT header size");
	uint32_t want = le32(g->hdr + 16);
	put32(g->hdr + 16, 0);
	uint32_t got = crc32(g->hdr, g->hdr_size);
	put32(g->hdr + 16, want);
	if (got != want)
		return fail("GPT header CRC mismatch");
	g->lba = le64(g->hdr + 24);
	g->alt_lba = le64(g->hdr + 32);
	g->entries_lba = le64(g->hdr + 72);
	g->n_entries = le32(g->hdr + 80);
	g->entry_size = le32(g->hdr + 84);
	if (g->lba != lba || g->entry_size < 128 || g->n_entries == 0 || g->n_entries > 4096)
		return fail("bad GPT header");
	size_t n = (size_t)g->n_entries * g->entry_size;
	g->entries_len = (n + ss - 1) / ss * ss;
	g->entries = calloc(1, g->entries_len);
	if (!g->entries || read_at(fd, g->entries, g->entries_len, g->entries_lba * ss) < 0)
		return -1;
	if (crc32(g->entries, n) != le32(g->hdr + 88))
		return fail("GPT entries CRC mismatch");
	return 0;
}

// The entry named name (ASCII, stored as UTF-16LE), or NULL.
static uint8_t *entry(struct gpt *g, const char *name)
{
	for (uint32_t i = 0; i < g->n_entries; i++) {
		uint8_t *e = g->entries + (size_t)i * g->entry_size;
		size_t k = 0;
		for (; k < 36 && name[k]; k++)
			if (e[56 + 2 * k] != (uint8_t)name[k] || e[57 + 2 * k] != 0)
				break;
		if (!name[k] && k < 36 && e[56 + 2 * k] == 0 && e[57 + 2 * k] == 0)
			return e;
	}
	return NULL;
}

static int store(int fd, unsigned ss, struct gpt *g)
{
	put32(g->hdr + 88, crc32(g->entries, (size_t)g->n_entries * g->entry_size));
	put32(g->hdr + 16, 0);
	put32(g->hdr + 16, crc32(g->hdr, g->hdr_size));
	if (write_at(fd, g->entries, g->entries_len, g->entries_lba * ss) < 0)
		return -1;
	return write_at(fd, g->hdr, ss, g->lba * ss);
}

// The disk (/dev/sdX) holding the partition labelled boot_a.
static int find_disk(char *out, size_t n)
{
	DIR *d = opendir("/sys/class/block");
	if (!d)
		return fail("no /sys/class/block");
	struct dirent *de;
	int found = -1;
	while (found < 0 && (de = readdir(d))) {
		char path[PATH_MAX], line[256];
		snprintf(path, sizeof(path), "/sys/class/block/%s/uevent", de->d_name);
		FILE *f = fopen(path, "r");
		if (!f)
			continue;
		while (fgets(line, sizeof(line), f))
			if (strcmp(line, "PARTNAME=boot_a\n") == 0) {
				char link[PATH_MAX], real[PATH_MAX];
				snprintf(link, sizeof(link), "/sys/class/block/%s", de->d_name);
				if (realpath(link, real)) {
					snprintf(out, n, "/dev/%s", basename(dirname(real)));
					found = 0;
				}
				break;
			}
		fclose(f);
	}
	closedir(d);
	return found < 0 ? fail("no partition labelled boot_a") : 0;
}

// The slot the bootloader says it booted, else the one marked active.
static char booted(uint8_t a, uint8_t b)
{
	char line[4096] = "";
	FILE *f = fopen("/proc/cmdline", "r");
	if (f) {
		if (!fgets(line, sizeof(line), f))
			line[0] = 0;
		fclose(f);
	}
	char *s = strstr(line, "androidboot.slot_suffix=_");
	if (s && (s[25] == 'a' || s[25] == 'b'))
		return s[25];
	if ((a & ACTIVE) && !(b & ACTIVE))
		return 'a';
	if ((b & ACTIVE) && !(a & ACTIVE))
		return 'b';
	return 0;
}

static void show(char slot, uint8_t v)
{
	printf("boot_%c: %spriority %d, tries left %d%s%s\n", slot, v & ACTIVE ? "active, " : "", v & PRIORITY,
	       (v & RETRIES) >> 3, v & SUCCESSFUL ? ", successful" : "", v & UNBOOTABLE ? ", unbootable" : "");
}

int main(int argc, char **argv)
{
	const char *disk = NULL, *cmd = NULL;
	unsigned ss = 0;
	int dry = 0;
	for (int i = 1; i < argc; i++) {
		if (strcmp(argv[i], "--disk") == 0 && i + 1 < argc)
			disk = argv[++i];
		else if (strcmp(argv[i], "--sector-size") == 0 && i + 1 < argc)
			ss = strtoul(argv[++i], NULL, 10);
		else if (strcmp(argv[i], "--dry-run") == 0)
			dry = 1;
		else if (!cmd && (strcmp(argv[i], "status") == 0 || strcmp(argv[i], "mark-good") == 0))
			cmd = argv[i];
		else
			cmd = NULL, i = argc;
	}
	if (!cmd) {
		fprintf(stderr, "usage: phone-abslot [--disk FILE] [--sector-size N] status | mark-good [--dry-run]\n");
		return 2;
	}
	int rw = strcmp(cmd, "mark-good") == 0 && !dry;
	char dev[PATH_MAX];
	if (!disk) {
		if (find_disk(dev, sizeof(dev)) < 0)
			return 1;
		disk = dev;
	}
	int fd = open(disk, rw ? O_RDWR : O_RDONLY);
	if (fd < 0) {
		fprintf(stderr, "phone-abslot: %s: %s\n", disk, strerror(errno));
		return 1;
	}
	if (!ss) {
		int bs;
		ss = ioctl(fd, BLKSSZGET, &bs) == 0 ? (unsigned)bs : 512;
	}
	struct gpt p, b;
	if (load(fd, ss, 1, &p) < 0)
		return 1;
	uint8_t *pa = entry(&p, "boot_a"), *pb = entry(&p, "boot_b");
	if (!pa || !pb) {
		fail("no boot_a and boot_b in the GPT");
		return 1;
	}
	show('a', pa[ATTR_OFFSET]);
	show('b', pb[ATTR_OFFSET]);
	char slot = booted(pa[ATTR_OFFSET], pb[ATTR_OFFSET]);
	if (!slot) {
		fail("cannot tell which slot booted");
		return 1;
	}
	printf("booted: %c\n", slot);
	if (strcmp(cmd, "status") == 0)
		return 0;
	uint8_t *e = slot == 'a' ? pa : pb;
	if (e[ATTR_OFFSET] & SUCCESSFUL) {
		printf("boot_%c is marked successful already\n", slot);
		return 0;
	}
	int have_backup = load(fd, ss, p.alt_lba, &b) == 0;
	uint8_t *be = have_backup ? entry(&b, slot == 'a' ? "boot_a" : "boot_b") : NULL;
	if (!be)
		fprintf(stderr, "phone-abslot: the backup GPT is unusable; only the primary is marked\n");
	if (dry) {
		printf("would mark boot_%c successful (dry run)\n", slot);
		return 0;
	}
	e[ATTR_OFFSET] |= SUCCESSFUL;
	if (store(fd, ss, &p) < 0)
		return 1;
	if (be) {
		be[ATTR_OFFSET] |= SUCCESSFUL;
		if (store(fd, ss, &b) < 0)
			return 1;
	}
	printf("marked boot_%c successful\n", slot);
	return 0;
}
