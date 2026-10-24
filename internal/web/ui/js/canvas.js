// The album wall: view transform, cards, selection, drag-to-reorder and
// progressive image loading.
import { S, on, emit, select, upsert } from './store.js';
import { post, upload } from './api.js';
import { h, svg, gestureId, clamp, toast } from './ui.js';
import { frameAspect } from './geom.js';

const vp = document.getElementById('viewport');
const world = document.getElementById('world');
const overlay = document.getElementById('overlay');
const marquee = document.getElementById('marquee');
const zoomLabel = document.getElementById('zoom-label');
const els = new Map();

// Tools register overlay painters and pointer handlers here.
export const hooks = { overlay: [], pointerDown: [], cursor: [] };

// ---------- view ----------

export function toWorld(sx, sy) {
  return [(sx - S.view.x) / S.view.z, (sy - S.view.y) / S.view.z];
}

export function toScreen(wx, wy) {
  return [wx * S.view.z + S.view.x, wy * S.view.z + S.view.y];
}

// While filters are on, the visible photos get their own masonry layout
// (view only; the album order and positions on the server do not change).
let override = null; // Map id -> {x, y, w, h}

export function setLayoutOverride(m) {
  override = m;
  renderAll();
}

// With a photo open on its own (S.loupe) only that photo is shown, at the
// origin of the album space.
export function isShown(n) {
  if (S.loupe) return n.id === S.loupe;
  return !override || override.has(n.id);
}

// box is the card's rectangle in album units.
export function box(n) {
  const p = S.loupe === n.id ? { x: 0, y: 0, w: n.w, h: n.h } : (override && override.get(n.id)) || n;
  const h = n.type === 'photo' && S.tool === 'crop' && S.active === n.id ? p.w / frameAspect(n) : p.h;
  return { x: p.x, y: p.y, w: p.w, h };
}

export function displayH(n) {
  return box(n).h;
}

// nodeRect is the card's rectangle in viewport pixels.
export function nodeRect(n) {
  const b = box(n);
  const [x, y] = toScreen(b.x, b.y);
  return { x, y, w: b.w * S.view.z, h: b.h * S.view.z };
}

export function localPoint(e) {
  const r = vp.getBoundingClientRect();
  return [e.clientX - r.left, e.clientY - r.top];
}

let viewRaf = 0;
// The album is never scaled with a CSS transform: WebKit would draw each
// card at its album size and stretch that drawing, blurring every photo.
// Cards are placed and sized in screen pixels instead, so an image whose
// render matches its size on screen is shown pixel for pixel.
let placedZ = 0;
let zoomingT = 0;
// cardPx is a card's rectangle in whole screen pixels: a fractional size
// would make the browser resample the image by a fraction of a pixel.
function cardPx(n) {
  const b = box(n), z = S.view.z;
  const x = Math.round(b.x * z), y = Math.round(b.y * z);
  return { x, y, w: Math.max(1, Math.round((b.x + b.w) * z) - x), h: Math.max(1, Math.round((b.y + b.h) * z) - y) };
}

function place(e, n) {
  const r = cardPx(n);
  const el = e.el;
  el.style.left = r.x + 'px';
  el.style.top = r.y + 'px';
  el.style.width = r.w + 'px';
  el.style.height = r.h + 'px';
}

export function applyView() {
  S.lastViewChange = Date.now();
  world.style.transform = `translate(${Math.round(S.view.x)}px, ${Math.round(S.view.y)}px)`;
  if (S.view.z !== placedZ) {
    placedZ = S.view.z;
    world.style.setProperty('--z', S.view.z);
    // While zooming, cards move without their layout transition.
    world.classList.add('zooming');
    clearTimeout(zoomingT);
    zoomingT = setTimeout(() => world.classList.remove('zooming'), 160);
    for (const [id, e] of els) {
      const n = S.nodes.get(id);
      if (n) place(e, n);
    }
  }
  // A photo open on its own shows its zoom relative to its own pixels.
  const z100 = S.loupe && photoZ100(S.nodes.get(S.loupe));
  zoomLabel.textContent = Math.round((z100 ? S.view.z / z100 : S.view.z) * 100) + '%';
  for (const [id, e] of els) {
    if (!e.cap) continue;
    // Hide captions and checks when cards get too small for them.
    const small = (S.nodes.get(id)?.w || 0) * S.view.z < 70 ? 'hidden' : '';
    e.cap.style.visibility = small;
    e.chk.style.visibility = small;
  }
  if (!viewRaf) {
    viewRaf = requestAnimationFrame(() => {
      viewRaf = 0;
      placeholders();
      drawOverlay();
      emit('view');
    });
  }
  scheduleImages();
}

let anim = 0;
// maxZoom lets a photo open on its own go to four times its pixels.
function maxZoom() {
  const z100 = S.loupe && photoZ100(S.nodes.get(S.loupe));
  return z100 ? Math.max(8, z100 * 4) : 8;
}

// imageLong is the long edge of a photo's (cropped) frame in pixels.
export function imageLong(n) {
  const W = n?.info?.width || 0;
  const H = n?.info?.height || 0;
  const c = n?.settings?.crop;
  return Math.max(W * (c?.w || 1), H * (c?.h || 1));
}

// photoZ100 is the zoom at which one photo pixel is one screen pixel.
export function photoZ100(n) {
  const long = imageLong(n);
  if (!n || !long) return 0;
  const b = box(n);
  return long / (Math.max(b.w, b.h) * (window.devicePixelRatio || 1));
}

