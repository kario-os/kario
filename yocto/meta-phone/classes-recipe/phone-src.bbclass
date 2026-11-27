# Where the platform's recipes take their sources from.
#
# PHONE_SRC = "tarball" (the default): the tarballs `make yocto-src` packs
# from the working tree into each recipe's files/, so an image carries what
# is not committed yet.
#
# PHONE_SRC = "git": one commit of the kario repository (PHONE_KARIO_REV, from
# PHONE_KARIO_GIT), fetched by bitbake's git fetcher; nothing of the working
# tree's sources goes in. Releases build this way (kario-internal's `make
# release`), and so can anyone with the repository's URL and a commit.
# The Go modules and Rust crates are fetched and checksummed by bitbake in
# both modes (the recipes' -go-mods.inc and -crates.inc).

PHONE_SRC ??= "tarball"
PHONE_KARIO_GIT ??= "git://github.com/kario-os/kario.git;protocol=https"
PHONE_KARIO_REV ??= ""
# The repository is unnamed in SRC_URI (cargo's class takes named git
# sources for crate dependencies), so its commit is SRCREV.
SRCREV ?= "${PHONE_KARIO_REV}"

def phone_git(d):
    return d.getVar('PHONE_SRC') == 'git'

def phone_src(d, tarball, dest='kario'):
    """The SRC_URI entry of a recipe's sources: its tarball, or in git mode
    the commit, checked out at dest in the unpack directory."""
    if not phone_git(d):
        return tarball
    if not d.getVar('PHONE_KARIO_REV'):
        bb.fatal('PHONE_SRC = "git" needs PHONE_KARIO_REV (the commit of kario to build)')
    return '%s;nobranch=1;destsuffix=%s' % (d.getVar('PHONE_KARIO_GIT'), dest)

def phone_dir(d, tarball_dir, tree_dir):
    """Where a part of the sources is unpacked: the tarball's top directory,
    or its path in the checkout."""
    base = d.getVar('UNPACKDIR')
    if phone_git(d):
        return os.path.join(base, 'kario', tree_dir)
    return os.path.join(base, tarball_dir) if tarball_dir else base

# kario's LICENSE (Apache-2.0): beside the tarball's top directory, or at the
# checkout's root.
PHONE_KARIO_LICENSE = "${@phone_dir(d, 'LICENSE', 'LICENSE')}"
