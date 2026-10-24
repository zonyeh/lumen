// Masks: the panel (list, components, local sliders) and on-photo editing
// of linear/radial gradients, brush painting and color sampling.
import { S, on, emit, upsert, activePhoto } from './store.js';
import { post } from './api.js';
import { h, svg, clamp, gestureId, toast } from './ui.js';
import { hooks, nodeRect, drawOverlay, refreshImage, renderNode, localPoint } from './canvas.js';
import { slider } from './develop.js';
import { boxToImage, imageToBox, imageAspect } from './geom.js';
import { icon } from './icons.js';

const tab = document.getElementById('tab-masks');
const brushCanvas = document.getElementById('brush-canvas');
const localCtl = new Map();
let pendingColor = null; // {maskId?, compId?} when waiting for a color sample click

// cancelColorPick stops waiting for a color sample click.
export function cancelColorPick() {
  if (!pendingColor) return false;
  pendingColor = null;
  document.getElementById('viewport').style.cursor = '';
  return true;
}

// brushShapeId is the brush shape strokes go to: the selected shape when
// it is a brush, else the mask's first brush shape (the server makes one
// when there is none).
function brushShapeId() {
  const m = activeMask();
  const comps = m?.components || [];
  const sel = comps.find((c) => c.id === S.compId);
  if (sel?.type === 'brush') return sel.id;
  return comps.find((c) => c.type === 'brush')?.id || '';
}

const TYPE_LABEL = { all: 'Whole photo', linear: 'Linear gradient', radial: 'Radial gradient', brush: 'Brush', luminance: 'Luminance range', color: 'Color range' };
const TYPE_ICON = { all: 'all', linear: 'linear', radial: 'radial', brush: 'brush', luminance: 'luminance', color: 'color' };
const TYPE_SHORT = { all: 'Whole', linear: 'Linear', radial: 'Radial', brush: 'Brush', luminance: 'Luminance', color: 'Color' };
const TYPE_HINT = {
  all: 'The whole photo (combine with other shapes)',
  linear: 'A gradient, e.g. to darken a sky or a foreground',
  radial: 'An ellipse, e.g. to light a subject',
  brush: 'Paint where the adjustment applies',
  luminance: 'Only areas within a brightness range',
  color: 'Only areas of a color you pick on the photo',
};

function activeMask(n = activePhoto()) {
  return n?.settings?.masks?.find((m) => m.id === S.maskId) || null;
}

function activeComp(m = activeMask()) {
  return m?.components?.find((c) => c.id === S.compId) || null;
}

export function brushActive() {
  return S.tab === 'masks' && activeComp()?.type === 'brush';
}

// ---------- server calls ----------

function applyNode(res) {
  if (res?.node) {
    upsert(res.node);
    renderNode(res.node.id);
    refreshImage(res.node.id);
  }
}

async function addMask(type, extra = {}) {
  const n = activePhoto();
  if (!n) return toast('Select a photo first');
  const comp = defaultComponent(type, extra);
  const mask = { name: '', components: [comp], adjust: type === 'linear' ? { exposure: -0.5 } : type === 'radial' ? { exposure: 0.4 } : {} };
  try {
    const res = await post('mask/add', { id: n.id, mask });
    applyNode(res);
    S.maskId = res.mask.id;
    S.compId = res.mask.components[0].id;
    build();
    drawOverlay();
  } catch (err) {
    toast(err.message, true);
  }
}

async function addComponent(type, extra = {}) {
  const n = activePhoto();
  const m = activeMask(n);
  if (!m) return addMask(type, extra);
  try {
    const res = await post('mask/update', { id: n.id, maskId: m.id, patch: { addComponent: defaultComponent(type, extra) }, label: 'Add to mask' });
    applyNode(res);
    S.compId = res.mask.components[res.mask.components.length - 1].id;
    build();
    drawOverlay();
  } catch (err) {
    toast(err.message, true);
  }
}

function defaultComponent(type, extra) {
  switch (type) {
    case 'linear': return { type, x1: 0.5, y1: 0.0, x2: 0.5, y2: 0.45, ...extra };
    case 'radial': return { type, cx: 0.5, cy: 0.5, rx: 0.28, ry: 0.32, feather: 60, ...extra };
    case 'luminance': return { type, lumMin: 60, lumMax: 100, lumFeather: 30, ...extra };
    case 'brush': return { type, strokes: [], ...extra };
    default: return { type, ...extra };
  }
}