// clampView keeps the album in sight: its edges stop at the margins of the
// window (the same ones fitAlbum uses), so it cannot be scrolled away into
// empty space. Along a side where the album fits, it stays centered.
function clampView(x, y, z) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const id of S.order) {
    const n = S.nodes.get(id);
    if (!n || !isShown(n)) continue;
    const b = box(n);
    x0 = Math.min(x0, b.x); y0 = Math.min(y0, b.y);
    x1 = Math.max(x1, b.x + b.w); y1 = Math.max(y1, b.y + b.h);
  }
  if (!(x0 < x1)) return [x, y];
  const W = vp.clientWidth, H = vp.clientHeight;
  if (!W || !H) return [x, y];
  const narrow = W < 600;
  const side = narrow ? 14 : S.loupe ? 24 : 40;
  const top = narrow ? 14 : S.loupe ? 20 : 32;
  const bottom = S.loupe ? 76 : 84;
  const axis = (v, c0, c1, lo, hi) => {
    if ((c1 - c0) * z <= hi - lo) return (lo + hi) / 2 - ((c0 + c1) / 2) * z;
    return clamp(v, hi - c1 * z, lo - c0 * z);
  };
  return [axis(x, x0, x1, side, W - side), axis(y, y0, y1, top, H - bottom)];
}

// keepInView moves the view back when the album changed under it (photos
// removed, filters, a narrower window).
export function keepInView() {
  const [x, y] = clampView(S.view.x, S.view.y, S.view.z);
  if (Math.abs(x - S.view.x) > 0.5 || Math.abs(y - S.view.y) > 0.5) setView(x, y, S.view.z, true);
}

export function setView(x, y, z, animate = false) {
  cancelAnimationFrame(anim);
  z = clamp(z, 0.02, maxZoom());
  [x, y] = clampView(x, y, z);
  if (!animate) {
    Object.assign(S.view, { x, y, z });
    applyView();
    return;
  }
  const from = { ...S.view };
  const t0 = performance.now();
  const step = (t) => {
    const k = Math.min(1, (t - t0) / 420);
    const e = 1 - Math.pow(1 - k, 3);
    // Interpolate zoom in log space for a natural feel.
    const zz = Math.exp(Math.log(from.z) + (Math.log(z) - Math.log(from.z)) * e);
    // Keep the world point under the screen center moving linearly.
    const vw = vp.clientWidth / 2, vh = vp.clientHeight / 2;
    const cx0 = (vw - from.x) / from.z, cy0 = (vh - from.y) / from.z;
    const cx1 = (vw - x) / z, cy1 = (vh - y) / z;
    const cx = cx0 + (cx1 - cx0) * e, cy = cy0 + (cy1 - cy0) * e;
    Object.assign(S.view, { x: vw - cx * zz, y: vh - cy * zz, z: zz });
    applyView();
    if (k < 1) anim = requestAnimationFrame(step);
    else {
      anim = 0;
      // The area may have changed size while moving (a panel opened).
      keepInView();
    }
  };
  anim = requestAnimationFrame(step);
}

export function zoomAt(sx, sy, factor) {
  const z = clamp(S.view.z * factor, 0.02, maxZoom());
  const [wx, wy] = toWorld(sx, sy);
  setView(sx - wx * z, sy - wy * z, z);
}

export function fitNodes(ids, animate = true, pad = 0.12, maxZ = 2) {
  const nodes = (ids && ids.length ? ids : S.order).map((id) => S.nodes.get(id)).filter((n) => n && isShown(n));
  if (!nodes.length) return;
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const n of nodes) {
    const b = box(n);
    x0 = Math.min(x0, b.x); y0 = Math.min(y0, b.y);
    x1 = Math.max(x1, b.x + b.w); y1 = Math.max(y1, b.y + b.h);
  }
  const W = vp.clientWidth, H = vp.clientHeight;
  const z = Math.min(maxZ, (W * (1 - pad * 2)) / (x1 - x0), (H * (1 - pad * 2)) / (y1 - y0));
  setView(W / 2 - ((x0 + x1) / 2) * z, H / 2 - ((y0 + y1) / 2) * z, z, animate);
}

// fitAlbum shows the album at full width, top aligned when it is taller
// than the window, like a photo wall you scroll down.
export function fitAlbum(animate = true, keepScroll = false) {
  if (S.loupe) return fitPhoto(animate);
  const nodes = S.order.map((id) => S.nodes.get(id)).filter((n) => n && isShown(n));
  if (!nodes.length) return;
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const n of nodes) {
    const b = box(n);
    x0 = Math.min(x0, b.x); y0 = Math.min(y0, b.y);
    x1 = Math.max(x1, b.x + b.w); y1 = Math.max(y1, b.y + b.h);
  }
  const W = vp.clientWidth, H = vp.clientHeight;
  const side = W < 600 ? 14 : 40, top = W < 600 ? 14 : 32, bottom = 84;
  const z = Math.min(1.25, (W - side * 2) / (x1 - x0));
  S.fitZ = z;
  const h = (y1 - y0) * z;
  const y = h < H - top - bottom ? (H - bottom + top - h) / 2 : top;
  let viewY = y - y0 * z;
  if (keepScroll && h >= H - top - bottom) {
    // Keep the part of the album that was at the top edge in place.
    const worldTop = (top - S.view.y) / S.view.z;
    viewY = top - Math.max(y0, Math.min(worldTop, y1 - (H - top - bottom) / z)) * z;
  }
  setView(W / 2 - ((x0 + x1) / 2) * z, viewY, z, animate);
}

