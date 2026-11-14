GO      ?= go
PREFIX  := build/usr
KAS     ?= $(HOME)/.local/kas-venv/bin/kas-container
YOCTO   ?= $(HOME)/yocto
# The Yocto build directory, its sources and the update keys (ota-keys/).
WORK    ?= $(YOCTO)/kario-work
KASENV   = KAS_CONTAINER_ENGINE=podman KAS_WORK_DIR=$(WORK) \
           DL_DIR=$(YOCTO)/cache/downloads SSTATE_DIR=$(YOCTO)/cache/sstate
# What kas builds, and where this tree is inside kas-container: /repo when
# the configuration is this tree's; the internal workspace (kario-internal)
# passes its own configuration, mounts this tree at /kario (KASRUNTIME) and
# leaves SKUYML to its configuration's includes.
KASFILE ?= yocto/kas/phone-qemuarm64.yml
KAS_KARIO ?= /repo
# This tree also at /kario, read-only: the git source of SRC=git builds, the
# same path in both workspaces, so they share bitbake's git mirror of it.
KASRUNTIME ?= -v $(CURDIR):/kario:ro
KASCMD   = $(KASENV) $(KAS) $(if $(KASRUNTIME),--runtime-args "$(KASRUNTIME)")
# The hardware variant built and booted (sku/skus, 24-sku-framework.md);
# its generated kas overlay follows every configuration below.
SKU ?= qemu-t9
SKUYML ?= yocto/kas/generated/sku.yml
# SRC=git: the recipes build one commit (REV, default HEAD) instead of the
# working tree's tarballs, the way releases build (phone-src.bbclass).
# GIT=local fetches it from this checkout (/kario in kas-container), GIT=github from
# github.com/kario-os/kario (the commit must be there). The internal workspace
# writes its own overlay (SRCGEN=).
SRC ?= tarball
REV ?= HEAD
GIT ?= local
SRCYML ?= yocto/kas/generated/src.yml
SRCGEN ?= 1
SRCCONF = $(if $(filter git,$(SRC)),:$(SRCYML))
# KASEXTRA: overlays after these, e.g. yocto/kas/phone-gui.yml (phone-gui
# in the slot, to compare) or yocto/kas/web.yml.
KASEXTRA ?=
KASCONF  = $(KASFILE)$(if $(SKUYML),:$(SKUYML))$(SRCCONF)$(if $(KASEXTRA),:$(KASEXTRA))
KAS_WEB ?= yocto/kas/web.yml
# The pixel renderer of the host targets: phone-paint, the device's;
# RENDERER=gui/target/release/phone-gui (make gui) to compare with phone-gui.
PAINT    := paint/target/release/phone-paint
GUI      := gui/target/release/phone-gui
RENDERER ?= $(PAINT)
SRC_TAR := yocto/meta-phone/recipes-phone/phone-fw/files/phone-fw-src.tar.gz
GUI_TAR := yocto/meta-phone/recipes-phone/phone-gui/files/phone-gui-src.tar.gz
PAINT_TAR := yocto/meta-phone/recipes-phone/phone-paint/files/phone-paint-src.tar.gz
TEL_TAR := yocto/meta-phone/recipes-phone/phone-telephonyd/files/phone-telephonyd-src.tar.gz
CAPI_TAR := yocto/meta-phone/recipes-phone/phone-capi/files/phone-capi-src.tar.gz

.PHONY: all build telephonyd capi lsp run preview selftest test license-check vendor doctor gomods src-conf paint gui gui-selftest skins renderer cts pixel-selftest webview-dev web-selftest yocto-src yocto-build yocto-selftest yocto-gui yocto-web-build yocto-bench yocto-stress yocto-run yocto-vnc sku-check sku-gen sku ota-keys yocto-bundle yocto-ota-test yocto-ota-base yocto-ota-delta yocto-ota-release yocto-cloud-test mm-selftest publish clean

all: build

# telephonyd is C++23: GCC 14 (gcc-toolset-14 on RHEL-family hosts) or newer.
CXX23 ?= $(firstword $(wildcard /opt/rh/gcc-toolset-14/root/usr/bin/g++) g++)
CC14 ?= $(firstword $(wildcard /opt/rh/gcc-toolset-14/root/usr/bin/gcc) gcc)

build: telephonyd capi $(if $(wildcard vendor),vendor/modules.txt)
	$(GO) build -o build/bin/ ./cmd/... ./apps/... ./cts/... ./examples/...
	sh scripts/stage.sh build/bin $(PREFIX)

