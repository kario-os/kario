#!/bin/sh
# The injected scripts and the translations as C string constants:
#	scripts.sh nav.js bridge.js locale/*.json > scripts.h
# NAME.js becomes NAME_js; locale/LANG.json an entry of catalogs.
set -e
cstring() {
	sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/^/    "/' -e 's/$/\\n"/' "$1"
}
langs=
for f in "$@"; do
	case $f in
	*.js)
		printf 'static const char %s_js[] =\n' "$(basename "$f" .js)"
		cstring "$f"
		printf '    ;\n'
		;;
	*.json) langs="$langs $f" ;;
	esac
done
printf 'static const struct {\n    const char *lang, *json;\n} catalogs[] = {\n'
for f in $langs; do
	printf '    {"%s",\n' "$(basename "$f" .json)"
	cstring "$f"
	printf '    },\n'
done
printf '    {NULL, NULL},\n};\n'
