package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

type genFile struct {
	path string
	data []byte
}

func generate(root string) ([]genFile, error) {
	ts, err := typescript(filepath.Join(root, "ui"))
	if err != nil {
		return nil, err
	}
	return []genFile{{"sdk/ts/ui.d.ts", ts}, {"jsrt/src/languages.h", languagesH()}, {"sdk/ts/node/languages.mjs", languagesMJS()}}, nil
}

// unions turn groups of string constants, by their names' prefix, into
// string literal types.
var unions = []struct {
	prefix, name, doc string
	skip              []string
}{
	{"Kind", "NodeKind", "A node's kind (22-developer-api.md §3).", nil},
	{"Style", "NodeStyle", "Item styles (bubbles, grid tiles) and text styles.", nil},
	{"Layout", "Layout", "Where content shorter than the screen sits.", nil},
	{"Group", "ActionGroup", "Action groups; danger asks the user first.", nil},
	{"Action", "SystemAction", "Action IDs the system gives a meaning to, and a focused canvas's key action.", nil},
	{"Msg", "MessageType", "The protocol's message types, both ways.", nil},
	{"State", "LifecycleState", "Lifecycle states.", nil},
	{"Event", "EventName", "Events the system sends apps (22-developer-api.md §5).", nil},
	{"Cap", "CapName", "Capabilities (22-developer-api.md §4).", nil},
	{"Perm", "Permission", "Manifest permissions (22-developer-api.md §6).", nil},
	{"Media", "MediaKind", "Media kinds of media.list and media.pick.", []string{"MediaPrefix"}},
	{"Play", "PlayState", "Playback states of audio streams and video players.", nil},
	{"Op", "PlaybackOp", "Ops of audio.control and video.control.", nil},
	{"Web", "WebOp", "Ops of web.control.", nil},
	{"Wallpaper", "WallpaperTarget", "Targets of wallpaper.set.", nil},
	{"Update", "UpdateAction", "Actions of system.update.", nil},
	{"Setting", "SettingKey", "Keys of settings.set (system apps).", nil},
	{"Err", "ErrorText", "Errors whose text means something (a rejected call's message ends in it).", nil},
	{"Number", "NumberLabel", "Labels of a contact's numbers.", nil},
	{"Keyboard", "KeyboardKind", "What an input takes: text (\"\"), numbers, a phone number, an address, or the system's date and time fields.", nil},
	{"Repeat", "RepeatKind", "How a scheduled notification repeats.", nil},
	{"Date", "DateOrder", "The order a region writes a date's parts in.", nil},
	{"JobStop", "JobStopReason", "Why the system stops a running job (job.stop).", nil},
	{"Keepalive", "KeepaliveEvent", "Values of the keepalive event, about the app's own connection (net.keepalive, a TypeScript app's keepalive WebSocket).", []string{"KeepaliveLostFull", "KeepaliveLostDropped", "KeepaliveLostUnsaved"}},
	{"NetBlocked", "NetBlockedReason", "Why an app's mobile data is blocked (net.state).", nil},
	{"Net", "NetworkKind", "The network the phone is on (net.state).", []string{"NetBlockedWifiOnly", "NetBlockedLimit", "NetBlockedBackground"}},
	{"Role", "NodeRole", "What a node is to the screen reader where its kind does not say.", nil},
}

// fieldTypes narrow string fields to the unions above.
var fieldTypes = map[string]string{
	"Node.Kind":             "NodeKind",
	"Node.Style":            "NodeStyle",
	"Screen.Layout":         "Layout",
	"Action.Group":          "ActionGroup",
	"ContactNumber.Label":   "NumberLabel",
	"Node.Keyboard":         "KeyboardKind",
	"ScheduledNotif.Repeat": "RepeatKind",
	"LocaleInfo.DateOrder":  "DateOrder",
	"JobStop.Reason":        "JobStopReason",
	"NetState.Network":      "NetworkKind",
	"NetState.Blocked":      "NetBlockedReason",
	"TrafficApp.Blocked":    "NetBlockedReason",
	"Node.Role":             "NodeRole",
	"MediaListArgs.Kind":    "MediaKind",
}