# CMake writes the compile commands of every C and C++ file; the link next
# to the sources is where clangd looks for them (make lsp).
CMAKE_DB = -DCMAKE_EXPORT_COMPILE_COMMANDS=ON

telephonyd:
	cmake -S telephonyd -B build/telephonyd -G Ninja -DCMAKE_CXX_COMPILER=$(CXX23) $(CMAKE_DB) >/dev/null
	ln -sfn ../build/telephonyd/compile_commands.json telephonyd/compile_commands.json
	ninja -C build/telephonyd
	build/telephonyd/telephonyd_test

# The C ABI SDK (sdk/c) and its C++ sample app.
capi:
	cmake -S sdk/c -B build/capi -G Ninja -DCMAKE_C_COMPILER=$(CC14) -DCMAKE_CXX_COMPILER=$(CXX23) $(CMAKE_DB) >/dev/null
	ln -sfn ../../build/capi/compile_commands.json sdk/c/compile_commands.json
	ninja -C build/capi
	build/capi/phoneui_test

# kario-jsrt (jsrt/), the runtime of TypeScript apps, on QuickJS-ng (MIT):
# a pinned release, fetched into build/ (its sources are not committed) and
# checked against its digest; QJS_CACHE keeps a copy for builds offline.
QJS_VERSION := 0.18.0
QJS_SHA256  := 28bcd89aa3683fd2a946537e5ce3f11cc46b5cbf3383c7561aaef3df5ff9f1dd
QJS_URL     := https://github.com/quickjs-ng/quickjs/archive/refs/tags/v$(QJS_VERSION).tar.gz
QJS_TAR     := build/dl/quickjs-$(QJS_VERSION).tar.gz
QJS_SRC     := build/quickjs-$(QJS_VERSION)
QJS_CACHE   ?= $(HOME)/.cache/kario
.PHONY: jsrt jsrt-if-fetched tsgen

jsrt: $(QJS_SRC)/quickjs.h
	cmake -S jsrt -B build/jsrt -G Ninja -DCMAKE_C_COMPILER=$(CC14) -DQJS_SRC=$(CURDIR)/$(QJS_SRC) $(CMAKE_DB) >/dev/null
	ninja -C build/jsrt

$(QJS_TAR):
	mkdir -p $(dir $@)
	if [ -f $(QJS_CACHE)/$(notdir $@) ]; then cp $(QJS_CACHE)/$(notdir $@) $@.part; else curl -fsSL -o $@.part $(QJS_URL); fi
	echo "$(QJS_SHA256)  $@.part" | sha256sum -c --quiet
	mv $@.part $@
	mkdir -p $(QJS_CACHE) && { [ -f $(QJS_CACHE)/$(notdir $@) ] || cp $@ $(QJS_CACHE)/; }

$(QJS_SRC)/quickjs.h: $(QJS_TAR)
	tar -xzf $(QJS_TAR) -C build
	touch $@

# make build and make test build kario-jsrt when the release is at hand
# (build/dl or QJS_CACHE). Without it and without the network they go on
# without the runtime, and stage.sh without the TypeScript sample.
jsrt-if-fetched:
	@if [ -f $(QJS_TAR) ] || [ -f $(QJS_CACHE)/$(notdir $(QJS_TAR)) ]; then $(MAKE) --no-print-directory jsrt; \
	else echo "kario-jsrt left out: no QuickJS-ng $(QJS_VERSION) in build/dl or $(QJS_CACHE) (make jsrt fetches it); the calculator will not start"; fi
build: jsrt-if-fetched
test: jsrt-if-fetched

# The files generated from the Go sources: @kario/sdk's types (sdk/ts/ui.d.ts)
# and kario-jsrt's languages (jsrt/src/languages.h); make test fails when
# they are stale.
tsgen:
	$(GO) run ./tools/tsgen

