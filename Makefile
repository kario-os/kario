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
# Kario's patch to it (MIT, as QuickJS-ng): the debugger's hooks; Yocto's
# quickjs recipe applies the same file.
QJS_PATCH   := yocto/meta-phone/recipes-phone/quickjs/files/kario-debugger.patch
.PHONY: jsrt tsgen

jsrt: $(QJS_SRC)/quickjs.h
	cmake -S jsrt -B build/jsrt -G Ninja -DCMAKE_C_COMPILER=$(CC14) -DQJS_SRC=$(CURDIR)/$(QJS_SRC) $(CMAKE_DB) >/dev/null
	ninja -C build/jsrt

$(QJS_TAR):
	mkdir -p $(dir $@)
	if [ -f $(QJS_CACHE)/$(notdir $@) ]; then cp $(QJS_CACHE)/$(notdir $@) $@.part; else curl -fsSL -o $@.part $(QJS_URL); fi
	echo "$(QJS_SHA256)  $@.part" | sha256sum -c --quiet
	mv $@.part $@
	mkdir -p $(QJS_CACHE) && { [ -f $(QJS_CACHE)/$(notdir $@) ] || cp $@ $(QJS_CACHE)/; }

$(QJS_SRC)/quickjs.h: $(QJS_TAR) $(QJS_PATCH)
	rm -rf $(QJS_SRC)
	tar -xzf $(QJS_TAR) -C build
	patch -s -d $(QJS_SRC) -p1 < $(QJS_PATCH)
	touch $@

# make build and make test build kario-jsrt, fetching the release if it is
# in neither build/dl nor QJS_CACHE: the calculator, a system app, is
# TypeScript and does not start without it.
build: jsrt
test: jsrt

# The native module of the sample examples/imgproc (22-developer-api.md
# §9.3), built for this machine into build/native/<arch>/: stage.sh puts it
# into the sample's package source, which the selftest installs; kario run
# -native build/native examples/imgproc runs the sample here.
NATIVE_ARCH := $(patsubst aarch64,arm64,$(filter x86_64 aarch64,$(shell uname -m)))
.PHONY: native-example
native-example:
	$(if $(NATIVE_ARCH),mkdir -p build/native/$(NATIVE_ARCH) && $(CC14) -shared -fPIC -O2 -Wall -Wextra -Wno-unused-parameter -Isdk/c/include \
	    -o build/native/$(NATIVE_ARCH)/imgproc.so examples/imgproc/native/imgproc.c,@echo "native-example: no native modules on $$(uname -m)")
build: native-example
test: native-example

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

# kario on PATH for writing apps (docs/35-quickstart.md): a link to this
# tree's build/bin/kario, which finds the Shell and phone-paint from where it
# really is (a copy would not). BINDIR has to be on PATH.
BINDIR ?= $(HOME)/.local/bin
.PHONY: install-kario
install-kario: build paint
	mkdir -p $(BINDIR)
	ln -sfn $(CURDIR)/build/bin/kario $(BINDIR)/kario
	@case ":$$PATH:" in *":$(BINDIR):"*) echo "install-kario: $(BINDIR)/kario -> $(CURDIR)/build/bin/kario";; \
	    *) echo "install-kario: $(BINDIR)/kario -> $(CURDIR)/build/bin/kario; $(BINDIR) is not on PATH: export PATH=$(BINDIR):\$$PATH";; esac

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

# capi: the end-to-end test runs the C++ sample app. The shell's scenario
# runs its parts at once, each a stack of processes, so it runs after the
# other packages: beside it their timed tests starve on a few cores.
test: telephonyd capi sku-check vendor/modules.txt
	cd paint && cargo test --release && cargo fetch --locked
	python3 scripts/license-check.py
	python3 scripts/const-check.py
	python3 scripts/docs-en-check.py
	python3 scripts/docs-links.py docs
	python3 scripts/no-chinese.py
	python3 scripts/i18n-check.py
	cd scripts && python3 -m unittest -q ci_plan_test ai_review_test yocto_check_test
	python3 yocto/abslot-test.py
	python3 yocto/fajita-flash-test.py
	python3 tools/gomodinc.py --check $(FW_RECIPE) phone-fw
	$(GO) vet -composites=false ./...
	$(GO) test -timeout 30m $$($(GO) list ./... | grep -vx 'github.com/kario-os/kario/shell')
	$(GO) test -timeout 30m ./shell