// capTypes are what each capability takes and gives back, read from the
// broker (shell/broker*.go): Go types of package ui or TypeScript.
// Capabilities missing here take any object and give back unknown.
var capTypes = map[string][2]string{
	"CapCallStart":      {"CallStartArgs", "CallStartResult"},
	"CapCallControl":    {"CallControlArgs", "Record<string, boolean>"},
	"CapCallEnd":        {"CallControlArgs", "Record<string, number>"},
	"CapScanQR":         {"undefined", "ScanResult"},
	"CapAuthConfirm":    {"AuthConfirmArgs", "KeyProof"},
	"CapKeySign":        {"SignArgs", "KeyProof"},
	"CapAttest":         {"AttestArgs", "Attestation"},
	"CapPermList":       {"undefined", "PermEntry[]"},
	"CapPermSet":        {"PermSetArgs", "{ state: string }"},
	"CapTelDial":        {"DialArgs", "unknown"},
	"CapTelAnswer":      {"CallIDArgs", "unknown"},
	"CapTelHangup":      {"CallIDArgs", "unknown"},
	"CapTelHold":        {"CallIDArgs", "unknown"},
	"CapTelMute":        {"CallIDArgs", "unknown"},
	"CapTelCalls":       {"undefined", "TelCall[]"},
	"CapTelLog":         {"undefined", "TelLogEntry[]"},
	"CapSMSSend":        {"SMSSendArgs", "unknown"},
	"CapSMSList":        {"SMSListArgs | undefined", "SMS[]"},
	"CapSMSRead":        {"PeerArgs", "unknown"},
	"CapSMSThreads":     {"SMSListArgs | undefined", "SMSThread[]"},
	"CapSMSAlerts":      {"undefined", "SMSAlert[]"},
	"CapSMSDelete":      {"SMSDeleteArgs", "unknown"},
	"CapSMSBlock":       {"SMSBlockArgs", "string[]"},
	"CapSMSBlocked":     {"undefined", "string[]"},
	"CapSMSDownload":    {"SMSIDArgs", "unknown"},
	"CapSMSDraft":       {"SMSDraftArgs", "{ ok: boolean }"},
	"CapSMSBackup":      {"undefined", "SMSFile"},
	"CapSMSRestore":     {"undefined", "SMSFile"},
	"CapSMSVCard":       {"SMSPartArgs", "ContactsFile"},
	"CapSMSOTP":         {"SMSOTPArgs | undefined", "SMSOTP"},
	"CapBlockList":      {"undefined", "BlockList"},
	"CapBlockSet":       {"BlockSetArgs", "BlockList"},
	"CapTelUSSD":        {"USSDArgs", "USSDReply"},
	"CapContacts":       {"undefined", "Contact[]"},
	"CapContactsSave":   {"Contact", "Contact"},
	"CapContactsDelete": {"ContactIDArgs", "{ ok: boolean }"},
	"CapContactsImport": {"undefined", "ContactsFile"},
	"CapContactsExport": {"undefined", "ContactsFile"},
	"CapContactsGroups": {"undefined", "ContactGroup[]"},
	"CapContactsGroup":  {"ContactGroupArgs", "ContactGroup"},
	"CapContactsMerge":  {"ContactMergeArgs", "Contact"},
	"CapContactsPick":   {"undefined", "Contact"},
	"CapContactsDates":  {"undefined", "ContactDate[]"},
	"CapMediaList":      {"MediaArgs | undefined", "MediaItem[]"},
	"CapMediaList2":     {"MediaListArgs | undefined", "MediaPage"},
	"CapAudioPlay":      {"AudioPlayArgs", "unknown"},
	"CapAudioControl":   {"AudioControlArgs", "unknown"},
	"CapAudioRoute":     {"AudioRouteArgs", "unknown"},
	"CapCameraCapture":  {"undefined", "MediaItem"},
	"CapMediaPick":      {"MediaArgs", "MediaItem"},
	"CapMediaSave":      {"MediaSaveArgs", "MediaItem"},
	"CapMediaDelete":    {"MediaArgs", "unknown"},
	"CapFileSave":       {"FileArgs", "FileItem"},
	"CapFilePick":       {"FileArgs | undefined", "FileItem"},
	"CapFileOpen":       {"FileArgs", "FileItem"},
	"CapAppSettings":    {"undefined", "Record<string, unknown>"},
	"CapWidgetSet":      {"WidgetArgs", "undefined"},
	"CapSettingsApps":   {"undefined", "AppSettings[]"},
	"CapSettingsAppSet": {"AppSettingArgs", "undefined"},
	"CapPushKey":        {"PushKeyArgs | undefined", "PushKeyRes"},
	"CapPushOpen":       {"PushOpenArgs", "PushOpenRes"},
	"CapPushRegister":   {"undefined", "PushToken"},
	"CapPkgInstall":     {"PkgArgs", "PkgInfo"},
	"CapPkgUninstall":   {"PkgArgs", "{ uninstalled: boolean }"},
	"CapPkgList":        {"undefined", "PkgInfo[]"},
	"CapPkgRevocations": {"RevocationArgs", "{ revoked_installed: number }"},
	"CapPkgCatalog":     {"CatalogArgs", "CatalogRes"},
	"CapIntentInvoke":   {"IntentArgs", "IntentRes"},
	"CapIntentReply":    {"IntentReplyArgs", "undefined"},
	"CapShareOpen":      {"ShareArgs", "{ shared: boolean }"},
	"CapLocationGet":    {"LocationGetArgs | undefined", "Location"},
	"CapLocationWatch":  {"LocationWatchArgs | undefined", "{ watching: boolean }"},
	"CapLocationStop":   {"undefined", "{ watching: boolean }"},
	"CapJobSchedule":    {"JobArgs", "{ ok: boolean }"},
	"CapJobCancel":      {"JobArgs", "{ ok: boolean }"},
	"CapNetState":       {"undefined", "NetState"},
	"CapClipboardSet":   {"ClipArgs", "undefined"},
	"CapNFCReply":       {"NFCReplyArgs", "{ ok: boolean }"},
	"CapNFCRead":        {"NFCReadArgs | undefined", "NFCTag"},
	"CapNFCTransceive":  {"NFCTransceiveArgs", "NFCTransceiveRes"},
	"CapSettingsSet":    {"SettingArgs", "unknown"},
	"CapSystemUpdate":   {"UpdateArgs", "unknown"},
	"CapWifiState":      {"undefined", "WifiState"},
	"CapWifiSet":        {"WifiArgs", "unknown"},
	"CapWifiConnect":    {"WifiArgs", "unknown"},
	"CapWifiForget":     {"WifiArgs", "unknown"},
	"CapBTState":        {"undefined", "BTState"},
	"CapBTSet":          {"BTArgs", "unknown"},
	"CapBTPair":         {"BTArgs", "unknown"},
	"CapBTForget":       {"BTArgs", "unknown"},
	"CapBTSend":         {"BTArgs", "{ sent: string }"},
	"CapVPNList":        {"undefined", "VPNState"},
	"CapVPNImport":      {"undefined", "VPNProfile"},
	"CapVPNConnect":     {"VPNArgs", "VPNState"},
	"CapVPNDisconnect":  {"VPNArgs", "VPNState"},
	"CapVPNDelete":      {"VPNArgs", "VPNState"},
	"CapVPNKeygen":      {"undefined", "{ public_key: string }"},
	"CapVPNSet":         {"VPNArgs", "VPNState"},
	"CapRadioState":     {"undefined", "RadioState"},
	"CapRadioSet":       {"RadioArgs", "RadioState"},
	"CapAPNList":        {"undefined", "APNState"},
	"CapAPNSet":         {"APNArgs", "APNState"},
	"CapHotspotState":   {"undefined", "HotspotState"},
	"CapHotspotSet":     {"HotspotArgs", "HotspotState"},
	"CapSwitchState":    {"undefined", "SwitchState"},
	"CapSwitchStart":    {"SwitchStartArgs", "SwitchState"},
	"CapSwitchStop":     {"undefined", "SwitchState"},
	"CapSwitchUndo":     {"undefined", "SwitchState"},
	"CapWebControl":     {"WebControlArgs", "undefined"},
	"CapWallpaperSet":   {"WallpaperArgs", "unknown"},
	"CapVideoOpen":      {"VideoArgs", "VideoState"},
	"CapVideoControl":   {"VideoArgs", "VideoState"},
	"CapAlarmSet":       {"Alarm", "unknown"},
	"CapAlarmCancel":    {"{ id: string }", "unknown"},
	"CapAlarmList":      {"undefined", "Alarm[]"},
	"CapNotifySchedule": {"ScheduledNotif", "ScheduledNotif"},
	"CapNotifyCancel":   {"NotifyCancelArgs", "{ cancelled: number }"},
	"CapNotifyList":     {"undefined", "ScheduledNotif[]"},
	"CapLocaleGet":      {"undefined", "LocaleInfo"},
	"CapHolidaysGet":    {"undefined", "HolidayCalendar"},
	"CapTimeOffsets":    {"ZoneOffsetsArgs", "ZoneOffsets"},
	"CapCalendarPull":   {"CalPullArgs", "CalPull"},
	"CapCalendarPush":   {"CalPushArgs", "CalPushRes"},
	"CapDisplayHint":    {"DisplayHintArgs", "DisplayHintRes"},

	"CapLocationBackground": {"undefined", "{ background: boolean }"},
}