# Language servers without a build: clangd gets the compile commands (in
# telephonyd/, sdk/c/ and, merged, at the root); gopls reads go.mod and
# rust-analyzer the Cargo.toml of paint/ and gui/, so they need nothing.
lsp:
	cmake -S telephonyd -B build/telephonyd -G Ninja -DCMAKE_CXX_COMPILER=$(CXX23) $(CMAKE_DB) >/dev/null
	cmake -S sdk/c -B build/capi -G Ninja -DCMAKE_C_COMPILER=$(CC14) -DCMAKE_CXX_COMPILER=$(CXX23) $(CMAKE_DB) >/dev/null
	ln -sfn ../build/telephonyd/compile_commands.json telephonyd/compile_commands.json
	ln -sfn ../../build/capi/compile_commands.json sdk/c/compile_commands.json
	python3 scripts/compile-db.py build/telephonyd build/capi > build/compile_commands.json
	ln -sfn build/compile_commands.json compile_commands.json
	@echo "lsp: compile_commands.json for $$(grep -c '"file"' build/compile_commands.json) C/C++ files; Go and Rust need nothing"

run: build
	$(PREFIX)/bin/phone-shell

# The pixel UI in a browser: http://127.0.0.1:$(PREVIEW_PORT) (over SSH:
# ssh -L $(PREVIEW_PORT):127.0.0.1:$(PREVIEW_PORT) <this machine>). The
# keyboard is the keypad; DEVICE picks the form factor.
PREVIEW_PORT ?= 8790
DEVICE ?= bar-t9
preview: build renderer
	$(PREFIX)/bin/phone-shell --gui $(RENDERER) --device $(DEVICE) --preview 127.0.0.1:$(PREVIEW_PORT)

# ONLY='regexp': only the cases whose name matches (a case may need what an
# earlier one left behind). The JUnit report is for CI's test view.
selftest: build
	$(PREFIX)/bin/phone-shell --selftest --snap-dir build/snapshots --junit build/selftest-junit.xml $(if $(ONLY),--only '$(ONLY)')

# capi: the end-to-end test runs the C++ sample app.
test: telephonyd capi sku-check vendor/modules.txt
	cd paint && cargo test --release && cargo fetch --locked
	python3 scripts/license-check.py
	python3 scripts/const-check.py
	python3 scripts/docs-en-check.py
	python3 scripts/no-chinese.py
	python3 scripts/i18n-check.py
	python3 tools/gomodinc.py --check $(FW_RECIPE) phone-fw
	$(GO) vet -composites=false ./...
	$(GO) test ./...

vendor:
	$(GO) mod tidy
	$(GO) mod vendor

# vendor/ is not in git; the licence check reads its modules.txt, and go
# builds from vendor/ whenever it exists, so an old one is made again.
vendor/modules.txt: go.mod go.sum
	$(GO) mod vendor

# What this machine has and lacks for the targets below.
doctor:
	sh scripts/doctor.sh $(YOCTO)

# The Go modules the phone-fw recipe fetches (gomod://) and their licences,
# after go.mod or go.sum change (needs the network; make test notices when
# they are stale).
FW_RECIPE := yocto/meta-phone/recipes-phone/phone-fw
gomods:
	python3 tools/gomodinc.py $(FW_RECIPE) phone-fw ./cmd/... ./apps/... ./cts/... ./examples/...

# The overlay that switches the recipes to a commit (SRC=git).
src-conf:
ifeq ($(SRC)$(SRCGEN),git1)
	mkdir -p $(dir $(SRCYML))
	printf 'header:\n  version: 14\nlocal_conf_header:\n  zz-src: |\n    PHONE_SRC = "git"\n    PHONE_KARIO_GIT = "%s"\n    PHONE_KARIO_REV = "%s"\n' \
	    '$(if $(filter github,$(GIT)),git://github.com/kario-os/kario.git;protocol=https,git:///kario;protocol=file)' \
	    "$$(git rev-parse --verify '$(REV)^{commit}')" > $(SRCYML)
endif

# Source tarballs of the working tree for the recipes (the layer is mounted
# read-only inside kas-container, so they unpack a tarball instead of using
# externalsrc); the Go modules come from -go-mods.inc, not vendor/.
# Byte-identical tarballs for identical sources (gzip -n: no timestamp in the
# header), so bitbake only rebuilds the recipes whose sources changed.
TAR_REPRO := tar --sort=name --mtime='2026-01-01' --owner=0 --group=0 --numeric-owner --use-compress-program='gzip -n'

