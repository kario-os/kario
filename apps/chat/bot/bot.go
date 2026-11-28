// Package bot is what the simulated chat server answers: Zhang San
// (zhangsan) replies to everything, and calls back when asked for a video
// call. The server (cmd/phone-chatsrv) uses it; the app falls back to it
// locally when it runs without a server.
package bot

import (
	"html"
	"strings"
	"time"
)

// Base is the server's URL, for the article Zhang San recommends (empty
// when the app plays the bot itself).
var Base string

// In the pages below, messages in element content are HTML; those in
// attribute values and scripts are escaped for their place.

// attr is s as an HTML attribute value.
func attr(s string) string { return html.EscapeString(s) }

var jsEscape = strings.NewReplacer(`\`, `\\`, `'`, `\x27`, `"`, `\x22`, `&`, `\x26`, `<`, `\x3c`, `>`, `\x3e`, "\n", `\n`)

// jsQuote is s as a JavaScript string literal, also safe inside a
// double-quoted attribute.
func jsQuote(s string) string { return "'" + jsEscape.Replace(s) + "'" }

// jsFormat is a JavaScript expression for format with each %s replaced by
// the next of the JavaScript expressions args.
func jsFormat(format string, args ...string) string {
	var parts []string
	for i, lit := range strings.Split(format, "%s") {
		if i > 0 && i <= len(args) {
			parts = append(parts, args[i-1])
		}
		if lit != "" {
			parts = append(parts, jsQuote(lit))
		}
	}
	if len(parts) == 0 {
		return "''"
	}
	return strings.Join(parts, "+")
}

// account is the title of a page of the Hongyan official account.
func account(title string) string { return tr.T("%s | Hongyan Official Account", title) }

// Article is the page at Base+"/article": an official-account post, with
// the clutter real pages have around it (navigation, an ad, a footer,
// scripts).
var Article = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>` + account(tr.T("The keyboard phone's second spring")) + `</title>
<style>body{font-family:sans-serif;margin:10px;line-height:1.5}h1{font-size:21px}img{display:block;width:100%;height:60px;background:#dde}aside{color:#a33;font-size:12px}</style>
<script>window.track && track()</script></head><body>
<header><nav><a href="/">` + tr.T("Home") + `</a> <a href="/login">` + tr.T("Log in") + `</a> <a href="/app">` + tr.T("Get the app") + `</a></nav></header>
<aside><p>` + tr.T("Ad: limited-time offer! Click to claim a huge red envelope. Miss it today and wait another year. Act now!") + `</p></aside>
<div class="post">
<h1>` + tr.T("The keyboard phone's second spring") + `</h1>
<p>` + tr.T("Small screen, real keys, a week on a charge: these phones are back. Not just for nostalgia: %s is what sells them.", "<strong>"+tr.T("focus")+"</strong>") + `</p>
<p>` + tr.T("The new system writes its interface as semantic nodes, so it works on a 9-key pad too; %s gets a post of its own.", `<a href="/arch">`+tr.T("Platform architecture")+"</a>") + `</p>
<img src="/phone.png" alt="` + attr(tr.T("Photo of the sample phone")) + `">
<ul><li>` + tr.T("Reliable keys: no mistakes typing blind") + `</li><li>` + tr.T("The system pops up payment confirmations") + `</li></ul>
<button onclick="phone.call('share.open',{kind:'text',text:document.title+' '+location.href}).then(function(){document.title=` + jsQuote(tr.T("Shared")) + `},function(e){document.title=` + jsFormat(tr.T("Not shared: %s"), "e.message") + `})">` + tr.T("Share this article") + `</button>
</div>
<footer><p>` + tr.T("This article is copyright of the Hongyan official account.") + `</p></footer></body></html>`

// Arch is Base+"/arch", where the article links.
var Arch = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>` + account(tr.T("Platform architecture")) + `</title></head><body style="font-family:sans-serif;margin:10px">
<h1 style="font-size:21px">` + tr.T("Platform architecture") + `</h1><p>` + tr.T("The shell, the renderer and the system services each run as a process of their own; apps speak only phone.Ui.") + `</p></body></html>`

// Search is Base+"/search": a site search with a text field, a list box and
// a confirm dialog, for the web view's form handling.
var Search = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>` + account(tr.T("Site search")) + `</title></head><body style="font-family:sans-serif;margin:10px">
<p><button onclick="document.title=confirm(` + jsQuote(tr.T("Clear the search history?")) + `)?` + jsQuote(tr.T("Cleared")) + `:` + jsQuote(tr.T("Not cleared")) + `">` + tr.T("Clear history") + `</button></p>
<form action="/search" method="get"><input name="q" placeholder="` + attr(tr.T("Search articles")) + `" style="width:6em">
<select name="sort" aria-label="` + attr(tr.T("Sort")) + `"><option value="new">` + tr.T("Newest") + `</option><option value="hot">` + tr.T("Most popular") + `</option></select>
<button>` + tr.T("Search") + `</button></form>
<script>const q=new URLSearchParams(location.search);if(q.get('q')){document.title=` + jsFormat(tr.T("Search: %s (%s)"), "q.get('q')", "(q.get('sort')==='hot'?"+jsQuote(tr.T("Most popular"))+":"+jsQuote(tr.T("Newest"))+")") + `}</script>
</body></html>`

// Peers the server knows by id; only zhangsan is a bot.
var Peers = map[string]string{"zhangsan": tr.T("Zhang San"), "lisi": tr.T("Li Si"), "team": tr.T("Project team")}

func IsBot(peer string) bool { return peer == "zhangsan" }

// Message is what the user sent.
type Message struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // text | photo | voice | ring (ask to be called)
	Text string `json:"text,omitempty"`
	Secs int    `json:"secs,omitempty"`
}

// Reply is one thing the peer does after a delay: a message or a call.
type Reply struct {
	After time.Duration
	Call  bool
	Text  string
}

// mentions reports whether text has the keyword word, which is in the
// user's language.
func mentions(text, word string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(word))
}

func Replies(m Message) []Reply {
	if !IsBot(m.To) {
		return nil
	}
	switch m.Kind {
	case "photo":
		return []Reply{{After: 900 * time.Millisecond, Text: tr.T("Nice shot!")}}
	case "voice":
		return []Reply{{After: 900 * time.Millisecond, Text: tr.T("Got your voice message (%d s)", max(1, m.Secs))}}
	case "ring":
		return []Reply{{After: 3 * time.Second, Call: true}}
	}
	if mentions(m.Text, tr.T("article")) && Base != "" {
		return []Reply{{After: 900 * time.Millisecond, Text: tr.T("A good read: %s", Base+"/article")}}
	}
	if mentions(m.Text, tr.T("search")) && Base != "" {
		return []Reply{{After: 900 * time.Millisecond, Text: tr.T("Site search: %s", Base+"/search")}}
	}
	if mentions(m.Text, tr.T("video")) {
		return []Reply{{After: 900 * time.Millisecond, Text: tr.T("OK, I'll call you")}, {After: 2500 * time.Millisecond, Call: true}}
	}
	return []Reply{{After: 900 * time.Millisecond, Text: tr.T("Got it: %s", m.Text)}}
}

// Tools is the browser test's page: an ad the content blocker drops, a
// download, a file field, a prompt(), a video download and a cookie.
var Tools = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>` + tr.T("Tools page") + `</title><script src="/phone-test-ad.js"></script></head>
<body style="font-family:sans-serif;margin:12px">
<h3>` + tr.T("Browser test page") + `</h3>
<div class="phone-test-ad" style="background:#fc0;padding:8px">` + tr.T("Ad slot") + `</div>
<p><a href="/manual.txt">` + tr.T("Download the manual") + `</a></p>
<p><input type="file" id="f" style="width:6em"></p>
<p><button id="name">` + tr.T("Give it a name") + `</button></p>
<p><a href="/clip.webm">` + tr.T("Download the video") + `</a></p>
<p>` + tr.T("A keyboard phone can browse the web properly too.") + `</p>
<script>
document.cookie = "phone_test=1; max-age=3600; path=/";
setTimeout(function () { document.title = window.phoneAd ? ` + jsQuote(tr.T("Tools page: the ad loaded")) + ` : ` + jsQuote(tr.T("Tools page: no ad")) + `; }, 300);
document.getElementById("f").onchange = function (e) { document.title = ` + jsFormat(tr.T("Chosen: %s"), "e.target.files[0].name") + `; };
document.getElementById("name").onclick = function () { var n = prompt(` + jsQuote(tr.T("Name this page")) + `, ` + jsQuote(tr.T("Example")) + `); document.title = n === null ? ` + jsQuote(tr.T("No name given")) + ` : ` + jsFormat(tr.T("Name: %s"), "n") + `; };
</script>
</body></html>`
