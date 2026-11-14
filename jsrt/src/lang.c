/* The device's language and where a message missing from it comes from, as
 * package i18n has them (Canonical, Chain); the languages themselves are
 * generated from i18n/languages.json. */
#include <ctype.h>
#include <string.h>
#include <strings.h>

#include "jsrt.h"
#include "languages.h"

#define NLANG (sizeof jsrt_languages / sizeof jsrt_languages[0])

static const struct jsrt_language *find(const char *id)
{
	for (size_t i = 0; i < NLANG; i++)
		if (!strcmp(jsrt_languages[i].id, id))
			return &jsrt_languages[i];
	return NULL;
}

const char *jsrt_canonical(const char *tag)
{
	char t[64];
	size_t n = 0;
	if (!tag)
		tag = "";
	while (isspace((unsigned char)*tag))
		tag++;
	for (; *tag && n < sizeof t - 1; tag++)
		t[n++] = *tag == '_' ? '-' : (char)tolower((unsigned char)*tag);
	while (n && isspace((unsigned char)t[n - 1]))
		n--;
	t[n] = 0;
	if (!n)
		return JSRT_DEFAULT_LANG;
	char base[64];
	const char *dash = strchr(t, '-');
	size_t bn = dash ? (size_t)(dash - t) : n;
	memcpy(base, t, bn);
	base[bn] = 0;
	if (!strcmp(base, "zh")) {
		for (const char *p = dash; p && *p;) {
			const char *part = p + 1, *end = strchr(part, '-');
			size_t pn = end ? (size_t)(end - part) : strlen(part);
			if ((pn == 4 && !strncmp(part, "hant", 4)) ||
			    (pn == 2 && (!strncmp(part, "tw", 2) || !strncmp(part, "hk", 2) || !strncmp(part, "mo", 2))))
				return "zh-Hant";
			p = end;
		}
		return "zh-Hans";
	}
	for (size_t i = 0; i < NLANG; i++)
		if (!strcasecmp(jsrt_languages[i].id, base) || !strcasecmp(jsrt_languages[i].id, t))
			return jsrt_languages[i].id;
	return "en";
}

int jsrt_chain(const char *lang, const char **out, int max)
{
	const struct jsrt_language *l = find(jsrt_canonical(lang));
	int n = 0;
	if (l && n < max)
		out[n++] = l->id;
	for (int i = 0; l && l->fallbacks[i] && n < max; i++)
		out[n++] = l->fallbacks[i];
	if ((!n || strcmp(out[n - 1], "en")) && n < max)
		out[n++] = "en";
	return n;
}
