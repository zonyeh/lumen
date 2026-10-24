// Entry point: loads the board, wires live events, tools, keyboard and the
// layer list.
import { S, on, emit, setDoc, upsert, removeIds, select, activePhoto, applyOrder } from './store.js';
import { post, getJSON, connectEvents } from './api.js';
import { h, toast, showMenu, hideMenu } from './ui.js';
import { renderAll, renderNode, refreshImage, fitNodes, fitAlbum, fitPhoto, applyView, setView, setSpace, scheduleImages, drawOverlay, hooks, nodeRect, editNote, setLayoutOverride, isShown, clickSelect, shownPhotos, reveal } from './canvas.js';
import { openPhoto, closePhoto, keyLoupe } from './loupe.js';
import { autoSelected, trashFiles } from './selection.js';
import { initDevelop, updatePanel, setTab, autoTone, resetGroups, setMeta, togglePick, pickMode, buildPresets, shutter, aperture } from './develop.js';
import { icon } from './icons.js';
import { initMasks, setOverlay, brushSize, rebuildMasks, cancelColorPick } from './masks.js';
import './crop.js';
import { initPresence, claudeActivity, addLog } from './presence.js';
import { openImport, openExport } from './dialogs.js';
import { albumsShown, hideAlbums } from './albums.js';
import { boxToImage } from './geom.js';

const layers = document.getElementById('layers');

// ---------- state sync ----------

async function loadState(first = false) {
  const st = await getJSON('/api/state');
  // A newer build is running: reload to pick up its interface.
  // A newer build is running (its interface differs from this page), or
  // the app restarted under us: load the page afresh. A plain reload can
  // bring the old page back from the cache in WebKit; a new navigation
  // with a changing query cannot.
  const mine = document.querySelector('meta[name=lumen-ui]')?.content;
  const restarted = S.boot && st.boot && st.boot !== S.boot;
  if ((st.ui && mine && st.ui !== mine) || restarted) {
    const key = 'lumen.reloaded.' + st.ui + '.' + st.boot;
    if (!sessionStorage.getItem(key)) {
      sessionStorage.setItem(key, '1');
      location.replace(location.pathname + '?v=' + encodeURIComponent(st.ui + '-' + st.boot));
      return;
    }
  }
  S.schema = st.schema;
  S.presets = st.presets || [];
  S.albums = st.albums || [];
  S.columns = st.doc.columns || 0;
  document.getElementById('cols-label').textContent = S.columns ? `${S.columns} columns` : 'Auto';
  S.path = st.path;
  S.home = st.home;
  S.boot = st.boot;
  const switched = S.path && document.body.dataset.album && document.body.dataset.album !== st.path;
  document.body.dataset.album = st.path;
  document.getElementById('album-name').textContent = st.doc.name;
  setDoc(st.doc);
  setHistory(st.undo, st.redo);
  if (first) {
    initDevelop();
    initMasks();
    initPresence(st.activity);
  }
  renderAll();
  renderLayers();
  applyFilter(false);
  lastCols = columnCount();
  colsLabel();
  sendViewHint();
  const saved = JSON.parse(localStorage.getItem('lumen.view.' + st.path) || 'null');
  if (first || switched) {
    if (saved) {
      Object.assign(S.view, saved);
      applyView();
    } else fitAlbum(false);
  }
  updatePanel(false);
}

function setHistory(u, r) {
  if (u == null) return;
  S.undo = u;
  S.redo = r;
  document.getElementById('undo-btn').disabled = !u;
  document.getElementById('redo-btn').disabled = !r;
}

