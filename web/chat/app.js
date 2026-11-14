// Web version of apps/chat for the semantic-vs-web comparison. Everything the
// phone.Ui shell provides for free (focus, menus, back stack, softkeys, toast)
// has to be implemented here by the app itself; it is marked "keyboard layer".
"use strict";

// The page's text is English, translated by <lang>.json beside the page
// ({"English": "translation"}); lang is ?lang=, zh-Hans by default.
const LANG = new URLSearchParams(location.search).get("lang") || "zh-Hans";
let messages = {};

// t is msg in the page's language, its %s and %d replaced by args in order.
function t(msg, ...args) {
  const s = Object.prototype.hasOwnProperty.call(messages, msg) ? messages[msg] : msg;
  if (!args.length) return s;
  return s.replace(/%([sd%])/g, (verb, v) => {
    if (v === "%") return "%";
    if (!args.length) return verb;
    const a = args.shift();
    return v === "d" ? String(Math.trunc(Number(a))) : String(a);
  });
}

const PHOTO = "data:image/svg+xml," + encodeURIComponent(
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 50"><rect width="120" height="50" fill="#8fa0e6"/>' +
  '<circle cx="90" cy="14" r="7" fill="#fadc5a"/><path d="M0 34 L30 22 L55 34 L80 20 L120 30 L120 50 L0 50Z" fill="#5a8c50"/></svg>');

let nextId = 0;
const now = Date.now();
function msg(from, text, img, ago) { return { id: ++nextId, from, text, img, at: new Date(now - ago * 60000) }; }
const convos = [];
function loadConvos() {
  convos.push(
    { id: "zhangsan", name: t("Zhang San"), icon: t("Z"), bot: true, unread: 0, msgs: [
      msg("zhangsan", t("Going hiking this weekend?"), "", 50), msg("me", t("Sure, what time do we leave?"), "", 45),
      msg("zhangsan", t("Eight in the morning. Have a look at this photo, from last time"), PHOTO, 40)] },
    { id: "lisi", name: t("Li Si"), icon: t("L"), unread: 2, msgs: [
      msg("lisi", t("I sent the contract to your email"), "", 180), msg("lisi", t("Remember to reply before you leave work today"), "", 120)] },
    { id: "team", name: t("Product group (%d)", 5), icon: t("G"), unread: 5, msgs: [
      msg("wangwu", t("%s: the new version ships tonight", t("Wang Wu")), "", 20)] },
    { id: "filehelper", name: t("File Transfer"), icon: t("F"), unread: 0, msgs: [msg("me", t("Invoice.pdf"), "", 1560)] },
  );
}
const hhmm = (d) => d.toTimeString().slice(0, 5);
const last = (c) => c.msgs[c.msgs.length - 1];
const preview = (m) => (m.img && !m.text ? t("[Photo]") : m.text);
const esc = (s) => String(s).replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[ch]));

// Screens: each returns { title, html, actions (page menu), right, focusables }.
function listScreen() {
  const sorted = [...convos].sort((a, b) => last(b).at - last(a).at);
  return {
    id: "list", title: t("Chat"), right: { label: t("New chat"), run: () => toast(t("Not in the prototype: %s", t("New chat"))) },
    actions: [{ label: t("New chat"), key: "n", run: () => toast(t("Not in the prototype: %s", t("New chat"))) }],
    html: sorted.map((c) => `<div class="item" data-focus="c:${c.id}"><div class="icon">${c.icon}</div>
      <div class="main"><div class="t">${esc(c.name)}</div><div class="s">${esc(preview(last(c)))}</div></div>
      <div class="r">${hhmm(last(c).at)}<br>${c.unread ? `<span class="badge">${c.unread}</span>` : ""}</div></div>`).join(""),
    primary: (id) => ({ label: t("Open"), run: () => openChat(id.slice(2)) }),
    itemActions: (id) => [
      { label: t("Open"), run: () => openChat(id.slice(2)) },
      { label: t("Mark as unread"), key: "u", run: () => { find(id.slice(2)).unread ||= 1; render(); } },
      { label: t("Delete chat"), danger: true, run: () => { convos.splice(convos.indexOf(find(id.slice(2))), 1); render(); } },
    ],
  };
}

