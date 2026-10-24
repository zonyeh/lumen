// A photo open on its own: double-click (or Enter) opens it, the album is
// set aside and its full-resolution RAW is decoded so it can be inspected
// down to its pixels. Esc or double-click goes back to the album.
import { S, on, emit, select } from './store.js';
import { renderAll, fitPhoto, setView, zoomAt, isShown, box, photoZ100, scheduleImages } from './canvas.js';
import { toast } from './ui.js';

const bar = document.getElementById('loupebar');
const filterbar = document.getElementById('filterbar');
const nameEl = document.getElementById('lp-name');
const posEl = document.getElementById('lp-pos');
const rawEl = document.getElementById('lp-raw');
const zoomSeg = document.getElementById('lp-zoom');
const vp = document.getElementById('viewport');

let saved = null; // album view to return to
let loadSeq = 0;
let lastIndex = 0; // position of the open photo, to move on when it is removed

// Photos the album currently shows (filters apply), in album order.
function sequence() {
  const was = S.loupe;
  S.loupe = null;
  const ids = S.order.filter((id) => {
    const n = S.nodes.get(id);
    return n && n.type === 'photo' && isShown(n);
  });
  S.loupe = was;
  return ids;
}

export function openPhoto(id) {
  const n = S.nodes.get(id);
  if (!n || n.type !== 'photo') return;
  if (!S.loupe) saved = { ...S.view };
  S.loupe = id;
  document.getElementById('main').classList.add('loupe');
  bar.hidden = false;
  filterbar.hidden = true;
  select([id]);
  renderAll();
  // The develop panel slides in with the selection: fit once it settled.
  fitPhoto(false);
  setTimeout(() => S.loupe === id && fitPhoto(false), 260);
  updateBar();
  loadFull(id);
}

export function closePhoto({ restore = true } = {}) {
  const id = S.loupe;
  if (!id) return;
  S.loupe = null;
  document.getElementById('main').classList.remove('loupe');
  bar.hidden = true;
  filterbar.hidden = false;
  renderAll();
  // Another album was opened meanwhile: its own view, not this one's.
  if (saved && restore) setView(saved.x, saved.y, saved.z, false);
  saved = null;
  // Keep the photo that was open in view (it may be another one after
  // moving through the album).
  const n = S.nodes.get(id);
  if (n && isShown(n)) {
    const b = box(n);
    const H = vp.clientHeight;
    const top = (b.y * S.view.z) + S.view.y;
    if (top < 0 || top + b.h * S.view.z > H) setView(S.view.x, H / 2 - (b.y + b.h / 2) * S.view.z, S.view.z, true);
  }
  emit('loupe-closed', id);
}

export function step(d) {
  const ids = sequence();
  let i = ids.indexOf(S.loupe);
  // The open photo may have left the filtered set (its rating changed):
  // move on from where it was.
  if (i < 0) i = d > 0 ? lastIndex - 1 : lastIndex;
  const next = ids[i + d];
  if (next) openPhoto(next);
}

// toggleZoom switches between the whole photo and 100% (one photo pixel per
// screen pixel), zooming on the point under the pointer when there is one.
export function toggleZoom(sx, sy) {
  const n = S.loupe && S.nodes.get(S.loupe);
  if (!n) return;
  const z100 = photoZ100(n);
  if (!z100) return;
  if (Math.abs(S.view.z - z100) / z100 < 0.02) {
    fitPhoto(true);
    return;
  }
  const W = vp.clientWidth, H = vp.clientHeight;
  zoomAt(sx ?? W / 2, sy ?? H / 2, z100 / S.view.z);
}

function updateBar() {
  const n = S.loupe && S.nodes.get(S.loupe);
  if (!n) return;
  const ids = sequence();
  const i = ids.indexOf(n.id);
  if (i >= 0) lastIndex = i;
  nameEl.textContent = n.name;
  posEl.textContent = i >= 0 ? `${i + 1} of ${ids.length}` : '';
  document.getElementById('lp-prev').disabled = i <= 0;
  document.getElementById('lp-next').disabled = i < 0 || i >= ids.length - 1;
  updateZoomSeg();
}