function onEvent(e) {
  switch (e.type) {
    case 'node': {
      setHistory(e.undo, e.redo);
      let structural = !!e.order;
      for (const n of e.nodes || []) {
        const isNew = !S.nodes.has(n.id);
        if (!upsert(n)) continue;
        structural ||= isNew;
        renderNode(n.id);
        refreshImage(n.id);
        if (n.id === S.active) {
          updatePanel(e.actor === 'claude');
          emit('node-updated', n.id);
        }
      }
      if (e.order) applyOrder(e.order);
      if (structural) renderAll();
      renderLayers();
      scheduleFilter();
      emit('relayout');
      emit('nodes', 'node');
      break;
    }
    case 'remove':
      setHistory(e.undo, e.redo);
      removeIds(e.ids || []);
      emit('nodes', 'remove');
      renderAll();
      renderLayers();
      updatePanel(false);
      scheduleFilter();
      break;
    case 'doc':
      loadState(false).then(() => emit('nodes', 'doc')).catch((err) => toast(err.message, true));
      break;
    case 'activity':
      if (e.actor === 'claude') claudeActivity(e);
      else addLog(e);
      break;
    case 'focus': {
      const ids = (e.ids || []).filter((id) => S.nodes.has(id));
      if (!ids.length) break;
      if (S.loupe) {
        if (S.nodes.get(ids[0]).type === 'photo') openPhoto(ids[0]);
        break;
      }
      fitNodes(ids, true, 0.1, 2);
      if (e.detail?.select !== false) select(ids);
      break;
    }
    case 'export':
      toast(e.text, /problem/i.test(e.text || ''), 6000);
      addLog({ actor: 'system', text: e.text, time: e.time });
      break;
  }
}

// ---------- layers ----------

// The photo list is built once per album change and then updated row by
// row; thumbnails load lazily from the small cached previews. The search
// field above it narrows it down by name.
let layersT = 0;
let layerSig = '';
const rows = new Map();
const search = document.getElementById('photo-search');
const noMatch = h('div', { class: 'layers-empty', hidden: true });
let query = '';

function renderLayers() {
  clearTimeout(layersT);
  layersT = setTimeout(() => {
    const ids = [...S.order];
    const sig = ids.join(',');
    if (sig !== layerSig) {
      layerSig = sig;
      rows.clear();
      layers.replaceChildren(...ids.map((id) => {
        const row = buildRow(S.nodes.get(id));
        rows.set(id, row);
        return row;
      }), noMatch);
    }
    for (const id of ids) updateRow(id);
    applySearch();
  }, 80);
}

function matchesSearch(id) {
  const n = S.nodes.get(id);
  return !query || (n?.name || n?.text || '').toLowerCase().includes(query);
}

// listOrder is the rows the list shows, top to bottom.
function listOrder() {
  return S.order.filter((id) => rows.has(id) && matchesSearch(id));
}

function applySearch() {
  let shown = 0;
  for (const [id, row] of rows) {
    const ok = matchesSearch(id);
    row.hidden = !ok;
    if (ok) shown++;
  }
  const total = S.order.length;
  document.getElementById('layer-count').textContent = query ? `${shown} of ${total}` : total || '';
  noMatch.hidden = !query || shown > 0;
  noMatch.textContent = `No photo name contains “${search.value.trim()}”`;
}

search.addEventListener('input', () => {
  query = search.value.trim().toLowerCase();
  applySearch();
  layers.scrollTop = 0;
});
search.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    e.preventDefault();
    search.value = '';
    query = '';
    applySearch();
    search.blur();
  } else if (e.key === 'Enter' && query) {
    // Enter selects the photos found (the ones the album shows).
    e.preventDefault();
    const found = listOrder().filter((id) => S.nodes.get(id)?.type === 'photo' && isShown(S.nodes.get(id)));
    if (S.loupe && found.length) openPhoto(found[0]);
    else if (found.length) {
      select(found, { checking: true });
      reveal(found[0]);
    }
  }
});

function buildRow(n) {
  const id = n.id;
  const thumb = n.type === 'photo'
    ? h('img', { class: 'thumb', loading: 'lazy', decoding: 'async', alt: '', src: `/api/thumb/${id}?s=${Math.round(64 * (window.devicePixelRatio || 1))}` })
    : h('span', { class: 'thumb note' }, icon('note', 'ico sm'));
  const row = h('div', { class: 'layer', 'data-id': id }, thumb, h('div', { class: 'lt' }, h('div', { class: 'nm' }), h('div', { class: 'sub' })), h('span', { class: 'marks' }));
  row.onclick = (e) => {
    if (S.loupe) {
      if (n.type === 'photo') openPhoto(id);
      return;
    }
    clickSelect(id, e, listOrder());
    if (!e.metaKey && !e.ctrlKey && !e.shiftKey) reveal(id);
  };
  row.ondblclick = () => (n.type === 'photo' ? openPhoto(id) : fitNodes([id], true, 0.06, 8));
  row._sig = '';
  return row;
}

