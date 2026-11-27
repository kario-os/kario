#!/bin/sh
# The second account's messages during the standby test (40-testing.md §9):
# the same text at the same fixed rate to both phones, each through its own
# messenger. To Kario's Hongyan from the demo chat server (phone-chatsrv's
# /v1/deliver: from the peer Li Si to the app's user), to WeChat from a
# sender phone over adb (WeChat logged in there as the second account, the
# chat with the first account open). Each message is logged with its time
# (traffic.log): standby.msgs, and the sending times that spot checks of
# the notifications compare with.
#
#   tools/android-compare/traffic.sh [--chat URL] [--sender SERIAL [--tap X,Y]]
#                                    [--every S] [--count N] [--log FILE] [--dry-run]
#
#   --chat URL       the phone-chatsrv the Kario phone's Hongyan uses
#   --sender SERIAL  the sender phone for WeChat (adb -s)
#   --tap X,Y        where its send button is (else the Enter key sends)
#   --every S        seconds between messages (900: 96 a day)
#   --count N        how many (96)
#   --log FILE       traffic.log
#   --dry-run        print what would be sent
set -u

CHAT=
SENDER=
TAP=
EVERY=900
COUNT=96
LOG=traffic.log
DRY=0

die() {
	echo "traffic: $*" >&2
	exit 1
}
usage() {
	sed -n '2,/^set -u/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case $1 in
	--chat | --sender | --tap | --every | --count | --log)
		[ $# -ge 2 ] || usage
		case $1 in
		--chat) CHAT=${2%/} ;;
		--sender) SENDER=$2 ;;
		--tap) TAP=$2 ;;
		--every) EVERY=$2 ;;
		--count) COUNT=$2 ;;
		--log) LOG=$2 ;;
		esac
		shift
		;;
	--dry-run) DRY=1 ;;
	*) usage ;;
	esac
	shift
done
[ -n "$CHAT$SENDER" ] || die "nothing to send to: --chat and/or --sender"
case $EVERY in "" | *[!0-9]*) usage ;; esac
case $COUNT in "" | *[!0-9]*) usage ;; esac
case $TAP in "" | [0-9]*,[0-9]*) ;; *) usage ;; esac

# to_kario TEXT: Li Si's message to Hongyan's user, pushed at once.
to_kario() {
	body="{\"from\":\"lisi\",\"to\":\"me\",\"text\":\"$1\"}"
	if [ "$DRY" = 1 ]; then
		echo "+ curl -d '$body' $CHAT/v1/deliver" >&2
		return 0
	fi
	curl -fsS -m 20 -H 'Content-Type: application/json' -d "$body" "$CHAT/v1/deliver" > /dev/null
}

# to_wechat TEXT: typed into the open chat on the sender phone, then sent.
to_wechat() {
	if [ -n "$TAP" ]; then send="input tap ${TAP%,*} ${TAP#*,}"; else send="input keyevent KEYCODE_ENTER"; fi
	if [ "$DRY" = 1 ]; then
		echo "+ adb -s $SENDER shell 'input text $1 && $send'" >&2
		return 0
	fi
	adb -s "$SENDER" shell "input text $1 && $send" > /dev/null
}

n=1
while [ "$n" -le "$COUNT" ]; do
	text=cmp-$n-$(date +%H%M%S)
	k=-
	w=-
	if [ -n "$CHAT" ]; then
		if to_kario "$text"; then k=ok; else k=failed; fi
	fi
	if [ -n "$SENDER" ]; then
		if to_wechat "$text"; then w=ok; else w=failed; fi
	fi
	echo "$(date +%s) $text kario=$k wechat=$w" | tee -a "$LOG"
	[ "$n" -eq "$COUNT" ] || [ "$DRY" = 1 ] || sleep "$EVERY"
	n=$((n + 1))
done
