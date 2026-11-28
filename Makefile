GO      ?= go
PREFIX  := build/usr
KAS     ?= $(firstword $(wildcard $(HOME)/.local/kas-venv/bin/kas-container) $(shell command -v kas-container 2>/dev/null) $(HOME)/.local/kas-venv/bin/kas-container)
KAS_CONTAINER_ENGINE ?= $(if $(shell command -v podman 2>/dev/null),podman,docker)
YOCTO   ?= $(HOME)/yocto
# The Yocto build directory, its sources and the update keys (ota-keys/).
WORK    ?= $(YOCTO)/kario-work
# How many bitbake tasks run at once, and make jobs in each: one per
# processor, at most one per 2 GiB of memory, a control group's limit
# counted (scripts/yocto-jobs.py); YOCTO_JOBS= sets both. kas passes the
# two to bitbake, and the kas files leave them alone.
YOCTO_JOBS ?= $(shell python3 scripts/yocto-jobs.py)
BB_NUMBER_THREADS ?= $(YOCTO_JOBS)
PARALLEL_MAKE ?= -j $(YOCTO_JOBS)
KASENV   = KAS_CONTAINER_ENGINE=$(KAS_CONTAINER_ENGINE) KAS_WORK_DIR=$(WORK) \
           DL_DIR=$(YOCTO)/cache/downloads SSTATE_DIR=$(YOCTO)/cache/sstate \
           BB_NUMBER_THREADS=$(BB_NUMBER_THREADS) PARALLEL_MAKE='$(PARALLEL_MAKE)'
# What kas builds, and where this tree is inside kas-container: /repo when
# the configuration is this tree's; the internal workspace (kario-internal)
# passes its own configuration, mounts this tree at /kario (KASRUNTIME) and
# leaves SKUYML to its configuration's includes.
KASFILE ?= yocto/kas/phone-qemuarm64.yml
KAS_KARIO ?= /repo
# This tree also at /kario, read-only: the git source of SRC=git builds, the
# same path in both workspaces, so they share bitbake's git mirror of it.
KASRUNTIME ?= -v $(CURDIR):/kario:ro
# kario-bsp, the public BSP layers (24-sku-framework.md §8.7.6): builds take
# them at the commit yocto/kas/phone-fajita.yml pins (repos.kario-bsp.commit,
# one line). make test and CI fetch that commit into build/kario-bsp for
# tools/sku.py's machine checks and the tests reading its recipes, and fail
# when they cannot; so does a build's sku-gen for a SKU whose machine is
# there, when it is not fetched yet. KARIO_BSP=../kario-bsp: a checkout of
# one's own instead, taken as it is; builds take it with
# KASEXTRA=yocto/kas/local-bsp.yml, mounted at /kario-bsp in kas-container.
KARIO_BSP ?=
BSP_DIR   = $(if $(KARIO_BSP),$(abspath $(KARIO_BSP)),$(CURDIR)/build/kario-bsp)
BSPENV    = KARIO_BSP_LAYERS=$$(python3 $(CURDIR)/scripts/kario-bsp.py layers $(BSP_DIR))
KASMOUNTS = $(KASRUNTIME)$(if $(KARIO_BSP), -v $(BSP_DIR):/kario-bsp:ro)
KASCMD   = $(KASENV) $(KAS) $(if $(strip $(KASMOUNTS)),--runtime-args "$(strip $(KASMOUNTS))")
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

.PHONY: all build telephonyd capi lsp run preview selftest selftest-baselines pixel-baselines test license-check vendor doctor gomods src-conf paint gui gui-selftest skins renderer cts pixel-selftest webview-dev web-selftest yocto-src yocto-build yocto-selftest yocto-gui yocto-gui-gpu yocto-web-build yocto-bench yocto-stress yocto-run yocto-vnc sku-check bsp-fetch sku-gen sku ota-keys yocto-bundle yocto-ota-test yocto-userdata-test yocto-charger-test yocto-ota-base yocto-ota-delta yocto-ota-release yocto-cloud-test mm-selftest publish clean

all: build