// sourceOrder puts the protocol's own types first.
var sourceOrder = []string{"proto.go", "caps.go", "names.go"}

type constant struct{ name, value string }

type pkgInfo struct {
	consts  []constant
	structs []*ast.TypeSpec
	docs    map[*ast.TypeSpec]*ast.CommentGroup
	named   map[string]ast.Expr // every type declared, by name
}

func parseUI(dir string) (*pkgInfo, error) {
	fset := token.NewFileSet()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Slice(names, func(i, j int) bool {
		oi, oj := slices.Index(sourceOrder, filepath.Base(names[i])), slices.Index(sourceOrder, filepath.Base(names[j]))
		if oi < 0 {
			oi = len(sourceOrder)
		}
		if oj < 0 {
			oj = len(sourceOrder)
		}
		if oi != oj {
			return oi < oj
		}
		return names[i] < names[j]
	})
	info := &pkgInfo{docs: map[*ast.TypeSpec]*ast.CommentGroup{}, named: map[string]ast.Expr{}}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					if gd.Tok != token.CONST || len(s.Values) != len(s.Names) {
						continue
					}
					for i, n := range s.Names {
						lit, ok := s.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING || !n.IsExported() {
							continue
						}
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							return nil, err
						}
						info.consts = append(info.consts, constant{n.Name, v})
					}
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					info.named[s.Name.Name] = s.Type
					if _, ok := s.Type.(*ast.StructType); ok {
						info.structs = append(info.structs, s)
						doc := s.Doc
						if doc == nil {
							doc = gd.Doc
						}
						info.docs[s] = doc
					}
				}
			}
		}
	}
	return info, nil
}

