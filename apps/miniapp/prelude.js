// Helpers every mini-program gets; they only build phone.Ui JSON nodes.
var ui = {
  text: function (t, style) { return { kind: "text", text: String(t), style: style || "" }; },
  section: function (t) { return { kind: "section", text: String(t) }; },
  doc: function (md, id) { return { kind: "doc", id: id || "", text: String(md) }; },
  item: function (o) { o.kind = "item"; return o; },
  input: function (o) { o.kind = "input"; return o; },
  image: function (src, rows) { return { kind: "image", src: src, rows: rows || 6 }; },
  progress: function (fraction, note, label) {
    return { kind: "progress", fraction: Number(fraction) || 0, trailing: note || "", text: label || "" };
  },
  button: function (id, label) {
    return { kind: "button", id: id, text: label, actions: [{ id: id, label: label, primary: true }] };
  },
  primary: function (id, label) { return { id: id, label: label, primary: true }; },
  action: function (id, label, key) { return { id: id, label: label, key: key || "" }; },
  danger: function (id, label, confirm) { return { id: id, label: label, group: "danger", confirm: confirm || "" }; },
};

// JSX (app.tsx): esbuild turns <item title="x"/> into h("item", {title: "x"}).
// An element is a phone.Ui node of that kind, its text children become its
// text; a function is a component returning nodes; arrays flatten.
function flatNodes(a) {
  var out = [];
  for (var i = 0; i < a.length; i++) {
    var x = a[i];
    if (Array.isArray(x)) out = out.concat(flatNodes(x));
    else if (x !== null && x !== undefined && x !== false && x !== true) out.push(x);
  }
  return out;
}
function Fragment(props) { return flatNodes(props.children || []); }
function h(type, props) {
  var children = flatNodes(Array.prototype.slice.call(arguments, 2));
  props = props || {};
  if (typeof type === "function") {
    var p = {};
    for (var k in props) p[k] = props[k];
    p.children = children;
    return type(p);
  }
  var node = { kind: type };
  for (var k2 in props) node[k2] = props[k2];
  var text = children.filter(function (c) { return typeof c === "string" || typeof c === "number"; }).join("");
  if (text !== "" && node.text === undefined) node.text = text;
  if (type === "button" && !node.actions) {
    node.actions = [{ id: node.action || node.id, label: node.text, primary: true }];
  }
  return node;
}