# telephonyd is C++23: GCC 14 (gcc-toolset-14 on RHEL-family hosts) or newer.
CXX23 ?= $(firstword $(wildcard /opt/rh/gcc-toolset-14/root/usr/bin/g++) $(shell command -v g++-14 2>/dev/null) g++)
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

# The copy in QJS_CACHE is renamed into place: a build ended while it
# copied (a killed test's, docs/40-testing.md) would leave a cut tarball
# that fails the build of every tree.
$(QJS_TAR):
	mkdir -p $(dir $@)
	if [ -f $(QJS_CACHE)/$(notdir $@) ]; then cp $(QJS_CACHE)/$(notdir $@) $@.part; else curl -fsSL -o $@.part $(QJS_URL); fi
	echo "$(QJS_SHA256)  $@.part" | sha256sum -c --quiet
	mv $@.part $@
	mkdir -p $(QJS_CACHE) && { [ -f $(QJS_CACHE)/$(notdir $@) ] || { cp $@ $(QJS_CACHE)/$(notdir $@).$$$$ && mv $(QJS_CACHE)/$(notdir $@).$$$$ $(QJS_CACHE)/$(notdir $@); }; }

$(QJS_SRC)/quickjs.h: $(QJS_TAR) $(QJS_PATCH)
	rm -rf $(QJS_SRC)
	tar -xzf $(QJS_TAR) -C build
	patch -s -d $(QJS_SRC) -p1 < $(QJS_PATCH)
	touch $@

# kario-jsrt and QuickJS-ng built with AddressSanitizer and UBSan, or with
# ThreadSanitizer, each into build/jsrt-<sanitizer>, then every runtime test
# against it (docs/40-testing.md §8). The harness gives each process a
# report file of its own under reports/ there and fails the test whose
# process wrote one, printing it; GCC's runtime symbolizes in the process,
# as the runtime's seccomp filter lets no symbolizer program start. The
# compiler is the first GCC that has the sanitizer's runtime (gcc-toolset-14
# comes without ThreadSanitizer's).
JSRT_SAN_asan := -fsanitize=address,undefined
JSRT_SAN_tsan := -fsanitize=thread
jsrt_san_cc = $(firstword $(foreach c,$(CC14) $(wildcard /opt/rh/gcc-toolset-*/root/usr/bin/gcc) gcc,$(if $(filter /%,$(shell $(c) -print-file-name=lib$(1).so 2>/dev/null)),$(c))) $(CC14))
.PHONY: jsrt-asan jsrt-tsan
jsrt-asan jsrt-tsan: jsrt-%: $(QJS_SRC)/quickjs.h $(if $(wildcard vendor),vendor/modules.txt)
	cmake -S jsrt -B build/jsrt-$* -G Ninja -DCMAKE_C_COMPILER=$(call jsrt_san_cc,$*) -DQJS_SRC=$(CURDIR)/$(QJS_SRC) -DCMAKE_BUILD_TYPE=RelWithDebInfo \
	    -DCMAKE_C_FLAGS='$(JSRT_SAN_$*) -fno-omit-frame-pointer' -DCMAKE_EXE_LINKER_FLAGS='$(JSRT_SAN_$*)' >/dev/null
	ninja -C build/jsrt-$*
	rm -rf build/jsrt-$*/reports && mkdir -p build/jsrt-$*/reports
	KARIO_JSRT=$(CURDIR)/build/jsrt-$*/kario-jsrt KARIO_JSRT_REQUIRED=1 KARIO_JSRT_SANITIZER=$(CURDIR)/build/jsrt-$*/reports \
	    $(GO) test -count=1 -timeout 30m ./jsrt

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