function updateRow(id) {
  const n = S.nodes.get(id);
  const row = rows.get(id);
  if (!n || !row) return;
  row.classList.toggle('sel', S.selection.has(id));
  const masks = (n.settings?.masks || []).length;
  const sig = [n.name, n.rating, n.flag, masks, n.sharp?.verdict].join('|');
  if (sig === row._sig) return;
  row._sig = sig;
  row.querySelector('.nm').textContent = n.name;
  const m = n.info?.meta || {};
  const sub = n.type === 'photo'
    ? [m.iso ? `ISO ${m.iso}` : '', m.fNumber ? aperture(m.fNumber) : '', m.exposureTime ? shutter(m.exposureTime) : ''].filter(Boolean)
    : ['Note'];
  row.querySelector('.sub').replaceChildren(...(sub.length ? sub : [(n.info?.kind || '').toUpperCase()]).map((t) => h('span', {}, t)));
  const marks = row.querySelector('.marks');
  marks.replaceChildren();
  if (n.rating) marks.append(h('span', { title: `${n.rating} stars` }, '★' + n.rating));
  if (masks) marks.append(h('span', { title: `${masks} mask${masks > 1 ? 's' : ''}` }, icon('luminance', 'ico sm')));
  if (n.flag === 'pick') marks.append(h('span', { title: 'Picked' }, icon('flag', 'ico sm')));
}

on('selection', () => { for (const id of rows.keys()) updateRow(id); });

// When the panel opens or closes: an album shown at full width is refitted
// to the new width; otherwise the selected photo is kept in view.
const fittedToWidth = () => S.fitZ && Math.abs(S.view.z - S.fitZ) / S.fitZ < 0.35;
// A photo open on its own is refitted when it was shown whole.
const photoFitted = () => S.loupe && (!S.photoFitZ || Math.abs(S.view.z - S.photoFitZ) / S.photoFitZ < 0.05);
on('inspector-close', () => {
  if (S.loupe) { if (photoFitted()) fitPhoto(true); return; }
  if (fittedToWidth()) fitAlbum(true, true);
});
on('inspector-open', () => {
  if (S.loupe) { if (photoFitted()) fitPhoto(true); return; }
  const n = S.active && S.nodes.get(S.active);
  if (!n) return;
  if (fittedToWidth() && !document.getElementById('right').matches('[style*="fixed"]') && getComputedStyle(document.getElementById('right')).position !== 'fixed') {
    fitAlbum(true, true);
    return;
  }
  const r = nodeRect(n);
  const vp = document.getElementById('viewport');
  const W = vp.clientWidth;
  let dx = 0;
  if (r.x + r.w > W - 24) dx = W - 24 - (r.x + r.w);
  if (r.x + dx < 24) dx = 24 - r.x;
  if (dx) setView(S.view.x + dx, S.view.y, S.view.z, true);
});

on('claude', (e) => {
  for (const el of layers.querySelectorAll('.layer.claude')) el.classList.remove('claude');
  for (const id of e.ids || []) layers.querySelector(`.layer[data-id="${id}"]`)?.classList.add('claude');
});

// Background around the photos: black, graphite or mid gray, as in Lightroom.
const SURROUND = { black: '#0b0b0b', graphite: '#232323', mid: '#484848' };
function setSurround(k) {
  if (!SURROUND[k]) k = 'graphite';
  document.getElementById('viewport').style.setProperty('--surround', SURROUND[k]);
  for (const b of document.querySelectorAll('#surround button')) b.classList.toggle('active', b.dataset.surround === k);
  localStorage.setItem('lumen.surround', k);
}
for (const b of document.querySelectorAll('#surround button')) b.onclick = () => setSurround(b.dataset.surround);
setSurround(localStorage.getItem('lumen.surround'));

