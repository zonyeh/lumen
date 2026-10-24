// Import, export and album dialogs.
import { S, upsert, select } from './store.js';
import { post, getJSON } from './api.js';
import { h, toast } from './ui.js';
import { renderAll, fitNodes } from './canvas.js';
import { icon } from './icons.js';

const imp = document.getElementById('import-dialog');
const fsPath = document.getElementById('fs-path');
const fsList = document.getElementById('fs-list');
const fsInfo = document.getElementById('fs-info');
const fsPlaces = document.getElementById('fs-volumes');
const fsCrumbs = document.getElementById('fs-crumbs');
const fsImport = document.getElementById('fs-import');
let cur = null;
let picked = new Set();
let lastClick = -1;

for (const el of document.querySelectorAll('dialog input')) el.addEventListener('keydown', (e) => e.stopPropagation());

function placeButton(label, path, ic) {
  return h('button', { type: 'button', class: cur && cur.path === path ? 'cur' : '', onclick: () => browse(path) }, icon(ic, 'ico sm'), label);
}

async function browse(path) {
  try {
    const d = await getJSON('/api/fs?path=' + encodeURIComponent(path || ''));
    cur = d;
    picked = new Set();
    lastClick = -1;
    fsPath.value = d.path;
    localStorage.setItem('lumen.importPath', d.path);
    fsPlaces.replaceChildren(
      h('div', { class: 'ph' }, 'Places'),
      placeButton('Home', d.home, 'home'),
      placeButton('Desktop', d.home + '/Desktop', 'desktop'),
      placeButton('Pictures', d.home + '/Pictures', 'image'),
      placeButton('Downloads', d.home + '/Downloads', 'folder'),
      ...(d.volumes.filter((v) => !v.endsWith('/Macintosh HD')).length ? [h('div', { class: 'ph' }, 'Drives')] : []),
      ...d.volumes.filter((v) => !v.endsWith('/Macintosh HD')).map((v) => placeButton(v.split('/').pop(), v, 'drive')),
    );
    renderCrumbs(d.path);
    renderList();
  } catch (err) {
    toast(`Cannot open that folder: ${err.message}`, true);
  }
}

function renderCrumbs(path) {
  const parts = path.split('/').filter(Boolean);
  const kids = [];
  parts.forEach((p, i) => {
    const full = '/' + parts.slice(0, i + 1).join('/');
    if (i) kids.push(h('span', { class: 'sep' }, '/'));
    kids.push(h('button', { type: 'button', title: full, onclick: () => browse(full) }, p));
  });
  fsCrumbs.replaceChildren(...kids);
  fsCrumbs.scrollLeft = fsCrumbs.scrollWidth;
}

function renderList() {
  const files = cur.entries.filter((e) => !e.dir);
  if (!cur.entries.length) {
    fsList.replaceChildren(h('div', { class: 'fs-empty' }, 'This folder is empty.'));
  } else {
    fsList.replaceChildren(...cur.entries.map((e, i) => {
      const row = h('div', { class: 'fs-item' + (e.dir ? ' dir' : '') + (picked.has(e.name) ? ' sel' : '') },
        h('span', { class: 'ic' }, icon(e.dir ? 'folder' : 'image', 'ico sm')),
        h('span', { class: 'nm' }, e.name),
        h('span', { class: 'sz' }, e.dir ? '' : (e.size / 1048576).toFixed(1) + ' MB'));
      row.onclick = (ev) => {
        if (e.dir) return browse(cur.path + '/' + e.name);
        if (ev.shiftKey && lastClick >= 0) {
          const [a, b] = [Math.min(lastClick, i), Math.max(lastClick, i)];
          for (let k = a; k <= b; k++) if (!cur.entries[k].dir) picked.add(cur.entries[k].name);
        } else if (ev.metaKey || ev.ctrlKey) {
          picked.has(e.name) ? picked.delete(e.name) : picked.add(e.name);
        } else {
          picked = new Set([e.name]);
        }
        lastClick = i;
        renderList();
      };
      row.ondblclick = () => { if (!e.dir) { picked = new Set([e.name]); doImport(); } };
      return row;
    }));
  }
  const n = picked.size || files.length;
  fsImport.disabled = !n;
  fsImport.textContent = picked.size ? `Import ${n} photo${n > 1 ? 's' : ''}` : n ? `Import all ${n}` : 'Import';
  fsInfo.textContent = picked.size ? 'Shift-click to select a range, ⌘-click to add' : files.length ? `${files.length} photo${files.length > 1 ? 's' : ''} in this folder` : 'No photos in this folder';
}

async function doImport() {
  if (!cur) return;
  const files = cur.entries.filter((e) => !e.dir);
  if (!picked.size && !files.length) return;
  const paths = picked.size ? [...picked].map((n) => cur.path + '/' + n) : [cur.path];
  fsImport.disabled = true;
  try {
    const res = await post('import', { paths });
    res.nodes.forEach(upsert);
    renderAll();
    select(res.nodes.map((n) => n.id));
    fitNodes(res.nodes.map((n) => n.id), true, 0.08, 1.25);
    imp.close();
    toast(`Imported ${res.nodes.length} photo${res.nodes.length > 1 ? 's' : ''}` + (res.skipped?.length ? `, ${res.skipped.length} skipped` : ''), !!res.skipped?.length);
  } catch (err) {
    toast(err.message, true);
  } finally {
    fsImport.disabled = false;
  }
}

export function openImport() {
  imp.showModal();
  browse(localStorage.getItem('lumen.importPath') || '');
}

document.getElementById('fs-up').onclick = () => cur && browse(cur.parent);
fsImport.onclick = doImport;

// ---------- export ----------

const exp = document.getElementById('export-dialog');
const segVal = (id) => document.querySelector(`#${id} button.active`)?.dataset.v;
for (const id of ['ex-format', 'ex-size']) {
  for (const b of document.querySelectorAll(`#${id} button`)) {
    b.onclick = () => {
      document.querySelectorAll(`#${id} button`).forEach((x) => x.classList.toggle('active', x === b));
      document.getElementById('ex-quality-row').hidden = segVal('ex-format') !== 'jpeg';
    };
  }
}
const q = document.getElementById('ex-quality');
q.oninput = () => (document.getElementById('ex-quality-v').value = q.value);

export function openExport() {
  const ids = [...S.selection].filter((id) => S.nodes.get(id)?.type === 'photo');
  const all = ids.length ? ids : S.order.filter((id) => S.nodes.get(id)?.type === 'photo');
  if (!all.length) return toast('There are no photos to export');
  exp.dataset.ids = JSON.stringify(all);
  document.getElementById('export-count').textContent = ids.length ? `${all.length} selected photo${all.length > 1 ? 's' : ''}` : `all ${all.length} photos`;
  document.getElementById('ex-dir').value = localStorage.getItem('lumen.exportDir') || S.home + '/Pictures/Lumen/exports';
  exp.showModal();
}

document.getElementById('ex-go').onclick = async () => {
  const ids = JSON.parse(exp.dataset.ids || '[]');
  const o = {
    dir: document.getElementById('ex-dir').value,
    format: segVal('ex-format'),
    quality: +q.value || 92,
    longEdge: +segVal('ex-size') || 0,
    suffix: document.getElementById('ex-suffix').value,
  };
  localStorage.setItem('lumen.exportDir', o.dir);
  try {
    await post('export', { ids, export: o });
    toast(`Exporting ${ids.length} photo${ids.length > 1 ? 's' : ''}…`);
    exp.close();
  } catch (err) {
    toast(err.message, true);
  }
};
