// DOM helpers.

export function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'html') el.innerHTML = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids.flat()) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

export function svg(tag, attrs = {}) {
  const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [k, v] of Object.entries(attrs)) if (v != null) el.setAttribute(k, v);
  return el;
}

export function fmtBytes(n) {
  if (n >= 1e12) return (n / 1e12).toFixed(1) + ' TB';
  if (n >= 1e9) return (n / 1e9).toFixed(1) + ' GB';
  if (n >= 1e6) return Math.round(n / 1e6) + ' MB';
  return Math.max(1, Math.round(n / 1e3)) + ' KB';
}

export function toast(msg, err = false, ms = 3200) {
  const t = h('div', { class: 'toast' + (err ? ' err' : '') }, msg);
  document.getElementById('toasts').append(t);
  setTimeout(() => t.remove(), ms);
}

export function fmtVal(p, v) {
  if (v == null || Number.isNaN(v)) return '';
  if (p.key === 'temp') return String(Math.round(v));
  if (p.step < 1) {
    const d = p.step < 0.01 ? 3 : p.step < 0.1 ? 2 : 1;
    const s = v.toFixed(d);
    return p.min < 0 && v > 0 ? '+' + s : s;
  }
  const r = Math.round(v);
  return p.min < 0 && r > 0 ? '+' + r : String(r);
}

export function clamp(v, a, b) {
  return Math.max(a, Math.min(b, v));
}

let gid = 0;
export function gestureId() {
  gid += 1;
  return `g${Date.now().toString(36)}${gid}`;
}

export function showMenu(x, y, items) {
  const m = document.getElementById('menu');
  m.replaceChildren();
  for (const it of items) {
    if (it === '-') { m.append(h('hr')); continue; }
    m.append(h('button', { onclick: () => { hideMenu(); it.run(); } }, it.label, it.key ? h('kbd', {}, it.key) : null));
  }
  m.hidden = false;
  const r = m.getBoundingClientRect();
  m.style.left = Math.min(x, innerWidth - r.width - 8) + 'px';
  m.style.top = Math.min(y, innerHeight - r.height - 8) + 'px';
}

export function hideMenu() {
  document.getElementById('menu').hidden = true;
}

export function timeAgo(ms) {
  const d = new Date(ms);
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}