yocto-src: sku-gen ota-keys src-conf
	python3 tools/gomodinc.py --check $(FW_RECIPE) phone-fw
	mkdir -p $(dir $(SRC_TAR)) $(dir $(GUI_TAR)) $(dir $(TEL_TAR)) $(dir $(PAINT_TAR))
	$(TAR_REPRO) --exclude=./build --exclude=./yocto --exclude=./gui --exclude=./paint --exclude=./telephonyd --exclude=./docs --exclude=./.git --exclude=./vendor \
	    --transform 's,^\.,kario,' -cf $(SRC_TAR) .
	$(TAR_REPRO) -cf $(TEL_TAR) telephonyd LICENSE
	mkdir -p $(dir $(CAPI_TAR))
	$(TAR_REPRO) --transform 's,^sdk/c,capi,' -cf $(CAPI_TAR) sdk/c LICENSE
	$(TAR_REPRO) --exclude=./target --transform 's,^\.,gui,' -C gui -cf $(GUI_TAR) .
	python3 tools/cratesinc.py gui/Cargo.lock > yocto/meta-phone/recipes-phone/phone-gui/phone-gui-crates.inc
	$(TAR_REPRO) --exclude=paint/target -cf $(PAINT_TAR) paint LICENSE
	python3 tools/cratesinc.py paint/Cargo.lock > yocto/meta-phone/recipes-phone/phone-paint/phone-paint-crates.inc

# phone-gui's community build for the host (headless PNG snapshots, and the
# preview); the device build adds the "kms" feature in its recipe.
gui:
	cd gui && cargo build --release

# phone-paint, the device's renderer (headless here, PNG snapshots).
paint:
	cd paint && cargo build --release

# The renderer the host targets use: phone-paint or phone-gui built here, or
# any other RENDERER names.
renderer:
	$(if $(filter $(PAINT),$(RENDERER)),$(MAKE) paint,$(if $(filter $(GUI),$(RENDERER)),$(MAKE) gui,test -x $(RENDERER)))

# Selftest through the pixel renderer, one PNG per snap step and device.
# Compatibility run: selftest + CTS cases + capability coverage report.
# Copyleft only in the renderer slot (gui/); the platform stays Apache-2.0 (D7).
license-check:
	python3 scripts/license-check.py

cts: build
	$(PREFIX)/bin/phone-shell --cts build/cts-report.json
	$(PREFIX)/bin/phone-cts-verify --root build/cts-report.json.root build/cts-report.json

# The selftest drawn by the pixel renderer on the three form factors.
pixel-selftest: build renderer
	for d in bar-t9 bar-qwerty communicator; do \
	    $(PREFIX)/bin/phone-shell --selftest --gui $(RENDERER) \
	        --device $$d --pixel-dir build/pixel/$$d || exit 1; \
	done

# The same drawn by phone-gui, to compare the two renderers.
gui-selftest:
	$(MAKE) pixel-selftest RENDERER=$(GUI)

# Each snapshot inside a phone body with its keypad (phone-paint draws it),
# for design reviews: build/skins/<form factor>/*.skin.png.
skins: build paint
	for d in bar-t9 bar-qwerty communicator; do \
	    $(PREFIX)/bin/phone-shell --selftest --gui $(PAINT) --skin \
	        --device $$d --pixel-dir build/skins/$$d || exit 1; \
	done

# The pixel selftest with the web view on a development machine without
# WPE packages: phone-webview built and run in a Debian container.
MM_IMAGE := localhost/phone-telephonyd-mm
mm-selftest:
	podman build -q -t $(MM_IMAGE) -f telephonyd/test/Containerfile telephonyd/test >/dev/null
	mkdir -p build/telephonyd-mm
	podman run --rm -v $(CURDIR)/telephonyd:/src:ro,Z -v $(CURDIR)/build/telephonyd-mm:/out:Z $(MM_IMAGE) sh -c \
	    'cmake -S /src -B /out -G Ninja -DTEL_MODEMMANAGER=ON >/dev/null && ninja --quiet -C /out && \
	     /out/telephonyd_test && python3 /src/test/mm_test.py /out/phone-telephonyd'

WEBVIEW_IMAGE := localhost/phone-webview-dev
webview-dev:
	podman build -q -t $(WEBVIEW_IMAGE) -f webview/dev.Containerfile webview >/dev/null
	mkdir -p build/webview
	podman run --rm -v $(CURDIR)/webview:/src:ro,Z -v $(CURDIR)/build/webview:/out:Z $(WEBVIEW_IMAGE) \
	    make -s -C /src O=/out CC=gcc

web-selftest: build renderer webview-dev
	PHONE_WEBVIEW=$(CURDIR)/webview/podman-run.sh $(PREFIX)/bin/phone-shell --selftest \
	    --gui $(RENDERER) --device bar-t9 --pixel-dir build/pixel-web

yocto-build: yocto-src
	$(KASCMD) build $(KASCONF)