# Fuzzing of the parsers of untrusted input (17-app-sandbox.md §9): each
# target for FUZZTIME with FUZZPAR workers, one after the other; ONLY='regexp'
# runs the targets whose package:name matches. make test runs their seeds and
# the inputs that failed once (testdata/fuzz). The C and C++ harnesses are
# built with the address and undefined behaviour sanitizers into FUZZBIN.
FUZZTIME ?= 30s
FUZZPAR ?= 8
FUZZBIN ?= /tmp/fuzz/bin
FUZZ_SAN := -g -O1 -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer
FUZZ_TARGETS := \
	./usbshare/mtp:FuzzResponder ./usbshare/mtp:FuzzControl ./usbshare/mtp:FuzzObjectInfo \
	./usbshare:FuzzServer ./usbshare:FuzzHelper ./usbshare:FuzzCheckName \
	./obex:FuzzServe ./obex:FuzzClient ./obex:FuzzDecode ./obex:FuzzParams ./obex:FuzzOldPhone ./obex:FuzzParsers ./contacts:FuzzParse \
	./contacts:FuzzPatchCard ./carddav:FuzzMultistatus ./smsbackup:FuzzParse \
	./hal/nfc:FuzzNCI ./hal/nfc:FuzzPacket ./hal/nfc:FuzzActivation ./hal/nfc:FuzzCard \
	./programs/smsd:FuzzFindCode ./mms:FuzzPush ./mms:FuzzPDU ./phonenum:FuzzParse ./ui:FuzzWAC ./location:FuzzDecode ./vpn:FuzzParse \
	./wire:FuzzRecv ./shell:FuzzScreen ./shell:FuzzDoc ./shell:FuzzCanvas ./shell:FuzzMermaid \
	./appstore:FuzzManifest ./apps/miniapp:FuzzProgram ./apps/calendar:FuzzICS ./tools/cfuzz:FuzzPhoneUI \
	./pkg:FuzzOpen ./pkg:FuzzSignedLists ./pkg:FuzzIDs \
	./ota:FuzzHolidays ./ota/verity:FuzzDescriptor ./ota/delta:FuzzManifest ./ota/delta:FuzzSlot \
	./ota/delta/bsdiff:FuzzApply ./ota/delta/bsdiff:FuzzApplyPiece ./ota/delta/bsdiff:FuzzMake ./ota/delta/xz:FuzzDecode ./ota/delta/ext4:FuzzImage \
	./ota/delta/squashfs:FuzzImage \
	./keystore:FuzzRoots ./keystore:FuzzVerify ./gm:FuzzSM2 ./push:FuzzSealed ./push:FuzzToken ./switchrecv:FuzzEnvelope \
	./tools/cfuzz:FuzzTelephonydJSON ./tools/cfuzz:FuzzWebSocketFrames \
	./audio/musicfile:FuzzProbe ./audio/tags:FuzzID3v2 ./audio/tags:FuzzVorbisComment ./audio/tags:FuzzTail \
	./audio/flac:FuzzDecoder ./audio/cue:FuzzParse ./audio/dsd:FuzzFile ./audio:FuzzPlay \
	./audio/voice:FuzzDecode ./audio/internal/textenc:FuzzDecode ./media/qrscan:FuzzDecode
.PHONY: fuzz fuzz-harnesses
fuzz-harnesses:
	mkdir -p $(FUZZBIN)
	$(CC14) -std=c11 $(FUZZ_SAN) -Isdk/c -o $(FUZZBIN)/phoneui-lines sdk/c/phoneui.c sdk/c/fuzz_lines.c
	$(CXX23) -std=c++23 $(FUZZ_SAN) -Itelephonyd/src -o $(FUZZBIN)/telephonyd-json telephonyd/src/json.cpp telephonyd/test/json_lines.cpp
	$(CC14) -std=c11 $(FUZZ_SAN) -o $(FUZZBIN)/ws-frames jsrt/src/wsframe.c jsrt/src/fuzz_ws.c