// fitPhoto fits the photo open on its own into the space above the dock.
export function fitPhoto(animate = true) {
  const n = S.loupe && S.nodes.get(S.loupe);
  if (!n) return;
  const b = box(n);
  const W = vp.clientWidth, H = vp.clientHeight;
  const side = W < 600 ? 10 : 24, top = W < 600 ? 10 : 20, bottom = 76;
  const z = Math.min((W - side * 2) / b.w, (H - top - bottom) / b.h);
  S.photoFitZ = z;
  setView(W / 2 - (b.x + b.w / 2) * z, top + (H - top - bottom) / 2 - (b.y + b.h / 2) * z, z, animate);
}

export function isVisible(n, margin = 200) {
  if (!isShown(n)) return false;
  const r = nodeRect(n);
  return r.x + r.w > -margin && r.y + r.h > -margin && r.x < vp.clientWidth + margin && r.y < vp.clientHeight + margin;
}

// ---------- cards ----------

function caption(n) {
  const kids = [h('span', {}, n.name)];
  if (n.rating) kids.push(h('span', { class: 'stars', title: `${n.rating} stars` }, '★'.repeat(n.rating)));
  if (n.flag === 'pick') kids.push(h('span', { class: 'flag' }, 'Pick'));
  if (n.flag === 'reject') kids.push(h('span', { class: 'flag' }, 'Rejected'));
  return kids;
}

function ensureEl(n) {
  let e = els.get(n.id);
  if (!e) {
    e = { el: null, img: null, reg: null, cap: null, st: { key: '', inflight: false, want: null, url: null, ph: false, ctrl: null, rkey: '', rurl: null, rinflight: false, rctrl: null } };
    if (n.type === 'photo') {
      e.img = h('img', { alt: '', draggable: 'false' });
      e.cap = h('div', { class: 'cap' });
      e.chk = h('span', { class: 'check', title: 'Add to the selection' }, svg('svg', { viewBox: '0 0 16 16', class: 'ico' }, svg('path', { d: 'm4 8.3 2.6 2.6L12 5.4' })));
      e.el = h('div', { class: 'node photo loading', 'data-id': n.id }, e.img, e.cap, e.chk, h('div', { class: 'work' }));
    } else {
      e.el = h('div', { class: 'node note', 'data-id': n.id });
    }
    els.set(n.id, e);
    world.append(e.el);
  }
  const el = e.el;
  place(e, n);
  el.hidden = !isShown(n);
  if (n.type === 'photo') {
    e.cap.replaceChildren(...caption(n));
    el.classList.toggle('reject', n.flag === 'reject');
  } else {
    if (el.getAttribute('contenteditable') !== 'true') el.textContent = n.text || '';
    el.className = 'node note' + (n.color && n.color !== 'default' ? ' ' + n.color : '');
  }
  return e;
}

export function renderAll() {
  for (const [id, e] of els) {
    if (!S.nodes.has(id)) {
      e.st.ctrl?.abort();
      hideRegion(e);
      if (e.st.url) URL.revokeObjectURL(e.st.url);
      e.el.remove();
      els.delete(id);
    }
  }
  let prev = null;
  for (const id of S.order) {
    const e = ensureEl(S.nodes.get(id));
    // Keep DOM order equal to the stacking order.
    if (prev ? prev.nextSibling !== e.el : world.firstChild !== e.el) {
      if (prev) prev.after(e.el);
      else world.prepend(e.el);
    }
    prev = e.el;
  }
  document.getElementById('empty').hidden = S.order.length > 0;
  drawOverlay();
  scheduleImages();
  if (!anim) keepInView();
}

export function renderNode(id) {
  const n = S.nodes.get(id);
  if (!n) return;
  ensureEl(n);
  drawOverlay();
}

export function elFor(id) {
  return els.get(id)?.el;
}

export function histFor(id) {
  const st = els.get(id)?.st;
  return st && st.hist ? { id, hist: st.hist, clip: st.clip } : null;
}

export function imgURL(id) {
  return els.get(id)?.st.url || null;
}

export function markClaude(ids, on) {
  for (const id of ids) els.get(id)?.el.classList.toggle('claude-active', on);
}

export function markWorking(ids, on) {
  for (const id of ids) els.get(id)?.el.classList.toggle('working', on);
}

// ---------- images ----------
//
// Photos on screen get a render sized to their exact on-screen pixels.
// With up to HQ_LIMIT photos on screen those renders come from the detail
// level (half the sensor resolution), so they are as sharp as the display
// allows. Photos that move off screen drop back to the camera's small
// preview and release their render, so neither Lumen nor the browser
// holds large pictures nobody is looking at.

const BUCKETS = [192, 320, 512, 768, 1024, 1536];
const HQ_LIMIT = 10;
let onScreen = 0;

// keepMargin is how far off screen a photo keeps its render (so a small
// scroll back does not reload it).
function keepMargin() {
  return Math.max(vp.clientWidth, vp.clientHeight) * 0.25;
}

// proxyEdge is the long edge of the on-disk preview a photo renders from
// without decoding the file (see develop/proxy.go).
function proxyEdge(n) {
  const long = Math.max(n.info?.width || 0, n.info?.height || 0);
  if (!long) return 1024;
  const div = n.info?.raw ? 2 : 1;
  let k = 1;
  while (long / (div * k) > 1600) k++;
  return Math.floor(long / (div * k));
}

function plainCrop(n) {
  const c = n.settings?.crop;
  return !c || (c.w === 1 && c.h === 1 && !c.angle && !c.rotate && !c.flipH && !c.flipV);
}

// detailCap is the largest useful render: the detail level is half the
// sensor for RAW files and the image itself otherwise.
function detailCap(n) {
  const W = n.info?.width || 0;
  const H = n.info?.height || 0;
  if (!W || !H) return 4096;
  const c = n.settings?.crop;
  const long = Math.max(W * (c?.w || 1), H * (c?.h || 1));
  return Math.min(4096, Math.ceil(n.info?.raw ? long / 2 : long));
}