# Boots the image in QEMU with phone.selftest=1; the image runs the same
# scenario at boot, prints PHONE-SELFTEST-RESULT and powers off.
yocto-selftest: build sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'sh $(KAS_KARIO)/yocto/qemu-selftest.sh'
	python3 yocto/audio-check.py $(WORK)/build/qemu-audio.wav
	$(PREFIX)/bin/phone-cts-verify --root $(WORK)/build/cts-device.json.root $(WORK)/build/cts-device.json

# Boots with phone.gui=1: the renderer on the virtio-gpu DRM display, keys
# from the QEMU monitor, screendumps under $(WORK)/build/gui-shots.
yocto-gui: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'sh $(KAS_KARIO)/yocto/qemu-gui.sh'

# Semantic-vs-web comparison on the device (adds WPE WebKit + cog, see docs/15).
yocto-web-build:
	$(MAKE) yocto-build KASEXTRA=$(KAS_WEB)

yocto-bench: sku-gen src-conf
	$(KASCMD) shell $(KASFILE)$(if $(SKUYML),:$(SKUYML))$(SRCCONF):$(KAS_WEB) -c 'sh $(KAS_KARIO)/yocto/qemu-bench.sh'

# The low-memory stress run: the web image on a 1 GB SKU, rounds of SMS,
# chat, photos, a video and the browser over it with music in the
# background; every step in its time, the renderer and the system services
# never lost, the kernel's OOM killer never needed (stress-boot.sh).
STRESS_SKU ?= qemu-t9-1g
STRESS_ROUNDS ?= 5
yocto-stress:
	$(MAKE) yocto-build SKU=$(STRESS_SKU) KASEXTRA=$(KAS_WEB)
	$(KASCMD) shell $(KASFILE)$(if $(SKUYML),:$(SKUYML))$(SRCCONF):$(KAS_WEB) \
	    -c 'PHONE_STRESS_ROUNDS=$(STRESS_ROUNDS) sh $(KAS_KARIO)/yocto/qemu-stress.sh'

yocto-run: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'runqemu qemuarm64 nographic slirp snapshot'

# The SKUs: check them all (part of make test), write one's files, build
# one's firmware package (make sku SKU=kario-t9-sd662-2g): its image, then
# build/sku/<sku>/firmware/ with a manifest.
sku-check:
	PHONE_KERNEL_SRC=$(WORK)/build/tmp/work-shared/qemuarm64/kernel-source python3 tools/sku.py check
	python3 -m unittest -q tools/sku_test.py tools/slots_test.py

sku-gen:
	python3 tools/sku.py gen $(SKU)