fuzz: fuzz-harnesses
	@for t in $(FUZZ_TARGETS); do \
	    echo "$$t" | grep -Eq '$(ONLY)' || continue; \
	    echo "fuzz: $$t for $(FUZZTIME)"; \
	    KARIO_CFUZZ_BIN=$(FUZZBIN) $(GO) test -run='^$$' -fuzz="^$${t#*:}\$$" -fuzztime=$(FUZZTIME) \
	        -fuzzminimizetime=10s -parallel=$(FUZZPAR) "$${t%%:*}" || exit 1; \
	done

# The same C and C++ harnesses as libFuzzer targets (clang), which follow
# the coverage of the code instead of mutating blindly: each for FUZZTIME
# (s, m or h) from its seeds (tools/cfuzz/testdata/libfuzzer) and the
# corpus it grew in FUZZCORPUS; what fails lands in FUZZOUT. make fuzz-list
# prints both kinds of target, ONLY applying (the fuzz workflow's jobs).
LF_CC ?= clang
LF_CXX ?= clang++
LF_SAN := -g -O1 -fsanitize=fuzzer,address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer -DKARIO_LIBFUZZER
FUZZCORPUS ?= /tmp/fuzz/corpus
FUZZOUT ?= /tmp/fuzz/out
LF_TARGETS := libfuzzer:phoneui-lines libfuzzer:telephonyd-json libfuzzer:ws-frames
.PHONY: fuzz-list fuzz-libfuzzer fuzz-libfuzzer-harnesses
fuzz-list:
	@for t in $(FUZZ_TARGETS) $(LF_TARGETS); do echo "$$t" | grep -E '$(ONLY)' || true; done

fuzz-libfuzzer-harnesses:
	mkdir -p $(FUZZBIN)
	$(LF_CC) -std=c11 $(LF_SAN) -Isdk/c -o $(FUZZBIN)/lf-phoneui-lines sdk/c/phoneui.c sdk/c/fuzz_lines.c -lpthread
	$(LF_CXX) -std=c++23 $(LF_SAN) -Itelephonyd/src -o $(FUZZBIN)/lf-telephonyd-json telephonyd/src/json.cpp telephonyd/test/json_lines.cpp
	$(LF_CC) -std=c11 $(LF_SAN) -o $(FUZZBIN)/lf-ws-frames jsrt/src/wsframe.c jsrt/src/fuzz_ws.c

