// phone-abslot: the Android A/B boot slots of a Qualcomm phone, as its
// bootloader keeps them in the GPT attributes of boot_a and boot_b.
//
//	phone-abslot [--disk FILE] [--sector-size N] [--cmdline FILE] status
//	phone-abslot [...] booted                  the slot this system started from
//	phone-abslot [...] slots [--booted a|b]    the slots as phone-ota's boot state
//	phone-abslot [...] get-state a|b           good (successful) or bad
//	phone-abslot [...] mark-good [a|b] [--dry-run]
//	phone-abslot [...] mark-unbootable a|b [--dry-run]
//	phone-abslot [...] set-active a|b [--dry-run]
//
// mark-good marks a slot (the one the phone booted from when none is named)
// as successful, so the bootloader stops counting its tries down and never
// falls back to the other slot. mark-unbootable is RAUC's "bad" before it
// writes a slot; set-active makes a slot the one to boot next with all its
// tries, as ABL's fastboot set_active does. They write nothing when the
// attributes are as asked already; otherwise the primary GPT and then the
// backup, each its entries before its header, so that one of the two is
// whole at any moment. slots prints slot=, good=, tries= and booted= lines,
// which phone-initramfs and RAUC's backend (rauc-abl) keep in
// /var/lib/slots/boot for the rest of the system. --disk names a disk image
// instead of the disk holding boot_a, --cmdline another kernel command
// line (for tests).
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
#define MAX_PRIORITY 3
#define MAX_RETRIES 7

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
static char booted(const char *cmdline, uint8_t a, uint8_t b)
{
	char line[4096] = "";
	FILE *f = fopen(cmdline, "r");
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

// The slot the bootloader boots next: the bootable one marked active, else
// the bootable one of the higher priority, else none.
static char primary(uint8_t a, uint8_t b)
{
	int ba = !(a & UNBOOTABLE), bb = !(b & UNBOOTABLE);
	if (ba && (a & ACTIVE) && !(bb && (b & ACTIVE)))
		return 'a';
	if (bb && (b & ACTIVE) && !(ba && (a & ACTIVE)))
		return 'b';
	if (ba && (!bb || (a & PRIORITY) >= (b & PRIORITY)))
		return 'a';
	return bb ? 'b' : 0;
}

static int good(uint8_t v) { return (v & SUCCESSFUL) && !(v & UNBOOTABLE); }

static void show(char slot, uint8_t v)
{
	printf("boot_%c: %spriority %d, tries left %d%s%s\n", slot, v & ACTIVE ? "active, " : "", v & PRIORITY,
	       (v & RETRIES) >> 3, v & SUCCESSFUL ? ", successful" : "", v & UNBOOTABLE ? ", unbootable" : "");
}

static int usage(void)
{
	fprintf(stderr, "usage: phone-abslot [--disk FILE] [--sector-size N] [--cmdline FILE] status | booted | slots [--booted a|b]\n"
			"       | get-state a|b | mark-good [a|b] | mark-unbootable a|b | set-active a|b  [--dry-run]\n");
	return 2;
}

int main(int argc, char **argv)
{
	const char *disk = NULL, *cmd = NULL, *cmdline = "/proc/cmdline";
	char slot = 0, given_booted = 0;
	unsigned ss = 0;
	int dry = 0;
	static const char *const cmds[] = {"status", "booted", "slots", "get-state", "mark-good", "mark-unbootable", "set-active"};
	for (int i = 1; i < argc; i++) {
		const char *a = argv[i];
		if (strcmp(a, "--disk") == 0 && i + 1 < argc)
			disk = argv[++i];
		else if (strcmp(a, "--sector-size") == 0 && i + 1 < argc)
			ss = strtoul(argv[++i], NULL, 10);
		else if (strcmp(a, "--cmdline") == 0 && i + 1 < argc)
			cmdline = argv[++i];
		else if (strcmp(a, "--booted") == 0 && i + 1 < argc && (argv[i + 1][0] == 'a' || argv[i + 1][0] == 'b') && !argv[i + 1][1])
			given_booted = argv[++i][0];
		else if (strcmp(a, "--dry-run") == 0)
			dry = 1;
		else if (cmd && !slot && (a[0] == 'a' || a[0] == 'b') && !a[1])
			slot = a[0];
		else if (!cmd) {
			for (size_t k = 0; k < sizeof(cmds) / sizeof(cmds[0]); k++)
				if (strcmp(a, cmds[k]) == 0)
					cmd = cmds[k];
			if (!cmd)
				return usage();
		} else
			return usage();
	}
	int needs_slot = !cmd || !strcmp(cmd, "get-state") || !strcmp(cmd, "mark-unbootable") || !strcmp(cmd, "set-active");
	int takes_slot = needs_slot || !strcmp(cmd, "mark-good");
	if (!cmd || (needs_slot && !slot) || (slot && !takes_slot) || (given_booted && strcmp(cmd, "slots")))
		return usage();
	int writes = !strcmp(cmd, "mark-good") || !strcmp(cmd, "mark-unbootable") || !strcmp(cmd, "set-active");
	char dev[PATH_MAX];
	if (!disk) {
		if (find_disk(dev, sizeof(dev)) < 0)
			return 1;
		disk = dev;
	}
	int fd = open(disk, writes && !dry ? O_RDWR : O_RDONLY);
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
	uint8_t va = pa[ATTR_OFFSET], vb = pb[ATTR_OFFSET];
	char now = booted(cmdline, va, vb);
	if (!strcmp(cmd, "status")) {
		show('a', va);
		show('b', vb);
		if (!now) {
			fail("cannot tell which slot booted");
			return 1;
		}
		printf("booted: %c\n", now);
		return 0;
	}
	if (!strcmp(cmd, "get-state")) {
		puts(good(slot == 'a' ? va : vb) ? "good" : "bad");
		return 0;
	}
	if (!strcmp(cmd, "booted") || !strcmp(cmd, "slots") || (!strcmp(cmd, "mark-good") && !slot)) {
		if (given_booted)
			now = given_booted;
		if (!now) {
			fail("cannot tell which slot booted");
			return 1;
		}
		if (!strcmp(cmd, "booted")) {
			printf("%c\n", now);
			return 0;
		}
		if (!strcmp(cmd, "slots")) {
			uint8_t vn = now == 'a' ? va : vb, vo = now == 'a' ? vb : va;
			char other = now == 'a' ? 'b' : 'a', next = primary(va, vb);
			char g = good(vn) || !good(vo) ? now : other;
			if (!next)
				next = g;
			uint8_t vnext = next == 'a' ? va : vb;
			printf("slot=%c\ngood=%c\ntries=%d\nbooted=%c\n", next, g, next == g ? 0 : (vnext & RETRIES) >> 3, now);
			return 0;
		}
		slot = now;
	}
	uint8_t na = va, nb = vb;
	uint8_t *mine = slot == 'a' ? &na : &nb, *other = slot == 'a' ? &nb : &na;
	if (!strcmp(cmd, "mark-good")) {
		if (good(*mine)) {
			printf("boot_%c is marked successful already\n", slot);
			return 0;
		}
		*mine = (uint8_t)((*mine | SUCCESSFUL) & ~UNBOOTABLE);
	} else if (!strcmp(cmd, "mark-unbootable")) {
		*mine = (uint8_t)((*mine | UNBOOTABLE) & ~SUCCESSFUL);
	} else {
		// As ABL's SetActiveSlot: the slot active, bootable, of the highest
		// priority with all its tries and not yet successful; the other
		// inactive one priority below it, its other bits kept.
		*mine = ACTIVE | MAX_PRIORITY | MAX_RETRIES << 3;
		*other = (uint8_t)((*other & ~(ACTIVE | PRIORITY)) | (MAX_PRIORITY - 1));
	}
	if (na == va && nb == vb) {
		printf("boot_a and boot_b are as asked already\n");
		return 0;
	}
	int have_backup = load(fd, ss, p.alt_lba, &b) == 0;
	uint8_t *ba = have_backup ? entry(&b, "boot_a") : NULL, *bb = have_backup ? entry(&b, "boot_b") : NULL;
	if (!ba || !bb)
		fprintf(stderr, "phone-abslot: the backup GPT is unusable; only the primary is changed\n");
	if (dry) {
		printf("would %s boot_%c (dry run)\n", cmd, slot);
		return 0;
	}
	pa[ATTR_OFFSET] = na;
	pb[ATTR_OFFSET] = nb;
	if (store(fd, ss, &p) < 0)
		return 1;
	if (ba && bb) {
		ba[ATTR_OFFSET] = na;
		bb[ATTR_OFFSET] = nb;
		if (store(fd, ss, &b) < 0)
			return 1;
	}
	if (!strcmp(cmd, "mark-good"))
		printf("marked boot_%c successful\n", slot);
	else
		printf("%s boot_%c\n", !strcmp(cmd, "set-active") ? "made active" : "marked unbootable", slot);
	return 0;
}