const actPane = document.getElementById('activity-pane');
actPane.classList.toggle('closed', localStorage.getItem('lumen.activity') === 'closed');
document.getElementById('activity-toggle').onclick = () => {
  actPane.classList.toggle('closed');
  localStorage.setItem('lumen.activity', actPane.classList.contains('closed') ? 'closed' : 'open');
};

// ---------- tools ----------

function setTool(t) {
  const prev = S.tool;
  if (t === 'mask') {
    setTab('masks');
    t = 'select';
  }
  S.tool = t;
  const masking = S.tab === 'masks' && t === 'select';
  for (const b of document.querySelectorAll('#tools .tool')) b.classList.toggle('active', b.dataset.tool === 'mask' ? masking : b.dataset.tool === t && !masking);
  const vp = document.getElementById('viewport');
  vp.classList.toggle('hand', t === 'hand');
  vp.classList.toggle('note-tool', t === 'note');
  if (prev === 'crop' || t === 'crop') {
    renderAll();
    const n = activePhoto();
    if (n) refreshImage(n.id);
  }
  drawOverlay();
}
on('tool', setTool);
on('tab', (t) => {
  setTool(S.tool);
  // Masks are drawn on one photo: opening them opens the photo on its own.
  if (t === 'masks' && !S.loupe) {
    const n = activePhoto();
    if (n) openPhoto(n.id);
  }
});
// Back in the album, the panel shows the develop sliders again.
on('loupe-closed', () => { if (S.tab === 'masks') setTab('edit'); });
for (const b of document.querySelectorAll('#tools .tool')) b.onclick = () => setTool(b.dataset.tool === 'mask' && S.tab === 'masks' ? (setTab('edit'), 'select') : b.dataset.tool);

// White balance picker on the photo.
hooks.pointerDown.unshift((e, sx, sy) => {
  if (!pickMode.wb) return false;
  const n = activePhoto();
  if (!n) return false;
  const r = nodeRect(n);
  const bx = (sx - r.x) / r.w, by = (sy - r.y) / r.h;
  if (bx < 0 || by < 0 || bx > 1 || by > 1) return false;
  const [u, v] = boxToImage(n, bx, by, S.tool === 'crop');
  post('wbpick', { id: n.id, u, v })
    .then((ns) => { ns.forEach(upsert); refreshImage(n.id); updatePanel(true); })
    .catch((err) => toast(err.message, true));
  togglePick(false);
  return true;
});

// ---------- commands ----------

const photosSel = () => [...S.selection].filter((id) => S.nodes.get(id)?.type === 'photo');

function undo() { post('undo').then((r) => r.ok && toast('Undo: ' + r.label)).catch((err) => toast(err.message, true)); }
function redo() { post('redo').then((r) => r.ok && toast('Redo: ' + r.label)).catch((err) => toast(err.message, true)); }

function removeSel() {
  const ids = [...S.selection];
  if (!ids.length) return;
  post('remove', { ids }).catch((err) => toast(err.message, true));
}

function duplicate() {
  const n = activePhoto() || (S.active && S.nodes.get(S.active));
  if (!n) return;
  post('duplicate', { id: n.id }).then((c) => { upsert(c); renderAll(); select([c.id]); }).catch((err) => toast(err.message, true));
}

function copySettings() {
  const n = activePhoto();
  if (!n) return;
  S.clipboard = n.id;
  toast('Settings copied from ' + n.name);
}

function pasteSettings() {
  const to = photosSel().filter((id) => id !== S.clipboard);
  if (!S.clipboard || !to.length) return toast('Copy settings from a photo, then select others');
  post('copy', { from: S.clipboard, to })
    .then((ns) => { ns.forEach(upsert); ns.forEach((n) => refreshImage(n.id)); updatePanel(true); toast(`Pasted to ${ns.length} photo(s)`); })
    .catch((err) => toast(err.message, true));
}
on('copy-settings', copySettings);
on('paste-settings', pasteSettings);