// fullView tells whether a photo is open on its own with its RAW loaded:
// its image is then computed from the full sensor data at exactly the
// screen's pixel size (the browser must not scale a huge image itself: it
// decodes it small and blurs it), and zoomed parts come at real pixels.
function fullView(n) {
  return S.loupe === n.id && S.rawLoaded === n.id;
}

function wantSize(n, hq) {
  const dpr = window.devicePixelRatio || 1;
  const r = cardPx(n);
  const long = Math.max(r.w, r.h) * dpr;
  if (hq && fullView(n)) {
    // The base image of the photo open on its own is exactly its size on
    // screen, never larger than the window: zooming in past that is served
    // by the region layer at real pixels, not by a bigger whole image.
    const cap = Math.max(vp.clientWidth, vp.clientHeight) * dpr;
    return Math.max(192, Math.min(Math.round(long), Math.ceil(cap), Math.ceil(imageLong(n))));
  }
  // Sharp renders are exactly the size on screen, so the browser never
  // resamples them; quick ones use a few sizes that cache well.
  if (hq) return Math.max(192, Math.min(Math.round(long), detailCap(n)));
  for (const b of BUCKETS) if (b >= long * 0.92) return b;
  return BUCKETS[BUCKETS.length - 1];
}

// beforeFor tells whether a photo shows without edits: the selected ones,
// or every photo when nothing is selected.
export function beforeFor(id) {
  return S.before && (S.selection.size === 0 || S.selection.has(id));
}

function wantFor(n, fast) {
  const hq = !fast && onScreen <= HQ_LIMIT && isVisible(n, 0);
  let s = wantSize(n, hq);
  if (fast) s = Math.min(s, 1024);
  const nocrop = S.tool === 'crop' && S.active === n.id ? 1 : 0;
  const before = beforeFor(n.id) ? 1 : 0;
  const ov = S.overlay && S.tab === 'masks' && S.active === n.id && S.maskId ? S.maskId : '';
  // Once the photo open on its own has its full RAW loaded, ask again so
  // the image is made from it.
  const raw = hq && fullView(n) ? '&raw=1' : '';
  const q = `s=${s}${hq ? '&hq=1' : ''}${raw}&h=${n.hash}&b=${S.boot || ''}&dpr=${window.devicePixelRatio || 1}` + (nocrop ? '&nocrop=1' : '') + (before ? '&before=1' : '') + (ov ? `&overlay=${ov}` : '') + (S.focus ? '&focus=1' : '') + (S.clip ? '&clip=1' : '');
  return { key: q, q, s, hq };
}

// ---------- full resolution ----------
//
// A photo open on its own and zoomed beyond its detail level gets a second
// image over it: just the part on screen, rendered from the full-resolution
// RAW at screen pixels (up to 100%; beyond, its pixels are enlarged).

function hideRegion(e) {
  e.st.rctrl?.abort();
  if (!e.reg) return;
  e.reg.hidden = true;
  if (e.st.rurl) setTimeout(() => URL.revokeObjectURL(e.st.rurl), 1000);
  e.st.rurl = null;
  e.st.rkey = '';
}

function refreshRegion(id) {
  const n = S.nodes.get(id);
  const e = els.get(id);
  if (!n || !e || n.type !== 'photo') return;
  const dpr = window.devicePixelRatio || 1;
  const b = box(n);
  const cardLong = Math.max(b.w, b.h) * S.view.z * dpr;
  const long = imageLong(n);
  // Zoomed in past what the base image holds: the part on screen comes
  // from the full sensor data at the exact scale (real pixels at 100%).
  const mainLong = e.st.url ? Math.max(e.img.naturalWidth, e.img.naturalHeight) : 0;
  // While the base image is on its way, wait for it rather than fetching
  // the same pixels twice.
  if (S.loupe !== id || !long || !mainLong || e.st.inflight || cardLong <= mainLong * 1.02 || S.tool === 'crop') return hideRegion(e);
  // The part of the photo on screen (plus a margin), in fractions of it,
  // snapped to a grid so small moves reuse the same render.
  const r = nodeRect(n);
  const W = vp.clientWidth, H = vp.clientHeight;
  const g = 1 / 32;
  const x0 = Math.max(0, Math.floor(((-W * 0.1 - r.x) / r.w) / g) * g);
  const y0 = Math.max(0, Math.floor(((-H * 0.1 - r.y) / r.h) / g) * g);
  const x1 = Math.min(1, Math.ceil(((W * 1.1 - r.x) / r.w) / g) * g);
  const y1 = Math.min(1, Math.ceil(((H * 1.1 - r.y) / r.h) / g) * g);
  if (x1 <= x0 || y1 <= y0) return hideRegion(e);
  const sc = Math.min(1, Math.round((cardLong / long) * 10000) / 10000);
  const before = beforeFor(id) ? 1 : 0;
  const ov = S.overlay && S.tab === 'masks' && S.active === id && S.maskId ? S.maskId : '';
  const q = `rx=${x0}&ry=${y0}&rw=${(x1 - x0).toFixed(5)}&rh=${(y1 - y0).toFixed(5)}&sc=${sc}&hq=1&s=4096&h=${n.hash}&b=${S.boot || ''}` + (before ? '&before=1' : '') + (ov ? `&overlay=${ov}` : '') + (S.focus ? '&focus=1' : '') + (S.clip ? '&clip=1' : '');
  if (q === e.st.rkey) return;
  if (e.st.rinflight) {
    e.st.rwant = true;
    return;
  }
  if (!e.reg) {
    e.reg = h('img', { class: 'region', alt: '', draggable: 'false', hidden: true });
    e.img.after(e.reg);
  }
  e.st.rinflight = true;
  const ctrl = new AbortController();
  e.st.rctrl = ctrl;
  emit('region', { id, loading: true });
  fetch(`/api/render/${id}?${q}`, { signal: ctrl.signal })
    .then(async (res) => {
      if (!res.ok) throw new Error(await res.text());
      const url = URL.createObjectURL(await res.blob());
      const tmp = new Image();
      tmp.src = url;
      await tmp.decode().catch(() => {});
      if (ctrl.signal.aborted || S.loupe !== id) {
        URL.revokeObjectURL(url);
        return;
      }
      const old = e.st.rurl;
      Object.assign(e.reg.style, { left: x0 * 100 + '%', top: y0 * 100 + '%', width: (x1 - x0) * 100 + '%', height: (y1 - y0) * 100 + '%' });
      e.reg.classList.toggle('px', cardLong / long > 1.05);
      e.reg.src = url;
      e.reg.hidden = false;
      e.st.rurl = url;
      e.st.rkey = q;
      if (old) setTimeout(() => URL.revokeObjectURL(old), 1000);
    })
    .catch((err) => { if (!ctrl.signal.aborted) console.warn('region', id, err); })
    .finally(() => {
      e.st.rinflight = false;
      if (e.st.rctrl === ctrl) e.st.rctrl = null;
      emit('region', { id, loading: false });
      if (e.st.rwant) {
        e.st.rwant = false;
        refreshRegion(id);
      }
    });
}