# The Go tests that run phone-paint or make build's prefix skip when it is
# missing or older than its sources (tools/prebuilt): make test builds both
# first, and those tests fail instead (KARIO_PREBUILT_REQUIRED).
test: paint build

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
# earlier one left behind). The JUnit report is for CI's test view. The
# snapshots are compared with BASELINES (docs/40-testing.md §2); UPDATE=1
# (make selftest-baselines, make pixel-baselines) records them instead.
# On a machine of 16 processors or more the scenario's parts run at once,
# each in a phone-shell of its own from the boot (phone-shell --parallel,
# as make cts runs them; ONLY: the parts with a case it names). A part's
# snapshots show what its own cases did only, so they are the same as in
# the whole script in one Shell, in order, which SELFTEST_PARTS=0 runs and
# UPDATE=1 always records.
BASELINES := shell/testdata/snapshots
SELFTEST_PARTS ?= $(shell [ "$$(nproc)" -ge 16 ] && echo 1 || echo 0)
selftest: build
	$(PREFIX)/bin/phone-shell --selftest $(if $(UPDATE),,$(if $(filter 1,$(SELFTEST_PARTS)),--parallel)) \
	    --snap-dir build/snapshots --baselines $(BASELINES) $(if $(UPDATE),--update-baselines) \
	    --junit build/selftest-junit.xml $(if $(ONLY),--only '$(ONLY)')

selftest-baselines:
	$(MAKE) selftest UPDATE=1

# capi: the end-to-end test runs the C++ sample app. The shell's scenario
# runs its parts at once, each a stack of processes, so it runs after the
# other packages: beside it their timed tests starve on a few cores.
test: telephonyd capi sku-check vendor/modules.txt
	cd paint && cargo test --profile checked && cargo fetch --locked
	python3 scripts/license-check.py
	python3 scripts/const-check.py
	python3 scripts/docs-en-check.py
	python3 scripts/docs-links.py docs
	python3 scripts/no-chinese.py
	python3 scripts/i18n-check.py
	python3 scripts/binary-check.py
	cd scripts && python3 -m unittest -q ci_plan_test ai_review_test yocto_check_test yocto_jobs_test doctor_test coverage_test binary_check_test docs_links_test kario_bsp_test
	python3 yocto/bringup-awake-test.py
	$(BSPENV) python3 yocto/charger-generator-test.py
	$(BSPENV) python3 yocto/boot-console-test.py
	python3 yocto/fajita-flash-test.py
	python3 yocto/modsig-test.py
	$(BSPENV) python3 yocto/svcusers-test.py
	python3 yocto/gpu-check-test.py
	$(BSPENV) python3 yocto/initramfs-firmware-test.py
	python3 yocto/verity-pad-test.py
	python3 yocto/qemu-ota-delta-test.py
	python3 -m unittest -q tools/gomodinc_test.py
	python3 yocto/gui-compare-test.py
	python3 yocto/qemu-gui-drive-test.py
	python3 yocto/selftest-plan-test.py
	python3 yocto/qemu-buttons-test.py
	python3 yocto/qemu-console-test.py
	python3 tools/gomodinc.py --check $(FW_RECIPE) phone-fw
	$(GO) vet -composites=false ./...
	KARIO_PREBUILT_REQUIRED=1 $(GO) test -timeout 30m $$($(GO) list ./... | grep -vx 'github.com/kario-os/kario/shell')
	KARIO_PREBUILT_REQUIRED=1 $(GO) test -timeout 30m ./shell

# Code coverage (docs/40-testing.md §7, scripts/coverage.py): the Go unit
# tests and the text selftest on the system built with -cover, merged;
# phone-paint's tests; telephonyd's and the C SDK's; the JavaScript run
# under Node.js. A part whose tools are missing is skipped, saying why.
# PARTS='go-e2e rust': only those, the report keeping the others' last
# results; ONLY='regexp': only those selftest cases. It runs the selftest
# and go test ./shell: under /tmp/kario-selftest.lock as they are (§5).
# The report: COVERAGE_OUT/summary.txt and report/index.html.
COVERAGE_OUT ?= build/coverage
.PHONY: coverage
coverage: telephonyd capi jsrt native-example $(if $(wildcard vendor),vendor/modules.txt)
	GO='$(GO)' CXX23='$(CXX23)' python3 scripts/coverage.py --out $(COVERAGE_OUT) \
	    $(if $(PARTS),--parts '$(PARTS)') $(if $(ONLY),--only '$(ONLY)')