// Focus peaking on every photo in view, to check sharpness and cull.
function toggleFocus(v) {
  S.focus = v ?? !S.focus;
  const b = document.getElementById('focus-btn');
  b.classList.toggle('on', S.focus);
  b.setAttribute('aria-pressed', String(S.focus));
  scheduleImages();
}

// Zebra on blown highlights and crushed shadows, for every photo in view.
function toggleClip(v) {
  S.clip = v ?? !S.clip;
  const b = document.getElementById('clip-btn');
  b.classList.toggle('on', S.clip);
  b.setAttribute('aria-pressed', String(S.clip));
  scheduleImages();
}

// Before shows the selected photos without edits, or every photo when
// nothing is selected.
function toggleBefore(v) {
  S.before = v ?? !S.before;
  document.getElementById('before-btn').classList.toggle('on', S.before);
  scheduleImages();
}
on('selection', () => { if (S.before) scheduleImages(); });
// The album was hidden behind the albums area: show what is on screen now.
on('albums-closed', () => { scheduleImages(); fitAlbum(false, true); });

function focusSearch() {
  if (narrowQ.matches) { S.leftDrawer = true; applyLeft(); }
  else if (!S.leftDocked) { S.leftDocked = true; applyLeft(); }
  search.focus();
  search.select();
}


document.getElementById('undo-btn').onclick = undo;
document.getElementById('redo-btn').onclick = redo;
document.getElementById('before-btn').onclick = () => toggleBefore();
document.getElementById('clip-btn').onclick = () => toggleClip();
// In the single photo view the photo stays selected: closing its panel
// goes back to the album.
document.getElementById('inspector-close').onclick = () => (S.loupe ? closePhoto() : select([]));
document.getElementById('focus-btn').onclick = () => toggleFocus();
document.getElementById('import-btn').onclick = openImport;
document.getElementById('empty-import').onclick = openImport;
document.getElementById('export-btn').onclick = openExport;

function setColumns(k) {
  S.columns = k;
  document.getElementById('cols-label').textContent = k ? `${k} columns` : 'Auto';
  post('sort', { by: 'none', columns: k })
    .then(() => setTimeout(() => fitAlbum(), 420))
    .catch((err) => toast(err.message, true));
}

document.getElementById('cols-btn').onclick = (e) => {
  const r = e.currentTarget.getBoundingClientRect();
  showMenu(r.left, r.top - 8 - 9 * 30, [
    { label: (S.columns ? '' : '✓ ') + 'Automatic', run: () => setColumns(0) },
    '-',
    ...[2, 3, 4, 5, 6, 7, 8].map((k) => ({ label: (S.columns === k ? '✓ ' : '') + `${k} columns`, run: () => setColumns(k) })),
  ]);
};

const sortBy = (by, reverse = false) => post('sort', { by, reverse }).catch((err) => toast(err.message, true));

function moveSel(where) {
  const ids = S.order.filter((id) => S.selection.has(id));
  if (!ids.length) return;
  const rest = S.order.filter((id) => !S.selection.has(id));
  if (!rest.length) return;
  const body = where === 'start' ? { before: rest[0] } : { after: rest[rest.length - 1] };
  post('reorder', { ids, ...body, label: where === 'start' ? 'Move to start' : 'Move to end' }).catch((err) => toast(err.message, true));
}

on('context', ({ x, y, node }) => {
  if (!node) {
    showMenu(x, y, [
      { label: 'Import photos…', run: openImport },
      { label: 'Fit the album to the window', key: '⇧1', run: () => fitAlbum() },
      '-',
      { label: 'Sort by capture time', run: () => sortBy('time') },
      { label: 'Sort by name', run: () => sortBy('name') },
      { label: 'Sort by rating', run: () => sortBy('rating') },
    ]);
    return;
  }
  const photo = node.type === 'photo';
  showMenu(x, y, [
    photo && { label: 'Virtual copy', key: '⌘D', run: duplicate },
    photo && { label: 'Auto tone', key: '⇧A', run: () => autoTone() },
    photo && { label: 'Copy settings', key: '⇧⌘C', run: copySettings },
    photo && { label: 'Paste settings', key: '⇧⌘V', run: pasteSettings },
    photo && { label: 'Reset settings', run: () => resetGroups([]) },
    photo && '-',
    node.type === 'note' && { label: 'Edit text', run: () => editNote(node.id) },
    { label: 'Move to the start', run: () => moveSel('start') },
    { label: 'Move to the end', run: () => moveSel('end') },
    photo && { label: 'Export…', key: '⌘E', run: openExport },
    '-',
    { label: 'Remove from album', key: '⌫', run: removeSel },
    photo && { label: S.selection.size > 1 ? 'Move files to Trash' : 'Move file to Trash', key: '⌘⌫', run: () => trashFiles() },
  ].filter(Boolean));
});
document.addEventListener('pointerdown', (e) => { if (!e.target.closest('#menu')) hideMenu(); });