# Leaks count as the Go driver counts them (tools/cfuzz): in the frame reader.
fuzz-libfuzzer: fuzz-libfuzzer-harnesses
	@secs=$$(echo '$(FUZZTIME)' | sed -n 's/^\([0-9][0-9]*\)s$$/\1/p; s/^\([0-9][0-9]*\)m$$/\1*60/p; s/^\([0-9][0-9]*\)h$$/\1*3600/p'); \
	[ -n "$$secs" ] || { echo "fuzz-libfuzzer: FUZZTIME is seconds, minutes or hours (30s, 10m, 1h)" >&2; exit 2; }; \
	for t in $(LF_TARGETS); do \
	    echo "$$t" | grep -Eq '$(ONLY)' || continue; \
	    n=$${t#libfuzzer:}; leaks=0; [ "$$n" = ws-frames ] && leaks=1; \
	    mkdir -p $(FUZZCORPUS)/$$n $(FUZZOUT); \
	    echo "fuzz: $$t for $(FUZZTIME)"; \
	    $(FUZZBIN)/lf-$$n -max_total_time=$$(($$secs)) -max_len=65536 -timeout=10 -detect_leaks=$$leaks \
	        -artifact_prefix=$(FUZZOUT)/$$n- -print_final_stats=1 $(FUZZCORPUS)/$$n tools/cfuzz/testdata/libfuzzer/$$n || exit 1; \
	done
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

# The soak run (22-developer-api.md §8.1): rounds of shell/soak.script on
# the full stack for SOAK, the Shell's descriptors, goroutines, heap and RSS
# and each system service's descriptors and RSS sampled after every round
# into SOAK_OUT (CSV); sustained growth past the limits in shell/soak_test.go
# fails it.
SOAK ?= 30m
SOAK_OUT ?= $(CURDIR)/build/soak.csv
.PHONY: soak compat-old
soak: telephonyd jsrt
	mkdir -p $(dir $(SOAK_OUT))
	PHONE_SOAK=$(SOAK) PHONE_SOAK_OUT=$(SOAK_OUT) $(GO) test ./shell -run '^TestSoak$$' -count=1 -timeout 0 -v

# The app binaries of an older commit (OLD_REV, a release) against this
# tree's Shell and services: each must start, show a page and take keys
# without a malformed or unknown message (22-developer-api.md §2.1). The
# old sources and binaries go to OLD_DIR.
OLD_REV ?=
OLD_DIR ?= /tmp/kario-compat-old
compat-old: telephonyd jsrt
	@test -n "$(OLD_REV)" || { echo "make compat-old OLD_REV=<commit or tag of this repository>"; exit 1; }
	rm -rf $(OLD_DIR) && mkdir -p $(OLD_DIR)/src $(OLD_DIR)/bin
	git archive "$(OLD_REV)" | tar -x -C $(OLD_DIR)/src
	cd $(OLD_DIR)/src && $(GO) build -o $(OLD_DIR)/bin/ ./apps/...
	PHONE_OLD_APPS=$(OLD_DIR) $(GO) test ./shell -run '^TestOldApps$$' -count=1 -timeout 30m -v

# The selftest drawn by the pixel renderer on the three form factors; at
# the end, how long phone-paint took a step of the live display (a report,
# never a failure; docs/37-motion.md §5). ONLY='regexp': only those cases,
# as make selftest's.
pixel-selftest: build renderer
	for d in bar-t9 bar-qwerty communicator; do \
	    PHONE_PAINT_STEPS=$(CURDIR)/build/pixel/$$d/motion.jsonl \
	    $(PREFIX)/bin/phone-shell --selftest --gui $(RENDERER) \
	        --device $$d --pixel-dir build/pixel/$$d $(if $(ONLY),--only '$(ONLY)') || exit 1; \
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

# The do and don't pictures of the app guidelines (docs/33-app-guidelines.md
# §1.3): examples/guidelines drawn by phone-paint, small, in docs/img/33/.
.PHONY: guideline-shots
guideline-shots: build paint
	PREFIX=$(abspath $(PREFIX)) PAINT=$(abspath $(PAINT)) sh scripts/guideline-shots.sh

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
# ONLY='regexp' (development only, as make selftest's): only those cases,
# hex on the kernel command line; without the whole run there is no sound,
# compatibility report or fd store check to verify. SELFTEST_ENV='NAME=value
# ...' (development only): set for phone-shell in the image, as a case's
# require-env wants.
yocto-selftest: build sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c '$(if $(ONLY),PHONE_SELFTEST_ONLY=$(shell printf %s '$(ONLY)' | od -An -tx1 | tr -d ' \n') )$(if $(SELFTEST_ENV),PHONE_SELFTEST_ENV=$(shell printf %s '$(SELFTEST_ENV)' | od -An -tx1 | tr -d ' \n') )sh $(KAS_KARIO)/yocto/qemu-selftest.sh'
	$(if $(ONLY),,python3 yocto/audio-check.py $(WORK)/build/qemu-audio.wav)
	$(if $(ONLY),,$(PREFIX)/bin/phone-cts-verify --root $(WORK)/build/cts-device.json.root $(WORK)/build/cts-device.json)

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

# The soak run on the device: SOAK_ROUNDS rounds of shell/soak.script in
# one Shell with the pixel renderer, the Shell's and its services' memory
# and descriptors sampled every 30 seconds; a failed round, a restarted
# service or sustained growth fails it (soak-boot.sh).
SOAK_ROUNDS ?= 20
.PHONY: yocto-soak
yocto-soak:
	$(MAKE) yocto-build
	$(KASCMD) shell $(KASCONF) -c 'PHONE_SOAK_ROUNDS=$(SOAK_ROUNDS) sh $(KAS_KARIO)/yocto/qemu-soak.sh'

yocto-run: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'runqemu qemuarm64 nographic slirp snapshot'

# The SKUs: check them all (part of make test), write one's files, build
# one's firmware package (make sku SKU=kario-t9-sd662-2g): its image, then
# build/sku/<sku>/firmware/ with a manifest.
sku-check:
	PHONE_KERNEL_SRC=$(WORK)/build/tmp/work-shared/qemuarm64/kernel-source python3 tools/sku.py check
	python3 -m unittest -q tools/sku_test.py tools/slots_test.py tools/phone_chroot_test.py

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