// A single in-flight queue for mask edits, coalescing drags.
// Waiting edits: drags of the same slider coalesce into the last one; edits
// that cannot be merged (another shape, a stroke, a removed shape, another
// mask) wait in order.
let mq = null, mInflight = false;
const mqueue = [];
const STRUCTURAL = ['addStroke', 'addComponent', 'removeComponent', 'components'];
function updateMask(patch, { gesture, label, final = true } = {}) {
  const n = activePhoto();
  const m = activeMask(n);
  if (!n || !m) return;
  if (mq && mq.id === n.id && mq.maskId === m.id && canMerge(mq.patch, patch)) {
    mergePatch(mq.patch, patch);
    Object.assign(mq, { gesture: gesture || mq.gesture, label: label || mq.label, final: final || mq.final });
  } else {
    if (mq) mqueue.push(mq);
    mq = { id: n.id, maskId: m.id, patch: structuredClone(patch), gesture, label, final };
  }
  flushMask();
}

function canMerge(a, b) {
  if (STRUCTURAL.some((k) => k in a || k in b)) return false;
  if (a.component && b.component && a.component.id !== b.component.id) return false;
  return true;
}

function mergePatch(a, b) {
  for (const [k, v] of Object.entries(b)) {
    if (k === 'component' && a.component) Object.assign(a.component, v);
    else if (v && typeof v === 'object' && !Array.isArray(v) && a[k] && typeof a[k] === 'object') Object.assign(a[k], v);
    else a[k] = v;
  }
}

function flushMask() {
  if (mInflight) return;
  if (mqueue.length) {
    // Older edits first; the coalescing one stays last.
    const q = mqueue.shift();
    sendMask(q);
    return;
  }
  if (!mq) return;
  const q = mq;
  mq = null;
  sendMask(q);
}

function sendMask(q) {
  mInflight = true;
  post('mask/update', { id: q.id, maskId: q.maskId, patch: q.patch, gesture: q.gesture, label: q.label })
    .then((res) => {
      if (res?.node) {
        upsert(res.node);
        renderNode(res.node.id);
        refreshImage(res.node.id, !q.final);
      }
      if (!mq && !mqueue.length) refreshPanel(false);
      drawOverlay();
    })
    .catch((err) => toast(err.message, true))
    .finally(() => {
      mInflight = false;
      if (mq || mqueue.length) flushMask();
      else if (q.final) refreshImage(q.id);
    });
}

// ---------- panel ----------

function build() {
  build.sig = maskSig(activePhoto());
  localCtl.clear();
  const n = activePhoto();
  if (!n) {
    tab.replaceChildren(h('div', { class: 'panel-empty' }, h('p', {}, 'Select a photo to add masks.')));
    return;
  }
  const masks = n.settings.masks || [];
  const addBtns = h('div', { class: 'mask-add' },
    ...['brush', 'linear', 'radial', 'luminance', 'color', 'all'].map((t) =>
      h('button', { title: TYPE_HINT[t], onclick: () => (t === 'color' ? startColorPick(null) : addMask(t)) }, icon(TYPE_ICON[t]), TYPE_SHORT[t])));
  const overlayToggle = h('label', { class: 'switch', title: 'Show the selected mask in red (O)' },
    h('input', { type: 'checkbox', checked: S.overlay ? 'checked' : null, onchange: (e) => setOverlay(e.target.checked) }), h('span', { class: 'track' }), 'Overlay');
  const body = [addBtns];
  if (masks.length) {
    body.push(h('div', { class: 'list-head' }, h('span', {}, masks.length === 1 ? '1 mask' : `${masks.length} masks`), overlayToggle));
    body.push(h('div', { class: 'mask-list' }, ...masks.map((m) => maskItem(n, m))));
  } else {
    body.push(h('div', { class: 'hint' }, 'Masks change light and color in one part of the photo. Pick a shape above, or ask Claude to light the subject.'));
  }
  const m = activeMask(n);
  const sections = [h('div', { class: 'section plain' }, h('div', { class: 'sb' }, body))];
  if (m) sections.push(...maskDetail(n, m));
  tab.replaceChildren(...sections);
  syncValues();
}