# Fuzzing of the parsers of untrusted input (17-app-sandbox.md §9): each
# target for FUZZTIME with FUZZPAR workers, one after the other; ONLY='regexp'
# runs the targets whose package:name matches. make test runs their seeds and
# the inputs that failed once (testdata/fuzz). The C and C++ harnesses are
# built with the address and undefined behaviour sanitizers into FUZZBIN.
FUZZTIME ?= 30s
FUZZPAR ?= 8
FUZZBIN ?= /tmp/fuzz/bin
FUZZ_SAN := -g -O1 -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer
TELEPHONYD_RPC_SRC := $(addprefix telephonyd/src/,json.cpp loop.cpp apn.cpp ims.cpp modem.cpp service.cpp server.cpp statefile.cpp) telephonyd/test/rpc_lines.cpp
FUZZ_TARGETS := \
	./usbshare/mtp:FuzzResponder ./usbshare/mtp:FuzzControl ./usbshare/mtp:FuzzObjectInfo \
	./usbshare/mtp:FuzzPropList ./usbshare/mtp:FuzzPlaylist \
	./usbshare:FuzzServer ./usbshare:FuzzHelper ./usbshare:FuzzCheckName \
	./obex:FuzzServe ./obex:FuzzClient ./obex:FuzzDecode ./obex:FuzzParams ./obex:FuzzOldPhone ./obex:FuzzParsers ./obex:FuzzAuth ./contacts:FuzzParse \
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
	./audio/voice:FuzzDecode ./audio/internal/textenc:FuzzDecode ./media/qrscan:FuzzDecode \
	./programs/smsd:FuzzRPC ./programs/imed:FuzzRPC ./programs/displayd:FuzzRPC ./programs/pushd:FuzzRPC \
	./programs/nfcd:FuzzRPC ./programs/keystored:FuzzRPC ./programs/audiod:FuzzRPC ./programs/mediad:FuzzRPC \
	./tools/cfuzz:FuzzTelephonydRPC ./shell:FuzzAppMsg ./tools/cfuzz:FuzzWebSocketAnswer ./tools/cfuzz:FuzzFetchHead \
	./tools/cfuzz:FuzzTextDecoder
.PHONY: fuzz fuzz-harnesses
fuzz-harnesses:
	mkdir -p $(FUZZBIN)
	$(CC14) -std=c11 $(FUZZ_SAN) -Isdk/c -o $(FUZZBIN)/phoneui-lines sdk/c/phoneui.c sdk/c/fuzz_lines.c
	$(CXX23) -std=c++23 $(FUZZ_SAN) -Itelephonyd/src -o $(FUZZBIN)/telephonyd-json telephonyd/src/json.cpp telephonyd/test/json_lines.cpp
	$(CC14) -std=c11 $(FUZZ_SAN) -o $(FUZZBIN)/ws-frames jsrt/src/wsframe.c jsrt/src/fuzz_ws.c
	$(CC14) -std=c11 $(FUZZ_SAN) -o $(FUZZBIN)/ws-answer jsrt/src/wsframe.c jsrt/src/fuzz_ws_answer.c
	$(CC14) -std=c11 $(FUZZ_SAN) -DFETCH_MAX_HEAD=8192 -o $(FUZZBIN)/fetch-head jsrt/src/fetchhead.c jsrt/src/fuzz_fetch_head.c
	$(CC14) -std=c11 $(FUZZ_SAN) -o $(FUZZBIN)/utf8-decode jsrt/src/utf8dec.c jsrt/src/fuzz_utf8.c
	$(CXX23) -std=c++23 $(FUZZ_SAN) -fsanitize=float-cast-overflow -Itelephonyd/src -o $(FUZZBIN)/telephonyd-rpc \
	    $(TELEPHONYD_RPC_SRC)

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
LF_TARGETS := libfuzzer:phoneui-lines libfuzzer:telephonyd-json libfuzzer:ws-frames libfuzzer:telephonyd-rpc libfuzzer:ws-answer \
	libfuzzer:fetch-head libfuzzer:utf8-decode
.PHONY: fuzz-list fuzz-libfuzzer fuzz-libfuzzer-harnesses fuzz-libfuzzer-replay
fuzz-list:
	@for t in $(FUZZ_TARGETS) $(LF_TARGETS); do echo "$$t" | grep -E '$(ONLY)' || true; done

