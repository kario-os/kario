#!/usr/bin/env python3
"""Whether a kernel under lockdown (integrity) loads every module given.

    phone-modsig-check.py --kernel KERNEL [--kernel KERNEL...] [--cert PEM] MODULES

KERNEL is an Image, an Image.gz or an Android boot image carrying one;
MODULES is a root file system's lib/modules. Every module there must be in
the directory of the kernel's release and end in a signature (PKCS#7, as
the kernel's sign-file appends it) that verifies against a certificate the
kernel carries: the kernel finds the key by the signer's issuer and serial
number (or key identifier), and refuses a module without one it has
(ENOKEY), unsigned (ENODATA) or with an unknown algorithm (ENOPKG) alike,
"unsigned module loading is restricted". With --cert (a PEM holding the
certificate, the build's signing key may be in it too) the kernel must
carry that certificate and the modules must be signed with it; without,
any certificate in the kernel's image counts (the regulatory database's
too, a superset of the keys the kernel loads modules with). RSA signatures
only, the kind CONFIG_MODULE_SIG_KEY_TYPE_RSA makes. Exit status 1 when a
module would not load.
"""
import argparse
import base64
import gzip
import hashlib
import lzma
import os
import re
import struct
import sys
import zlib

MARKER = b"~Module signature appended~\n"
# struct module_signature: algo, hash, id_type, signer_len, key_id_len,
# 3 bytes of padding, the signature's length (big-endian).
SIGINFO = struct.Struct(">BBBBB3xI")
PKEY_ID_PKCS7 = 2
SIGNED_DATA = "1.2.840.113549.1.7.2"
MESSAGE_DIGEST = "1.2.840.113549.1.9.4"
RSA = "1.2.840.113549.1.1.1"
RSA_SIGNATURES = {RSA, "1.2.840.113549.1.1.11", "1.2.840.113549.1.1.12", "1.2.840.113549.1.1.13", "1.2.840.113549.1.1.14"}
DIGESTS = {
    "2.16.840.1.101.3.4.2.1": "sha256", "2.16.840.1.101.3.4.2.2": "sha384", "2.16.840.1.101.3.4.2.3": "sha512",
    "2.16.840.1.101.3.4.2.4": "sha224", "2.16.840.1.101.3.4.2.8": "sha3_256", "2.16.840.1.101.3.4.2.9": "sha3_384",
    "2.16.840.1.101.3.4.2.10": "sha3_512",
}
SKID = "2.5.29.14"
CN = "2.5.4.3"
# A certificate's start: SEQUENCE { SEQUENCE (the signed part) { [0] { INTEGER 2 } (version 3) ...
CERT_START = re.compile(rb"\x30\x82..\x30\x82..\xa0\x03\x02\x01\x02", re.S)
BANNER = re.compile(rb"Linux version (\S+) \(")


class Bad(Exception):
    pass


def tlv(b, i):
    """The tag of the DER element at i, where its contents start and end."""
    if i + 2 > len(b):
        raise Bad("truncated DER")
    tag, n, i = b[i], b[i + 1], i + 2
    if n & 0x80:
        k = n & 0x7F
        if not 0 < k <= 4 or i + k > len(b):
            raise Bad("bad DER length")
        n, i = int.from_bytes(b[i:i + k], "big"), i + k
    if i + n > len(b):
        raise Bad("truncated DER")
    return tag, i, i + n


def items(b, start, end):
    """The elements between start and end: (tag, contents start, end, element start)."""
    out, i = [], start
    while i < end:
        tag, s, e = tlv(b, i)
        out.append((tag, s, e, i))
        i = e
    return out