function adjustSummary(m) {
  const parts = [];
  for (const lp of S.schema.localParams) {
    const v = m.adjust?.[lp.key];
    if (!v || lp.key === 'colorHue') continue;
    const s = lp.step < 1 ? v.toFixed(2) : String(Math.round(v));
    parts.push(`${lp.label} ${v > 0 ? '+' : ''}${s}`);
  }
  return parts.length ? parts.slice(0, 3).join(', ') + (parts.length > 3 ? ` and ${parts.length - 3} more` : '') : 'No adjustments yet';
}

function maskItem(n, m) {
  const type = m.components?.[0]?.type || 'all';
  const name = h('span', { class: 'mi-name', title: 'Double-click to rename' }, m.name);
  const summary = adjustSummary(m);
  const el = h('div', { class: 'mask-item' + (m.id === S.maskId ? ' active' : '') + (m.hidden ? ' hidden-mask' : '') },
    h('span', { class: 'mi-ic' }, icon(TYPE_ICON[type], 'ico sm')),
    h('div', { class: 'mi-text' }, name, h('div', { class: 'mi-sub', title: summary }, summary)),
    h('div', { class: 'mi-actions' },
      h('button', { class: 'icon-btn sm', title: m.hidden ? 'Show mask' : 'Hide mask', onclick: (e) => { e.stopPropagation(); S.maskId = m.id; updateMask({ hidden: !m.hidden }, { label: m.hidden ? 'Show mask' : 'Hide mask' }); } }, icon(m.hidden ? 'eyeOff' : 'eye', 'ico sm')),
      h('button', { class: 'icon-btn sm', title: 'Delete mask', onclick: (e) => { e.stopPropagation(); removeMask(n, m); } }, icon('trash', 'ico sm'))));
  el.addEventListener('click', (e) => {
    // The second click of a double-click (rename) or a click inside the
    // name being edited must not rebuild the list under it.
    if (e.detail > 1 || name.isContentEditable) return;
    if (S.maskId === m.id) return;
    S.maskId = m.id;
    S.compId = m.components?.[0]?.id || null;
    build();
    drawOverlay();
    if (S.overlay) refreshImage(n.id);
  });
  name.addEventListener('dblclick', (e) => {
    e.stopPropagation();
    name.contentEditable = 'true';
    name.focus();
    getSelection().selectAllChildren(name);
    let cancel = false;
    const done = () => {
      name.contentEditable = 'false';
      const v = name.textContent.trim();
      if (!cancel && v && v !== m.name) { S.maskId = m.id; updateMask({ name: v }, { label: 'Rename mask' }); }
      else name.textContent = m.name;
    };
    name.addEventListener('blur', done, { once: true });
    name.addEventListener('keydown', (k) => {
      k.stopPropagation();
      if (k.key === 'Enter' || k.key === 'Escape') {
        k.preventDefault();
        cancel = k.key === 'Escape';
        name.blur();
      }
    });
  });
  return el;
}

async function removeMask(n, m) {
  try {
    const res = await post('mask/remove', { id: n.id, maskId: m.id });
    applyNode(res);
    if (S.maskId === m.id) { S.maskId = null; S.compId = null; }
    build();
    drawOverlay();
  } catch (err) {
    toast(err.message, true);
  }
}