function chatScreen(c) {
  return {
    id: "chat:" + c.id, title: c.name, bottom: true,
    right: { label: t("Video call"), run: () => toast(t("The web version needs WebRTC and system permission; not implemented")) },
    actions: [{ label: t("Video call"), key: "v", run: () => toast(t("The web version needs WebRTC and system permission; not implemented")) },
      { label: t("Send photo"), key: "p", run: () => send(c, "", PHOTO) }],
    html: c.msgs.map((m) => `<div class="msg ${m.from === "me" ? "out" : ""}" data-focus="m:${m.id}">
      <div class="meta">${hhmm(m.at)}</div><div class="bubble">${esc(m.text)}${m.img ? `<img src="${m.img}" alt="${esc(t("Photo"))}">` : ""}</div></div>`).join("") +
      `<div class="input" data-focus="compose"><input id="compose" placeholder="${esc(t("Type a message…"))}" autocomplete="off"></div>`,
    primary: (id) => (id === "compose" ? { label: t("Send"), run: () => submit(c) } : null),
    itemActions: (id) => {
      if (id === "compose") return [];
      const m = c.msgs.find((x) => "m:" + x.id === id);
      return [
        { label: t("Copy"), key: "c", run: () => toast(t("Copied")) },
        { label: t("Forward"), key: "f", run: () => push(forwardScreen(m)) },
        { label: t("Quote"), key: "q", run: () => { draft = t("\"%s\" ", preview(m)); render(); focusId("compose"); } },
        { label: t("Delete"), danger: true, run: () => { c.msgs.splice(c.msgs.indexOf(m), 1); render(); } },
      ];
    },
  };
}

function forwardScreen(m) {
  return {
    id: "fwd", title: t("Forward to…"),
    html: convos.map((c) => `<div class="item" data-focus="c:${c.id}"><div class="icon">${c.icon}</div><div class="main"><div class="t">${esc(c.name)}</div></div></div>`).join(""),
    primary: (id) => ({ label: t("Send"), run: () => { const to = find(id.slice(2)); pop(); send(to, m.text, m.img); toast(t("Forwarded to %s", to.name)); } }),
    itemActions: () => [],
  };
}

const find = (id) => convos.find((c) => c.id === id);
let draft = "";

function openChat(id) { const c = find(id); c.unread = 0; push(chatScreen(c)); }
function send(c, text, img) {
  c.msgs.push(msg("me", text, img, 0));
  rebuild();
  if (c.bot) setTimeout(() => { c.msgs.push(msg(c.id, img ? t("Nice shot!") : t("Got it: %s", text), "", 0)); rebuild(); }, 900);
}
function submit(c) {
  const v = document.getElementById("compose").value.trim();
  if (!v) return;
  draft = "";
  send(c, v, "");
}

// ---- keyboard layer: focus, menus, back stack, softkeys, toast ----
const stack = [];
let focus = null, menu = null;

function push(s) { stack.push({ s, focus: null }); render(); }
function pop() { if (stack.length > 1) { stack.pop(); render(); } }
function topView() { return stack[stack.length - 1]; }

function rebuild() {
  for (const v of stack) {
    if (v.s.id === "list") v.s = listScreen();
    else if (v.s.id.startsWith("chat:")) v.s = chatScreen(find(v.s.id.slice(5)));
  }
  render();
}

function focusables() { return [...document.querySelectorAll("#body [data-focus]")]; }
function focusId(id) {
  const el = document.querySelector(`#body [data-focus="${id}"]`);
  if (!el) return;
  document.querySelectorAll("#body .focus").forEach((e) => e.classList.remove("focus"));
  el.classList.add("focus");
  el.scrollIntoView({ block: "nearest" });
  focus = topView().focus = id;
  const input = el.querySelector("input");
  if (input) input.focus(); else if (document.activeElement) document.activeElement.blur();
  softkeys();
}

