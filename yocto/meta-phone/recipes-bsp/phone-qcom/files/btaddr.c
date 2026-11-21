// phone-btaddr: gives a Bluetooth controller without an address of its own
// a stable public one. WCN3990 on Qualcomm phones has none: its controller
// stays unconfigured, and BlueZ cannot use it, until one is set. The
// address is made from the phone's serial number (the bootloader's
// androidboot.serialno, else the machine ID) and set through the kernel's
// Bluetooth management interface, only when the controller lacks one.
//
//	phone-btaddr [--print] [hciN]
//
// --print shows the address and sets nothing.
#define _GNU_SOURCE
#include <errno.h>
#include <poll.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

// The kernel's Bluetooth management interface (BlueZ's lib/bluetooth/mgmt.h).
#define BTPROTO_HCI 1
#define HCI_DEV_NONE 0xffff
#define HCI_CHANNEL_CONTROL 3
#define MGMT_OP_READ_CONFIG_INFO 0x0037
#define MGMT_OP_SET_PUBLIC_ADDRESS 0x0039
#define MGMT_EV_CMD_COMPLETE 0x0001
#define MGMT_EV_CMD_STATUS 0x0002
#define MGMT_OPTION_PUBLIC_ADDRESS 0x00000002

struct sockaddr_hci {
	sa_family_t hci_family;
	unsigned short hci_dev;
	unsigned short hci_channel;
};

static uint16_t le16(const uint8_t *p) { return p[0] | p[1] << 8; }
static uint32_t le32(const uint8_t *p) { return le16(p) | (uint32_t)le16(p + 2) << 16; }
static void put16(uint8_t *p, uint16_t v) { p[0] = v; p[1] = v >> 8; }

static void serial(char *out, size_t n)
{
	char line[4096] = "";
	FILE *f = fopen("/proc/cmdline", "r");
	if (f) {
		if (!fgets(line, sizeof(line), f))
			line[0] = 0;
		fclose(f);
	}
	char *s = strstr(line, "androidboot.serialno=");
	if (s) {
		s += strlen("androidboot.serialno=");
		snprintf(out, n, "%.*s", (int)strcspn(s, " \n"), s);
		if (*out)
			return;
	}
	f = fopen("/etc/machine-id", "r");
	if (f) {
		if (!fgets(out, n, f))
			*out = 0;
		out[strcspn(out, "\n")] = 0;
		fclose(f);
	}
}

// The address as printed (most significant octet first): FNV-1a over the
// serial, locally administered and unicast as the shell's USB addresses.
static void address(const char *serial, uint8_t a[6])
{
	uint64_t h = 0xcbf29ce484222325ULL;
	for (const char *p = "kario-bt-addr\n"; *p; p++)
		h = (h ^ (uint8_t)*p) * 0x100000001b3ULL;
	for (const char *p = serial; *p; p++)
		h = (h ^ (uint8_t)*p) * 0x100000001b3ULL;
	for (int i = 0; i < 6; i++)
		a[i] = h >> (8 * i);
	a[0] = (a[0] & 0xfe) | 0x02;
}

// Sends one command and waits for its completion; the command's status, or
// -1. The reply's parameters go to rsp.
static int command(int fd, uint16_t op, uint16_t index, const uint8_t *param, uint16_t len, uint8_t *rsp, size_t rsp_size)
{
	uint8_t buf[6 + 16];
	put16(buf, op);
	put16(buf + 2, index);
	put16(buf + 4, len);
	memcpy(buf + 6, param, len);
	// A command the kernel turns down is answered by a status event, its
	// write() returning 0 instead of the length.
	if (write(fd, buf, 6 + len) < 0) {
		perror("phone-btaddr: write");
		return -1;
	}
	for (;;) {
		struct pollfd p = {fd, POLLIN, 0};
		if (poll(&p, 1, 5000) <= 0) {
			fprintf(stderr, "phone-btaddr: no reply from the kernel\n");
			return -1;
		}
		uint8_t ev[512];
		ssize_t n = read(fd, ev, sizeof(ev));
		if (n < 9)
			continue;
		uint16_t code = le16(ev), plen = le16(ev + 4);
		if (le16(ev + 2) != index || (size_t)n < 6u + plen || plen < 3 || le16(ev + 6) != op)
			continue;
		if (code == MGMT_EV_CMD_COMPLETE) {
			size_t got = (size_t)plen - 3;
			if (rsp)
				memcpy(rsp, ev + 9, got < rsp_size ? got : rsp_size);
			return ev[8];
		}
		if (code == MGMT_EV_CMD_STATUS)
			return ev[8];
	}
}

int main(int argc, char **argv)
{
	int print = 0;
	const char *hci = "hci0";
	for (int i = 1; i < argc; i++) {
		if (strcmp(argv[i], "--print") == 0)
			print = 1;
		else if (strncmp(argv[i], "hci", 3) == 0 && argv[i][3])
			hci = argv[i];
		else {
			fprintf(stderr, "usage: phone-btaddr [--print] [hciN]\n");
			return 2;
		}
	}
	char sn[256] = "";
	serial(sn, sizeof(sn));
	if (!*sn) {
		fprintf(stderr, "phone-btaddr: no serial number or machine ID\n");
		return 1;
	}
	uint8_t a[6];
	address(sn, a);
	char text[18];
	snprintf(text, sizeof(text), "%02X:%02X:%02X:%02X:%02X:%02X", a[0], a[1], a[2], a[3], a[4], a[5]);
	if (print) {
		printf("%s\n", text);
		return 0;
	}
	uint16_t index = strtoul(hci + 3, NULL, 10);
	int fd = socket(PF_BLUETOOTH, SOCK_RAW | SOCK_CLOEXEC, BTPROTO_HCI);
	struct sockaddr_hci sa = {AF_BLUETOOTH, HCI_DEV_NONE, HCI_CHANNEL_CONTROL};
	if (fd < 0 || bind(fd, (struct sockaddr *)&sa, sizeof(sa)) < 0) {
		perror("phone-btaddr: the Bluetooth management socket");
		return 1;
	}
	uint8_t info[10] = {0};
	int st = command(fd, MGMT_OP_READ_CONFIG_INFO, index, NULL, 0, info, sizeof(info));
	if (st != 0) {
		fprintf(stderr, "phone-btaddr: %s: reading its configuration failed (%d)\n", hci, st);
		return 1;
	}
	if (!(le32(info + 6) & MGMT_OPTION_PUBLIC_ADDRESS)) {
		printf("phone-btaddr: %s has its address\n", hci);
		return 0;
	}
	uint8_t bdaddr[6];
	for (int i = 0; i < 6; i++)
		bdaddr[i] = a[5 - i];
	st = command(fd, MGMT_OP_SET_PUBLIC_ADDRESS, index, bdaddr, sizeof(bdaddr), NULL, 0);
	if (st != 0) {
		fprintf(stderr, "phone-btaddr: %s: setting %s failed (%d)\n", hci, text, st);
		return 1;
	}
	printf("phone-btaddr: %s has %s\n", hci, text);
	return 0;
}