fuzz-libfuzzer-harnesses:
	mkdir -p $(FUZZBIN)
	$(LF_CC) -std=c11 $(LF_SAN) -Isdk/c -o $(FUZZBIN)/lf-phoneui-lines sdk/c/phoneui.c sdk/c/fuzz_lines.c -lpthread
	$(LF_CXX) -std=c++23 $(LF_SAN) -Itelephonyd/src -o $(FUZZBIN)/lf-telephonyd-json telephonyd/src/json.cpp telephonyd/test/json_lines.cpp
	$(LF_CC) -std=c11 $(LF_SAN) -o $(FUZZBIN)/lf-ws-frames jsrt/src/wsframe.c jsrt/src/fuzz_ws.c
	$(LF_CC) -std=c11 $(LF_SAN) -o $(FUZZBIN)/lf-ws-answer jsrt/src/wsframe.c jsrt/src/fuzz_ws_answer.c
	$(LF_CC) -std=c11 $(LF_SAN) -DFETCH_MAX_HEAD=8192 -o $(FUZZBIN)/lf-fetch-head jsrt/src/fetchhead.c jsrt/src/fuzz_fetch_head.c
	$(LF_CC) -std=c11 $(LF_SAN) -o $(FUZZBIN)/lf-utf8-decode jsrt/src/utf8dec.c jsrt/src/fuzz_utf8.c
	$(LF_CXX) -std=c++23 $(LF_SAN) -Itelephonyd/src -o $(FUZZBIN)/lf-telephonyd-rpc $(TELEPHONYD_RPC_SRC)