// ---------- keyboard ----------

document.addEventListener('keydown', (e) => {
  if (albumsShown() && !e.target.closest('dialog')) {
    // The albums area has no album shortcuts: Esc goes back to the album.
    if (e.key === 'Escape') { e.preventDefault(); hideAlbums(); }
    return;
  }
  const typing = e.target.closest('textarea, select, [contenteditable=true], dialog') || (e.target.matches('input') && e.target.type !== 'range' && e.target.type !== 'checkbox');
  if (typing) return;
  if (e.target.type === 'range' && e.key.startsWith('Arrow')) return;
  const mod = e.metaKey || e.ctrlKey;
  const k = e.key.toLowerCase();
  S.lastUserInput = Date.now();
  if (k === ' ' && !e.repeat) { setSpace(true); e.preventDefault(); return; }
  if (mod && k === 'z') { e.preventDefault(); e.shiftKey ? redo() : undo(); return; }
  if (mod && k === 'y') { e.preventDefault(); redo(); return; }
  if (mod && k === 'd') { e.preventDefault(); duplicate(); return; }
  if (mod && e.shiftKey && k === 'c') { e.preventDefault(); copySettings(); return; }
  if (mod && e.shiftKey && k === 'v') { e.preventDefault(); pasteSettings(); return; }
  if (mod && k === 'e') { e.preventDefault(); openExport(); return; }
  if (mod && k === 'a') { e.preventDefault(); select(shownPhotos(), { checking: true }); return; }
  if (mod && k === 'f') { e.preventDefault(); focusSearch(); return; }
  if (mod && k === 'backspace') { e.preventDefault(); trashFiles(); return; }
  if (mod && (k === '=' || k === '+')) { e.preventDefault(); document.getElementById('zoom-in').click(); return; }
  if (mod && k === '-') { e.preventDefault(); document.getElementById('zoom-out').click(); return; }
  if (mod && k === '0') { e.preventDefault(); fitAlbum(); return; }
  if (mod) return;
  if (keyLoupe(k)) { e.preventDefault(); return; }
  switch (k) {
    case 'v': setTool('select'); break;
    case 'h': setTool('hand'); break;
    case 'c': setTool(S.tool === 'crop' ? 'select' : 'crop'); break;
    case 'm': setTool('mask'); break;
    case 'n': case 't': setTool('note'); break;
    case 'w': togglePick(); break;
    case 'j': toggleClip(); break;
    case 'o': setOverlay(!S.overlay); break;
    case '\\': toggleBefore(); break;
    case '[': brushSize(1 / 1.2); break;
    case ']': brushSize(1.2); break;
    case 'f':
      if (e.shiftKey) toggleFocus();
      else if (S.loupe) fitPhoto();
      else fitNodes(S.selection.size ? [...S.selection] : null, true, 0.08, 8);
      break;
    case '!': fitAlbum(); break;
    case 'p': setMeta({ flag: 'pick' }); break;
    case 'x': setMeta({ flag: 'reject' }); break;
    case 'u': setMeta({ flag: '' }); break;
    case 'a': if (e.shiftKey) (S.selection.size > 1 ? autoSelected() : autoTone()); break;
    case 'backspace': case 'delete': removeSel(); break;
    case 'escape':
      if (!document.getElementById('menu').hidden) { hideMenu(); break; }
      if (cancelColorPick()) break;
      if (document.getElementById('app').classList.contains('left-open')) { S.leftDrawer = false; applyLeft(); break; }
      if (pickMode.wb) togglePick(false);
      else if (S.tool !== 'select') setTool('select');
      else if (S.loupe) closePhoto();
      else select([]);
      break;
    case 'enter': if (S.tool === 'crop') setTool('select'); break;
    default:
      if (/^[0-5]$/.test(k)) setMeta({ rating: +k });
      else return;
  }
  e.preventDefault();
});
document.addEventListener('keyup', (e) => { if (e.key === ' ') setSpace(false); });
// Space released while the window was in the background must not leave
// every click panning.
window.addEventListener('blur', () => setSpace(false));