// placeholder shows the camera's preview until the first render arrives.
function placeholder(id, n, e) {
  if (e.st.url || e.st.ph) return;
  e.st.ph = true;
  if (plainCrop(n)) e.img.src = `/api/thumb/${id}`;
}

// lowRes drops a photo that left the screen back to its placeholder.
function lowRes(id, n, e) {
  e.st.ctrl?.abort();
  hideRegion(e);
  if (!e.st.url) return;
  const old = e.st.url;
  e.st.url = null;
  e.st.key = '';
  if (plainCrop(n)) e.img.src = `/api/thumb/${id}`;
  else {
    e.img.removeAttribute('src');
    e.el.classList.add('loading');
  }
  setTimeout(() => URL.revokeObjectURL(old), 1000);
}

export function refreshImage(id, fast = false) {
  const n = S.nodes.get(id);
  const e = els.get(id);
  if (!n || !e || n.type !== 'photo') return;
  if (id !== S.active && !isVisible(n, keepMargin())) {
    // Off screen: it renders again when it comes back into view.
    lowRes(id, n, e);
    return;
  }
  placeholder(id, n, e);
  if (e.st.failed === n.hash) return;
  const want = wantFor(n, fast);
  if (want.key === e.st.key && !e.st.inflight) return;
  if (e.st.inflight) {
    e.st.want = { fast };
    return;
  }
  if (want.key === e.st.key) return;
  e.st.inflight = true;
  const started = want.key;
  const ctrl = new AbortController();
  e.st.ctrl = ctrl;
  fetch(`/api/render/${id}?${want.q}`, { signal: ctrl.signal })
    .then(async (r) => {
      if (!r.ok) throw new Error(await r.text());
      const hist = r.headers.get('X-Hist');
      const clip = r.headers.get('X-Clip');
      const blob = await r.blob();
      if (ctrl.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const tmp = new Image();
      tmp.src = url;
      await tmp.decode().catch(() => {});
      if (ctrl.signal.aborted) {
        URL.revokeObjectURL(url);
        return;
      }
      const old = e.st.url;
      e.img.src = url;
      e.st.url = url;
      e.st.key = started;
      e.st.hist = hist;
      e.st.clip = clip;
      e.st.failed = null;
      e.el.classList.remove('loading', 'missing');
      if (old) setTimeout(() => URL.revokeObjectURL(old), 1000);
      if (S.active === id) emit('histogram', { id, hist, clip });
      emit('image', id);
      if (S.loupe === id) refreshRegion(id);
    })
    .catch((err) => {
      if (ctrl.signal.aborted) return;
      // Not retried until the photo or its size changes: a missing file
      // (a disconnected drive) must not be asked for again and again.
      e.st.key = started;
      e.st.failed = n.hash;
      e.el.classList.remove('loading');
      e.el.classList.toggle('missing', /no such file|not found/i.test(err.message));
      console.warn('render', id, err.message);
    })
    .finally(() => {
      e.st.inflight = false;
      if (e.st.ctrl === ctrl) e.st.ctrl = null;
      const next = e.st.want;
      e.st.want = null;
      const cur = S.nodes.get(id) || n;
      if (next) refreshImage(id, next.fast);
      else if (!ctrl.signal.aborted && wantFor(cur, false).key !== e.st.key && isVisible(cur, 0)) refreshImage(id, false);
    });
}

let imgTimer = 0;
export function scheduleImages() {
  clearTimeout(imgTimer);
  imgTimer = setTimeout(updateImages, 90);
}

function updateImages() {
  const keep = keepMargin();
  // While the view is still moving (a zoom, a fit animation) only quick
  // renders are asked for; the sharp ones, at exact sizes, once it settles.
  const moving = Date.now() - (S.lastViewChange || 0) < 200;
  if (moving) {
    clearTimeout(imgTimer);
    imgTimer = setTimeout(updateImages, 220);
  }
  let count = 0;
  for (const id of S.order) {
    const n = S.nodes.get(id);
    if (n && n.type === 'photo' && isVisible(n, 0)) count++;
  }
  onScreen = count;
  for (const id of S.order) {
    const n = S.nodes.get(id);
    const e = els.get(id);
    if (!n || !e || n.type !== 'photo') continue;
    if (isVisible(n, 0)) {
      // A render beyond the preview decodes the file: show a quick one first.
      const first = !e.st.url && wantFor(n, false).s > proxyEdge(n);
      refreshImage(id, first || moving);
      if (S.loupe === id && !moving) refreshRegion(id);
    } else if (id !== S.active && !isVisible(n, keep)) {
      lowRes(id, n, e);
    }
  }
}

// placeholders gives photos entering the screen the camera preview while
// scrolling, before their renders are requested.
function placeholders() {
  const keep = keepMargin();
  for (const [id, e] of els) {
    if (e.st.url || e.st.ph) continue;
    const n = S.nodes.get(id);
    if (n && n.type === 'photo' && isVisible(n, keep)) placeholder(id, n, e);
  }
}

// ---------- overlay ----------

export function drawOverlay() {
  overlay.replaceChildren();
  // Checks fill for photos checked on purpose (several selected, or one
  // checked with its circle); a photo that is only clicked is just active.
  const checking = S.checking || S.selection.size > 1;
  for (const [id, e] of els) {
    const has = S.selection.has(id);
    e.el.classList.toggle('sel', has);
    e.el.classList.toggle('checked', has && checking);
  }
  // While photos are being checked every card shows its check.
  world.classList.toggle('selecting', checking && S.selection.size > 0);
  const lifting = drag && drag.kind === 'reorder' && drag.moved;
  for (const id of S.selection) {
    const n = S.nodes.get(id);
    if (!n) continue;
    const r = nodeRect(n);
    // While dragging, the empty slot shows as a dashed placeholder.
    overlay.append(svg('rect', { class: lifting ? 'slot' : 'sel-box', x: r.x - 0.5, y: r.y - 0.5, width: r.w + 1, height: r.h + 1, rx: lifting ? 3 : 0 }));
  }
  if (hover && !S.selection.has(hover) && S.nodes.has(hover) && !(drag && drag.kind === 'reorder')) {
    const r = nodeRect(S.nodes.get(hover));
    overlay.append(svg('rect', { class: 'hover-box', x: r.x, y: r.y, width: r.w, height: r.h }));
  }
  if (drag && drag.kind === 'reorder' && drag.target) {
    // Insertion marker on the edge of the card the items will go next to.
    const r = nodeRect(S.nodes.get(drag.target.id));
    const y = drag.target.before ? r.y - 5 : r.y + r.h + 3;
    overlay.append(svg('rect', { class: 'drop-bar', x: r.x, y, width: r.w, height: 3, rx: 1.5 }));
  }
  for (const fn of hooks.overlay) fn(overlay);
}

// ---------- selection ----------

// shownItems is what the album shows (filters apply), in album order.
export function shownItems() {
  return S.order.filter((id) => {
    const n = S.nodes.get(id);
    return n && isShown(n);
  });
}

export function shownPhotos() {
  return shownItems().filter((id) => S.nodes.get(id).type === 'photo');
}

// clickSelect applies a click on an item: alone it selects the item, with
// ⌘ it adds or removes it, with ⇧ it selects the range from the last item
// clicked, following `order` (the album, or the photo list).
let anchor = null;
export function clickSelect(id, e, order) {
  const from = anchor && order.includes(anchor) ? anchor : S.active;
  if (e.metaKey || e.ctrlKey) {
    select([id], { toggle: true });
    anchor = id;
  } else if (e.shiftKey && from && order.includes(from)) {
    const a = order.indexOf(from), b = order.indexOf(id);
    const range = a <= b ? order.slice(a, b + 1) : order.slice(b, a + 1);
    // The clicked item becomes the active one (shown in the panel).
    select([...range.filter((x) => x !== id), id]);
  } else {
    select([id]);
    anchor = id;
  }
}

// reveal brings an item into view at the current zoom.
export function reveal(id) {
  const n = S.nodes.get(id);
  if (!n || !isShown(n) || S.loupe || isVisible(n, -40)) return;
  const b = box(n);
  const W = vp.clientWidth, H = vp.clientHeight, z = S.view.z;
  const cx = (b.x + b.w / 2) * z + S.view.x;
  const x = cx < 0 || cx > W ? W / 2 - (b.x + b.w / 2) * z : S.view.x;
  setView(x, H / 2 - (b.y + b.h / 2) * z, z, true);
}

// ---------- interaction ----------

// onChrome tells whether an event happened on the controls floating over
// the album (dock, selection bar) or on text being edited.
function onChrome(e) {
  return !!e.target.closest('#dock, #selbar, #empty, [contenteditable=true]');
}

let hover = null;
let drag = null;
let spaceDown = false;

export function setSpace(v) {
  spaceDown = v;
  vp.classList.toggle('panning', v || S.tool === 'hand');
}

export function hitTest(sx, sy) {
  const [wx, wy] = toWorld(sx, sy);
  for (let i = S.order.length - 1; i >= 0; i--) {
    const n = S.nodes.get(S.order[i]);
    if (!isShown(n)) continue;
    const b = box(n);
    if (wx >= b.x && wy >= b.y && wx <= b.x + b.w && wy <= b.y + b.h) return n;
  }
  return null;
}

vp.addEventListener('pointerdown', (e) => {
  S.lastUserInput = Date.now();
  if (onChrome(e)) return;
  vp.focus();
  const [sx, sy] = localPoint(e);
  if (e.button === 1 || spaceDown || S.tool === 'hand' || (e.button === 0 && e.altKey && S.tool === 'select' && !hitTest(sx, sy))) {
    drag = { kind: 'pan', sx, sy, vx: S.view.x, vy: S.view.y };
    vp.setPointerCapture(e.pointerId);
    vp.classList.add('panning');
    return;
  }
  if (e.button !== 0) return;
  for (const fn of hooks.pointerDown) {
    if (fn(e, sx, sy)) return;
  }
  if (S.tool === 'note' && S.loupe) {
    toast('Notes go in the album: go back to it to add one.');
    emit('tool', 'select');
    return;
  }
  if (S.tool === 'note') {
    // Notes join the album after the item clicked (or at the end).
    const at = hitTest(sx, sy);
    post('note', { text: 'Note', after: at ? at.id : '' })
      .then((n) => { upsert(n); renderAll(); select([n.id]); editNote(n.id); })
      .catch((err) => toast(err.message, true));
    emit('tool', 'select');
    return;
  }
  if (S.loupe && S.tool === 'select') {
    // A photo open on its own stays selected: dragging moves the view.
    drag = { kind: 'pan', sx, sy, vx: S.view.x, vy: S.view.y };
    vp.setPointerCapture(e.pointerId);
    return;
  }
  const n = hitTest(sx, sy);
  if (n) {
    if (e.target.closest('.check')) {
      // The first check starts a new selection: a photo that was only
      // clicked (active) is not one of the checked ones.
      if (S.checking || S.selection.size > 1) select([n.id], { toggle: true, checking: true });
      else select([n.id], { checking: true });
      anchor = n.id;
      return;
    }
    // A click on a photo selects that photo alone; several photos are
    // selected with their checks. Pressing on one of several selected
    // photos keeps them all so they can be dragged together; releasing
    // without dragging leaves it alone.
    let single = null;
    if (!S.selection.has(n.id)) {
      select([n.id]);
      anchor = n.id;
    } else if (S.selection.size > 1 && !e.ctrlKey) {
      single = n.id;
      S.active = n.id;
      emit('selection');
    } else {
      S.active = n.id;
      emit('selection');
    }
    drag = { kind: 'reorder', sx, sy, ids: S.order.filter((id) => S.selection.has(id)), moved: false, target: null, single };
    vp.setPointerCapture(e.pointerId);
  } else {
    if (!e.shiftKey) select([]);
    drag = { kind: 'marquee', sx, sy, base: new Set(S.selection) };
    vp.setPointerCapture(e.pointerId);
  }
});

vp.addEventListener('pointermove', (e) => {
  const [sx, sy] = localPoint(e);
  for (const fn of hooks.cursor) fn(e, sx, sy);
  if (!drag) {
    const n = hitTest(sx, sy);
    const id = n ? n.id : null;
    if (id !== hover) {
      hover = id;
      drawOverlay();
    }
    return;
  }
  S.lastUserInput = Date.now();
  if (drag.kind === 'pan') {
    setView(drag.vx + sx - drag.sx, drag.vy + sy - drag.sy, S.view.z);
  } else if (drag.kind === 'reorder') {
    if (!drag.moved && Math.hypot(sx - drag.sx, sy - drag.sy) < 4) return;
    if (!drag.moved) {
      drag.moved = true;
      for (const id of drag.ids) els.get(id)?.el.classList.add('lifted');
    }
    const dx = sx - drag.sx, dy = sy - drag.sy;
    for (const id of drag.ids) {
      const el = els.get(id)?.el;
      if (el) el.style.transform = `translate(${dx}px, ${dy}px)`;
    }
    drag.target = dropTarget(sx, sy, drag.ids);
    drawOverlay();
    emit('moved');
  } else if (drag.kind === 'marquee') {
    const x = Math.min(sx, drag.sx), y = Math.min(sy, drag.sy);
    const w = Math.abs(sx - drag.sx), hh = Math.abs(sy - drag.sy);
    Object.assign(marquee.style, { display: 'block', left: x + 'px', top: y + 'px', width: w + 'px', height: hh + 'px' });
    const ids = new Set(drag.base);
    for (const id of S.order) {
      const n = S.nodes.get(id);
      if (!isShown(n)) continue;
      const r = nodeRect(n);
      if (r.x < x + w && r.x + r.w > x && r.y < y + hh && r.y + r.h > y) ids.add(id);
    }
    const sig = [...ids].join(',');
    if (sig !== drag.sig) {
      drag.sig = sig;
      select([...ids], { checking: true });
    }
  }
});

function endDrag(e) {
  if (!drag) return;
  const d = drag;
  drag = null;
  vp.classList.toggle('panning', spaceDown || S.tool === 'hand');
  marquee.style.display = 'none';
  if (d.kind === 'reorder' && !d.moved && d.single) {
    select([d.single]);
    anchor = d.single;
  }
  if (d.kind === 'reorder' && d.moved) {
    for (const id of d.ids) {
      const el = els.get(id)?.el;
      if (!el) continue;
      el.classList.remove('lifted');
      el.classList.add('settling');
      el.style.transform = '';
      setTimeout(() => el.classList.remove('settling'), 400);
    }
    if (d.target) {
      const t = d.target;
      const label = d.ids.length > 1 ? `Move ${d.ids.length} photos` : 'Move photo';
      post('reorder', { ids: d.ids, before: t.before ? t.id : '', after: t.before ? '' : t.id, label }).catch((err) => toast(err.message, true));
    }
    drawOverlay();
  }
}

// dropTarget finds the card under (or nearest to) the pointer and whether
// the dragged items go before or after it.
function dropTarget(sx, sy, ids) {
  const [wx, wy] = toWorld(sx, sy);
  let best = null, bestD = Infinity;
  for (const id of S.order) {
    if (ids.includes(id)) continue;
    const n = S.nodes.get(id);
    if (!isShown(n)) continue;
    const b = box(n);
    // Distance to the card rectangle (0 inside).
    const dx = Math.max(b.x - wx, 0, wx - (b.x + b.w));
    const dy = Math.max(b.y - wy, 0, wy - (b.y + b.h));
    const d = Math.hypot(dx, dy);
    if (d < bestD) {
      best = n;
      bestD = d;
    }
  }
  if (!best || bestD > 400) return null;
  const bb = box(best);
  return { id: best.id, before: wy < bb.y + bb.h / 2 };
}

vp.addEventListener('pointerup', endDrag);
vp.addEventListener('pointercancel', endDrag);
vp.addEventListener('pointerleave', () => {
  if (hover) {
    hover = null;
    drawOverlay();
  }
});

vp.addEventListener('wheel', (e) => {
  e.preventDefault();
  S.lastUserInput = Date.now();
  const [sx, sy] = localPoint(e);
  if (e.ctrlKey || e.metaKey) {
    if (pinch != null) return;
    const f = Math.exp(-e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.0085));
    zoomAt(sx, sy, f);
  } else {
    setView(S.view.x - e.deltaX, S.view.y - e.deltaY, S.view.z);
  }
}, { passive: false });

