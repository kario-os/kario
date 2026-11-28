package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Capability versions (22-developer-api.md §2.3). An app names a capability
// "name" or "name@N"; the bare name is version 1, so "media.list@1" is
// "media.list", the name a system before versions knows. What §2.1 allows
// only in a new major (a field renamed, retyped or made required, a result
// of another shape) comes instead as the capability's next version, with
// argument and result types of its own, beside the old one. CapLevels has
// a key per version served (CapKey: the bare name for 1, "name@N" past it)
// with the platform level it came in at; CapRemoved the versions no longer
// served.

// CapKeepLevels: a version stays served for at least this many platform
// levels from the one that brought the version replacing it, so it may go
// at that level plus CapKeepLevels at the earliest (CheckCapTables).
const CapKeepLevels = 3

// CapRemoved are the capability versions this system no longer serves, by
// the platform level that stopped serving them. The broker answers a call
// to one, the installer refuses an app whose manifest needs one
// ("capabilities") and such an app already installed does not start. Once
// version 1 is gone the bare name is refused too: it does not move on to
// the next version, whose arguments the old app does not send.
var CapRemoved = map[string]int{}

var capName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// ParseCap reads a capability as an app names it: its name and version, 1
// for the bare name; N is a decimal number from 1, without leading zeros.
func ParseCap(c string) (name string, version int, ok bool) {
	name, v, versioned := strings.Cut(c, "@")
	if !capName.MatchString(name) {
		return "", 0, false
	}
	if !versioned {
		return name, 1, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || v != strconv.Itoa(n) {
		return "", 0, false
	}
	return name, n, true
}

// CapKey is version v of capability name as CapLevels and the broker key it.
func CapKey(name string, v int) string {
	if v <= 1 {
		return name
	}
	return name + "@" + strconv.Itoa(v)
}

// CapVersions are the versions of capability name this system serves,
// oldest first.
func CapVersions(name string) []int { return capVersions(CapLevels, name) }

func capVersions(levels map[string]int, name string) []int {
	var out []int
	for key := range levels {
		if n, v, ok := ParseCap(key); ok && n == name {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// ResolveCap is the key (CapLevels') of the capability version c names, or
// the error the broker answers with. Every such error begins "unknown
// capability", as a system before versions answers any name it lacks.
func ResolveCap(c string) (string, error) { return ResolveCapIn(CapLevels, CapRemoved, c) }

// ResolveCapIn is ResolveCap against other tables (tests).
func ResolveCapIn(levels, removed map[string]int, c string) (string, error) {
	name, v, ok := ParseCap(c)
	if !ok {
		return "", fmt.Errorf("unknown capability %q", c)
	}
	key := CapKey(name, v)
	if _, ok := levels[key]; ok {
		return key, nil
	}
	served := capVersions(levels, name)
	at, gone := removed[key]
	switch {
	case gone:
		return "", fmt.Errorf("unknown capability %q: removed at platform level %d (served: %s)", c, at, capNames(name, served))
	case len(served) == 0:
		return "", fmt.Errorf("unknown capability %q", c)
	}
	return "", fmt.Errorf("unknown capability %q (served: %s)", c, capNames(name, served))
}

func capNames(name string, versions []int) string {
	if len(versions) == 0 {
		return "none"
	}
	var out []string
	for _, v := range versions {
		out = append(out, name+"@"+strconv.Itoa(v))
	}
	return strings.Join(out, ", ")
}

// CheckCapabilities checks a manifest's "capabilities", the capability
// versions the app cannot work without, against the platform this package
// describes (kario build and the store's review; the installer checks them
// against the phone's own): each one served, of a level min_platform
// reaches.
func CheckCapabilities(caps []string, minPlatform int) error {
	for _, c := range caps {
		key, err := ResolveCap(c)
		if err != nil {
			return fmt.Errorf("capabilities: %w", err)
		}
		if level := CapLevels[key]; level > max(minPlatform, 1) {
			return fmt.Errorf("capabilities: %s came in at platform level %d, above min_platform %d", c, level, minPlatform)
		}
	}
	return nil
}

// CheckCapTables holds tables of capability versions to the rules of
// 22-developer-api.md §2.3: every key a capability's CapKey, at a level from
// 1 to platform; a version past 1 follows one served or removed, at a later
// level; a removed version is served no longer, and went at a level the
// system reached, at least CapKeepLevels levels after a later version of
// it came in.
func CheckCapTables(levels, removed map[string]int, platform int) error {
	for key, level := range levels {
		name, v, ok := ParseCap(key)
		switch {
		case !ok || CapKey(name, v) != key:
			return fmt.Errorf("%q is not a capability's key (name, or name@N past version 1)", key)
		case level < 1 || level > platform:
			return fmt.Errorf("%s: level %d, not 1 to %d", key, level, platform)
		case v == 1:
			continue
		}
		prev := CapKey(name, v-1)
		if pl, ok := levels[prev]; ok && pl >= level {
			return fmt.Errorf("%s: level %d, not after %s's %d", key, level, prev, pl)
		} else if _, gone := removed[prev]; !ok && !gone {
			return fmt.Errorf("%s: no %s before it", key, prev)
		}
	}
	for key, at := range removed {
		name, v, ok := ParseCap(key)
		switch {
		case !ok || CapKey(name, v) != key:
			return fmt.Errorf("removed %q is not a capability's key", key)
		case at > platform:
			return fmt.Errorf("%s removed at level %d, past this system's %d", key, at, platform)
		}
		if _, ok := levels[key]; ok {
			return fmt.Errorf("%s is removed and served", key)
		}
		replaced := 0
		for _, w := range capVersions(levels, name) {
			if w > v && (replaced == 0 || levels[CapKey(name, w)] < replaced) {
				replaced = levels[CapKey(name, w)]
			}
		}
		switch {
		case replaced == 0:
			return fmt.Errorf("%s removed with no later version served", key)
		case at < replaced+CapKeepLevels:
			return fmt.Errorf("%s removed at level %d: its replacement came at %d, so level %d at the earliest", key, at, replaced, replaced+CapKeepLevels)
		}
	}
	return nil
}