on('view', () => {
  clearTimeout(on.viewT);
  on.viewT = setTimeout(() => localStorage.setItem('lumen.view.' + S.path, JSON.stringify(S.view)), 400);
});

// ---------- boot ----------

let booted = false;
connectEvents(onEvent, () => {
  loadState(!booted).then(() => { booted = true; }).catch((err) => toast('Could not load the album: ' + err.message, true));
});
setTool('select');
window.lumen = { S, fitNodes, refreshImage, scheduleImages, buildPresets, rebuildMasks };

// ---------- filters ----------

// Same geometry as internal/doc/layout.go.
const COLUMN = 400, GUTTER = 14;
const fstars = document.getElementById('f-stars');
const fsharp = document.getElementById('f-sharp');
const fcount = document.getElementById('f-count');
const fclear = document.getElementById('f-clear');
S.filter = JSON.parse(localStorage.getItem('lumen.filter') || '{"rating":0,"sharp":"all"}');

for (const b of fstars.children) b.append(icon('star', ''));

function filterActive() {
  return S.filter.rating > 0 || S.filter.sharp !== 'all';
}

function matches(n, ignoreSharp = false) {
  if (!filterActive()) return true;
  if (n.type !== 'photo') return false;
  if ((n.rating || 0) < S.filter.rating) return false;
  if (!ignoreSharp && S.filter.sharp !== 'all' && n.sharp?.verdict !== S.filter.sharp) return false;
  return true;
}

// applyFilter hides what does not match and lays the rest out as its own
// masonry wall (with the album's current number of columns).
function applyFilter(refit = true) {
  const photos = S.order.map((id) => S.nodes.get(id)).filter((n) => n?.type === 'photo');
  for (const b of fstars.children) b.classList.toggle('on', +b.dataset.r <= S.filter.rating);
  const counts = { sharp: 0, soft: 0, motion: 0 };
  for (const n of photos) if (matches(n, true) && n.sharp) counts[n.sharp.verdict] = (counts[n.sharp.verdict] || 0) + 1;
  for (const b of fsharp.children) {
    b.classList.toggle('active', b.dataset.v === S.filter.sharp);
    const c = b.querySelector('.fc');
    if (c) c.textContent = counts[b.dataset.v] ?? '';
  }
  const pending = photos.filter((n) => !n.sharp).length;
  fsharp.title = pending ? `Measuring sharpness of ${pending} photo${pending > 1 ? 's' : ''}…` : 'Sharpness is measured on the crispest edges of each photo';
  const active = filterActive();
  document.getElementById('filterbar').classList.toggle('active', active);
  fclear.hidden = !active;
  if (!active) {
    fcount.textContent = `${photos.length} photo${photos.length === 1 ? '' : 's'}`;
    setLayoutOverride(null);
  } else {
    const shown = S.order.map((id) => S.nodes.get(id)).filter((n) => n && matches(n));
    fcount.textContent = `${shown.length} of ${photos.length} photos`;
    let cols = 1;
    for (const n of S.nodes.values()) cols = Math.max(cols, Math.round(n.x / (COLUMN + GUTTER)) + 1);
    cols = Math.max(1, Math.min(cols, shown.length || 1));
    const heights = new Array(cols).fill(0);
    const m = new Map();
    for (const n of shown) {
      let c = 0;
      for (let i = 1; i < cols; i++) if (heights[i] < heights[c] - 0.5) c = i;
      m.set(n.id, { x: c * (COLUMN + GUTTER), y: heights[c], w: COLUMN, h: n.h });
      heights[c] += n.h + GUTTER;
    }
    setLayoutOverride(m);
    const hidden = [...S.selection].filter((id) => !m.has(id));
    if (hidden.length && !S.loupe) select([...S.selection].filter((id) => m.has(id)));
  }
  localStorage.setItem('lumen.filter', JSON.stringify(S.filter));
  if (refit) fitAlbum(true);
  scheduleImages();
}

