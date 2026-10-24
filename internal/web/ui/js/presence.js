// Claude's presence in the album: a cursor with a name tag that travels to
// the photo being edited, a status capsule, a progress line on the card
// while its new render is on the way, and the activity timeline.
import { S, on, emit, select } from './store.js';
import { h, timeAgo } from './ui.js';
import { nodeRect, fitNodes, isVisible, markClaude, markWorking } from './canvas.js';
import { openPhoto } from './loupe.js';

const cursor = document.getElementById('claude-cursor');
const bubble = document.getElementById('claude-bubble');
const pill = document.getElementById('presence');
const pillText = document.getElementById('presence-text');
const log = document.getElementById('activity');
const followBtn = document.getElementById('follow-btn');

let anchor = null; // {id, fx, fy}
let idleT = 0, hideT = 0;
const marked = new Set();
const working = new Map(); // id -> timeout

const EDITING_TOOLS = new Set(['set_adjustments', 'auto_tone', 'pick_white_balance', 'reset_adjustments', 'copy_adjustments', 'add_mask', 'update_mask', 'remove_mask', 'set_crop', 'presets']);

function place(instant) {
  if (!anchor) return;
  const n = S.nodes.get(anchor.id);
  if (!n) return;
  const r = nodeRect(n);
  const x = r.x + r.w * anchor.fx, y = r.y + r.h * anchor.fy;
  if (instant) cursor.style.transition = 'none';
  cursor.style.transform = `translate(${x}px, ${y}px)`;
  cursor.classList.toggle('flip', x > document.getElementById('viewport').clientWidth - 360);
  if (instant) requestAnimationFrame(() => (cursor.style.transition = ''));
}

export function addLog(e) {
  document.querySelector('#activity .activity-empty')?.remove();
  const who = e.actor === 'claude' ? 'Claude' : e.actor === 'user' ? 'You' : 'Lumen';
  const row = h('div', { class: 'act ' + (e.actor || '') }, h('span', { class: 'who' }, who), e.text || '', h('time', {}, timeAgo(e.time || Date.now())));
  log.prepend(row);
  while (log.children.length > 120) log.lastChild.remove();
}

function setWorking(id) {
  clearTimeout(working.get(id));
  markWorking([id], true);
  // The line goes away when the new render arrives, or after a while.
  working.set(id, setTimeout(() => { markWorking([id], false); working.delete(id); }, 6000));
}

export function claudeActivity(e) {
  addLog(e);
  pill.classList.remove('idle');
  pill.classList.add('live');
  pillText.textContent = e.text || e.tool || 'is working';
  pill.title = e.text || '';
  clearTimeout(idleT);
  idleT = setTimeout(() => {
    pill.classList.remove('live');
    pill.classList.add('idle');
    pillText.textContent = 'is idle';
  }, 6000);
  const ids = (e.ids || []).filter((id) => S.nodes.has(id));
  if (ids.length) {
    for (const id of marked) markClaude([id], false);
    marked.clear();
    markClaude(ids, true);
    ids.forEach((id) => marked.add(id));
    if (EDITING_TOOLS.has(e.tool)) ids.forEach(setWorking);
    anchor = { id: ids[0], fx: 0.38 + Math.random() * 0.3, fy: 0.32 + Math.random() * 0.3 };
    if (S.follow) {
      const quiet = Date.now() - S.lastUserInput > 2500;
      if (S.loupe) {
        // A photo is open on its own: follow Claude to the photo it edits.
        if (quiet && ids[0] !== S.loupe && S.nodes.get(ids[0])?.type === 'photo') openPhoto(ids[0]);
      } else if (quiet && ids.some((id) => !isVisible(S.nodes.get(id), -40))) fitNodes(ids, true, 0.12, 1.2);
      if (quiet && e.tool !== 'screenshot_album' && S.nodes.get(ids[0])?.type === 'photo' && (S.active !== ids[0] || S.selection.size !== 1)) select([ids[0]]);
    }
    setTimeout(() => place(false), S.follow ? 60 : 0);
  }
  if (e.text) {
    bubble.textContent = e.text;
    bubble.style.animation = 'none';
    void bubble.offsetWidth;
    bubble.style.animation = '';
    cursor.hidden = !anchor;
  }
  clearTimeout(hideT);
  hideT = setTimeout(() => {
    cursor.hidden = true;
    for (const id of marked) markClaude([id], false);
    marked.clear();
  }, 14000);
  emit('claude', e);
}

function setFollow(v) {
  S.follow = v;
  followBtn.classList.toggle('on', v);
  followBtn.setAttribute('aria-pressed', String(v));
  localStorage.setItem('lumen.follow', v ? '1' : '0');
}

export function initPresence(activity) {
  if (!activity || !activity.length) log.append(h('div', { class: 'activity-empty' }, 'Edits by you and Claude will be listed here.'));
  for (const e of activity || []) addLog(e);
  on('view', () => place(true));
  on('moved', () => place(true));
  on('image', (id) => {
    if (!working.has(id)) return;
    clearTimeout(working.get(id));
    working.delete(id);
    markWorking([id], false);
  });
  setFollow(localStorage.getItem('lumen.follow') !== '0');
  followBtn.onclick = () => setFollow(!S.follow);
}
