# ccache for WebKit only (over an hour at -j 8): a rebuild whose signature
# changed but whose sources and flags did not reuses the objects. Not global:
# the class exports its settings into every task, so enabling it everywhere
# would change all signatures and miss the whole shared state cache.
inherit ccache

# Outside tmp/, so cleaning tmp keeps the cache; WebKit's objects with debug
# info outgrow ccache's 5 GB default.
CCACHE_TOP_DIR = "${TOPDIR}/ccache"
export CCACHE_MAXSIZE = "20G"
