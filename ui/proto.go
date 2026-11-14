// Package ui defines phone.Ui v0: the semantic UI protocol between apps and
// the system shell. Apps describe content and the actions each element
// supports; the shell owns focus, keys, menus, rendering and system dialogs.
//
// Transport: one JSON object per line over the socket the shell hands to the
// app process (fd 3, see PHONE_UI_FD). Any language that can read and write
// JSON lines can implement an app.
package ui

import "encoding/json"

const Proto = "phone.ui/0"

// Node kinds.
const (
	KindText    = "text"
	KindSection = "section"
	KindItem    = "item"
	KindInput   = "input"
	KindButton  = "button"
	KindImage   = "image"
	KindMedia   = "media"
	// KindDoc carries Markdown in Text; the system renders it (headings, lists,
	// quotes, code, tables, links) in every renderer, so rich documents need
	// no browser. Links become menu actions "link:N" with the URL as value.
	KindDoc = "doc"
	// KindProgress is a bar filled to Fraction (0-1) with an optional label
	// in Text and a right-hand note in Trailing (playback, downloads, updates).
	KindProgress = "progress"
	// KindWeb shows the web page at Src (http or https) as it is: the
	// system runs the web view (WPE WebKit) in a sandbox of its own and the
	// page reaches the screen as a media surface. With focus, the arrow
	// keys move between the page's links and controls, OK activates, back
	// goes back in the page's history. Pages call phone.call(cap, args)
	// under the app's permissions and their origin. Without a pixel
	// renderer the node shows the page's title and address.
	KindWeb = "web"
	// KindCanvas is drawn by the app (the exception tier for games, maps,
	// charts): shapes in Canvas, which the system paints in every renderer.
	KindCanvas = "canvas"
)

// Canvas holds shapes in a W×H grid of the app's own units (at most
// MaxCanvasSide each way, MaxCanvasOps shapes), painted over Bg. Text stays
// in ordinary nodes next to it, which every renderer, search and the input
// method understand. With Keys, a focused canvas gets the keys (OK, arrows,
// digits, * and #, letters) as "key" actions with the key in Value instead
// of moving the focus; back, the soft keys and the end key stay the system's.
type Canvas struct {
	W    int      `json:"w"`
	H    int      `json:"h"`
	Bg   string   `json:"bg,omitempty"`
	Ops  []DrawOp `json:"ops,omitempty"`
	Keys bool     `json:"keys,omitempty"`
}

// DrawOp is one shape: "rect" (X, Y, W, H), "line" (X, Y to X2, Y2),
// "circle" (centre X, Y, radius R), "dot" (X, Y) or "poly" (Pts: x0, y0,
// x1, y1, ...). Color is "#rrggbb"; Fill fills a rect, circle or poly
// instead of outlining it; Width is a line's thickness in units (1).
type DrawOp struct {
	Op    string `json:"op"`
	X     int    `json:"x,omitempty"`
	Y     int    `json:"y,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
	X2    int    `json:"x2,omitempty"`
	Y2    int    `json:"y2,omitempty"`
	R     int    `json:"r,omitempty"`
	Pts   []int  `json:"pts,omitempty"`
	Color string `json:"color"`
	Fill  bool   `json:"fill,omitempty"`
	Width int    `json:"width,omitempty"`
}

const (
	MaxCanvasSide = 320
	MaxCanvasOps  = 4000
	// ActionKey is the action a canvas with Keys sends for a key press.
	ActionKey = "key"
)

// Action groups. The "global" group is owned by the shell and never sent by apps.
const (
	GroupItem   = "item"
	GroupPage   = "page"
	GroupDanger = "danger"
)

type Action struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Key     string `json:"key,omitempty"`
	Primary bool   `json:"primary,omitempty"`
	Group   string `json:"group,omitempty"`
	Confirm string `json:"confirm,omitempty"`
	// Hidden actions only answer their key (a calculator's digits); they
	// are left out of menus and the command palette.
	Hidden bool `json:"hidden,omitempty"`
	// Input: on a notification, the action asks for text first (a quick
	// reply) on a system screen; this is the input's placeholder, and the
	// text comes back in the notif_action message's Text.
	Input string `json:"input,omitempty"`
}

type Node struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`

	Text  string `json:"text,omitempty"`
	Style string `json:"style,omitempty"`

	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Trailing string `json:"trailing,omitempty"`
	Badge    string `json:"badge,omitempty"`
	Icon     string `json:"icon,omitempty"`

	Value       string `json:"value,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Submit      string `json:"submit,omitempty"`
	Clear       bool   `json:"clear,omitempty"`
	// Keyboard hints the input method: "" (text), "number", "phone" (digits
	// bypass pinyin) or "email" (starts in English).
	Keyboard string `json:"keyboard,omitempty"`
	// Live inputs send a "changed" action on every edit (search as you
	// type); others report their value only with an action.
	Live bool `json:"live,omitempty"`

	Src    string `json:"src,omitempty"`
	Stream string `json:"stream,omitempty"`
	Pip    string `json:"pip,omitempty"`
	Rows   int    `json:"rows,omitempty"`
	Label  string `json:"label,omitempty"`
	Secure bool   `json:"secure,omitempty"`

	Fraction float64 `json:"fraction,omitempty"`

	Canvas *Canvas `json:"canvas,omitempty"`

	Actions []Action `json:"actions,omitempty"`
}

// Item styles.
const (
	StyleBubbleIn  = "bubble-in"
	StyleBubbleOut = "bubble-out"
	StyleTile      = "tile" // grid cell, see Screen.Columns
)

// Text styles.
const (
	StyleDim    = "dim"
	StyleAccent = "accent"
	StyleTitle  = "title"
	StyleOK     = "ok"
	StyleWarn   = "warn"
	StyleBig    = "big"
	// StyleDisplay is one line as large as the width allows (a calculator's
	// display, a player's title).
	StyleDisplay = "display"
)

// Screen layouts: where content shorter than the screen sits.
const (
	LayoutTop    = ""
	LayoutCenter = "center" // around the optical centre, a little below the middle (players)
	LayoutBottom = "bottom" // against the soft keys, under the thumb (calculators, keypads)
)

type Screen struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    []Node   `json:"body"`
	Actions []Action `json:"actions,omitempty"`
	Right   string   `json:"right,omitempty"`
	// Center is the page action OK runs while no element has focus (the
	// idle screen's Menu).
	Center string `json:"center,omitempty"`
	Focus  string `json:"focus,omitempty"`
	Bottom bool   `json:"bottom,omitempty"`
	// Columns lays consecutive "tile" items out as a grid (home screen,
	// calculator keys); arrows then move in two dimensions.
	Columns int `json:"columns,omitempty"`
	// Layout places content that does not fill the screen: LayoutTop,
	// LayoutCenter or LayoutBottom.
	Layout string `json:"layout,omitempty"`
}

type Notification struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind,omitempty"` // "" (message) | "call"
	Title   string   `json:"title"`
	Body    string   `json:"body,omitempty"`
	Actions []Action `json:"actions,omitempty"`
}