def oid(b):
    first, parts, v = b[0], [], 0
    parts += [min(first // 40, 2), first - 40 * min(first // 40, 2)]
    for c in b[1:]:
        v = v << 7 | c & 0x7F
        if not c & 0x80:
            parts.append(v)
            v = 0
    return ".".join(map(str, parts))


def want(item, tag, what):
    if item[0] != tag:
        raise Bad(f"{what}: tag {item[0]:#x}, want {tag:#x}")
    return item


def alg_oid(b, item):
    """The OID that starts an AlgorithmIdentifier."""
    first = items(b, item[1], item[2])[0]
    return oid(b[first[1]:first[2]])


def hexid(b):
    return b.lstrip(b"\0").hex(":")


class Cert:
    def __init__(self, der):
        self.der = der
        tag, s, e = tlv(der, 0)
        if tag != 0x30 or e != len(der):
            raise Bad("not a certificate")
        parts = items(der, s, e)
        if len(parts) != 3:
            raise Bad("not a certificate")
        tbs = want(parts[0], 0x30, "certificate")
        f = items(der, tbs[1], tbs[2])
        k = 1 if f[0][0] == 0xA0 else 0
        serial = want(f[k], 0x02, "serial")
        self.serial = der[serial[1]:serial[2]]
        issuer, subject = want(f[k + 2], 0x30, "issuer"), want(f[k + 4], 0x30, "subject")
        self.issuer = der[issuer[3]:issuer[2]]
        self.cn = name_cn(der, subject)
        self.n, self.e = rsa_key(der, want(f[k + 5], 0x30, "public key"))
        self.skid = None
        for x in f[k + 6:]:
            if x[0] != 0xA3:
                continue
            for ext in items(der, *items(der, x[1], x[2])[0][1:3]):
                fields = items(der, ext[1], ext[2])
                if oid(der[fields[0][1]:fields[0][2]]) == SKID:
                    value = fields[-1]
                    inner = tlv(der, value[1])
                    self.skid = der[inner[1]:inner[2]]

    def describe(self):
        return f"{self.cn or '?'}, serial {hexid(self.serial)}"


def name_cn(b, name):
    for rdn in items(b, name[1], name[2]):
        for atv in items(b, rdn[1], rdn[2]):
            t, v = items(b, atv[1], atv[2])[:2]
            if oid(b[t[1]:t[2]]) == CN:
                return b[v[1]:v[2]].decode("utf-8", "replace")
    return ""


def rsa_key(b, spki):
    alg, bits = items(b, spki[1], spki[2])[:2]
    if alg_oid(b, alg) != RSA:
        return None, None
    seq = tlv(b, bits[1] + 1)
    n, e = items(b, seq[1], seq[2])[:2]
    return int.from_bytes(b[n[1]:n[2]], "big"), int.from_bytes(b[e[1]:e[2]], "big")


def read(path, mode="rb"):
    with open(path, mode) as f:
        return f.read()


def kernel_image(path):
    """The kernel's uncompressed image from an Image, an Image.gz or an
    Android boot image (header versions 0 to 4)."""
    data = read(path)
    if data[:8] == b"ANDROID!":
        size, version = struct.unpack_from("<I", data, 8)[0], struct.unpack_from("<I", data, 40)[0]
        page = 4096 if version >= 3 else struct.unpack_from("<I", data, 36)[0]
        data = data[page:page + size]
    if data[:2] == b"\x1f\x8b":
        # The device tree may follow the compressed kernel (phone-bootimg).
        data = zlib.decompressobj(16 + zlib.MAX_WBITS).decompress(data)
    return data


def kernel_certs(image):
    certs, seen = [], set()
    for m in CERT_START.finditer(image):
        n = int.from_bytes(image[m.start() + 2:m.start() + 4], "big")
        der = image[m.start():m.start() + 4 + n]
        if der in seen:
            continue
        try:
            certs.append(Cert(der))
            seen.add(der)
        except (Bad, IndexError):
            pass
    return certs


def pem_cert(path):
    text = read(path, "rb").decode("ascii", "replace")
    m = re.search(r"-----BEGIN CERTIFICATE-----(.*?)-----END CERTIFICATE-----", text, re.S)
    if not m:
        raise Bad(f"{path}: no certificate")
    return Cert(base64.b64decode("".join(m.group(1).split())))


def module_data(path):
    data = read(path)
    if path.endswith(".ko.xz"):
        return lzma.decompress(data)
    if path.endswith(".ko.gz"):
        return gzip.decompress(data)
    if path.endswith(".ko"):
        return data
    raise Bad("compressed in a way this check does not read")


def signature(data):
    """The signed part of a module and its PKCS#7 signature."""
    if not data.endswith(MARKER):
        raise Bad("unsigned")
    end = len(data) - len(MARKER) - SIGINFO.size
    if end < 0:
        raise Bad("unsigned")
    algo, hash_, id_type, signer_len, key_id_len, sig_len = SIGINFO.unpack_from(data, end)
    if id_type != PKEY_ID_PKCS7:
        raise Bad(f"signature of type {id_type}, not PKCS#7")
    if sig_len > end:
        raise Bad("signature longer than the module")
    return data[:end - sig_len], data[end - sig_len:end]


def verify(body, sig, trusted):
    """The certificate whose key signed body (sig: DER PKCS#7 SignedData)."""
    tag, s, e = tlv(sig, 0)
    ci = items(sig, s, e)
    if tag != 0x30 or oid(sig[ci[0][1]:ci[0][2]]) != SIGNED_DATA:
        raise Bad("signature not PKCS#7 signed data")
    sd = items(sig, *items(sig, ci[1][1], ci[1][2])[0][1:3])
    infos = want(sd[-1], 0x31, "signer infos")
    signers = items(sig, infos[1], infos[2])
    if not signers:
        raise Bad("signature without a signer")
    found = None
    for si in signers:
        f = items(sig, si[1], si[2])
        sid = f[1]
        if sid[0] == 0x30:
            issuer, serial = items(sig, sid[1], sid[2])[:2]
            name, number = sig[issuer[3]:issuer[2]], sig[serial[1]:serial[2]]
            match = [c for c in trusted if c.issuer == name and c.serial == number]
            who = f"{name_cn(sig, issuer) or '?'} (issuer), serial {hexid(number)}"
        elif sid[0] == 0x80:
            keyid = sig[sid[1]:sid[2]]
            match = [c for c in trusted if c.skid == keyid]
            who = f"key identifier {keyid.hex(':')}"
        else:
            raise Bad("signer of an unknown kind")
        if not match:
            raise Bad(f"signed by a key the kernel does not have: {who}")
        cert = match[0]
        hname = DIGESTS.get(alg_oid(sig, want(f[2], 0x30, "digest algorithm")))
        if hname is None:
            raise Bad("digest the kernel does not take")
        digest = hashlib.new(hname, body).digest()
        signed, attrs, k = digest, None, 3
        if f[k][0] == 0xA0:
            attrs, k = f[k], k + 1
        if alg_oid(sig, want(f[k], 0x30, "signature algorithm")) not in RSA_SIGNATURES or cert.n is None:
            raise Bad("not an RSA signature: this check reads RSA only")
        value = want(f[k + 1], 0x04, "signature")
        if attrs is not None:
            md = None
            for a in items(sig, attrs[1], attrs[2]):
                t, vals = items(sig, a[1], a[2])[:2]
                if oid(sig[t[1]:t[2]]) == MESSAGE_DIGEST:
                    v = items(sig, vals[1], vals[2])[0]
                    md = sig[v[1]:v[2]]
            if md != digest:
                raise Bad("signature does not verify: the module changed after signing")
            # The signature covers the attributes as a SET, not as [0].
            signed = hashlib.new(hname, b"\x31" + sig[attrs[3] + 1:attrs[2]]).digest()
        if not rsa_verifies(cert, sig[value[1]:value[2]], hname, signed):
            raise Bad("signature does not verify: the module changed after signing")
        found = cert
    return found


def rsa_verifies(cert, s, hname, digest):
    """RSASSA-PKCS1-v1_5 (RFC 8017 §8.2.2) with the certificate's key."""
    k = (cert.n.bit_length() + 7) // 8
    v = int.from_bytes(s, "big")
    if len(s) != k or v >= cert.n:
        return False
    em = pow(v, cert.e, cert.n).to_bytes(k, "big")
    if em[:2] != b"\0\1":
        return False
    j = em.find(b"\0", 2)
    if j < 10 or em[2:j].strip(b"\xff"):
        return False
    di = em[j + 1:]
    try:
        tag, s0, e0 = tlv(di, 0)
        alg, octets = items(di, s0, e0)
        name = DIGESTS.get(alg_oid(di, alg))
    except (Bad, IndexError):
        return False
    return e0 == len(di) and name == hname and di[octets[1]:octets[2]] == digest


def check(kernels, cert_file, modules, out=print):
    """The problems a kernel under lockdown would have with these modules."""
    trusted, releases, problems = None, set(), []
    for path in kernels:
        image = kernel_image(path)
        m = BANNER.search(image)
        if not m:
            return [f"{path}: no Linux kernel in it"]
        releases.add(m.group(1).decode())
        certs = kernel_certs(image)
        out(f"phone-modsig-check: {path}: Linux {m.group(1).decode()}, carries "
            + ("; ".join(c.describe() for c in certs) or "no certificate"))
        ders = {c.der for c in certs}
        trusted = certs if trusted is None else [c for c in trusted if c.der in ders]
    if len(releases) > 1:
        return [f"the kernels given are different releases: {', '.join(sorted(releases))}"]
    release = releases.pop()
    if cert_file:
        want_cert = pem_cert(cert_file)
        if want_cert.der not in {c.der for c in trusted}:
            return [f"the kernel does not carry the certificate of {cert_file} ({want_cert.describe()})"]
        trusted = [want_cert]
    if not trusted:
        return ["the kernel carries no certificate: it can check no module's signature"]
    if not os.path.isdir(modules):
        out(f"phone-modsig-check: no {modules}: no modules to check")
        return []
    count, signers = 0, {}
    for d in sorted(os.listdir(modules)):
        top = os.path.join(modules, d)
        for root, dirs, files in os.walk(top):
            dirs.sort()
            for name in sorted(files):
                if not re.search(r"\.ko(\.\w+)?$", name):
                    continue
                path = os.path.join(root, name)
                rel = os.path.relpath(path, modules)
                count += 1
                if d != release:
                    problems.append(f"{rel}: for Linux {d}, the kernel is {release}")
                    continue
                try:
                    body, sig = signature(module_data(path))
                    c = verify(body, sig, trusted)
                    signers[c.describe()] = signers.get(c.describe(), 0) + 1
                except (Bad, IndexError, ValueError, lzma.LZMAError, OSError, EOFError) as e:
                    problems.append(f"{rel}: {e}")
    if count == 0:
        out(f"phone-modsig-check: no modules in {modules}")
    else:
        out(f"phone-modsig-check: {count} modules for Linux {release}; signed by "
            + (", ".join(f"{k} ({v})" for k, v in sorted(signers.items())) or "nothing the kernel has"))
    return problems


def main():
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--kernel", action="append", required=True)
    p.add_argument("--cert")
    p.add_argument("modules")
    a = p.parse_args()
    try:
        problems = check(a.kernel, a.cert, a.modules)
    except (Bad, OSError, zlib.error) as e:
        problems = [str(e)]
    by = {}
    for line in problems:
        by.setdefault(line.split(": ", 1)[-1], []).append(line)
    for why, lines in sorted(by.items(), key=lambda kv: -len(kv[1])):
        for line in lines[:5]:
            print("phone-modsig-check:", line)
        if len(lines) > 5:
            print(f"phone-modsig-check: ... and {len(lines) - 5} more: {why}")
    if problems:
        print(f"phone-modsig-check: FAIL: under lockdown the kernel would refuse these modules ({len(problems)} problems)")
        return 1
    print("phone-modsig-check: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