function render() {
  const v = topView();
  document.getElementById("title").textContent = v.s.title;
  const body = document.getElementById("body");
  body.innerHTML = v.s.html;
  const input = document.getElementById("compose");
  if (input) {
    input.value = draft;
    input.addEventListener("input", () => (draft = input.value));
  }
  const fs = focusables();
  const want = v.focus && fs.find((e) => e.dataset.focus === v.focus) ? v.focus
    : (v.s.bottom ? fs[fs.length - 1] : fs[0])?.dataset.focus;
  if (want) focusId(want);
  if (v.s.bottom) body.scrollTop = body.scrollHeight;
  softkeys();
}

function softkeys() {
  const v = topView(), p = focus && v.s.primary(focus);
  document.getElementById("center").textContent = p ? "⏎" + p.label : (focus ? "⏎" + t("Menu") : "");
  document.getElementById("right").textContent = v.s.right ? "F2" + v.s.right.label : "";
}

function toast(text) {
  const t = document.getElementById("toast");
  t.textContent = text; t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), 2500);
}

function openMenu() {
  const v = topView();
  const entries = [];
  if (focus) v.s.itemActions(focus).forEach((a) => entries.push({ group: t("Selected"), ...a }));
  (v.s.actions || []).forEach((a) => entries.push({ group: t("This page"), ...a }));
  entries.push({ group: t("System"), label: t("Back"), run: pop });
  menu = { entries, sel: 0 };
  drawMenu();
}

function drawMenu() {
  const el = document.getElementById("menu");
  if (!menu) { el.hidden = true; return; }
  let html = "", group = "";
  menu.entries.forEach((e, i) => {
    if (e.group !== group) { html += `<div class="group">${e.group}</div>`; group = e.group; }
    html += `<div class="entry ${i === menu.sel ? "sel" : ""}"><span>${esc(e.label)}</span><span>${e.key || ""}</span></div>`;
  });
  el.innerHTML = html; el.hidden = false;
}

function runEntry(e) {
  menu = null; drawMenu();
  if (e.danger && !confirm(t("Really %s?", e.label))) return;
  e.run();
}

document.addEventListener("keydown", (ev) => {
  if (!stack.length) return;
  const v = topView();
  if (menu) {
    if (ev.key === "ArrowDown" || ev.key === "Tab") menu.sel = Math.min(menu.sel + 1, menu.entries.length - 1);
    else if (ev.key === "ArrowUp") menu.sel = Math.max(menu.sel - 1, 0);
    else if (ev.key === "Enter") return runEntry(menu.entries[menu.sel]);
    else if (ev.key === "Escape") menu = null;
    else { const e = menu.entries.find((x) => x.key === ev.key); if (e) return runEntry(e); }
    ev.preventDefault(); drawMenu(); return;
  }
  const fs = focusables().map((e) => e.dataset.focus);
  const i = fs.indexOf(focus);
  const inInput = focus === "compose";
  switch (ev.key) {
    case "ArrowDown": focusId(fs[Math.min(i + 1, fs.length - 1)]); break;
    case "ArrowUp": focusId(fs[Math.max(i - 1, 0)]); break;
    case "Enter": { const p = focus && v.s.primary(focus); if (p) p.run(); else if (focus) openMenu(); break; }
    case "Tab": openMenu(); break;
    case "Escape": pop(); break;
    case "F2": if (v.s.right) v.s.right.run(); break;
    default:
      if (inInput) return;
      if (ev.key.length === 1 && focus) {
        const a = v.s.itemActions(focus).concat(v.s.actions || []).find((x) => x.key === ev.key);
        if (a) { a.run(); break; }
        if (fs.includes("compose")) { focusId("compose"); return; }
      }
      return;
  }
  ev.preventDefault();
});

setInterval(() => (document.getElementById("clock").textContent = hhmm(new Date())), 1000);
document.getElementById("clock").textContent = hhmm(new Date());
// Without the translations (none for the language, or a file: page) the
// page shows its English.
fetch(LANG + ".json")
  .then((r) => (r.ok ? r.json() : {}))
  .catch(() => ({}))
  .then((m) => {
    messages = m;
    document.title = t("Chat (web version)");
    document.getElementById("options").textContent = "Tab" + t("Options");
    loadConvos();
    push(listScreen());
    // Benchmark hook: report first render to the measuring server.
    if (location.protocol.startsWith("http")) requestAnimationFrame(() => fetch("/ready").catch(() => {}));
  });
