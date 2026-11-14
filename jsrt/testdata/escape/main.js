// App code reaches nothing but "kario": no QuickJS std or os module, no
// other module, no host global that touches files, sockets or processes.
import * as kario from "kario";

const seen = [];
for (const name of ["std", "os", "scriptArgs", "print", "require", "process", "Deno", "fetch", "XMLHttpRequest",
  "WebSocket", "Worker", "importScripts", "bjson"]) {
  if (typeof globalThis[name] !== "undefined") seen.push("global " + name);
}
for (const name of ["os", "std", "qjs:os", "qjs:std", "qjs:bjson", "bjson", "./other.js", "/etc/passwd", "kario"]) {
  try {
    await import(name);
    if (name !== "kario") seen.push("import " + name);
  } catch (e) {
    if (name === "kario") seen.push("kario refused: " + e.message);
  }
}

kario.on("start", () => {
  kario.reset({ id: "main", title: "escape", body: [{ kind: "text", text: seen.length ? seen.join("; ") : "nothing reachable" }] });
});
