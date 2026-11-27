// The manifest editor's decisions, without VS Code: the form's state read
// from manifest.json and written back into it (fields the form does not
// show are kept as they were), what kario manifest check -json reports,
// and the webview's page. The rules are kario's: the form only shows what
// kario manifest check says, beside the field it names.

export interface Problem {
  field: string;
  message: string;
}

export interface Permission {
  name: string;
  label: string;
  runtime: boolean;
  data?: string;
}

export interface Schema {
  runtime: string;
  main: string;
  permissions: Permission[];
  max_purpose: number;
  privacy_data: string[];
  privacy_uses: string[];
  net_permission: string;
  languages: string[];
}

export interface Report {
  ok: boolean;
  problems: Problem[];
  schema: Schema;
}

/** parseReport reads kario manifest check -json. */
export function parseReport(text: string): Report {
  const r = JSON.parse(text);
  if (!r || typeof r.ok !== "boolean" || !Array.isArray(r.problems) || !r.schema || !Array.isArray(r.schema.permissions)) {
    throw new Error("kario manifest check -json: not a report");
  }
  return r as Report;
}

export interface Collected {
  data: string;
  use: string[];
  linked?: boolean;
  shared?: boolean;
}

/** What the form edits. */
export interface Form {
  id: string;
  name: string;
  version: string;
  description: string;
  icon: string;
  icon_image: string;
  lang: string;
  permissions: string[];
  purposes: Record<string, string>;
  /** Whether the manifest has a privacy label at all (the Kario store refuses an app with network access without one). */
  privacy: boolean;
  collects: Collected[];
  local: string[];
}

const TEXT_FIELDS = ["id", "name", "version", "description", "icon", "icon_image", "lang"] as const;

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