function maskDetail(n, m) {
  const head = [];
  const amount = slider({ key: 'amount', label: 'Amount', min: 0, max: 200, step: 1 }, {
    key: 'mask.amount', def: 100, noRegister: true,
    onChange: (p, v, g, final) => { const mm = activeMask(); if (mm) mm.amount = v; updateMask({ amount: v }, { gesture: g, label: `Mask amount ${v}`, final }); },
  });
  localCtl.set('amount', amount);
  head.push(amount.row);
  const invertBox = h('input', { type: 'checkbox', checked: m.invert ? 'checked' : null, onchange: (e) => updateMask({ invert: e.target.checked }, { label: 'Invert mask' }) });
  localCtl.set('invert', { set: (v) => { invertBox.checked = !!v; } });
  head.push(h('div', { class: 'ctl-row' }, h('span', { class: 'lbl' }, 'Invert'),
    h('label', { class: 'switch' }, invertBox, h('span', { class: 'track' }))));

  // Components.
  head.push(h('div', { class: 'list-head' }, 'Shapes in this mask'));
  for (const c of m.components || []) {
    const mode = h('select', { class: 'sel', title: 'How this shape combines with the ones above', onchange: (e) => updateMask({ component: { id: c.id, mode: e.target.value } }, { label: 'Mask mode' }) },
      ...[['add', 'Add'], ['subtract', 'Subtract'], ['intersect', 'Intersect']].map(([v, t]) => h('option', { value: v, selected: (c.mode || 'add') === v ? 'selected' : null }, t)));
    const row = h('div', { class: 'comp' + (c.id === S.compId ? ' active' : '') },
      icon(TYPE_ICON[c.type], 'ico sm'),
      h('span', { class: 'ct' }, TYPE_LABEL[c.type]),
      mode,
      h('button', { class: 'icon-btn sm' + (c.invert ? ' on' : ''), title: c.invert ? 'Inverted' : 'Invert this shape', onclick: (e) => { e.stopPropagation(); updateMask({ component: { id: c.id, invert: !c.invert } }, { label: 'Invert shape' }); } }, icon('invert', 'ico sm')),
      h('button', { class: 'icon-btn sm', title: 'Remove this shape', onclick: (e) => { e.stopPropagation(); updateMask({ removeComponent: c.id }, { label: 'Remove shape' }); } }, icon('x', 'ico sm')));
    row.addEventListener('click', (e) => {
      if (e.target.closest('select,button')) return;
      S.compId = c.id;
      build();
      drawOverlay();
    });
    head.push(row);
  }
  head.push(h('div', { class: 'chips' },
    ...['brush', 'linear', 'radial', 'luminance', 'color'].map((t) =>
      h('button', { class: 'chip', title: 'Add a ' + TYPE_LABEL[t].toLowerCase() + ' to this mask', onclick: () => (t === 'color' ? startColorPick(m.id) : addComponent(t)) }, icon('plus'), TYPE_SHORT[t]))));
  const shapeBody = [];
  const c = activeComp(m);
  if (c?.type === 'brush') {
    shapeBody.push(h('div', { class: 'sub-head' }, 'Brush'));
    const mk = (key, label, min, max, step) => slider({ key, label, min, max, step }, {
      key: 'brush.' + key, noRegister: true, def: { size: 0.05, feather: 60, flow: 100 }[key],
      onChange: (p, v) => { S.brush[key] = v; drawOverlay(); },
    });
    const sz = mk('size', 'Size', 0.005, 0.4, 0.005);
    const fe = mk('feather', 'Feather', 0, 100, 1);
    const fl = mk('flow', 'Flow', 1, 100, 1);
    sz.set(S.brush.size); fe.set(S.brush.feather); fl.set(S.brush.flow);
    shapeBody.push(sz.row, fe.row, fl.row,
      h('div', { class: 'ctl-row' }, h('span', { class: 'lbl' }, 'Erase'), h('label', { class: 'switch', title: 'Or hold ⌥ while painting' }, h('input', { type: 'checkbox', checked: S.brush.erase ? 'checked' : null, onchange: (e) => { S.brush.erase = e.target.checked; } }), h('span', { class: 'track' }))),
      h('div', { class: 'hint' }, 'Paint on the photo. [ and ] change the size.'));
  } else if (c?.type === 'luminance') {
    shapeBody.push(h('div', { class: 'sub-head' }, 'Luminance range'));
    for (const [key, label] of [['lumMin', 'From'], ['lumMax', 'To'], ['lumFeather', 'Smoothness']]) {
      const ctl = slider({ key, label, min: 0, max: 100, step: 1 }, {
        key: 'comp.' + key, noRegister: true,
        onChange: (p, v, g, final) => { const cc = liveComp(c.id); if (cc) cc[key] = v; updateMask({ component: { id: c.id, [key]: v } }, { gesture: g, label: 'Luminance range', final }); },
      });
      ctl.set(c[key] || 0);
      localCtl.set('comp.' + key, ctl);
      shapeBody.push(ctl.row);
    }
  } else if (c?.type === 'color') {
    shapeBody.push(h('div', { class: 'sub-head' }, 'Color range'));
    const ctl = slider({ key: 'colorRange', label: 'Range', min: 1, max: 100, step: 1 }, {
      key: 'comp.colorRange', noRegister: true,
      onChange: (p, v, g, final) => { const cc = liveComp(c.id); if (cc) cc.colorRange = v; updateMask({ component: { id: c.id, colorRange: v } }, { gesture: g, label: 'Color range', final }); },
    });
    ctl.set(c.colorRange || 30);
    localCtl.set('comp.colorRange', ctl);
    shapeBody.push(ctl.row, h('button', { class: 'btn small', onclick: () => startColorPick(m.id, c.id) }, 'Pick the color again'));
  } else if (c && (c.type === 'linear' || c.type === 'radial')) {
    shapeBody.push(h('div', { class: 'hint' }, c.type === 'linear' ? 'Drag the end points on the photo; drag the middle to move.' : 'Drag the center to move, the side handles to resize and the outer dot to rotate.'));
    if (c.type === 'radial') {
      const ctl = slider({ key: 'feather', label: 'Feather', min: 0, max: 100, step: 1 }, {
        key: 'comp.feather', noRegister: true,
        onChange: (p, v, g, final) => { const cc = liveComp(c.id); if (cc) cc.feather = v; drawOverlay(); updateMask({ component: { id: c.id, feather: v } }, { gesture: g, label: 'Feather', final }); },
      });
      ctl.set(c.feather || 0);
      localCtl.set('comp.feather', ctl);
      shapeBody.push(ctl.row);
    }
  }

  const adj = [];
  for (const lp of S.schema.localParams) {
    const ctl = slider(lp, {
      key: 'local.' + lp.key, def: 0, noRegister: true,
      track: lp.key === 'temp' ? 'linear-gradient(90deg,#3d6fd8,#cfd4dc 50%,#e2b23a)' : lp.key === 'tint' ? 'linear-gradient(90deg,#3fae4a,#cfd4dc 50%,#c34fc7)' : lp.key === 'colorHue' ? 'linear-gradient(90deg,#f55,#fa3,#ee4,#4d4,#4dd,#46f,#c4f,#f55)' : null,
      onChange: (p, v, g, final) => {
        const mm = activeMask();
        if (mm) mm.adjust[lp.key] = v;
        updateMask({ adjust: { [lp.key]: v } }, { gesture: g, label: `Mask ${lp.label} ${v}`, final });
      },
    });
    localCtl.set(lp.key, ctl);
    adj.push(ctl.row);
  }
  const sec = (title, kids) => h('div', { class: 'section' }, h('div', { class: 'sh', style: { cursor: 'default' } }, title), h('div', { class: 'sb' }, kids));
  return [sec(m.name, [...head, ...shapeBody]), sec('Adjustments', adj)];
}