vp.addEventListener('dblclick', (e) => {
  if (onChrome(e)) return;
  const [sx, sy] = localPoint(e);
  const n = hitTest(sx, sy);
  if (!n) return;
  getSelection()?.removeAllRanges();
  if (n.type === 'note') editNote(n.id);
  else if (S.tool === 'select' && !(S.loupe && S.tab === 'masks')) emit(S.loupe ? 'close-photo' : 'open-photo', n.id);
});

// Safari and the Lumen window (WebKit) report trackpad pinches as gesture
// events rather than ctrl+wheel.
let pinch = null;
vp.addEventListener('gesturestart', (e) => {
  e.preventDefault();
  pinch = e.scale || 1;
});
vp.addEventListener('gesturechange', (e) => {
  e.preventDefault();
  if (pinch == null) return;
  S.lastUserInput = Date.now();
  const [sx, sy] = localPoint(e);
  zoomAt(sx, sy, e.scale / pinch);
  pinch = e.scale;
});
vp.addEventListener('gestureend', (e) => {
  e.preventDefault();
  pinch = null;
});

vp.addEventListener('contextmenu', (e) => {
  e.preventDefault();
  if (onChrome(e)) return;
  const [sx, sy] = localPoint(e);
  const n = hitTest(sx, sy);
  if (n && !S.selection.has(n.id)) select([n.id]);
  emit('context', { x: e.clientX, y: e.clientY, node: n });
});