# Every SKU in sku/skus is a target of its own: make sku-<name> builds its
# firmware package (make sku SKU=<name>); make skus lists them.
SKUS := $(sort $(basename $(notdir $(wildcard sku/skus/*.toml))))
SKU_TARGETS := $(addprefix sku-,$(SKUS))
.PHONY: skus $(SKU_TARGETS)
skus:
	@python3 tools/sku.py list | sed 's/^/sku-/'
$(SKU_TARGETS): sku-%:
	$(MAKE) sku SKU=$*

# System updates (25-system-update.md): development keys outside git, and
# the A/B update test on QEMU (eleven boots: held back by a staged rollout
# and a pause, refuse, install adaptively, mark good with the shell and
# renderer up, install back, three boots whose renderer cannot start, roll
# back, then a tampered slot and a tampered descriptor).
OTAKEYS ?= $(WORK)/ota-keys
ota-keys:
	sh yocto/ota-keys.sh $(OTAKEYS)

# phone-ota-delta (25-system-update.md §5): for this machine, where it makes
# incremental payloads, and for the phones, inside each delta bundle.
OTA_GOARCH ?= arm64
.PHONY: ota-delta
ota-delta:
	CGO_ENABLED=0 $(GO) build -trimpath -o build/bin/ ./cmd/phone-ota-delta
	CGO_ENABLED=0 GOOS=linux GOARCH=$(OTA_GOARCH) $(GO) build -trimpath -ldflags='-s -w' -o build/ota/linux-$(OTA_GOARCH)/ ./cmd/phone-ota-delta

# The signed update bundle of the image (phone-bundle, and the one signed by
# an untrusted key for the update test). OTA_DELTA_FROM="<label>[@<formats>]=<image> ...":
# also a delta bundle from each earlier slot image or A/B disk (paths in the
# container) into OTA_DELTA_OUT, <label>.raucb with the generator's report;
# without the applier where <formats> (that system's own) has the payload's.
OTA_DELTA_FROM ?=
OTA_DELTA_OUT ?= /work/build/ota-deltas
yocto-bundle: sku-gen src-conf $(if $(OTA_DELTA_FROM),ota-delta)
	$(KASCMD) shell $(KASCONF) -c 'bitbake phone-bundle$(if $(OTA_DELTA_FROM), && sh $(KAS_KARIO)/yocto/qemu-ota-delta.sh bundle $(OTA_DELTA_OUT) $(OTA_DELTA_FROM))'

# OTA_SERVER and OTA_ROLLOUT (paths in the container): an update server and
# a staged-rollout tool to run against instead of checking the reports alone.
yocto-ota-test: build sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'bitbake phone-bundle phone-bundle-untrusted && OTA_SERVER=$(OTA_SERVER) OTA_ROLLOUT=$(OTA_ROLLOUT) sh $(KAS_KARIO)/yocto/qemu-ota.sh'

# An update across versions: yocto-ota-base keeps this build (A/B disk, slot
# image, file list, its applier's payload formats) as the base; after changes
# and a rebuild, yocto-ota-delta installs this build on copies of the base
# from a delta bundle made against the base (with a rollback, an update from
# B back to A, a power cut while it is written, a damaged delta falling back
# to the full bundle) and adaptively from the full bundle, and reports the
# bytes each downloaded against the blocks and files that changed.
yocto-ota-base: ota-delta sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'sh $(KAS_KARIO)/yocto/qemu-ota-delta.sh save'

yocto-ota-delta: build ota-delta sku-gen src-conf yocto-src
	$(KASCMD) shell $(KASCONF) -c 'bitbake phone-bundle phone-bundle-untrusted && sh $(KAS_KARIO)/yocto/qemu-ota-delta.sh run'

# Earlier releases' A/B disks updated through Settings to a later release,
# from its own latest.json and bundles (paths inside kas-container, e.g.
# OTA_RELEASE="/repo/dist/v0.7.1 /repo/dist/v0.7.0 /repo/dist/v0.6.0" from
# kario-internal: the new one first).
yocto-ota-release: sku-gen src-conf
	@test -n "$(OTA_RELEASE)" || { echo "make yocto-ota-release OTA_RELEASE=\"<new dist dir> <old dist dir>...\""; exit 1; }
	$(KASCMD) shell $(KASCONF) -c 'bitbake phone-bundle-untrusted && sh $(KAS_KARIO)/yocto/qemu-ota-delta.sh release $(OTA_RELEASE)'

# The phone against cloud services (R42): the store, push gateway and update
# server run in the build container, the image is built naming them
# (phone-services) and boots its cloud test; then it is built without them.
yocto-cloud-test: build sku-gen src-conf yocto-src
	$(KASCMD) shell $(KASCONF) -c 'sh $(KAS_KARIO)/yocto/qemu-cloud.sh'

sku: yocto-build
	python3 tools/sku.py package $(SKU) $(WORK)/build/tmp/deploy/images

# Try the image by hand: it boots into the pixel UI on QEMU's display,
# served over VNC on 127.0.0.1:5900 of this machine (forward the port to
# yours); the PC keyboard is the keypad (13 §4.1). Changes are not kept.
yocto-vnc: sku-gen src-conf
	$(KASENV) $(KAS) --runtime-args "--publish 127.0.0.1:5900:5900 $(KASRUNTIME)" shell $(KASCONF) \
	    -c 'runqemu qemuarm64 publicvnc serialstdio slirp snapshot bootparams="phone.gui=1"'

# Regenerate the embedded Mermaid renderer (lovely-mermaid, Apache-2.0).
mermaid-bundle:
	cd build && npm init -y >/dev/null && npm i lovely-mermaid@0.3.3 && \
	npx -y esbuild@0.25.0 node_modules/lovely-mermaid/dist/index.js --bundle --format=iife \
	    --global-name=LovelyMermaid --target=es2017 --minify --legal-comments=eof \
	    --outfile=../shell/mermaid/lovely-mermaid.iife.js

# This repository as its one commit, pushed over the remote's
# (scripts/publish.sh).
publish:
	sh scripts/publish.sh "Kario, a keyboard-first phone OS: shell, services, renderer, SDK, apps, Yocto layer, docs"

clean:
	rm -rf build