type writer struct{ bytes.Buffer }

func (w *writer) p(format string, args ...any) { fmt.Fprintf(&w.Buffer, format+"\n", args...) }

// doc writes a comment as TSDoc, indented.
func (w *writer) doc(indent string, cg *ast.CommentGroup) {
	if cg == nil {
		return
	}
	text := strings.TrimSpace(cg.Text())
	if text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 1 {
		w.p("%s/** %s */", indent, strings.ReplaceAll(lines[0], "*/", "* /"))
		return
	}
	w.p("%s/**", indent)
	for _, l := range lines {
		w.p("%s * %s", indent, strings.TrimRight(strings.ReplaceAll(l, "*/", "* /"), " "))
	}
	w.p("%s */", indent)
}

func typescript(dir string) ([]byte, error) {
	info, err := parseUI(dir)
	if err != nil {
		return nil, err
	}
	var w writer
	w.p("// Code generated by tools/tsgen from package ui (ui/*.go) and i18n/languages.json; DO NOT EDIT.")
	w.p("// The phone.Ui protocol's types for TypeScript apps (22-developer-api.md); the module")
	w.p("// \"kario\" itself is declared in index.d.ts.")
	w.p("")
	var langs []string
	for _, l := range i18n.Languages() {
		langs = append(langs, strconv.Quote(l.ID))
	}
	w.p("/** The languages the system offers (PHONE_LANG). */")
	w.p("export type Language = %s;", strings.Join(langs, " | "))
	caps := map[string]string{}
	for _, u := range unions {
		var vals []string
		seen := map[string]bool{}
		for _, c := range info.consts {
			if !strings.HasPrefix(c.name, u.prefix) || slices.Contains(u.skip, c.name) || seen[c.value] {
				continue
			}
			seen[c.value] = true
			vals = append(vals, strconv.Quote(c.value))
			if u.prefix == "Cap" {
				caps[c.name] = c.value
			}
		}
		if len(vals) == 0 {
			return nil, fmt.Errorf("no constants named %s* in package ui", u.prefix)
		}
		w.p("")
		w.p("/** %s */", u.doc)
		w.p("export type %s =", u.name)
		for i, v := range vals {
			end := ""
			if i == len(vals)-1 {
				end = ";"
			}
			w.p("  | %s%s", v, end)
		}
	}
	emitted := map[string]bool{}
	for _, s := range info.structs {
		emitted[s.Name.Name] = true
	}
	for _, s := range info.structs {
		w.p("")
		w.doc("", info.docs[s])
		w.p("export interface %s {", s.Name.Name)
		for _, f := range s.Type.(*ast.StructType).Fields.List {
			if len(f.Names) == 0 {
				return nil, fmt.Errorf("%s: embedded fields are not supported", s.Name.Name)
			}
			tag := ""
			if f.Tag != nil {
				t, _ := strconv.Unquote(f.Tag.Value)
				tag = reflect.StructTag(t).Get("json")
			}
			jsonName, opts, _ := strings.Cut(tag, ",")
			if jsonName == "-" {
				continue
			}
			optional := slices.Contains(strings.Split(opts, ","), "omitempty")
			for _, n := range f.Names {
				if !n.IsExported() {
					continue
				}
				name := jsonName
				if name == "" {
					name = n.Name
				}
				typ, err := tsType(info, emitted, f.Type)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", s.Name.Name, n.Name, err)
				}
				if o, ok := fieldTypes[s.Name.Name+"."+n.Name]; ok {
					typ = o
				}
				if _, ptr := f.Type.(*ast.StarExpr); ptr && !optional {
					typ += " | null"
				}
				doc := f.Doc
				if doc == nil {
					doc = f.Comment
				}
				w.doc("  ", doc)
				q := ""
				if optional {
					q = "?"
				}
				w.p("  %s%s: %s;", name, q, typ)
			}
		}
		w.p("}")
	}
	var capNames []string
	for name := range caps {
		capNames = append(capNames, name)
	}
	sort.Slice(capNames, func(i, j int) bool { return caps[capNames[i]] < caps[capNames[j]] })
	for name := range capTypes {
		if _, ok := caps[name]; !ok {
			return nil, fmt.Errorf("capTypes names %s, which package ui does not have", name)
		}
	}
	for _, part := range []struct {
		iface, doc, missing string
		i                   int
	}{
		{"CapArgs", "What each capability takes; undefined: nothing.", "Record<string, unknown> | undefined", 0},
		{"CapResult", "What each capability's promise resolves to.", "unknown", 1},
	} {
		w.p("")
		w.p("/** %s */", part.doc)
		w.p("export interface %s {", part.iface)
		for _, name := range capNames {
			t, ok := capTypes[name]
			typ := part.missing
			if ok {
				typ = t[part.i]
				if err := checkRefs(typ, emitted); err != nil {
					return nil, fmt.Errorf("capTypes %s: %w", name, err)
				}
			}
			if note := superseded(caps[name]); note != "" {
				w.p("  /** %s */", note)
			}
			w.p("  %s: %s;", strconv.Quote(caps[name]), typ)
		}
		w.p("}")
	}
	w.p("")
	w.p("/** The platform level this SDK describes (22-developer-api.md §2.2): kario new writes it as the manifest's min_platform. */")
	w.p("export type PlatformLevel = %d;", ui.PlatformLevel)
	w.p("")
	w.p("/**")
	w.p(" * The platform level each capability came in at; a system of a lower level answers \"unknown capability\".")
	w.p(" * A capability's later versions are keys of their own (\"media.list@2\"), version 1 the bare name (22-developer-api.md §2.3).")
	w.p(" */")
	w.p("export interface CapLevel {")
	for _, name := range capNames {
		level, ok := ui.CapLevels[caps[name]]
		if !ok {
			return nil, fmt.Errorf("%s has no level in ui.CapLevels", name)
		}
		w.p("  %s: %d;", strconv.Quote(caps[name]), level)
	}
	w.p("}")
	return w.Bytes(), nil
}