// Message types, app -> shell.
const (
	MsgHello   = "hello"
	MsgPush    = "push"
	MsgReplace = "replace"
	MsgUpdate  = "update"
	MsgPop     = "pop"
	MsgReset   = "reset"
	MsgToast   = "toast"
	MsgNotify  = "notify"
	MsgReq     = "req"
	MsgPong    = "pong" // answers ping with its value
	// MsgState hands over (in Data, at most MaxSavedState bytes of JSON) what
	// the app needs to rebuild its pages if the system ends it in the
	// background; a new process gets it back in start's Data.
	MsgState = "state"
)

// MaxSavedState bounds MsgState's Data.
const MaxSavedState = 64 << 10

// Message types, shell -> app.
const (
	MsgStart       = "start"
	MsgAction      = "action"
	MsgPopped      = "popped"
	MsgLifecycle   = "lifecycle"
	MsgRes         = "res"
	MsgEvent       = "event"
	MsgNotifAction = "notif_action"
	// MsgPing checks the app is responsive: answer pong from the loop that
	// handles actions, so a stuck handler delays it (ANR detection).
	MsgPing = "ping"
	// MsgResync comes from a Shell that took over this connection after the
	// previous one crashed or restarted: send the page stack again (reset,
	// then push the rest). Requests the previous Shell had not answered get
	// no answer.
	MsgResync = "resync"
)

// Msg is the single envelope for every message in both directions; unused
// fields are omitted on the wire.
type Msg struct {
	T string `json:"t"`

	App   string `json:"app,omitempty"`
	Proto string `json:"proto,omitempty"`

	Screen   *Screen           `json:"screen,omitempty"`
	ScreenID string            `json:"screen_id,omitempty"`
	Node     string            `json:"node,omitempty"`
	Action   string            `json:"action,omitempty"`
	Value    string            `json:"value,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`

	Text  string        `json:"text,omitempty"`
	Notif *Notification `json:"notif,omitempty"`
	State string        `json:"state,omitempty"`

	Req int    `json:"req,omitempty"`
	Cap string `json:"cap,omitempty"`
	// Origin names content the app hosts (e.g. a mini-program) on whose behalf
	// it asks; system dialogs show it next to the app name. Attribution only:
	// the app stays accountable and its own permissions still apply.
	Origin string `json:"origin,omitempty"`
	// Purposes go with Origin: why the hosted content asks for each runtime
	// permission, from its own manifest, shown in the system's dialog; the
	// host vouches for them as it does for Origin.
	Purposes map[string]string `json:"purposes,omitempty"`
	Args     json.RawMessage   `json:"args,omitempty"`
	OK       bool              `json:"ok,omitempty"`
	Err      string            `json:"err,omitempty"`
	Data     json.RawMessage   `json:"data,omitempty"`
	// FDs is how many open files travel with this message (SCM_RIGHTS on
	// the app's socket), e.g. the shared ring of an audio stream.
	FDs int `json:"fds,omitempty"`

	Event string `json:"event,omitempty"`
}

// Focusable reports whether the shell gives keyboard focus to the node.
func (n *Node) Focusable() bool {
	switch n.Kind {
	case KindInput, KindButton, KindWeb:
		return true
	case KindItem, KindMedia, KindImage, KindDoc:
		return len(n.Actions) > 0
	case KindCanvas:
		return n.Canvas != nil && n.Canvas.Keys || len(n.Actions) > 0
	}
	return false
}

// PrimaryAction returns the action executed by OK, if any.
func (n *Node) PrimaryAction() *Action {
	for i := range n.Actions {
		if n.Actions[i].Primary {
			return &n.Actions[i]
		}
	}
	if n.Kind == KindButton && len(n.Actions) > 0 {
		return &n.Actions[0]
	}
	return nil
}