// maskSig changes when the panel's structure does: masks, their names and
// visibility, and their shapes. Values (amount, adjustments, feather,
// ranges, invert) are not part of it: a rebuild would replace the slider
// under the pointer mid-drag, so syncValues moves them instead.
function maskSig(n) {
  return (n?.settings?.masks || []).map((m) => [m.id, m.name, m.hidden,
    (m.components || []).map((c) => [c.id, c.type, c.mode, c.invert, (c.strokes || []).length].join(',')).join(';')].join(':')).join('|');
}

// refreshPanel rebuilds the masks panel when its structure changed (a
// shape added or removed, a mask renamed…) and otherwise moves the values.
function refreshPanel(animate) {
  if (S.tab !== 'masks') return;
  const n = activePhoto();
  if (!n) return;
  if (S.maskId && !activeMask(n)) { S.maskId = null; S.compId = null; }
  const m = activeMask(n);
  if (m && S.compId && !(m.components || []).some((c) => c.id === S.compId)) S.compId = m.components?.[0]?.id || null;
  if (maskSig(n) !== build.sig) build();
  else syncValues(animate);
}

function syncValues(animate = false) {
  const m = activeMask();
  if (!m) return;
  localCtl.get('amount')?.set(m.amount || 100, animate);
  localCtl.get('invert')?.set(!!m.invert);
  for (const lp of S.schema.localParams) localCtl.get(lp.key)?.set(m.adjust?.[lp.key] || 0, animate);
  const c = activeComp(m);
  if (!c) return;
  localCtl.get('comp.feather')?.set(c.feather || 0, animate);
  for (const key of ['lumMin', 'lumMax', 'lumFeather']) localCtl.get('comp.' + key)?.set(c[key] || 0, animate);
  localCtl.get('comp.colorRange')?.set(c.colorRange || 30, animate);
}

// The mask and shape objects a control was built with go stale as soon as
// the server answers (the node is replaced): handlers look them up again.
function liveComp(compId) {
  const m = activeMask();
  return (m?.components || []).find((c) => c.id === compId);
}