# Leaks count as the Go driver counts them (tools/cfuzz): in the WebSocket
# reader and handshake check, fetch's response headers, TextDecoder, and
# telephonyd's server (LF_LEAKS sets leaks for target n).
LF_LEAKS = leaks=1; [ "$$n" = phoneui-lines ] || [ "$$n" = telephonyd-json ] && leaks=0
fuzz-libfuzzer: fuzz-libfuzzer-harnesses
	@secs=$$(echo '$(FUZZTIME)' | sed -n 's/^\([0-9][0-9]*\)s$$/\1/p; s/^\([0-9][0-9]*\)m$$/\1*60/p; s/^\([0-9][0-9]*\)h$$/\1*3600/p'); \
	[ -n "$$secs" ] || { echo "fuzz-libfuzzer: FUZZTIME is seconds, minutes or hours (30s, 10m, 1h)" >&2; exit 2; }; \
	for t in $(LF_TARGETS); do \
	    echo "$$t" | grep -Eq '$(ONLY)' || continue; \
	    n=$${t#libfuzzer:}; $(LF_LEAKS); \
	    mkdir -p $(FUZZCORPUS)/$$n $(FUZZOUT); \
	    echo "fuzz: $$t for $(FUZZTIME)"; \
	    $(FUZZBIN)/lf-$$n -max_total_time=$$(($$secs)) -max_len=65536 -timeout=10 -detect_leaks=$$leaks \
	        -artifact_prefix=$(FUZZOUT)/$$n- -print_final_stats=1 $(FUZZCORPUS)/$$n tools/cfuzz/testdata/libfuzzer/$$n || exit 1; \
	done

# Each libFuzzer target once over its committed seeds and nothing more: no
# mutation, no corpus from earlier runs (CI's libfuzzer-replay job). A
# crash, leak or timeout fails it, its input in FUZZOUT; the other targets
# still run.
fuzz-libfuzzer-replay: fuzz-libfuzzer-harnesses
	@failed=; \
	for t in $(LF_TARGETS); do \
	    echo "$$t" | grep -Eq '$(ONLY)' || continue; \
	    n=$${t#libfuzzer:}; $(LF_LEAKS); \
	    mkdir -p $(FUZZOUT); \
	    echo "replay: $$t over tools/cfuzz/testdata/libfuzzer/$$n"; \
	    $(FUZZBIN)/lf-$$n -runs=0 -timeout=10 -detect_leaks=$$leaks -artifact_prefix=$(FUZZOUT)/$$n- \
	        tools/cfuzz/testdata/libfuzzer/$$n || failed="$$failed $$t"; \
	done; \
	[ -z "$$failed" ] || { echo "fuzz-libfuzzer-replay: failed:$$failed (inputs in $(FUZZOUT))" >&2; exit 1; }
vendor:
	$(GO) mod tidy
	$(GO) mod vendor

# vendor/ is not in git; the licence check reads its modules.txt, and go
# builds from vendor/ whenever it exists, so an old one is made again.
vendor/modules.txt: go.mod go.sum
	$(GO) mod vendor

# What this machine has and lacks for the targets below, as they would run
# here; yocto-doctor: the device image's part alone, failing when it misses
# something.
DOCTORENV = GO='$(GO)' CXX23='$(CXX23)' KAS='$(KAS)' KAS_CONTAINER_ENGINE=$(KAS_CONTAINER_ENGINE) WORK='$(WORK)' \
            BB_NUMBER_THREADS=$(BB_NUMBER_THREADS) PARALLEL_MAKE='$(PARALLEL_MAKE)'
.PHONY: yocto-doctor
doctor:
	$(DOCTORENV) sh scripts/doctor.sh $(YOCTO)
yocto-doctor:
	$(DOCTORENV) sh scripts/doctor.sh --image $(YOCTO)

# The Go modules the phone-fw recipe fetches (gomod://) and their licences,
# after go.mod or go.sum change (needs the network and a Go as new as
# go.mod's; make test notices when they are stale, make yocto-src writes
# them again).
FW_RECIPE := yocto/meta-phone/recipes-phone/phone-fw
FW_PKGS := ./cmd/... ./apps/... ./cts/... ./examples/...
gomods:
	GO='$(GO)' python3 tools/gomodinc.py $(FW_RECIPE) phone-fw $(FW_PKGS)

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
# externalsrc); the Go modules come from -go-mods.inc, not vendor/, written
# again first when it is not from the go.mod and go.sum built (SRC=git:
# REV's).
# Byte-identical tarballs for identical sources (gzip -n: no timestamp in the
# header), so bitbake only rebuilds the recipes whose sources changed.
TAR_REPRO := tar --sort=name --mtime='2026-01-01' --owner=0 --group=0 --numeric-owner --use-compress-program='gzip -n'

yocto-src: sku-gen ota-keys src-conf
	GO='$(GO)' python3 tools/gomodinc.py --ensure $(if $(filter git,$(SRC)),--rev '$(REV)') $(FW_RECIPE) phone-fw $(FW_PKGS)
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

# It compares no snapshots, so on a machine of 16 processors or more the
# scenario's parts run at once, each in a phone-shell of its own from the
# boot (phone-shell --parallel, as go test ./shell and the emulators run
# them), each with a report signed by its own device
# (cts-report-<part>.json), checked together; CTS_PARTS=0: the whole script
# in one Shell and one report, the one a certifier takes.
CTS_PARTS ?= $(shell [ "$$(nproc)" -ge 16 ] && echo 1 || echo 0)
cts: build
	rm -f build/cts-report*.json build/cts-report*.json.root
	$(PREFIX)/bin/phone-shell --cts build/cts-report.json $(if $(filter 1,$(CTS_PARTS)),--parallel)
	$(PREFIX)/bin/phone-cts-verify --root-beside build/cts-report*.json

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

# The selftest drawn by the pixel renderer on the three form factors at
# once, each as make selftest's: its parts at once on a machine of 32
# processors or more (three times the stacks, each with its renderer),
# else (PIXEL_PARTS=0, and UPDATE=1) the whole script in one Shell, in
# order; each form factor's output goes to build/pixel/<form factor>.log,
# printed with its result when all three are done. At the end of each, how
# long phone-paint took a step of the live display (a report, never a
# failure; docs/37-motion.md §5). ONLY='regexp': only those cases, as make
# selftest's. The PNGs of the baselines are kept in build/pixel-baseline
# for diff images.
FORM_FACTORS := bar-t9 bar-qwerty communicator
PIXEL_PARTS ?= $(shell [ "$$(nproc)" -ge 32 ] && echo 1 || echo 0)
pixel-selftest: build renderer
	mkdir -p build/pixel; pids=; for d in $(FORM_FACTORS); do \
	    PHONE_PAINT_STEPS=$(CURDIR)/build/pixel/$$d/motion.jsonl \
	    $(PREFIX)/bin/phone-shell --selftest $(if $(UPDATE),,$(if $(filter 1,$(PIXEL_PARTS)),--parallel)) --gui $(RENDERER) \
	        --device $$d --pixel-dir build/pixel/$$d --baselines $(BASELINES) --baseline-images build/pixel-baseline/$$d \
	        $(if $(UPDATE),--update-baselines) $(if $(ONLY),--only '$(ONLY)') > build/pixel/$$d.log 2>&1 & pids="$$pids $$!"; \
	done; status=0; set -- $$pids; for d in $(FORM_FACTORS); do \
	    if wait $$1; then r=PASS; else r=FAIL; status=1; fi; shift; \
	    echo "=== $$d"; cat build/pixel/$$d.log; echo "pixel-selftest: $$d $$r (build/pixel/$$d.log)"; \
	done; exit $$status

pixel-baselines:
	$(MAKE) pixel-selftest UPDATE=1

# The same drawn by phone-gui, to compare the two renderers.
gui-selftest:
	$(MAKE) pixel-selftest RENDERER=$(GUI)

# Each snapshot inside a phone body with its keypad (phone-paint draws it),
# for design reviews: build/skins/<form factor>/*.skin.png, the three form
# factors at once (their output in build/skins/<form factor>.log).
skins: build paint
	mkdir -p build/skins; pids=; for d in $(FORM_FACTORS); do \
	    $(PREFIX)/bin/phone-shell --selftest --gui $(PAINT) --skin \
	        --device $$d --pixel-dir build/skins/$$d > build/skins/$$d.log 2>&1 & pids="$$pids $$!"; \
	done; status=0; set -- $$pids; for d in $(FORM_FACTORS); do \
	    if wait $$1; then r=PASS; else r=FAIL; status=1; fi; shift; \
	    echo "skins: $$d $$r (build/skins/$$d.log)"; \
	done; exit $$status

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
# scenario at boot, prints PHONE-SELFTEST-RESULT and powers off; its parts
# on several emulators at once (at most SELFTEST_VMS, default from the
# processors and memory, and no more than end as soon; 1: the whole
# scenario in one), whose recordings and compatibility reports are checked
# together.
# ONLY='regexp' (development only, as make selftest's): only those cases,
# hex on the kernel command line, in one emulator; without the whole run
# there is no sound or compatibility report to verify, and the fd store
# check runs only its cases that match (shell/fdstore.script), if any.
# SELFTEST_ENV='NAME=value ...' (development only): set for phone-shell in
# the image, as a case's require-env wants. The emulators are counted here,
# as YOCTO_JOBS is: rootless podman's container does not see the user's
# memory limit.
SELFTEST_VMS ?= $(shell python3 yocto/selftest-plan.py --machines shell/selftest.script shell/cts.script)
yocto-selftest: build sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c '$(if $(ONLY),PHONE_SELFTEST_ONLY=$(shell printf %s '$(ONLY)' | od -An -tx1 | tr -d ' \n') )$(if $(SELFTEST_ENV),PHONE_SELFTEST_ENV=$(shell printf %s '$(SELFTEST_ENV)' | od -An -tx1 | tr -d ' \n') )PHONE_SELFTEST_VMS=$(SELFTEST_VMS) sh $(KAS_KARIO)/yocto/qemu-selftest.sh'
	$(if $(ONLY),,python3 yocto/audio-check.py $(WORK)/build/selftest/vm*/audio.wav)
	$(if $(ONLY),,$(PREFIX)/bin/phone-cts-verify --root-beside $(WORK)/build/selftest/vm*/cts-device.json)

# Boots with phone.gui=1: the renderer on the virtio-gpu DRM display, keys
# from the QEMU monitor, screendumps under $(WORK)/build/gui-shots.
# GUI_ENV='NAME=value ...': set for every service, e.g. PHONE_PAINT_GPU=soft;
# GUI_WAIT: a whole number, above 1 for a renderer that takes many seconds a
# frame (each screen then waits until it settles, yocto/qemu-gui-drive.py).
GUI_ENV ?=
GUI_WAIT ?= 1
yocto-gui: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'PHONE_GUI_ENV="$(GUI_ENV)" PHONE_GUI_WAIT=$(GUI_WAIT) sh $(KAS_KARIO)/yocto/qemu-gui.sh'

# phone-paint's GPU path on that display through Mesa's softpipe, also
# rendering on another DRM file than the display's, its screendumps
# against the CPU path's, and a GPU failing mid-run
# (docs/42-gpu-rendering.md §7; about five hours). GUI_GPU_RUNS: some of
# gpu gpu-split gpu-fail only, after the CPU's run.
GUI_GPU_RUNS ?=
yocto-gui-gpu: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'PHONE_GUI_GPU_RUNS="$(GUI_GPU_RUNS)" sh $(KAS_KARIO)/yocto/qemu-gui-gpu.sh'

# Semantic-vs-web comparison on the device (adds WPE WebKit + cog, see docs/15).
yocto-web-build:
	$(MAKE) yocto-build KASEXTRA=$(KAS_WEB)

# runqemu boots the image built last: the web image, whose python3 runs
# bench.py and cog the web route (the default image has neither).
yocto-bench: yocto-web-build
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
sku-check: bsp-fetch
	$(BSPENV) PHONE_KERNEL_SRC=$(WORK)/build/tmp/work-shared/qemuarm64/kernel-source python3 tools/sku.py check
	$(BSPENV) python3 -m unittest -q tools/sku_test.py tools/slots_test.py tools/phone_chroot_test.py tools/android_compare_test.py

# kario-bsp at the pinned commit in build/kario-bsp, or KARIO_BSP as it is.
bsp-fetch:
	$(if $(KARIO_BSP),@echo "kario-bsp: $(BSP_DIR) as it is (KARIO_BSP)",python3 scripts/kario-bsp.py fetch $(BSP_DIR))
	@python3 scripts/kario-bsp.py layers $(BSP_DIR) > /dev/null

# kario-bsp's layers when there are (make bsp-fetch, KARIO_BSP): gen refuses
# a board of theirs without them, so without KARIO_BSP and with no layers
# fetched yet a refusal fetches them and gens again (a QEMU SKU's build
# needs no network for it).
SKUGEN = KARIO_BSP_LAYERS=$$(python3 scripts/kario-bsp.py layers $(BSP_DIR) 2>/dev/null) python3 tools/sku.py gen $(SKU) $(if $(filter git,$(SRC)),--rev '$(REV)')
sku-gen:
	$(SKUGEN)$(if $(KARIO_BSP),, || { ! python3 scripts/kario-bsp.py layers $(BSP_DIR) > /dev/null 2>&1 && python3 scripts/kario-bsp.py fetch $(BSP_DIR) && $(SKUGEN); })

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
# the A/B update test on QEMU (thirteen boots: held back by a staged rollout
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

# The OnePlus 6T's userdata layout on QEMU (39-oneplus-6t.md §1): the root
# file system on a GPT partition named userdata, booted through
# phone-initramfs with what the phone's bootloader may append to the
# command line, and without an initramfs, as the 6T's boot image was.
yocto-userdata-test: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'sh $(KAS_KARIO)/yocto/qemu-userdata.sh'

# Charger mode on QEMU (28-battery.md §10): booted as the 6T's bootloader
# boots a phone its charger started, with test_power's battery; the
# charger's screen, then the power-off once the chargers are unplugged;
# a second boot: a hold at 2% waits, and the start comes at 3%.
yocto-charger-test: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'sh $(KAS_KARIO)/yocto/qemu-charger.sh'

# Factory mode on QEMU (40-testing.md §10): booted with kario.factory=1,
# the runner's automatic checks, a missing sound card failing them, the
# provisioning through the runner, the refusals after it; a second boot of
# the provisioned disk refuses factory mode.
yocto-factory-test: sku-gen src-conf
	$(KASCMD) shell $(KASCONF) -c 'sh $(KAS_KARIO)/yocto/qemu-factory.sh'

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
	$(KASENV) $(KAS) --runtime-args "--publish 127.0.0.1:5900:5900 $(KASMOUNTS)" shell $(KASCONF) \
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
