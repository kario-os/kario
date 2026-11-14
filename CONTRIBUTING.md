# Contributing

Contributions are welcome. Everything is under Apache-2.0 except gui/
(phone-gui: GPL-3.0-only, gui/CONTRIBUTING.md)
and docs/ (CC BY 4.0).

Every commit must be signed off: `git commit -s` adds
`Signed-off-by: Your Name <you@example.com>`, which certifies the Developer
Certificate of Origin 1.1 (https://developercertificate.org/): you wrote the
change or otherwise have the right to submit it under the licence of the
files it touches. CI checks this with scripts/check-dco.sh.

Code is English, comments included: UI text is written as English messages
(the source language, no catalog) and translated in each component's
locale/<lang>.json for zh-Hans, zh-Hant and es (package i18n, which falls
back from zh-Hant to zh-Hans and from everything to English;
docs/22-developer-api.md §9.1); an app manifest's text in other languages
goes in its "locales". A new message needs its zh-Hans translation; zh-Hant
and es may lag, and scripts/i18n-check.py reports per catalog what they
lack. Data (selftest scripts, manifests, sku/, testdata/, docs/) may be in
any language. `make test` runs scripts/no-chinese.py and
scripts/i18n-check.py.
