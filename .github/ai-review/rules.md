# Review rules for the AI report

The AI review (scripts/ai-review.py) gives these rules and CONTRIBUTING.md to
the model. They restate what the maintainers check in a pull request; the
scripts named enforce part of them in CI.

1. Documents match the code. A change of behaviour, an interface, a command,
   a file name or a flag updates the documents that describe it (docs/ and
   docs/en/, README.md, CONTRIBUTING.md) in the same pull request. What is
   planned or simulated is marked so: **未实现** (not implemented),
   **未上真机** (not run on a real phone), **未验证** (not verified),
   **未跑** (not run). A document must not claim more than the code does.
2. Documents are written in Chinese in docs/ (authoritative), each with an
   English translation of the same name in docs/en/ whose first line links
   back to it and whose headings, table rows and code blocks match
   (scripts/docs-en-check.py). Relative links resolve, the section numbers
   after them (§8.2) are headings of the target, and no file numbers two
   sections alike (scripts/docs-links.py). A new document takes the next free number of 01-50 and is listed in
   docs/README.md and docs/en/README.md.
3. Code is English, comments included: no Chinese characters in code,
   scripts, build files or workflows (scripts/no-chinese.py); Chinese belongs
   in data (locale catalogs, manifests, selftest scripts, sku/, testdata/,
   docs/). Code that must name a Chinese character writes an escape.
4. Text a user reads is an English message passed to the translation call:
   tr.T("...") in Go, t("...") in Rust, T("...") in Python, tr("...") in C,
   C++ and TypeScript apps, phone.t("...") in mini-programs. Each new message
   has its zh-Hans translation in the component's locale/zh-Hans.json;
   zh-Hant and es may lag (scripts/i18n-check.py).
5. Shared vocabulary is spelled once: service sockets and methods in
   svc/svc.go, what apps and the system share in ui/names.go, capabilities in
   ui/caps.go; other Go code uses the constants (scripts/const-check.py).
6. A change to package ui regenerates the TypeScript types and kario-jsrt's
   language table (go run ./tools/tsgen: sdk/ts/ui.d.ts, jsrt/src/languages.h,
   sdk/ts/node/languages.mjs); the generated files are not edited by hand.
7. A bug fix comes with a test that fails without it; new behaviour comes
   with tests (unit tests, a selftest or compatibility case where the user
   sees it). Parsers of untrusted input keep their fuzz targets.
8. The platform stays Apache-2.0 and free of copyleft dependencies
   (scripts/license-check.py); gui/ is GPL-3.0-only and never in a device
   image; docs/ is CC BY 4.0. After go.mod or go.sum change, make gomods
   updates the Yocto recipe's module list.
9. Every commit is signed off (git commit -s, DCO; scripts/check-dco.sh).
10. Security: apps are untrusted (sandbox, permissions, the broker in
    shell/); input from apps, the network, USB, NFC and the modem is
    untrusted; no secrets or keys in the repository.
11. A pull request that changes .github/, scripts/ or the Makefile changes
    how CI judges it: say so, and review those changes with extra care.
