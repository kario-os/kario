#!/bin/sh
# Development keys for system update bundles (R37) and kernel modules, never
# in git:
#   ota-keys.sh <dir>
# ca.cert.pem         the keyring the image trusts
# dev.*.pem           the signing certificate and key, issued by that CA
# untrusted.*.pem     a certificate of another CA, for the "bad signature" test
# kernel-modules.pem  the key the build signs kernel modules with and its
#                     certificate, one PEM as CONFIG_MODULE_SIG_KEY takes it;
#                     the kernel built beside them trusts only this
#                     certificate (phone-kernel.inc)
# Products sign with a key kept in an HSM (59-business-model.md §2).
# Every certificate carries only the extensions given here, whatever the
# machine's OpenSSL configuration adds by default. Each set is made once:
# a directory with update keys gets the module key on its own.
set -eu
dir=$1
mkdir -p "$dir"
cd "$dir"
made=""
# The kernel's own certs/default_x509.genkey, under Kario's name.
if [ ! -e kernel-modules.pem ]; then
	printf '%s\n' "basicConstraints=critical,CA:FALSE" "keyUsage=digitalSignature" \
		"subjectKeyIdentifier=hash" "authorityKeyIdentifier=keyid" > modules.ext
	openssl req -new -newkey rsa:4096 -nodes -keyout modules.key.pem -subj "/O=Kario/CN=Kario kernel modules (development)" \
		-out modules.csr 2>/dev/null
	openssl x509 -req -in modules.csr -signkey modules.key.pem -days 36500 -extfile modules.ext -out modules.cert.pem 2>/dev/null
	(umask 077 && cat modules.key.pem modules.cert.pem > kernel-modules.pem)
	rm -f modules.ext modules.csr modules.key.pem modules.cert.pem
	made="the kernel module key"
fi
if [ ! -e ca.cert.pem ]; then
	printf '%s\n' "basicConstraints=critical,CA:TRUE" "keyUsage=critical,keyCertSign,cRLSign" \
		"subjectKeyIdentifier=hash" > ca.ext
	printf '%s\n' "basicConstraints=CA:FALSE" "keyUsage=digitalSignature" "subjectKeyIdentifier=hash" \
		"authorityKeyIdentifier=keyid,issuer" > leaf.ext
	request() { # request <name> <subject>
		openssl req -new -newkey rsa:2048 -nodes -keyout "$1.key.pem" -subj "/O=Kario/CN=$2" -out "$1.csr" 2>/dev/null
	}
	root() { # root <name> <subject>
		request "$1" "$2"
		openssl x509 -req -in "$1.csr" -signkey "$1.key.pem" -days 36500 -extfile ca.ext -out "$1.cert.pem" 2>/dev/null
	}
	issue() { # issue <name> <ca> <subject>
		request "$1" "$3"
		openssl x509 -req -in "$1.csr" -CA "$2.cert.pem" -CAkey "$2.key.pem" -CAcreateserial -days 36500 \
			-extfile leaf.ext -out "$1.cert.pem" 2>/dev/null
	}
	root ca "Kario update CA (development)"
	issue dev ca "Kario update signing (development)"
	root other-ca "Someone else's CA"
	issue untrusted other-ca "Someone else's signing key"
	rm -f ca.ext leaf.ext ./*.csr ./*.srl
	made="update keys${made:+ and $made}"
fi
[ -z "$made" ] || echo "$made in $dir"
