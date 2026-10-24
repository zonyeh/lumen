// With several photos selected a bar over the album offers what applies to
// all of them: auto tone, reset, a new album made of them, removal.
import { S, on, select, upsert } from './store.js';
import { post } from './api.js';
import { h, toast, fmtBytes } from './ui.js';
import { ask } from './ask.js';
import { icon } from './icons.js';
import { renderAll, refreshImage, shownPhotos } from './canvas.js';

const vp = document.getElementById('viewport');
const count = h('span', { class: 'sb-count' });
const all = h('button', { class: 'link sb-all', type: 'button', title: 'Select every photo the album shows (⌘A)', onclick: () => select(shownPhotos(), { checking: true }) }, 'Select all');
const btn = (name, label, title, fn) => h('button', { class: 'dock-text icon-text', type: 'button', title, onclick: fn }, icon(name, 'ico'), h('span', { class: 'lbl' }, label));
const bar = h('div', { class: 'selbar', id: 'selbar', hidden: true },
  count, all,
  h('span', { class: 'dock-sep' }),
  btn('sparkle', 'Auto', 'Auto tone each photo (⇧A)', () => autoSelected()),
  btn('reset', 'Reset', 'Remove all edits, crop and masks included', () => resetSelected()),
  btn('plus', 'New album', 'Make a new album with these photos', () => newAlbum()),
  btn('remove', 'Remove', 'Remove from the album (⌫); the files stay on disk', () => removeSelected()),
  btn('trash', 'Move to Trash', 'Move the photo files to the Trash (⌘⌫)', () => trashFiles()),
  h('span', { class: 'dock-sep' }),
  h('button', { class: 'tool', type: 'button', title: 'Clear the selection (Esc)', onclick: () => select([]) }, icon('x', 'ico')));
vp.append(bar);

function photos() {
  return [...S.selection].filter((id) => S.nodes.get(id)?.type === 'photo');
}

const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;

function update() {
  const ids = photos();
  bar.hidden = S.loupe || ids.length < 2;
  if (bar.hidden) return;
  count.textContent = `${ids.length} selected`;
  const shown = shownPhotos();
  all.hidden = shown.every((id) => S.selection.has(id));
}

on('selection', update);
on('nodes', update);

let busy = false;

async function run(label, fn) {
  if (busy) return;
  busy = true;
  bar.classList.add('busy');
  try {
    await fn();
  } catch (err) {
    toast(`${label}: ${err.message}`, true);
  } finally {
    busy = false;
    bar.classList.remove('busy');
  }
}

function applied(ns) {
  ns.forEach(upsert);
  renderAll();
  ns.forEach((n) => refreshImage(n.id));
}

export function autoSelected() {
  const ids = photos();
  if (!ids.length) return;
  run('Auto tone', async () => {
    if (ids.length > 8) toast(`Auto tone on ${plural(ids.length, 'photo')}…`);
    applied(await post('auto', { ids }));
    toast(`Auto tone applied to ${plural(ids.length, 'photo')}. Undo with ⌘Z.`);
  });
}

function resetSelected() {
  const ids = photos();
  if (!ids.length) return;
  run('Reset', async () => {
    applied(await post('reset', { ids }));
    toast(`Edits removed from ${plural(ids.length, 'photo')}. Undo with ⌘Z.`);
  });
}

function removeSelected() {
  const ids = [...S.selection];
  if (!ids.length) return;
  run('Remove', async () => {
    await post('remove', { ids });
    toast(`${plural(ids.length, 'item')} removed from the album. Undo with ⌘Z; the files stay on disk.`, false, 5000);
  });
}

// ---------- files to the Trash ----------

const and = (xs) => (xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`);

// trashFiles moves the files of the selected photos to the Trash, after
// saying what that involves.
export async function trashFiles(ids) {
  ids = ids || photos();
  if (!ids.length) return;
  let plan;
  try {
    plan = await post('trash/plan', { ids });
  } catch (err) {
    toast(err.message, true);
    return;
  }
  const n = plan.files.length;
  const names = n <= 3 ? and(plan.files) : `${plan.files.slice(0, 2).join(', ')} and ${n - 2} more`;
  const text = [`${names} (${fmtBytes(plan.bytes)}) ${n === 1 ? 'goes' : 'go'} to the Trash and ${n === 1 ? 'leaves' : 'leave'} this album.`];
  if (plan.copies) text.push(`${plan.copies === 1 ? 'A virtual copy' : `${plan.copies} virtual copies`} of ${n === 1 ? 'it leaves' : 'them leave'} too.`);
  if (plan.others?.length) text.push(`${n === 1 ? 'It is' : 'They are'} also in ${and(plan.others.map((a) => `“${a}”`))}, where ${n === 1 ? 'it' : 'they'} will show as not found.`);
  text.push(n === 1 ? 'Finder can put it back from the Trash; ⌘Z returns it to this album.' : 'Finder can put them back from the Trash; ⌘Z returns them to this album.');
  ask({
    title: n === 1 ? 'Move this photo file to the Trash?' : `Move ${n} photo files to the Trash?`,
    text: text.join(' '),
    ok: 'Move to Trash',
    danger: true,
    run: async () => {
      const r = await post('trash', { ids });
      toast(`${plural(r.moved, 'file')} moved to the Trash. ⌘Z returns ${r.moved === 1 ? 'it' : 'them'} to the album.`, false, 5000);
    },
  });
}

// ---------- new album ----------

const dlg = document.getElementById('newalbum-dialog');
const nameIn = document.getElementById('na-name');
const openIn = document.getElementById('na-open');
const err = document.getElementById('na-err');
let pending = [];

export function newAlbum(ids) {
  pending = ids || [...S.selection].filter((id) => S.nodes.has(id));
  if (!pending.length) {
    toast('Select the photos for the new album first.', true);
    return;
  }
  document.getElementById('na-count').textContent = plural(pending.length, 'photo');
  const taken = new Set(S.albums.map((p) => p.split('/').pop().replace(/\.lumen$/, '').toLowerCase()));
  let name = 'Selection';
  for (let i = 2; taken.has(name.toLowerCase()); i++) name = `Selection ${i}`;
  nameIn.value = name;
  err.hidden = true;
  dlg.showModal();
  nameIn.select();
}

let creating = false;
document.getElementById('na-create').onclick = async () => {
  const name = nameIn.value.trim();
  if (!name) {
    nameIn.focus();
    return;
  }
  if (creating) return;
  creating = true;
  try {
    const r = await post('albums/new', { name, ids: pending, open: openIn.checked });
    dlg.close();
    if (!openIn.checked) {
      S.albums = [...new Set([...S.albums, r.path])].sort();
      toast(`Album “${name}” created with ${plural(pending.length, 'photo')}.`);
    }
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  } finally {
    creating = false;
  }
};
nameIn.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') {
    e.preventDefault();
    document.getElementById('na-create').click();
  }
});