let filterT = 0;
function scheduleFilter() {
  clearTimeout(filterT);
  filterT = setTimeout(() => applyFilter(false), 120);
}

for (const b of fstars.children) {
  b.onclick = () => {
    const r = +b.dataset.r;
    S.filter.rating = S.filter.rating === r ? 0 : r;
    applyFilter();
  };
}
for (const b of fsharp.children) b.onclick = () => { S.filter.sharp = b.dataset.v; applyFilter(); };
fclear.onclick = () => { S.filter = { rating: 0, sharp: 'all' }; applyFilter(); };

// ---------- responsive panels ----------

const narrowQ = matchMedia('(max-width: 1100px)');
const sidebarBtn = document.getElementById('sidebar-btn');
S.leftDocked = localStorage.getItem('lumen.left') !== 'closed';
S.leftDrawer = false;

function applyLeft() {
  const app = document.getElementById('app');
  if (narrowQ.matches) {
    app.classList.remove('left-closed');
    app.classList.toggle('left-open', S.leftDrawer);
  } else {
    app.classList.remove('left-open');
    app.classList.toggle('left-closed', !S.leftDocked);
  }
  const on = narrowQ.matches ? S.leftDrawer : S.leftDocked;
  sidebarBtn.classList.toggle('on', on);
  sidebarBtn.setAttribute('aria-pressed', String(on));
}

sidebarBtn.onclick = () => {
  if (narrowQ.matches) S.leftDrawer = !S.leftDrawer;
  else {
    S.leftDocked = !S.leftDocked;
    localStorage.setItem('lumen.left', S.leftDocked ? 'open' : 'closed');
  }
  applyLeft();
  setTimeout(() => { sendViewHint(); fitAlbum(true, true); }, 320);
};
narrowQ.addEventListener('change', () => { S.leftDrawer = false; applyLeft(); });
// A click on the album closes the photo-list drawer.
document.getElementById('viewport').addEventListener('pointerdown', () => {
  if (S.leftDrawer) { S.leftDrawer = false; applyLeft(); }
}, true);
applyLeft();

// The album width (ignoring the develop panel, which comes and goes)
// decides how many automatic columns fit.
function sendViewHint() {
  const leftW = !narrowQ.matches && S.leftDocked ? 248 : 0;
  return post('view', { width: window.innerWidth - leftW }).catch(() => {});
}

// Columns follow the window: when a relayout changes their number, an album
// shown at full width is refitted.
let lastCols = 0;
function columnCount() {
  const xs = new Set();
  for (const n of S.nodes.values()) xs.add(Math.round(n.x));
  return xs.size;
}
function colsLabel() {
  const fit = columnCount();
  const el = document.getElementById('cols-label');
  el.textContent = S.columns ? `${Math.min(S.columns, fit || S.columns)} columns` : 'Auto';
  document.getElementById('cols-btn').title = S.columns > fit ? `${S.columns} columns chosen, ${fit} fit in this window` : 'Number of columns';
}

on('relayout', () => {
  if (S.loupe) return;
  colsLabel();
  const c = columnCount();
  if (lastCols && c !== lastCols && (fittedToWidth() || !S.fitZ)) fitAlbum(true, true);
  lastCols = c;
});

let resizeT = 0;
window.addEventListener('resize', () => {
  clearTimeout(resizeT);
  resizeT = setTimeout(() => {
    sendViewHint().then(() => setTimeout(() => { applyFilter(false); fitAlbum(true, true); }, 350));
  }, 200);
});