// superseded says of a capability version a later one replaces which, and
// how long it is served at least (ui.CapKeepLevels); "" for the newest.
func superseded(capName string) string {
	name, v, ok := ui.ParseCap(capName)
	if !ok {
		return ""
	}
	for _, next := range ui.CapVersions(name) {
		if next > v {
			key := ui.CapKey(name, next)
			level := ui.CapLevels[key]
			return fmt.Sprintf("Superseded by %q (platform level %d); served through level %d at least (22-developer-api.md §2.3).",
				key, level, level+ui.CapKeepLevels-1)
		}
	}
	return ""
}

// checkRefs: the identifiers of a TypeScript type are types this file has.
func checkRefs(typ string, emitted map[string]bool) error {
	for _, word := range strings.FieldsFunc(typ, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if word[0] >= 'A' && word[0] <= 'Z' && word != "Record" && !emitted[word] {
			return fmt.Errorf("no type %s in package ui", word)
		}
	}
	return nil
}

func tsType(info *pkgInfo, emitted map[string]bool, e ast.Expr) (string, error) {
	switch t := e.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return "string", nil
		case "bool":
			return "boolean", nil
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
			return "number", nil
		case "any":
			return "unknown", nil
		}
		if emitted[t.Name] {
			return t.Name, nil
		}
		if u, ok := info.named[t.Name]; ok {
			return tsType(info, emitted, u)
		}
		return "", fmt.Errorf("unknown type %s", t.Name)
	case *ast.StarExpr:
		return tsType(info, emitted, t.X)
	case *ast.ArrayType:
		if id, ok := t.Elt.(*ast.Ident); ok && id.Name == "byte" {
			return "string", nil
		}
		inner, err := tsType(info, emitted, t.Elt)
		if err != nil {
			return "", err
		}
		if strings.Contains(inner, " ") {
			inner = "(" + inner + ")"
		}
		return inner + "[]", nil
	case *ast.MapType:
		v, err := tsType(info, emitted, t.Value)
		if err != nil {
			return "", err
		}
		return "Record<string, " + v + ">", nil
	case *ast.InterfaceType:
		return "unknown", nil
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			switch x.Name + "." + t.Sel.Name {
			case "json.RawMessage":
				return "unknown", nil
			case "time.Time":
				return "string", nil
			}
		}
	}
	return "", fmt.Errorf("unsupported type %T", e)
}