export function setOverlay(v) {
  S.overlay = v;
  const n = activePhoto();
  if (n) refreshImage(n.id);
  const cb = tab.querySelector('.list-head .switch input');
  if (cb) cb.checked = v;
}

function startColorPick(maskId, compId) {
  if (!activePhoto()) return toast('Select a photo first');
  pendingColor = { maskId, compId };
  document.getElementById('viewport').style.cursor = 'crosshair';
  toast('Click the color to select on the photo');
}

// ---------- on-photo interaction ----------

function photoPoint(n, sx, sy) {
  const r = nodeRect(n);
  return [(sx - r.x) / r.w, (sy - r.y) / r.h];
}

function toScreenPt(n, u, v) {
  const [bx, by] = imageToBox(n, u, v);
  const r = nodeRect(n);
  return [r.x + bx * r.w, r.y + by * r.h];
}

function screenToImage(n, sx, sy) {
  const [bx, by] = photoPoint(n, sx, sy);
  return boxToImage(n, bx, by);
}

function insidePhoto(n, sx, sy) {
  const [bx, by] = photoPoint(n, sx, sy);
  return bx >= 0 && by >= 0 && bx <= 1 && by <= 1;
}

// pixels on screen per image pixel
function screenPerImagePx(n) {
  const r = nodeRect(n);
  const c = n.settings.crop;
  const fw = (c.rotate === 90 || c.rotate === 270 ? n.info.height : n.info.width) * c.w;
  return r.w / fw;
}

hooks.pointerDown.push((e, sx, sy) => {
  const n = activePhoto();
  if (!n || S.tool === 'crop') return false;
  if (pendingColor && S.tab !== 'masks') cancelColorPick();
  if (pendingColor && insidePhoto(n, sx, sy)) {
    const [u, v] = screenToImage(n, sx, sy);
    const pc = pendingColor;
    pendingColor = null;
    document.getElementById('viewport').style.cursor = '';
    post('sample', { id: n.id, u, v }).then((lab) => {
      const fields = { colorL: lab[0], colorA: lab[1], colorB: lab[2] };
      if (pc.compId) {
        S.maskId = pc.maskId;
        updateMask({ component: { id: pc.compId, ...fields } }, { label: 'Pick color' });
      } else if (pc.maskId) {
        S.maskId = pc.maskId;
        addComponent('color', { ...fields, colorRange: 30 });
      } else {
        addMask('color', { ...fields, colorRange: 30 });
      }
    }).catch((err) => toast(err.message, true));
    return true;
  }
  if (S.tab !== 'masks') return false;
  if (brushActive() && insidePhoto(n, sx, sy)) {
    startStroke(e, n, sx, sy);
    return true;
  }
  return false;
});

// --- brush ---

let stroke = null;
function startStroke(e, n, sx, sy) {
  const vp = document.getElementById('viewport');
  vp.setPointerCapture(e.pointerId);
  const erase = S.brush.erase || e.altKey;
  stroke = { n, pts: [screenToImage(n, sx, sy)], screen: [[sx, sy]], erase };
  const ctx = brushCanvas.getContext('2d');
  brushCanvas.width = brushCanvas.clientWidth * devicePixelRatio;
  brushCanvas.height = brushCanvas.clientHeight * devicePixelRatio;
  ctx.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
  const long = Math.max(n.info.width, n.info.height);
  ctx.lineWidth = Math.max(2, S.brush.size * long * screenPerImagePx(n));
  ctx.lineCap = ctx.lineJoin = 'round';
  ctx.strokeStyle = erase ? 'rgba(80,140,255,0.35)' : 'rgba(255,64,96,0.4)';
  const move = (ev) => {
    const [x, y] = localPoint(ev);
    const last = stroke.screen[stroke.screen.length - 1];
    if (Math.hypot(x - last[0], y - last[1]) < 2) return;
    stroke.screen.push([x, y]);
    stroke.pts.push(screenToImage(n, x, y));
    ctx.clearRect(0, 0, brushCanvas.width, brushCanvas.height);
    ctx.beginPath();
    stroke.screen.forEach(([a, b], i) => (i ? ctx.lineTo(a, b) : ctx.moveTo(a, b)));
    ctx.stroke();
  };
  const up = () => {
    vp.removeEventListener('pointermove', move);
    vp.removeEventListener('pointerup', up);
    const pts = stroke.pts.map(([u, v]) => [+u.toFixed(4), +v.toFixed(4)]);
    if (pts.length === 1) pts.push([pts[0][0] + 0.0001, pts[0][1]]);
    updateMask({ addStroke: { points: pts, size: S.brush.size, feather: S.brush.feather, flow: S.brush.flow, erase: stroke.erase, componentId: brushShapeId() } }, { label: stroke.erase ? 'Erase brush' : 'Brush stroke' });
    setTimeout(() => ctx.clearRect(0, 0, brushCanvas.width, brushCanvas.height), 350);
    stroke = null;
  };
  vp.addEventListener('pointermove', move);
  vp.addEventListener('pointerup', up);
}

