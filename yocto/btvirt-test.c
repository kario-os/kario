/* btvirt's virtual controllers (BlueZ's emulator/btdev.c on
 * src/shared/mainloop.c), driven through HCI the way the kernel drives the
 * selftest's two, built with AddressSanitizer by btvirt-test.py:
 *
 *   inquiry  an inquiry that runs to its end (inquiry_timeout)
 *   batch    the same, with the inquiry's end and its next result timer in
 *            one epoll batch, as on a busy emulator
 *   page     a connection to nobody, which ends in a page timeout
 *   repair   two controllers pair, disconnect and pair again (Secure
 *            Simple Pairing): each Simple Pairing Complete a controller
 *            reports needs an IO Capability Request before it, as the
 *            kernel holds the connection from one to the other
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/uio.h>
#include <unistd.h>

#include "src/shared/mainloop.h"
#include "src/shared/timeout.h"
#include "monitor/bt.h"
#include "emulator/btdev.h"

static int inquiry_complete, conn_complete;

static void sent(const struct iovec *iov, int iovlen, void *user_data)
{
	const uint8_t *type = iov[0].iov_base;
	const uint8_t *hdr;

	if (iovlen < 2 || *type != BT_H4_EVT_PKT)
		return;
	hdr = iov[1].iov_base;
	if (hdr[0] == BT_HCI_EVT_INQUIRY_COMPLETE)
		inquiry_complete++;
	if (hdr[0] == BT_HCI_EVT_CONN_COMPLETE)
		conn_complete++;
}

static bool quit(void *user_data)
{
	mainloop_quit();
	return false;
}

/* Busy past the inquiry's end (1280 ms) and the result timer's next turn
 * (every 100 ms), so both are ready, in that order, when the loop polls. */
static bool stall(void *user_data)
{
	usleep(200 * 1000);
	return false;
}

/* The repair scenario's hosts: what each controller is sent next goes
 * through the main loop, as a kernel's commands come after its events. */
struct host {
	struct btdev *dev;
	struct host *peer;
	uint16_t handle;
	int io_cap_requests, pairing_completes;
};

static struct host hosts[2];
static int pairings;
static bool unbalanced;

struct cmd {
	struct btdev *dev;
	uint8_t pkt[32];
	uint8_t len;
};

static struct cmd queue[16];
static int queued;

static bool flush(void *user_data)
{
	struct cmd q[16];
	int i, n = queued;

	memcpy(q, queue, sizeof(q));
	queued = 0;
	for (i = 0; i < n; i++)
		btdev_receive_h4(q[i].dev, q[i].pkt, q[i].len);
	return false;
}

static void cmd(struct host *h, uint16_t opcode, const void *param,
							uint8_t len)
{
	struct cmd *c = &queue[queued++];

	c->dev = h->dev;
	c->pkt[0] = BT_H4_CMD_PKT;
	c->pkt[1] = opcode & 0xff;
	c->pkt[2] = opcode >> 8;
	c->pkt[3] = len;
	memcpy(c->pkt + 4, param, len);
	c->len = 4 + len;
	if (queued == 1)
		timeout_add(1, flush, NULL, NULL);
}

static void connect_peer(void)
{
	uint8_t p[13] = { 0 };

	memcpy(p, btdev_get_bdaddr(hosts[1].dev), 6);
	p[6] = 0x18;
	p[7] = 0xcc;
	p[8] = 0x01;
	cmd(&hosts[0], BT_HCI_CMD_CREATE_CONN, p, sizeof(p));
}

