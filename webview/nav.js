// Spatial navigation and form editing for keypads, injected into every page's
// top frame. The arrow keys move a focus ring between links and controls by
// their positions; text fields take what the system input method commits.
// TEXT translates the words it adds to labels (view.c puts the device's
// catalog in place of the empty one at the end).
(function (TEXT) {
  if (window.__phoneNav) return;
  const tr = s => (typeof TEXT[s] === 'string' ? TEXT[s] : s);
  const FOCUSABLE = 'a[href],button,input:not([type=hidden]),select,textarea,' +
    '[tabindex]:not([tabindex="-1"]),[onclick]';
  const style = document.createElement('style');
  style.textContent = '.__phone_focus{outline:3px solid #4ca3ff!important;outline-offset:1px;' +
    'background:rgba(76,163,255,.18)!important;border-radius:3px}';
  document.documentElement.appendChild(style);

  let cur = null;
  const shown = e => {
    const r = e.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden' && !e.disabled;
  };
  const all = () => Array.from(document.querySelectorAll(FOCUSABLE)).filter(shown);
  const mid = r => ({ x: r.left + r.width / 2, y: r.top + r.height / 2 });

  // The kind of text field, as the system's input method needs it.
  const KINDS = { text: 'text', search: 'search', email: 'email', url: 'url', password: 'password', tel: 'tel', number: 'number' };
  const editKind = e => {
    if (!e) return '';
    if (e.tagName === 'TEXTAREA') return 'text';
    if (e.tagName === 'INPUT') return KINDS[(e.type || 'text').toLowerCase()] || '';
    return '';
  };
  const label = e => {
    if (!e) return '';
    const field = e.tagName === 'INPUT' || e.tagName === 'TEXTAREA' || e.tagName === 'SELECT';
    let t = field
      ? (e.labels && e.labels[0] && e.labels[0].innerText) || e.getAttribute('aria-label') || e.placeholder || e.name || ''
      : e.innerText || e.value || e.getAttribute('aria-label') || e.getAttribute('title') || '';
    if (e.tagName === 'SELECT' && e.selectedIndex >= 0) t += tr(': ') + e.options[e.selectedIndex].text;
    if (e.tagName === 'INPUT' && e.type === 'file') t = (t || tr('Choose file')) + (e.files && e.files.length ? tr(': ') + e.files[0].name : '');
    return t.trim().replace(/\s+/g, ' ').slice(0, 64);
  };

  const report = () => {
    const edit = editKind(cur);
    window.webkit.messageHandlers.phoneNav.postMessage(JSON.stringify({
      focus: label(cur), edit: edit, value: edit ? cur.value : '',
    }));
  };
  // A moved focus is reported two animation frames later, after the frame
  // that shows it (phone-webview.c).
  const later = () => requestAnimationFrame(() => requestAnimationFrame(report));

  const mark = e => {
    if (cur) cur.classList.remove('__phone_focus');
    cur = e;
    if (e) {
      e.classList.add('__phone_focus');
      try { e.focus({ preventScroll: true }); } catch (x) {}
      e.scrollIntoView({ block: 'nearest' });
    }
    later();
  };
  const page = d => {
    window.scrollBy(0, (d === 'down' ? 1 : d === 'up' ? -1 : 0) * innerHeight * 0.8);
    later();
  };

  // The nearest control past the focus's edge in the direction (a control
  // on the same line a few pixels lower is not "down"), by the gap between
  // edges plus double the offset across. A target within one more screen
  // gets the focus (scrolled to, the text in between stays on screen); one
  // farther away pages first.
  function move(d) {
    const es = all();
    if (!cur || !document.contains(cur)) {
      const f = es.find(e => { const r = e.getBoundingClientRect(); return r.top >= 0 && r.top < innerHeight; });
      if (f && (d === 'down' || d === 'right')) { mark(f); return; }
      page(d);
      return;
    }
    const a = cur.getBoundingClientRect(), c = mid(a), T = 4;
    let best = null, bd = 1e9;
    for (const e of es) {
      if (e === cur) continue;
      const r = e.getBoundingClientRect(), p = mid(r);
      const gap = d === 'down' ? r.top - a.bottom : d === 'up' ? a.top - r.bottom : d === 'right' ? r.left - a.right : a.left - r.right;
      if (gap < -T) continue;
      const across = (d === 'down' || d === 'up') ? Math.abs(p.x - c.x) : Math.abs(p.y - c.y);
      const v = Math.max(gap, 0) + 2 * across;
      if (v < bd) { bd = v; best = e; }
    }
    if (!best) { page(d); return; }
    const r = best.getBoundingClientRect();
    if ((d === 'down' && r.bottom > innerHeight * 1.8) || (d === 'up' && r.top < -innerHeight * 0.8)) { page(d); return; }
    mark(best);
  }

  // OK on the focus: a list box and a file field open with a real key press
  // (pages cannot open them from script), so they are left to phone-webview.
  function activate() {
    if (!cur) return '';
    if (cur.tagName === 'SELECT' || (cur.tagName === 'INPUT' && cur.type === 'file')) return 'native';
    cur.click();
    return '';
  }

  // The input method's text into the focused field, through the native
  // setter so that pages that track their fields (React and the like) see it.
  function setValue(v) {
    if (!editKind(cur)) return;
    const proto = cur.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(cur, v);
    cur.dispatchEvent(new Event('input', { bubbles: true }));
  }

  // Enter in a field: the page's own key handlers first, then its form.
  function submit() {
    if (!cur) return;
    cur.dispatchEvent(new Event('change', { bubbles: true }));
    const k = { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true };
    const go = cur.dispatchEvent(new KeyboardEvent('keydown', k));
    cur.dispatchEvent(new KeyboardEvent('keyup', k));
    if (go && cur.tagName === 'INPUT' && cur.form) {
      if (cur.form.requestSubmit) cur.form.requestSubmit(); else cur.form.submit();
    }
  }

  window.__phoneNav = { move, activate, setValue, submit };
  addEventListener('pageshow', e => { if (e.persisted) report(); });
  document.addEventListener('change', e => { if (e.target === cur) later(); }, true);
  report();
})(/*TEXT*/{});