export function editNote(id) {
  const e = els.get(id);
  if (!e) return;
  const el = e.el;
  el.setAttribute('contenteditable', 'true');
  el.focus();
  const range = document.createRange();
  range.selectNodeContents(el);
  getSelection().removeAllRanges();
  getSelection().addRange(range);
  const done = () => {
    el.removeEventListener('blur', done);
    el.setAttribute('contenteditable', 'false');
    const text = el.innerText.trim();
    const n = S.nodes.get(id);
    if (n && text !== n.text) {
      post('meta', { ids: [id], meta: { text }, label: 'Edit note' }).then((ns) => ns.forEach(upsert)).catch((err) => toast(err.message, true));
    }
  };
  el.addEventListener('blur', done);
  el.addEventListener('keydown', (k) => {
    if (k.key === 'Escape' || (k.key === 'Enter' && (k.metaKey || k.ctrlKey))) {
      k.preventDefault();
      el.blur();
    }
    k.stopPropagation();
  });
}

// ---------- drop files ----------

vp.addEventListener('dragover', (e) => {
  e.preventDefault();
  vp.classList.add('drop');
});
vp.addEventListener('dragleave', () => vp.classList.remove('drop'));
vp.addEventListener('drop', async (e) => {
  e.preventDefault();
  vp.classList.remove('drop');
  const files = [...(e.dataTransfer?.files || [])];
  if (!files.length) return;
  const [sx, sy] = localPoint(e);
  const at = hitTest(sx, sy);
  toast(`Copying ${files.length} file${files.length > 1 ? 's' : ''} into ~/Pictures/Lumen/imports…`);
  const paths = [];
  for (const f of files) {
    try { paths.push(await upload(f)); } catch (err) { toast(`${f.name}: ${err.message}`, true); }
  }
  if (!paths.length) return;
  try {
    const res = await post('import', { paths, after: at ? at.id : '' });
    res.nodes.forEach(upsert);
    renderAll();
    select(res.nodes.map((n) => n.id));
  } catch (err) {
    toast(err.message, true);
  }
});

document.getElementById('zoom-in').onclick = () => zoomAt(vp.clientWidth / 2, vp.clientHeight / 2, 1.25);
document.getElementById('zoom-out').onclick = () => zoomAt(vp.clientWidth / 2, vp.clientHeight / 2, 0.8);
zoomLabel.onclick = () => (S.loupe ? emit('toggle-zoom') : fitAlbum());
// Panels opening and closing resize the album area: keep it in place.
new ResizeObserver(() => {
  applyView();
  if (!anim) keepInView();
}).observe(vp);

on('selection', () => {
  drawOverlay();
  post('selection', { ids: [...S.selection] }).catch(() => {});
});
