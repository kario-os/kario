# ModemManager 1.24 died of SIGSEGV on the OnePlus 6T as its bearers
# connected (at boot, the IMS and data bearers at once; after airplane
# mode): on the reply to a netlink request (the data link up, its MTU) it
# called the request's callback after freeing the request (0001). And it
# aborts as it stops
# with a bearer connected on a modem of multiplexed bearers only, as the
# 6T's IPA modem is: the bearers counted outgrow the maximum it computes
# (0002). Both are on ModemManager's main branch after 1.24.2 (39-oneplus-6t.md
# §14.20): this append goes with OE's move to a release that has them.
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"
SRC_URI:append = " \
    file://0001-netlink-fix-use-after-free-calling-completion_fn-on-.patch \
    file://0002-base-manager-fix-cleanup-of-multiplexed-bearers.patch \
"
