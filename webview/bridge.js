// window.phone.call(capability, args): the page's way to system capabilities,
// answered by the shell's broker under the host app and the page's origin.
(function () {
  if (window.phone) return;
  let n = 0;
  const waiting = {};
  window.phone = {
    call: function (cap, args) {
      return new Promise(function (resolve, reject) {
        const id = ++n;
        waiting[id] = { resolve: resolve, reject: reject };
        window.webkit.messageHandlers.phone.postMessage(JSON.stringify({ id: id, cap: String(cap), args: args || {} }));
      });
    },
  };
  window.__phoneReply = function (id, ok, value) {
    const w = waiting[id];
    if (!w) return;
    delete waiting[id];
    if (ok) w.resolve(value); else w.reject(new Error(value));
  };
})();
