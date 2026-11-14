#!/bin/sh
# Development keys for system update bundles (R37), never in git:
#   ota-keys.sh <dir>
# ca.cert.pem      the keyring the image trusts
# dev.*.pem        the signing certificate and key, issued by that CA
# untrusted.*.pem  a certificate of another CA, for the "bad signature" test
# Products sign with a key kept in an HSM (59-business-model.md §2).
# Every certificate carries only the extensions given here, whatever the
# machine's OpenSSL configuration adds by default.
set -eu
dir=$1
[ -e "$dir/ca.cert.pem" ] && exit 0
mkdir -p "$dir"
cd "$dir"
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
echo "update keys in $dir"
