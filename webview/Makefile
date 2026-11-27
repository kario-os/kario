# phone-webview. Cross builds pass CC, CFLAGS, LDFLAGS and PKG_CONFIG;
# O is the output directory.
PKG_CONFIG ?= pkg-config
PKGS := wpe-webkit-2.0 wpebackend-fdo-1.0 wayland-server glib-2.0
O ?= .
CFLAGS ?= -O2

$(O)/phone-webview: phone-webview.c view.c view.h $(O)/scripts.h
	$(CC) $(CFLAGS) -Wall -Wextra -I$(O) $(shell $(PKG_CONFIG) --cflags $(PKGS)) $(LDFLAGS) \
	    -o $@ phone-webview.c view.c $(shell $(PKG_CONFIG) --libs $(PKGS))

$(O)/scripts.h: scripts.sh nav.js bridge.js $(wildcard locale/*.json)
	sh scripts.sh nav.js bridge.js locale/*.json > $@

clean:
	rm -f $(O)/phone-webview $(O)/scripts.h

.PHONY: clean
