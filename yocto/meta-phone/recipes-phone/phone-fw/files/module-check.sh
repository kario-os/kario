#!/bin/sh
# Under lockdown (integrity) the kernel loads a module only with a signature
# by a key it was built with (phone-lockdown.cfg); the 6T's first
# whole-phone image refused every module, signed by another build's key.
# This loads one module and what it needs afresh: the kernel must have
# verified its signature (no E taint), and a copy of it whose signature
# the kernel cannot find (a byte after it) must be refused. The module is
# left loaded or not, as it was found.
#   module-check.sh [module]
# The default, nft_masq (the hotspot's NAT), is in every Kario image and
# loaded only while the hotspot is on. Prints PHONE-MODULES: PASS, or FAIL
# and why.
m=${1:-nft_masq}
fail() {
	echo "PHONE-MODULES: FAIL: $*"
	exit 1
}
level=$(sed -n 's/.*\[\([a-z]*\)\].*/\1/p' /sys/kernel/security/lockdown 2>/dev/null)
case $level in
integrity | confidentiality) ;;
*) fail "the kernel is not locked down (/sys/kernel/security/lockdown: ${level:-missing})" ;;
esac
file=$(modinfo -n "$m" 2>/dev/null)
[ -f "$file" ] || fail "no module $m for Linux $(uname -r): ${file:-modinfo knows none}"
loaded=""
if [ -e "/sys/module/$m/initstate" ]; then
	loaded=1
	modprobe -r "$m" 2>/dev/null || fail "$m is loaded and in use: there is no fresh load to show"
fi
out=$(modprobe "$m" 2>&1) || fail "modprobe $m under lockdown $level: ${out:-exit status $?}"
[ "$(cat "/sys/module/$m/initstate" 2>/dev/null)" = live ] || fail "modprobe $m left it unloaded"
taint=$(cat "/sys/module/$m/taint" 2>/dev/null)
case $taint in
*E*) fail "$m loaded without a signature the kernel verified (taint $taint)" ;;
esac
modprobe -r "$m" 2>/dev/null || fail "modprobe -r $m"
copy=/tmp/module-check-$$.ko
{ cp "$file" "$copy" && printf '\n' >> "$copy"; } || fail "cannot copy $file to $copy"
refused=$(insmod "$copy" 2>&1)
status=$?
rm -f "$copy"
if [ "$status" = 0 ]; then
	rmmod "$m" 2>/dev/null
	fail "a copy of $m without a signature the kernel can find loaded under lockdown $level"
fi
case $refused in
*"not permitted"*) ;;
*) fail "the copy without a signature: ${refused:-insmod exit status $status} (want: Operation not permitted, from lockdown)" ;;
esac
if [ -n "$loaded" ]; then
	modprobe "$m" 2>/dev/null || fail "modprobe $m, loaded again as it was"
fi
signer=$(modinfo -F signer "$file" 2>/dev/null)
echo "PHONE-MODULES: PASS: lockdown $level; $m and what it needs loaded with signatures the kernel verified (signer: ${signer:-not shown}); its copy without one refused (${refused##*: })"