/** formOf reads the form's state from a manifest's text; undefined when it is not a JSON object. */
export function formOf(text: string): Form | undefined {
  let m: Record<string, unknown>;
  try {
    m = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (!m || typeof m !== "object" || Array.isArray(m)) return undefined;
  const purposes: Record<string, string> = {};
  if (m.purposes && typeof m.purposes === "object") {
    for (const [k, v] of Object.entries(m.purposes as Record<string, unknown>)) purposes[k] = str(v);
  }
  const p = m.privacy && typeof m.privacy === "object" ? (m.privacy as Record<string, unknown>) : undefined;
  const collects: Collected[] = [];
  if (p && Array.isArray(p.collects)) {
    for (const c of p.collects as Record<string, unknown>[]) {
      if (!c || typeof c.data !== "string") continue;
      collects.push({ data: c.data, use: strings(c.use), linked: c.linked === true, shared: c.shared === true });
    }
  }
  const f = {} as Form;
  for (const k of TEXT_FIELDS) f[k] = str(m[k]);
  return { ...f, permissions: strings(m.permissions), purposes, privacy: !!p, collects, local: p ? strings(p.local) : [] };
}

/**
 * applyForm writes the form into a manifest's text: the fields it shows
 * set (empty optional ones removed), purposes only for runtime permissions
 * the app declares, the label as the form has it; every other field kept,
 * in its place. Two-space JSON, as kario new writes it.
 */
export function applyForm(text: string, f: Form, schema: Schema): string {
  let m: Record<string, unknown> = {};
  try {
    const v = JSON.parse(text);
    if (v && typeof v === "object" && !Array.isArray(v)) m = v;
  } catch {
    // Not JSON: the form starts it again.
  }
  const required = new Set(["id", "name"]);
  for (const k of TEXT_FIELDS) {
    if (f[k] !== "" || required.has(k)) m[k] = f[k];
    else delete m[k];
  }
  m.runtime ??= schema.runtime;
  m.main ??= schema.main;
  m.permissions = [...f.permissions];
  const runtime = new Set(schema.permissions.filter((p) => p.runtime).map((p) => p.name));
  const purposes: Record<string, string> = {};
  for (const p of f.permissions) if (runtime.has(p)) purposes[p] = f.purposes[p] ?? "";
  if (Object.keys(purposes).length) m.purposes = purposes;
  else delete m.purposes;
  if (f.privacy) {
    const collects = f.collects.map((c) => {
      const o: Record<string, unknown> = { data: c.data, use: [...c.use] };
      if (c.linked) o.linked = true;
      if (c.shared) o.shared = true;
      return o;
    });
    const label: Record<string, unknown> = {};
    if (collects.length) label.collects = collects;
    if (f.local.length) label.local = [...f.local];
    m.privacy = label;
  } else {
    delete m.privacy;
  }
  return JSON.stringify(m, null, 2) + "\n";
}

/** problemsByField groups the report's problems under the fields the form shows; the rest go under "". */
export function problemsByField(problems: Problem[], f: Form, schema: Schema): Map<string, string[]> {
  const shown = new Set<string>(["", ...TEXT_FIELDS, "privacy"]);
  for (const p of schema.permissions) shown.add("permissions." + p.name).add("purposes." + p.name);
  for (const p of f.permissions) shown.add("permissions." + p);
  const out = new Map<string, string[]>();
  for (const p of problems) {
    const k = shown.has(p.field) ? p.field : "";
    out.set(k, [...(out.get(k) ?? []), p.message]);
  }
  return out;
}

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const LABELS: Record<string, string> = {
  id: "Id (reverse domain name)",
  name: "Name",
  version: "Version",
  description: "Description",
  icon: "Icon (one character, text UI)",
  icon_image: "Icon image (PNG beside manifest.json)",
  lang: "Language of the manifest's texts",
};

/**
 * editorHTML is the webview's page: the form, every problem kario reported
 * beside its field (and all of them at the top), and the script that posts
 * the form's state ({type: "edit", form}) on every change.
 */
export function editorHTML(f: Form, report: Report, nonce: string, cspSource: string): string {
  const s = report.schema;
  const byField = problemsByField(report.problems, f, s);
  const errs = (field: string) =>
    (byField.get(field) ?? []).map((m) => `<div class="problem" data-for="${esc(field)}">${esc(m)}</div>`).join("");
  const text = (k: (typeof TEXT_FIELDS)[number]) => {
    if (k === "lang") {
      const opts = ["", ...s.languages].map((l) => `<option value="${esc(l)}"${l === f.lang ? " selected" : ""}>${esc(l || "(not set)")}</option>`);
      return `<label>${LABELS[k]}<select data-text="lang">${opts.join("")}</select></label>${errs(k)}`;
    }
    return `<label>${LABELS[k]}<input data-text="${k}" value="${esc(f[k])}"></label>${errs(k)}`;
  };
  const perms = s.permissions
    .map((p) => {
      const on = f.permissions.includes(p.name);
      const purpose = p.runtime
        ? `<input class="purpose" data-purpose="${esc(p.name)}" maxlength="${s.max_purpose}" placeholder="Why the app asks (shown in the system's dialog)" value="${esc(f.purposes[p.name] ?? "")}"${on ? "" : " disabled"}>${errs("purposes." + p.name)}`
        : "";
      return `<div class="perm"><label><input type="checkbox" data-perm="${esc(p.name)}"${on ? " checked" : ""}> <b>${esc(p.name)}</b> ${esc(p.label)}${p.runtime ? " <i>(asked at first use)</i>" : ""}${p.data ? ` <i>(opens ${esc(p.data)})</i>` : ""}</label>${purpose}${errs("permissions." + p.name)}</div>`;
    })
    .join("");
  const rows = s.privacy_data
    .map((d) => {
      const c = f.collects.find((x) => x.data === d);
      const uses = s.privacy_uses
        .map((u) => `<label><input type="checkbox" data-use="${esc(d)}:${esc(u)}"${c?.use.includes(u) ? " checked" : ""}> ${esc(u)}</label>`)
        .join(" ");
      return `<tr><td>${esc(d)}</td><td><input type="checkbox" data-collect="${esc(d)}"${c ? " checked" : ""}></td><td>${uses}</td><td><input type="checkbox" data-linked="${esc(d)}"${c?.linked ? " checked" : ""}></td><td><input type="checkbox" data-shared="${esc(d)}"${c?.shared ? " checked" : ""}></td><td><input type="checkbox" data-local="${esc(d)}"${f.local.includes(d) ? " checked" : ""}></td></tr>`;
    })
    .join("");
  const all = report.problems.length
    ? `<ul class="problems">${report.problems.map((p) => `<li>${esc(p.message)}</li>`).join("")}</ul>`
    : `<p class="ok">kario manifest check: no problems</p>`;
  return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${cspSource} 'nonce-${nonce}'; script-src 'nonce-${nonce}';">
<style nonce="${nonce}">
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 0 1em 2em; }
label { display: block; margin: .3em 0; }
input:not([type]), select { display: block; width: 100%; max-width: 40em; }
.perm { margin: .4em 0; } .perm .purpose { margin-left: 1.6em; width: calc(100% - 1.6em); }
.problem, .problems { color: var(--vscode-errorForeground); } .ok { color: var(--vscode-testing-iconPassed); }
table { border-collapse: collapse; } td, th { padding: .2em .6em; text-align: left; } td label { display: inline; }
</style>
</head>
<body>
<h2>manifest.json</h2>
${all}
${errs("")}
<h3>App</h3>
${TEXT_FIELDS.map(text).join("\n")}
<h3>Permissions</h3>
${perms}
<h3>Privacy label</h3>
<label><input type="checkbox" id="privacy"${f.privacy ? " checked" : ""}> The manifest has a privacy label (the Kario store refuses an app with network access without one)</label>
${errs("privacy")}
<table id="label"${f.privacy ? "" : ' hidden'}>
<tr><th>Data</th><th>Sent off the phone</th><th>Used for</th><th>Linked to the user</th><th>Shared</th><th>Kept on the phone</th></tr>
${rows}
</table>
<script nonce="${nonce}">
const vscode = acquireVsCodeApi();
const q = (sel) => [...document.querySelectorAll(sel)];
function state() {
  const f = { permissions: [], purposes: {}, collects: [], local: [], privacy: document.getElementById("privacy").checked };
  for (const e of q("[data-text]")) f[e.dataset.text] = e.value;
  for (const e of q("[data-perm]")) if (e.checked) f.permissions.push(e.dataset.perm);
  for (const e of q("[data-purpose]")) {
    e.disabled = !f.permissions.includes(e.dataset.purpose);
    if (e.value) f.purposes[e.dataset.purpose] = e.value;
  }
  for (const e of q("[data-collect]")) {
    if (!e.checked) continue;
    const d = e.dataset.collect;
    const use = q("[data-use]").filter((u) => u.checked && u.dataset.use.startsWith(d + ":")).map((u) => u.dataset.use.slice(d.length + 1));
    f.collects.push({ data: d, use, linked: q('[data-linked="' + d + '"]')[0].checked, shared: q('[data-shared="' + d + '"]')[0].checked });
  }
  for (const e of q("[data-local]")) if (e.checked) f.local.push(e.dataset.local);
  document.getElementById("label").hidden = !f.privacy;
  return f;
}
document.addEventListener("input", () => vscode.postMessage({ type: "edit", form: state() }));
window.addEventListener("message", (ev) => {
  if (ev.data.type !== "problems") return;
  for (const e of q(".problem")) e.remove();
  const list = document.querySelector(".problems, .ok");
  list.outerHTML = ev.data.problems.length ? "<ul class=problems></ul>" : "<p class=ok>kario manifest check: no problems</p>";
  const ul = document.querySelector(".problems");
  for (const p of ev.data.problems) {
    if (ul) { const li = document.createElement("li"); li.textContent = p.message; ul.appendChild(li); }
    const [kind, name] = p.field.split(/\\.(.*)/);
    const where = kind === "purposes" ? document.querySelector('[data-purpose="' + CSS.escape(name) + '"]')
      : kind === "permissions" ? document.querySelector('[data-perm="' + CSS.escape(name) + '"]')
      : kind === "privacy" ? document.getElementById("privacy")
      : p.field ? document.querySelector('[data-text="' + CSS.escape(p.field) + '"]') : null;
    if (!where) continue;
    const div = document.createElement("div");
    div.className = "problem";
    div.textContent = p.message;
    (where.closest("label") || where).after(div);
  }
});
</script>
</body>
</html>`;
}
