// The albums area: every album as a card with what it holds. The album
// button in the top bar opens and closes it.
import { S, on, emit } from './store.js';
import { post, getJSON } from './api.js';
import { h, toast, showMenu, fmtBytes as bytes } from './ui.js';
import { icon } from './icons.js';
import { ask } from './ask.js';

const main = document.getElementById('main');
const view = document.getElementById('albums-view');
const grid = document.getElementById('albums-grid');
const count = document.getElementById('albums-count');
const albumBtn = document.getElementById('album-btn');

export function albumsShown() {
  return !view.hidden;
}

export function showAlbums() {
  main.classList.add('albums');
  view.hidden = false;
  albumBtn.classList.add('on');
  document.getElementById('albums-back-name').textContent = document.getElementById('album-name').textContent;
  load();
}

export function hideAlbums() {
  if (view.hidden) return;
  main.classList.remove('albums');
  view.hidden = true;
  albumBtn.classList.remove('on');
  emit('albums-closed');
}

export function toggleAlbums() {
  if (view.hidden) showAlbums();
  else hideAlbums();
}

let seq = 0;
async function load() {
  const my = ++seq;
  try {
    const list = await getJSON('/api/albums');
    if (my !== seq) return;
    S.albums = list.map((a) => a.path);
    count.textContent = list.length;
    grid.replaceChildren(...list.map(card));
  } catch (err) {
    toast(err.message, true);
  }
}

const num = (n) => n.toLocaleString('en-US');

function dates(first, last) {
  if (!first) return '';
  const a = new Date(first), b = new Date(last || first);
  const day = { day: 'numeric' }, dm = { day: 'numeric', month: 'short' }, full = { day: 'numeric', month: 'short', year: 'numeric' };
  const f = (d, o) => d.toLocaleDateString('en-GB', o);
  if (a.toDateString() === b.toDateString()) return `Shot on ${f(a, full)}`;
  if (a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth()) return `Shot ${f(a, day)} to ${f(b, full)}`;
  if (a.getFullYear() === b.getFullYear()) return `Shot ${f(a, dm)} to ${f(b, full)}`;
  return `Shot ${f(a, full)} to ${f(b, full)}`;
}

function ago(t) {
  const s = (Date.now() - t) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 2 * 86400) return 'yesterday';
  if (s < 7 * 86400) return `${Math.round(s / 86400)} days ago`;
  return 'on ' + new Date(t).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' });
}

function list(parts) {
  return parts.filter(Boolean).join(', ');
}

function card(a) {
  const v = +new Date(a.modified);
  const cover = h('div', { class: `al-cover n${a.covers}` });
  for (let i = 0; i < a.covers; i++) {
    cover.append(h('img', { alt: '', loading: 'lazy', decoding: 'async', src: `/api/albums/cover?name=${encodeURIComponent(a.name)}&i=${i}&v=${v}` }));
  }
  if (!a.covers) cover.append(h('div', { class: 'al-empty' }, icon('image', 'ico'), h('span', {}, 'No photos yet')));

  const stats = h('dl', { class: 'exif al-stats' }, ...[
    ['Photos', a.photos], ['Edited', a.edited], ['Picks', a.picks], ['Rejected', a.rejects],
  ].map(([k, n]) => h('div', {}, h('dt', {}, k), h('dd', {}, num(n)))));

  const kinds = Object.entries(a.kinds || {}).sort((x, y) => y[1] - x[1]).map(([k, n]) => `${num(n)} ${k}`);
  const gear = list([a.cameras?.join(' and '), a.lenses?.length ? `with ${a.lenses.join(' and ')}` : '']).replace(', with', ' with');
  const focus = list([a.sharp && `${num(a.sharp)} sharp`, a.soft && `${num(a.soft)} out of focus`, a.motion && `${num(a.motion)} motion blur`]);
  const more = list([a.rated && `${num(a.rated)} rated`, a.masked && `${num(a.masked)} with masks`, a.notes && `${num(a.notes)} note${a.notes > 1 ? 's' : ''}`]);
  const lines = [
    dates(a.first, a.last),
    gear,
    focus,
    more,
    a.photos ? list([bytes(a.bytes) + ' on disk', kinds.join(', ')]) : '',
  ].filter(Boolean).map((t) => h('p', {}, t));
  if (a.missing) lines.push(h('p', { class: 'al-missing', title: 'The disk holding them may be disconnected, or the files were moved' }, `${num(a.missing)} file${a.missing > 1 ? 's' : ''} not found`));

  const menuBtn = h('button', { class: 'icon-btn sm al-menu', type: 'button', title: 'More', 'aria-label': `More for ${a.name}` }, h('span', { class: 'dots' }, '•••'));
  menuBtn.onclick = (e) => {
    e.stopPropagation();
    const r = menuBtn.getBoundingClientRect();
    showMenu(r.left, r.bottom + 4, [
      { label: a.current ? 'Back to this album' : 'Open', run: () => open(a) },
      { label: 'Rename', run: () => rename(a) },
      '-',
      { label: 'Move to Trash', run: () => remove(a) },
    ]);
  };

  const el = h('article', { class: 'al-card' + (a.current ? ' current' : ''), tabindex: '0', title: a.current ? 'The album open now' : `Open ${a.name}` },
    cover,
    h('div', { class: 'al-body' },
      h('div', { class: 'al-head' },
        h('div', { class: 'al-name' }, a.name),
        a.current ? h('span', { class: 'al-badge' }, 'Open') : null,
        menuBtn),
      stats,
      h('div', { class: 'al-lines' }, ...lines),
      h('div', { class: 'al-foot' }, `Changed ${ago(+new Date(a.modified))}`)));
  el.onclick = () => open(a);
  el.onkeydown = (e) => { if (e.key === 'Enter') open(a); };
  return el;
}

async function open(a) {
  hideAlbums();
  if (a.current) return;
  try {
    await post('albums/open', { name: a.path });
  } catch (err) {
    toast(err.message, true);
  }
}

function rename(a) {
  ask({
    title: 'Rename album',
    value: a.name,
    ok: 'Rename',
    run: async (to) => {
      if (!to || to === a.name) return;
      await post('albums/rename', { name: a.name, newName: to });
      load();
    },
  });
}

function remove(a) {
  if (a.current) {
    toast('Open another album before moving this one to the Trash.', true);
    return;
  }
  ask({
    title: `Move “${a.name}” to the Trash?`,
    text: 'Only the album goes: the photo files stay where they are. You can put it back from the Trash in Finder.',
    ok: 'Move to Trash',
    danger: true,
    run: async () => {
      await post('albums/delete', { name: a.name });
      load();
    },
  });
}

document.getElementById('albums-new').onclick = () => ask({
  title: 'New album',
  text: 'An empty album opens. Import photos into it, or select photos in another album and choose New album.',
  value: 'Untitled',
  ok: 'Create album',
  run: async (name) => {
    if (!name) throw new Error('Give the album a name.');
    const r = await post('albums/new', { name, ids: [] });
    hideAlbums();
    await post('albums/open', { name: r.path });
  },
});
document.getElementById('albums-back').onclick = hideAlbums;
albumBtn.onclick = toggleAlbums;

// Another album opened (by Claude, for example): refresh the cards.
on('nodes', (why) => { if (why === 'doc' && !view.hidden) load(); });