function updateZoomSeg() {
  const n = S.loupe && S.nodes.get(S.loupe);
  if (!n) return;
  const z100 = photoZ100(n);
  const at100 = z100 && Math.abs(S.view.z - z100) / z100 < 0.02;
  for (const b of zoomSeg.children) b.classList.toggle('active', b.dataset.z === (at100 ? '100' : 'fit'));
}

// loadFull asks the app to decode the full-resolution RAW of the photo.
function loadFull(id) {
  const seq = ++loadSeq;
  const n = S.nodes.get(id);
  rawEl.textContent = n?.info?.raw ? 'Loading RAW…' : 'Loading…';
  rawEl.classList.add('busy');
  fetch(`/api/full/${id}`)
    .then(async (r) => {
      if (!r.ok) throw new Error(await r.text());
      return r.json();
    })
    .then((f) => {
      if (seq !== loadSeq) return;
      S.rawLoaded = id;
      rawEl.classList.remove('busy');
      rawEl.textContent = `${f.raw ? 'RAW' : (n?.info?.kind || 'Image').toUpperCase()} ${f.w} × ${f.h}`;
      rawEl.title = `Full resolution loaded in ${(f.ms / 1000).toFixed(1)} s: zoom in to see every pixel`;
      scheduleImages();
    })
    .catch((err) => {
      if (seq !== loadSeq) return;
      rawEl.classList.remove('busy');
      rawEl.title = '';
      if (/no such file/i.test(err.message)) {
        rawEl.textContent = 'File not found';
        toast(`${n?.name || 'This photo'} was not found: its drive may be disconnected, or the file was moved.`, true, 5000);
      } else {
        rawEl.textContent = 'Preview only';
        toast(`Could not load the full image: ${err.message}`, true);
      }
    });
}

document.getElementById('lp-back').onclick = closePhoto;
document.getElementById('lp-prev').onclick = () => step(-1);
document.getElementById('lp-next').onclick = () => step(1);
for (const b of zoomSeg.children) {
  b.onclick = () => (b.dataset.z === 'fit' ? fitPhoto(true) : !b.classList.contains('active') && toggleZoom());
}

on('open-photo', openPhoto);
on('close-photo', closePhoto);
on('toggle-zoom', () => toggleZoom());
on('view', () => S.loupe && updateZoomSeg());
on('region', ({ id, loading }) => {
  if (id === S.loupe) rawEl.classList.toggle('busy', loading);
});
on('nodes', (why) => {
  if (!S.loupe) return;
  if (!S.nodes.has(S.loupe)) {
    // The open photo was removed: show the one that took its place, or go
    // back to the album (also when another album was opened).
    const ids = sequence();
    if (why === 'remove' && ids.length) openPhoto(ids[Math.min(lastIndex, ids.length - 1)]);
    else closePhoto({ restore: why !== 'doc' });
    return;
  }
  updateBar();
});

// keyLoupe handles the keys of a photo open on its own (and Enter, which
// opens the selected photo); Esc is handled with the other Esc uses.
export function keyLoupe(k) {
  if (!S.loupe) {
    if (k === 'enter' && S.tool === 'select' && S.active && S.nodes.get(S.active)?.type === 'photo') {
      openPhoto(S.active);
      return true;
    }
    return false;
  }
  switch (k) {
    case 'arrowleft': case 'arrowup': step(-1); return true;
    case 'arrowright': case 'arrowdown': step(1); return true;
    case 'z': toggleZoom(pointer?.[0], pointer?.[1]); return true;
    case 'enter': if (S.tool !== 'select') return false; closePhoto(); return true;
  }
  return false;
}

// Z zooms on the point under the pointer.
let pointer = null;
vp.addEventListener('pointermove', (e) => {
  const r = vp.getBoundingClientRect();
  pointer = [e.clientX - r.left, e.clientY - r.top];
});
vp.addEventListener('pointerleave', () => { pointer = null; });

