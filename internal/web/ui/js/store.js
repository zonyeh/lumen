// Client state and a tiny event bus.

export const S = {
  nodes: new Map(),
  order: [],
  selection: new Set(),
  active: null,
  tool: 'select',
  view: { x: 0, y: 0, z: 0.5 },
  schema: null,
  params: new Map(),
  tab: 'edit',
  before: false,
  focus: false,
  clip: false,
  maskId: null,
  compId: null,
  overlay: false,
  follow: true,
  loupe: null, // id of the photo open on its own
  checking: false, // the selection was made with the photos' checks
  photoFitZ: 0,
  lastUserInput: 0,
  brush: { size: 0.05, feather: 60, flow: 100, erase: false },
  clipboard: null,
  undo: 0,
  redo: 0,
  path: '',
  albums: [],
  home: '',
  presets: [],
};

const listeners = new Map();

export function on(ev, fn) {
  if (!listeners.has(ev)) listeners.set(ev, new Set());
  listeners.get(ev).add(fn);
}

export function emit(ev, data) {
  for (const fn of listeners.get(ev) || []) {
    try { fn(data); } catch (e) { console.error(ev, e); }
  }
}

export function setDoc(doc) {
  S.nodes.clear();
  S.order = [];
  for (const n of doc.nodes || []) {
    S.nodes.set(n.id, n);
    S.order.push(n.id);
  }
  for (const id of [...S.selection]) if (!S.nodes.has(id)) S.selection.delete(id);
  if (S.active && !S.nodes.has(S.active)) S.active = null;
}

// upsert returns true when the node is new or newer than what we hold.
export function upsert(n) {
  const cur = S.nodes.get(n.id);
  if (cur && cur.rev > n.rev) return false;
  S.nodes.set(n.id, n);
  if (!cur) S.order.push(n.id);
  return true;
}

// applyOrder sets the album order sent by the server.
export function applyOrder(order) {
  if (!order) return;
  const known = order.filter((id) => S.nodes.has(id));
  const rest = S.order.filter((id) => !known.includes(id) && S.nodes.has(id));
  S.order = [...known, ...rest];
}

export function removeIds(ids) {
  for (const id of ids) {
    S.nodes.delete(id);
    S.selection.delete(id);
    if (S.active === id) S.active = null;
  }
  S.order = S.order.filter((id) => S.nodes.has(id));
}

export function activeNode() {
  return S.active ? S.nodes.get(S.active) : null;
}

export function activePhoto() {
  const n = activeNode();
  return n && n.type === 'photo' ? n : null;
}

// select changes the selection. `checking` marks a selection made with the
// photos' checks (or the rectangle, or select all): only those fill the
// check; a plain click makes a photo the active one without checking it.
export function select(ids, { add = false, toggle = false, checking = false } = {}) {
  S.checking = checking;
  if (S.loupe && S.nodes.has(S.loupe)) {
    // A photo open on its own is always the selection.
    ids = [S.loupe];
    add = toggle = false;
  }
  if (!add && !toggle) S.selection.clear();
  for (const id of ids) {
    if (toggle && S.selection.has(id)) S.selection.delete(id);
    else S.selection.add(id);
  }
  S.active = ids.length ? (S.selection.has(ids[ids.length - 1]) ? ids[ids.length - 1] : [...S.selection].pop() || null) : null;
  if (!S.selection.size) S.active = null;
  const n = activeNode();
  if (!n || n.type !== 'photo' || !(n.settings.masks || []).some((m) => m.id === S.maskId)) {
    S.maskId = null;
    S.compId = null;
  }
  emit('selection');
}

export function defaultsFor(n) {
  return n && n.info && n.info.raw ? S.schema.defaultsRaw : S.schema.defaultsImage;
}

// getPath / setPath read and write dotted keys inside settings.
export function getPath(obj, key) {
  let o = obj;
  for (const k of key.split('.')) {
    if (o == null) return undefined;
    o = o[k];
  }
  return o;
}

export function setPath(obj, key, val) {
  const ks = key.split('.');
  let o = obj;
  for (let i = 0; i < ks.length - 1; i++) {
    if (o[ks[i]] == null || typeof o[ks[i]] !== 'object') o[ks[i]] = {};
    o = o[ks[i]];
  }
  o[ks[ks.length - 1]] = val;
}

export function nested(key, val) {
  const out = {};
  setPath(out, key, val);
  return out;
}

export function mergeDeep(dst, src) {
  for (const [k, v] of Object.entries(src)) {
    if (v && typeof v === 'object' && !Array.isArray(v) && dst[k] && typeof dst[k] === 'object' && !Array.isArray(dst[k])) mergeDeep(dst[k], v);
    else dst[k] = v;
  }
  return dst;
}