// languagesH is kario-jsrt's table of the languages package i18n offers.
func languagesH() []byte {
	langs := i18n.Languages()
	most := 0
	for _, l := range langs {
		most = max(most, len(l.Fallbacks))
	}
	var w writer
	w.p("/* Code generated by tools/tsgen from i18n/languages.json; DO NOT EDIT. */")
	w.p("#define JSRT_DEFAULT_LANG %s", strconv.Quote(i18n.DefaultLang))
	w.p("")
	w.p("static const struct jsrt_language {")
	w.p("\tconst char *id;")
	w.p("\tconst char *fallbacks[%d];", most+1)
	w.p("} jsrt_languages[] = {")
	for _, l := range langs {
		var fb []string
		for _, f := range l.Fallbacks {
			fb = append(fb, strconv.Quote(f))
		}
		fb = append(fb, "0")
		w.p("\t{%s, {%s}},", strconv.Quote(l.ID), strings.Join(fb, ", "))
	}
	w.p("};")
	return w.Bytes()
}

// languagesMJS is the same table for the Node.js runtime (sdk/ts/node).
func languagesMJS() []byte {
	var w writer
	w.p("// Code generated by tools/tsgen from i18n/languages.json; DO NOT EDIT.")
	w.p("export const defaultLang = %s;", strconv.Quote(i18n.DefaultLang))
	w.p("")
	w.p("export const languages = [")
	for _, l := range i18n.Languages() {
		var fb []string
		for _, f := range l.Fallbacks {
			fb = append(fb, strconv.Quote(f))
		}
		w.p("  { id: %s, fallbacks: [%s] },", strconv.Quote(l.ID), strings.Join(fb, ", "))
	}
	w.p("];")
	return w.Bytes()
}

// stale lists the generated files that differ from what is in the tree.
func stale(root string) ([]string, error) {
	files, err := generate(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		old, _ := os.ReadFile(filepath.Join(root, f.path))
		if !bytes.Equal(old, f.data) {
			out = append(out, f.path)
		}
	}
	return out, nil
}