static void host_event(const struct iovec *iov, int iovlen, void *user_data)
{
	struct host *h = user_data;
	const uint8_t *type = iov[0].iov_base;
	const uint8_t *hdr, *p;
	uint8_t reply[9];

	if (iovlen < 3 || *type != BT_H4_EVT_PKT)
		return;
	hdr = iov[1].iov_base;
	p = iov[2].iov_base;
	switch (hdr[0]) {
	case BT_HCI_EVT_CONN_REQUEST:
		memcpy(reply, p, 6);
		reply[6] = 0x01;
		cmd(h, BT_HCI_CMD_ACCEPT_CONN_REQUEST, reply, 7);
		break;
	case BT_HCI_EVT_CONN_COMPLETE:
		if (p[0])
			break;
		h->handle = p[1] | p[2] << 8;
		if (h == &hosts[0])
			cmd(h, BT_HCI_CMD_AUTH_REQUESTED, p + 1, 2);
		break;
	case BT_HCI_EVT_LINK_KEY_REQUEST:
		cmd(h, BT_HCI_CMD_LINK_KEY_REQUEST_NEG_REPLY, p, 6);
		break;
	case BT_HCI_EVT_IO_CAPABILITY_REQUEST:
		h->io_cap_requests++;
		memcpy(reply, p, 6);
		reply[6] = 0x01;	/* DisplayYesNo */
		reply[7] = 0x00;
		reply[8] = 0x03;	/* dedicated bonding, MITM */
		cmd(h, BT_HCI_CMD_IO_CAPABILITY_REQUEST_REPLY, reply, 9);
		break;
	case BT_HCI_EVT_USER_CONFIRM_REQUEST:
		cmd(h, BT_HCI_CMD_USER_CONFIRM_REQUEST_REPLY, p, 6);
		break;
	case BT_HCI_EVT_SIMPLE_PAIRING_COMPLETE:
		if (++h->pairing_completes > h->io_cap_requests)
			unbalanced = true;
		break;
	case BT_HCI_EVT_AUTH_COMPLETE:
		if (h != &hosts[0])
			break;
		pairings++;
		memcpy(reply, p + 1, 2);
		reply[2] = 0x13;	/* remote user terminated */
		cmd(h, BT_HCI_CMD_DISCONNECT, reply, 3);
		break;
	case BT_HCI_EVT_DISCONNECT_COMPLETE:
		if (h != &hosts[0])
			break;
		if (pairings < 2)
			connect_peer();
		else
			mainloop_quit();
		break;
	}
}

static int repair(void)
{
	static const uint8_t ssp_on = 0x01, page_scan = 0x02;
	int i;

	for (i = 0; i < 2; i++) {
		hosts[i].dev = btdev_create(BTDEV_TYPE_BREDRLE, i);
		hosts[i].peer = &hosts[!i];
		btdev_set_send_handler(hosts[i].dev, host_event, &hosts[i]);
		cmd(&hosts[i], BT_HCI_CMD_WRITE_SIMPLE_PAIRING_MODE, &ssp_on, 1);
	}
	cmd(&hosts[1], BT_HCI_CMD_WRITE_SCAN_ENABLE, &page_scan, 1);
	connect_peer();
	timeout_add(5000, quit, NULL, NULL);
	mainloop_run();
	for (i = 0; i < 2; i++) {
		printf("repair: controller %d: %d IO Capability Request, %d Simple Pairing Complete events\n",
			i, hosts[i].io_cap_requests, hosts[i].pairing_completes);
		btdev_destroy(hosts[i].dev);
	}
	return pairings == 2 && !unbalanced ? 0 : 1;
}

int main(int argc, char *argv[])
{
	/* HCI Inquiry: the general inquiry access code, 1.28 s, any number. */
	static const uint8_t inquiry[] = { BT_H4_CMD_PKT, 0x01, 0x04, 5,
					0x33, 0x8b, 0x9e, 0x01, 0x00 };
	/* HCI Create Connection to an address no controller has. */
	static const uint8_t create_conn[] = { BT_H4_CMD_PKT, 0x05, 0x04, 13,
					0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
					0x18, 0xcc, 0x01, 0x00, 0x00, 0x00, 0x00 };
	const char *what = argc > 1 ? argv[1] : "";
	unsigned int run_ms = 2000;
	struct btdev *dev;
	bool ok;

	mainloop_init();
	if (!strcmp(what, "repair"))
		return repair();
	dev = btdev_create(BTDEV_TYPE_BREDRLE, 0);
	btdev_set_send_handler(dev, sent, NULL);
	if (!strcmp(what, "inquiry") || !strcmp(what, "batch")) {
		btdev_receive_h4(dev, inquiry, sizeof(inquiry));
		if (!strcmp(what, "batch"))
			timeout_add(1250, stall, NULL, NULL);
	} else if (!strcmp(what, "page")) {
		btdev_receive_h4(dev, create_conn, sizeof(create_conn));
		run_ms = 6000;
	} else {
		fprintf(stderr, "usage: %s inquiry|batch|page|repair\n", argv[0]);
		return 2;
	}
	timeout_add(run_ms, quit, NULL, NULL);
	mainloop_run();
	btdev_destroy(dev);

	ok = strcmp(what, "page") ? inquiry_complete == 1 : conn_complete == 0;
	printf("%s: %d inquiry complete, %d connection complete events\n",
					what, inquiry_complete, conn_complete);
	return ok ? 0 : 1;
}