let cursorPt = null;
hooks.cursor.push((e, sx, sy) => {
  if (brushActive()) {
    cursorPt = [sx, sy];
    drawOverlay();
  } else if (cursorPt) {
    cursorPt = null;
    drawOverlay();
  }
});

// --- gradient handles ---

function handle(cls, x, y, r, onDrag) {
  const c = svg('circle', { class: cls, cx: x, cy: y, r });
  c.addEventListener('pointerdown', (e) => {
    e.stopPropagation();
    e.preventDefault();
    const vp = document.getElementById('viewport');
    const g = gestureId();
    const move = (ev) => { const [px, py] = localPoint(ev); onDrag(px, py, g, false); };
    const up = (ev) => {
      vp.removeEventListener('pointermove', move);
      vp.removeEventListener('pointerup', up);
      vp.removeEventListener('pointercancel', up);
      const [px, py] = localPoint(ev);
      onDrag(px, py, g, true);
    };
    // Released over a panel, the drag still ends.
    vp.setPointerCapture?.(e.pointerId);
    vp.addEventListener('pointermove', move);
    vp.addEventListener('pointerup', up);
    vp.addEventListener('pointercancel', up);
  });
  return c;
}

function paintLinear(o, n, c) {
  const [ax, ay] = toScreenPt(n, c.x1, c.y1);
  const [bx, by] = toScreenPt(n, c.x2, c.y2);
  const dx = bx - ax, dy = by - ay;
  const len = Math.hypot(dx, dy) || 1;
  const px = (-dy / len) * 4000, py = (dx / len) * 4000;
  o.append(svg('line', { class: 'ml', x1: ax - px, y1: ay - py, x2: ax + px, y2: ay + py }));
  o.append(svg('line', { class: 'ml dash', x1: bx - px, y1: by - py, x2: bx + px, y2: by + py }));
  o.append(svg('line', { class: 'ml dash', x1: ax, y1: ay, x2: bx, y2: by }));
  const set = (patch, g, final) => {
    Object.assign(c, patch);
    drawOverlay();
    updateMask({ component: { id: c.id, ...patch } }, { gesture: g, label: 'Move gradient', final });
  };
  o.append(handle('mh', ax, ay, 6, (x, y, g, f) => { const [u, v] = screenToImage(n, x, y); set({ x1: +u.toFixed(4), y1: +v.toFixed(4) }, g, f); }));
  o.append(handle('mh', bx, by, 6, (x, y, g, f) => { const [u, v] = screenToImage(n, x, y); set({ x2: +u.toFixed(4), y2: +v.toFixed(4) }, g, f); }));
  let origin = null;
  o.append(handle('mh pin', (ax + bx) / 2, (ay + by) / 2, 6, (x, y, g, f) => {
    const [u, v] = screenToImage(n, x, y);
    if (!origin) origin = [u, v, { ...c }];
    const du = u - origin[0], dv = v - origin[1], s = origin[2];
    set({ x1: +(s.x1 + du).toFixed(4), y1: +(s.y1 + dv).toFixed(4), x2: +(s.x2 + du).toFixed(4), y2: +(s.y2 + dv).toFixed(4) }, g, f);
    if (f) origin = null;
  }));
}

function paintRadial(o, n, c) {
  const asp = imageAspect(n);
  const a = ((c.angle || 0) * Math.PI) / 180, ca = Math.cos(a), sa = Math.sin(a);
  const local = (lx, ly) => {
    // aspect-corrected local -> image fractions
    const px = lx * ca - ly * sa, py = lx * sa + ly * ca;
    return [c.cx + px / asp, c.cy + py];
  };
  const ring = (k) => {
    const pts = [];
    for (let i = 0; i <= 72; i++) {
      const t = (i / 72) * Math.PI * 2;
      pts.push(toScreenPt(n, ...local(Math.cos(t) * c.rx * asp * k, Math.sin(t) * c.ry * k)).join(','));
    }
    return pts.join(' ');
  };
  o.append(svg('polyline', { class: 'ml', points: ring(1) }));
  if (c.feather > 0 && c.feather < 100) o.append(svg('polyline', { class: 'ml dash', points: ring(1 - c.feather / 100) }));
  const toLocal = (x, y) => {
    const [u, v] = screenToImage(n, x, y);
    const px = (u - c.cx) * asp, py = v - c.cy;
    return [px * ca + py * sa, -px * sa + py * ca, px, py];
  };
  const set = (patch, g, f) => {
    Object.assign(c, patch);
    drawOverlay();
    updateMask({ component: { id: c.id, ...patch } }, { gesture: g, label: 'Edit radial gradient', final: f });
  };
  const [cx, cy] = toScreenPt(n, c.cx, c.cy);
  o.append(handle('mh pin', cx, cy, 6, (x, y, g, f) => {
    const [u, v] = screenToImage(n, x, y);
    set({ cx: +clamp(u, -0.5, 1.5).toFixed(4), cy: +clamp(v, -0.5, 1.5).toFixed(4) }, g, f);
  }));
  for (const [lx, ly, axis] of [[c.rx * asp, 0, 'x'], [-c.rx * asp, 0, 'x'], [0, c.ry, 'y'], [0, -c.ry, 'y']]) {
    const [hx, hy] = toScreenPt(n, ...local(lx, ly));
    o.append(handle('mh', hx, hy, 5, (x, y, g, f) => {
      const [qx, qy] = toLocal(x, y);
      if (axis === 'x') set({ rx: +Math.max(0.01, Math.abs(qx) / asp).toFixed(4) }, g, f);
      else set({ ry: +Math.max(0.01, Math.abs(qy)).toFixed(4) }, g, f);
    }));
  }
  const [rx, ry] = toScreenPt(n, ...local(c.rx * asp * 1.18, 0));
  o.append(handle('mh', rx, ry, 4, (x, y, g, f) => {
    const [, , px, py] = toLocal(x, y);
    set({ angle: +((Math.atan2(py, px) * 180) / Math.PI).toFixed(1) }, g, f);
  }));
}

hooks.overlay.push((o) => {
  const n = activePhoto();
  if (!n || S.tab !== 'masks' || S.tool === 'crop') return;
  const m = activeMask(n);
  if (m) {
    for (const c of m.components || []) {
      if (c.type === 'linear') paintLinear(o, n, c);
      if (c.type === 'radial') paintRadial(o, n, c);
    }
  }
  if (brushActive() && cursorPt) {
    const long = Math.max(n.info.width, n.info.height);
    const r = Math.max(2, (S.brush.size * long * screenPerImagePx(n)) / 2);
    o.append(svg('circle', { class: 'brush-cursor', cx: cursorPt[0], cy: cursorPt[1], r }));
    o.append(svg('circle', { class: 'brush-cursor inner', cx: cursorPt[0], cy: cursorPt[1], r: r * (1 - S.brush.feather / 100) }));
  }
});

export function brushSize(f) {
  S.brush.size = clamp(+(S.brush.size * f).toFixed(4), 0.005, 0.4);
  build();
  drawOverlay();
}

export function initMasks() {
  on('selection', () => {
    cancelColorPick();
    if (S.tab === 'masks') build();
  });
  on('tab', (t) => {
    if (t !== 'masks') cancelColorPick();
    if (t === 'masks') build();
    drawOverlay();
    const n = activePhoto();
    if (n && S.overlay) refreshImage(n.id);
  });
  on('node-updated', (id) => {
    if (id !== S.active || S.tab !== 'masks') return;
    if (!mq && !mqueue.length && !mInflight && !stroke) refreshPanel(true);
  });
  on('claude-mask', ({ id, maskId }) => {
    if (id === S.active && maskId) {
      S.maskId = maskId;
      const m = activeMask();
      S.compId = m?.components?.[0]?.id || null;
      if (S.tab === 'masks') build();
    }
  });
}

export function rebuildMasks() {
  if (S.tab === 'masks') build();
}
