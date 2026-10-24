// The develop panel: histogram, Lightroom-style sliders, tone curve, color
// mixer and grading wheels. Edits are sent as coalesced patches.
import { S, on, emit, upsert, activePhoto, defaultsFor, getPath, setPath, nested, mergeDeep } from './store.js';
import { post } from './api.js';
import { h, fmtVal, clamp, gestureId, toast } from './ui.js';
import { refreshImage, renderNode, renderAll, histFor } from './canvas.js';
import { icon } from './icons.js';

const panel = document.getElementById('panel');
const empty = document.getElementById('panel-empty');
const tabEdit = document.getElementById('tab-edit');
const histCanvas = document.getElementById('histogram');
const clipInfo = document.getElementById('clip-info');

const controls = new Map(); // key -> control
const sections = [];
let dragging = null; // key being dragged
let lastHist = null;

// ---------- color helpers ----------

export function oklchCss(L, C, hDeg) {
  const hr = (hDeg * Math.PI) / 180;
  const a = C * Math.cos(hr), b = C * Math.sin(hr);
  const l_ = L + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = L - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = L - 0.0894841775 * a - 1.291485548 * b;
  const l = l_ ** 3, m = m_ ** 3, s = s_ ** 3;
  let r = 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s;
  let g = -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s;
  let bb = -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s;
  const enc = (v) => {
    v = clamp(v, 0, 1);
    return Math.round((v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055) * 255);
  };
  return `rgb(${enc(r)},${enc(g)},${enc(bb)})`;
}

const BAND_HUE = [25, 58, 100, 145, 195, 255, 295, 335];

function trackFor(key) {
  if (key === 'temp') return 'linear-gradient(90deg,#3d6fd8,#cfd4dc 50%,#e2b23a)';
  if (key === 'tint') return 'linear-gradient(90deg,#3fae4a,#cfd4dc 50%,#c34fc7)';
  let m = key.match(/^hsl\.(\w+)\.(hue|sat|lum)$/);
  if (m) {
    const hue = BAND_HUE[S.schema.bands.indexOf(m[1])];
    if (m[2] === 'hue') return `linear-gradient(90deg,${oklchCss(0.7, 0.14, hue - 35)},${oklchCss(0.7, 0.14, hue)},${oklchCss(0.7, 0.14, hue + 35)})`;
    if (m[2] === 'sat') return `linear-gradient(90deg,${oklchCss(0.7, 0, hue)},${oklchCss(0.7, 0.16, hue)})`;
    return `linear-gradient(90deg,${oklchCss(0.35, 0.1, hue)},${oklchCss(0.9, 0.08, hue)})`;
  }
  m = key.match(/^bw\.(\w+)$/);
  if (m) {
    const hue = BAND_HUE[S.schema.bands.indexOf(m[1])];
    return `linear-gradient(90deg,#222,${oklchCss(0.7, 0.13, hue)},#ddd)`;
  }
  if (/^grading\.\w+\.lum$/.test(key)) return 'linear-gradient(90deg,#1a1a1d,#d8d8dc)';
  if (key === 'calibration.shadowTint') return 'linear-gradient(90deg,#3fae4a,#cfd4dc 50%,#c34fc7)';
  return null;
}

// ---------- sliders ----------

function paramOf(key) {
  return S.params.get(key);
}

// Temperature moves on a logarithmic scale, as in Lightroom: the useful
// 2500-8000 K range gets most of the track instead of a sliver.
const isLog = (p) => p.key === 'temp' && p.min > 0; // not the relative mask slider
const toPos = (p, v) => (isLog(p) ? Math.log(Math.max(v, 1)) : v);
const fromPos = (p, x) => (isLog(p) ? Math.round(Math.exp(x) / 10) * 10 : x);

function setFill(input, p, v) {
  const lo = toPos(p, p.min), span = toPos(p, p.max) - lo;
  const pos = ((toPos(p, v) - lo) / span) * 100;
  if (p.min < 0 && p.max > 0 && p.key !== 'temp') {
    const zero = ((0 - p.min) / span) * 100;
    input.style.setProperty('--a', Math.min(zero, pos) + '%');
    input.style.setProperty('--b', Math.max(zero, pos) + '%');
  } else if (p.key === 'temp') {
    input.style.setProperty('--a', '0%');
    input.style.setProperty('--b', '0%');
  } else {
    input.style.setProperty('--a', '0%');
    input.style.setProperty('--b', pos + '%');
  }
}

// slider builds one labelled slider row bound to a settings key (or a
// custom getter/setter for mask sliders).
export function slider(p, opts = {}) {
  const input = h('input', { type: 'range', min: toPos(p, p.min), max: toPos(p, p.max), step: isLog(p) ? 0.0005 : p.step });
  const val = h('input', { class: 'val', value: '' });
  const label = h('label', { title: (p.desc || '') + '\nDouble-click to reset' }, opts.label || p.label);
  const row = h('div', { class: 'row' }, label, input, val);
  const track = opts.track || trackFor(p.key);
  if (track) {
    input.classList.add('grad');
    input.style.setProperty('--track', track);
  }
  let gesture = null;
  // Values at their default are shown dimmed so edits stand out.
  const markZero = (v) => {
    const n = activePhoto();
    const d = opts.def != null ? opts.def : (n ? getPath(defaultsFor(n), p.key) : null) ?? 0;
    row.classList.toggle('zero', Math.abs(v - d) < p.step / 2);
  };
  const send = (v, final) => {
    v = clamp(v, p.min, p.max);
    val.value = fmtVal(p, v);
    setFill(input, p, v);
    markZero(v);
    (opts.onChange || onSlider)(p, v, gesture, final);
  };
  input.addEventListener('pointerdown', () => {
    gesture = gestureId();
    dragging = opts.key || p.key;
  });
  // A click on the knob without moving fires no change event: let go of
  // the slider anyway, or it would stop following undo and Claude's edits.
  const release = () => {
    setTimeout(() => {
      if (dragging === (opts.key || p.key)) {
        dragging = null;
        gesture = null;
      }
    }, 0);
  };
  input.addEventListener('pointerup', release);
  input.addEventListener('pointercancel', release);
  input.addEventListener('input', () => {
    if (!gesture) gesture = gestureId();
    dragging = opts.key || p.key;
    send(fromPos(p, +input.value), false);
  });
  input.addEventListener('change', () => {
    send(fromPos(p, +input.value), true);
    dragging = null;
    gesture = null;
    // Give the keyboard back to the board shortcuts (undo, tools...).
    input.blur();
  });
  label.addEventListener('dblclick', () => {
    gesture = gestureId();
    let d = opts.def != null ? opts.def : getPath(defaultsFor(activePhoto()), p.key) ?? 0;
    // Temperature resets to the camera's white balance.
    if (p.key === 'temp' && activePhoto()?.asShot) d = activePhoto().asShot[0];
    input.value = toPos(p, d);
    send(d, true);
    gesture = null;
  });
  val.addEventListener('keydown', (e) => {
    e.stopPropagation();
    if (e.key === 'Enter') val.blur();
  });
  val.addEventListener('change', () => {
    const v = parseFloat(val.value.replace(',', '.'));
    if (Number.isNaN(v)) return;
    gesture = gestureId();
    input.value = toPos(p, v);
    send(v, true);
    gesture = null;
  });
  const ctl = {
    p, row, input, val,
    set(v, animate = false) {
      if (v == null) v = 0;
      if (dragging === (opts.key || p.key)) return;
      const cur = fromPos(p, +input.value);
      if (animate && Math.abs(cur - v) > p.step / 2) {
        row.classList.add('flash');
        input.classList.add('claude');
        const t0 = performance.now();
        const step = (t) => {
          const k = Math.min(1, (t - t0) / 480);
          const e = 1 - Math.pow(1 - k, 3);
          const x = cur + (v - cur) * e;
          input.value = toPos(p, x);
          val.value = fmtVal(p, x);
          setFill(input, p, x);
          markZero(x);
          if (k < 1) requestAnimationFrame(step);
          else setTimeout(() => { row.classList.remove('flash'); input.classList.remove('claude'); }, 900);
        };
        requestAnimationFrame(step);
        return;
      }
      input.value = toPos(p, v);
      val.value = fmtVal(p, v);
      setFill(input, p, v);
      markZero(v);
    },
  };
  if (!opts.noRegister) controls.set(opts.key || p.key, ctl);
  return ctl;
}

// ---------- edits ----------

let queued = null; // {ids, patch, gesture, label, final}
let inflight = false;

function labelFor(key, v) {
  const p = paramOf(key);
  if (!p) return key;
  const sec = { hsl: 'Color mixer ', curve: 'Curve ', grading: 'Grading ', detail: '', effects: '', lens: 'Lens ', calibration: 'Calibration ', bw: 'B&W ' }[p.section] || '';
  let lbl = p.label;
  if (p.section === 'hsl') {
    const [, band, kind] = key.split('.');
    lbl = `${band} ${kind}`;
  }
  return `${sec}${lbl} ${fmtVal(p, v)}`.trim();
}

function onSlider(p, v, gesture, final) {
  adjust(nested(p.key, v), { gesture, label: labelFor(p.key, v), final });
}

// adjust applies a patch to the active photo (or ids) optimistically and
// streams it to the server, coalescing while a request is in flight.
export function adjust(patch, { ids, gesture, label, final = true } = {}) {
  ids = ids || (S.active ? [S.active] : []);
  if (!ids.length) return;
  for (const id of ids) {
    const n = S.nodes.get(id);
    if (n?.settings) {
      mergeDeep(n.settings, structuredClone(patch));
      if ('temp' in patch || 'tint' in patch) n.settings.wb = patch.wb || 'custom';
    }
  }
  if (queued && queued.ids.join() === ids.join()) {
    mergeDeep(queued.patch, patch);
    Object.assign(queued, { gesture: gesture || queued.gesture, label: label || queued.label, final: final || queued.final });
  } else {
    if (queued) flush(true);
    queued = { ids, patch: structuredClone(patch), gesture, label, final };
  }
  markSections();
  flush();
}

function flush(force = false) {
  if ((inflight && !force) || !queued) return;
  const q = queued;
  queued = null;
  inflight = true;
  post('settings', { ids: q.ids, patch: q.patch, gesture: q.gesture, label: q.label })
    .then((nodes) => {
      for (const n of nodes) {
        // Keep edits typed while this request was in flight.
        if (queued && queued.ids.includes(n.id)) mergeDeep(n.settings, structuredClone(queued.patch));
        upsert(n);
        renderNode(n.id);
        refreshImage(n.id, !q.final);
      }
      if (q.ids.includes(S.active) && !dragging) updatePanel(false);
      emit('edited', q.ids);
    })
    .catch((err) => toast(err.message, true))
    .finally(() => {
      inflight = false;
      if (queued) flush();
      else if (q.final) for (const id of q.ids) setTimeout(() => refreshImage(id), 30);
    });
}

// ---------- histogram ----------

function drawHistogram(hist) {
  const ctx = histCanvas.getContext('2d');
  const W = histCanvas.width, H = histCanvas.height;
  ctx.clearRect(0, 0, W, H);
  if (!hist) return;
  const bytes = Uint8Array.from(atob(hist), (c) => c.charCodeAt(0));
  const chans = [
    [0, 'rgba(255,70,70,0.55)'],
    [128, 'rgba(70,220,90,0.55)'],
    [256, 'rgba(80,130,255,0.6)'],
  ];
  ctx.globalCompositeOperation = 'lighter';
  for (const [off, col] of chans) {
    ctx.beginPath();
    ctx.moveTo(0, H);
    for (let i = 0; i < 128; i++) {
      const x = (i / 127) * W;
      const y = H - (bytes[off + i] / 255) * (H - 6);
      ctx.lineTo(x, y);
    }
    ctx.lineTo(W, H);
    ctx.closePath();
    ctx.fillStyle = col;
    ctx.fill();
  }
  ctx.globalCompositeOperation = 'source-over';
  ctx.strokeStyle = 'rgba(255,255,255,0.07)';
  for (let i = 1; i < 4; i++) {
    ctx.beginPath();
    ctx.moveTo((i / 4) * W, 0);
    ctx.lineTo((i / 4) * W, H);
    ctx.stroke();
  }
}

on('histogram', ({ id, hist, clip }) => {
  if (id !== S.active) return;
  lastHist = hist;
  drawHistogram(hist);
  drawCurve();
  const [lo, hi] = (clip || '0,0').split(',').map(Number);
  document.getElementById('clip-lo').classList.toggle('warn', lo > 0.005);
  document.getElementById('clip-hi').classList.toggle('warn', hi > 0.005);
  document.getElementById('clip-lo').title = `Shadows clipped: ${(lo * 100).toFixed(1)}% of pixels`;
  document.getElementById('clip-hi').title = `Highlights clipped: ${(hi * 100).toFixed(1)}% of pixels`;
  clipInfo.replaceChildren(
    h('span', { class: lo > 0.005 ? 'warn' : '' }, `Shadows ${(lo * 100).toFixed(1)}%`),
    h('span', { class: hi > 0.005 ? 'warn' : '' }, `Highlights ${(hi * 100).toFixed(1)}%`),
  );
});

// ---------- sections ----------

const closedKey = 'lumen.closed';
const closed = new Set(JSON.parse(localStorage.getItem(closedKey) || '["calibration","lens","effects","detail","grading","bw"]'));

const RESET_GROUP = { wb: ['wb'], light: ['light'], presence: ['presence'], curve: ['curve'], hsl: ['hsl', 'bw'], grading: ['grading'], detail: ['detail'], lens: ['lens'], effects: ['effects'], calibration: ['calibration'], crop: ['crop'] };

function section(id, title, body) {
  const reset = RESET_GROUP[id]
    ? h('button', { class: 'icon-btn sm reset', title: `Reset ${title.toLowerCase()}`, onclick: (e) => { e.stopPropagation(); resetGroups(RESET_GROUP[id]); } }, icon('reset', 'ico sm'))
    : null;
  const sh = h('div', { class: 'sh' }, title, h('span', { class: 'dot', title: 'Changed' }), h('span', { class: 'tail' }, reset, icon('chevron', 'ico sm chev')));
  const sb = h('div', { class: 'sb' }, body);
  const el = h('div', { class: 'section' + (closed.has(id) ? ' closed' : ''), 'data-sec': id }, sh, sb);
  sh.addEventListener('click', (e) => {
    if (e.target.closest('.reset')) return;
    el.classList.toggle('closed');
    if (el.classList.contains('closed')) closed.add(id);
    else closed.delete(id);
    localStorage.setItem(closedKey, JSON.stringify([...closed]));
  });
  sections.push({ id, el });
  return el;
}

// Shorter labels where the section heading already gives the context.
const SHORT = {
  'detail.sharpen': 'Amount', 'detail.noise': 'Luminance', 'detail.colorNoise': 'Color', 'detail.colorNoiseDetail': 'Color detail',
  'effects.vignette': 'Amount', 'effects.grain': 'Amount', 'curve.shadowSplit': 'Shadows', 'curve.midSplit': 'Midtones', 'curve.highlightSplit': 'Highlights',
  'calibration.shadowTint': 'Shadow tint',
};

function rows(keys) {
  return keys.map((k) => slider(paramOf(k), SHORT[k] ? { label: SHORT[k] } : {}).row);
}

function markSections() {
  const n = activePhoto();
  if (!n) return;
  const d = defaultsFor(n);
  const secOf = { wb: ['wb'], light: ['light'], presence: ['presence'], curve: ['curve'], hsl: ['hsl'], grading: ['grading'], detail: ['detail'], lens: ['lens'], effects: ['effects'], calibration: ['calibration'], crop: ['crop'] };
  for (const { id, el } of sections) {
    const secs = secOf[id] || [];
    let changed = false;
    for (const p of S.schema.params) {
      if (!secs.includes(p.section) || p.key === 'temp' || p.key === 'tint') continue;
      if (Math.abs((getPath(n.settings, p.key) ?? 0) - (getPath(d, p.key) ?? 0)) > 1e-9) changed = true;
    }
    if (id === 'wb') changed = n.settings.wb !== 'as_shot';
    if (id === 'curve' && ['rgb', 'red', 'green', 'blue'].some((c) => (n.settings.curve[c] || []).length)) changed = true;
    el.classList.toggle('changed', changed);
  }
}

// ---------- build ----------

let profileSel, treatSeg, wbSel, pickBtn, hslSeg, aspectSel;
let hslMode = 'hue';
let curveChannel = 'rgb';
let curveCanvas;
const wheels = [];

export const pickMode = { wb: false };

function buildEdit() {
  tabEdit.replaceChildren();
  controls.clear();
  sections.length = 0;
  wheels.length = 0;

  profileSel = h('select', { class: 'sel' }, ...S.schema.profiles.map((p) => h('option', { value: p }, p[0].toUpperCase() + p.slice(1))));
  profileSel.onchange = () => adjust({ profile: profileSel.value }, { label: 'Profile ' + profileSel.value });
  treatSeg = h('div', { class: 'seg', style: { flex: 1 } },
    h('button', { 'data-v': 'color', onclick: () => adjust({ treatment: 'color' }, { label: 'Color' }) }, 'Color'),
    h('button', { 'data-v': 'bw', onclick: () => adjust({ treatment: 'bw' }, { label: 'Black & white' }) }, 'B&W'));
  const basic = [
    h('div', { class: 'ctl-row' }, h('span', { class: 'lbl' }, 'Profile'), profileSel),
    h('div', { class: 'ctl-row' }, h('span', { class: 'lbl' }, 'Treatment'), treatSeg),
    h('div', { class: 'actions' },
      h('button', { class: 'btn small', title: 'Set light and presence from the histogram (⇧A)', onclick: () => autoTone() }, 'Auto tone'),
      h('button', { class: 'btn small ghost', onclick: () => resetGroups([]) }, 'Reset all'),
      h('div', { class: 'spacer' }),
      h('button', { class: 'link', title: 'Copy settings (⇧⌘C)', onclick: () => emit('copy-settings') }, 'Copy'),
      h('button', { class: 'link', title: 'Paste settings on the selected photos (⇧⌘V)', onclick: () => emit('paste-settings') }, 'Paste')),
  ];
  tabEdit.append(h('div', { class: 'section plain' }, h('div', { class: 'sb' }, basic)));

  wbSel = h('select', { class: 'sel' }, h('option', { value: 'as_shot' }, 'As shot'), h('option', { value: 'auto' }, 'Auto'), h('option', { value: 'custom' }, 'Custom'));
  wbSel.onchange = () => adjust({ wb: wbSel.value }, { label: 'White balance ' + wbSel.selectedOptions[0].textContent });
  pickBtn = h('button', { class: 'iconbtn', title: 'White balance picker: click something that should be neutral gray (W)', onclick: () => togglePick() }, icon('picker'));
  tabEdit.append(section('wb', 'White balance', [h('div', { class: 'ctl-row' }, h('span', { class: 'lbl' }, 'Mode'), wbSel, pickBtn), ...rows(['temp', 'tint'])]));
  tabEdit.append(section('light', 'Light', rows(['exposure', 'contrast', 'highlights', 'shadows', 'whites', 'blacks'])));
  tabEdit.append(section('presence', 'Presence', rows(['texture', 'clarity', 'dehaze', 'vibrance', 'saturation'])));

  // Tone curve.
  curveCanvas = h('canvas', { width: 512, height: 512 });
  const chSeg = h('div', { class: 'seg' }, ...['rgb', 'red', 'green', 'blue'].map((c) =>
    h('button', { 'data-v': c, class: c === curveChannel ? 'active' : '', onclick: (e) => { curveChannel = c; [...chSeg.children].forEach((b) => b.classList.toggle('active', b.dataset.v === c)); drawCurve(); } }, c === 'rgb' ? 'RGB' : c[0].toUpperCase())));
  bindCurve(curveCanvas);
  tabEdit.append(section('curve', 'Tone curve', [
    h('div', { class: 'ctl-row' }, chSeg, h('button', { class: 'btn small ghost', onclick: () => adjust(nested('curve.' + curveChannel, []), { label: 'Reset curve' }) }, 'Reset')),
    h('div', { class: 'curve-box' }, curveCanvas),
    h('div', { class: 'hint' }, 'Click to add a point, drag to move, double-click to remove.'),
    ...rows(['curve.highlights', 'curve.lights', 'curve.darks', 'curve.shadows']),
    h('div', { class: 'sub-head' }, 'Splits'),
    ...rows(['curve.shadowSplit', 'curve.midSplit', 'curve.highlightSplit']),
  ]));

  // Color mixer.
  const hslBody = h('div');
  const bands = S.schema.bands;
  const mkHsl = () => {
    hslBody.replaceChildren(...bands.map((b) => {
      const key = hslMode === 'bw' ? `bw.${b}` : `hsl.${b}.${hslMode}`;
      return slider(paramOf(key), { label: b[0].toUpperCase() + b.slice(1) }).row;
    }));
    updatePanel(false);
  };
  hslSeg = h('div', { class: 'seg' }, ...[['hue', 'Hue'], ['sat', 'Saturation'], ['lum', 'Luminance'], ['bw', 'B&W']].map(([v, t]) =>
    h('button', { 'data-v': v, class: v === hslMode ? 'active' : '', onclick: () => { hslMode = v; [...hslSeg.children].forEach((b) => b.classList.toggle('active', b.dataset.v === v)); mkHsl(); } }, t)));
  tabEdit.append(section('hsl', 'Color mixer', [h('div', { class: 'ctl-row' }, hslSeg), hslBody]));
  setTimeout(mkHsl);

  // Color grading.
  const wheelEls = ['shadows', 'midtones', 'highlights'].map((z) => wheel(z));
  const gw = wheel('global');
  gw.el.classList.add('global');
  tabEdit.append(section('grading', 'Color grading', [
    h('div', { class: 'wheels' }, ...wheelEls.map((w) => w.el)),
    ...['shadows', 'midtones', 'highlights'].map((z) => slider(paramOf(`grading.${z}.lum`), { label: z[0].toUpperCase() + z.slice(1) + ' lum' }).row),
    h('div', { class: 'wheels' }, gw.el),
    slider(paramOf('grading.global.lum'), { label: 'Global lum' }).row,
    ...rows(['grading.blending', 'grading.balance']),
  ]));

  tabEdit.append(section('detail', 'Detail', [
    h('div', { class: 'sub-head' }, 'Sharpening'),
    ...rows(['detail.sharpen', 'detail.sharpenRadius', 'detail.sharpenDetail', 'detail.sharpenMasking']),
    h('div', { class: 'sub-head' }, 'Noise reduction'),
    ...rows(['detail.noise', 'detail.noiseDetail', 'detail.colorNoise', 'detail.colorNoiseDetail']),
    h('div', { class: 'hint' }, 'Zoom in or render at 100% to judge detail settings.'),
  ]));
  tabEdit.append(section('lens', 'Lens corrections', rows(['lens.distortion', 'lens.vignetting', 'lens.vignettingMidpoint'])));
  tabEdit.append(section('effects', 'Effects', [
    h('div', { class: 'sub-head' }, 'Post-crop vignette'),
    ...rows(['effects.vignette', 'effects.vignetteMidpoint', 'effects.vignetteRoundness', 'effects.vignetteFeather', 'effects.vignetteHighlights']),
    h('div', { class: 'sub-head' }, 'Grain'),
    ...rows(['effects.grain', 'effects.grainSize', 'effects.grainRoughness']),
  ]));
  tabEdit.append(section('calibration', 'Calibration', rows(['calibration.shadowTint', 'calibration.redHue', 'calibration.redSat', 'calibration.greenHue', 'calibration.greenSat', 'calibration.blueHue', 'calibration.blueSat'])));

  aspectSel = h('select', { class: 'sel' }, ...S.schema.aspects.map((a) => h('option', { value: a }, a === 'original' ? 'Original' : a === 'free' ? 'Free' : a)));
  aspectSel.onchange = () => adjust({ crop: { aspect: aspectSel.value } }, { label: 'Aspect ' + aspectSel.value });
  const rot = (d) => {
    const n = activePhoto();
    if (!n) return;
    const r = (((n.settings.crop.rotate || 0) + d) % 360 + 360) % 360;
    adjust({ crop: { rotate: r } }, { label: d > 0 ? 'Rotate right' : 'Rotate left' });
  };
  tabEdit.append(section('crop', 'Crop & rotate', [
    h('div', { class: 'ctl-row' }, h('span', { class: 'lbl' }, 'Aspect'), aspectSel),
    slider(paramOf('crop.angle'), { label: 'Straighten' }).row,
    h('div', { class: 'ctl-row' },
      h('button', { class: 'btn small', onclick: () => rot(-90) }, '⟲ 90°'),
      h('button', { class: 'btn small', onclick: () => rot(90) }, '⟳ 90°'),
      h('button', { class: 'btn small', onclick: () => { const n = activePhoto(); if (n) adjust({ crop: { flipH: !n.settings.crop.flipH } }, { label: 'Flip' }); } }, 'Flip'),
      h('div', { class: 'spacer' }),
      h('button', { class: 'btn small ghost', onclick: () => adjust({ crop: { x: 0, y: 0, w: 1, h: 1, angle: 0, aspect: 'free', rotate: 0, flipH: false, flipV: false } }, { label: 'Reset crop' }) }, 'Reset')),
    h('div', { class: 'hint' }, 'Press C to drag the crop on the photo; drag outside the frame to straighten.'),
  ]));
}

// ---------- tone curve editor ----------

function curvePoints(n) {
  const pts = n?.settings?.curve?.[curveChannel];
  return pts && pts.length >= 2 ? pts.map((p) => [...p]) : [[0, 0], [255, 255]];
}

function spline(pts) {
  const xs = pts.map((p) => p[0] / 255), ys = pts.map((p) => p[1] / 255);
  const n = xs.length;
  if (n < 2) return (x) => x;
  const d = [];
  for (let i = 0; i < n - 1; i++) d.push((ys[i + 1] - ys[i]) / Math.max(1e-6, xs[i + 1] - xs[i]));
  const m = new Array(n).fill(0);
  m[0] = d[0];
  m[n - 1] = d[n - 2];
  for (let i = 1; i < n - 1; i++) m[i] = d[i - 1] * d[i] <= 0 ? 0 : (d[i - 1] + d[i]) / 2;
  for (let i = 0; i < n - 1; i++) {
    if (d[i] === 0) { m[i] = m[i + 1] = 0; continue; }
    const a = m[i] / d[i], b = m[i + 1] / d[i], s = a * a + b * b;
    if (s > 9) { const t = 3 / Math.sqrt(s); m[i] = t * a * d[i]; m[i + 1] = t * b * d[i]; }
  }
  return (x) => {
    if (x <= xs[0]) return ys[0];
    if (x >= xs[n - 1]) return ys[n - 1];
    let k = 0;
    while (k < n - 2 && x > xs[k + 1]) k++;
    const hh = xs[k + 1] - xs[k], t = (x - xs[k]) / hh, t2 = t * t, t3 = t2 * t;
    return (2 * t3 - 3 * t2 + 1) * ys[k] + (t3 - 2 * t2 + t) * hh * m[k] + (-2 * t3 + 3 * t2) * ys[k + 1] + (t3 - t2) * hh * m[k + 1];
  };
}

function parametric(c) {
  const s1 = clamp(c.shadowSplit / 100, 0.05, 0.95), s2 = clamp(c.midSplit / 100, s1 + 0.02, 0.97), s3 = clamp(c.highlightSplit / 100, s2 + 0.02, 0.99);
  const regs = [[c.shadows, 0, s2], [c.darks, s1 * 0.4, (s2 + s3) / 2], [c.lights, (s1 + s2) / 2, 1 - (1 - s3) * 0.4], [c.highlights, s2, 1]];
  return (v) => {
    let o = v;
    for (const [amt, lo, hi] of regs) {
      if (!amt || v <= lo || v >= hi) continue;
      const b = Math.sin((Math.PI * (v - lo)) / (hi - lo));
      o += (amt / 100) * 0.16 * b * b;
    }
    return o;
  };
}

export function drawCurve() {
  if (!curveCanvas) return;
  const n = activePhoto();
  const ctx = curveCanvas.getContext('2d');
  const W = curveCanvas.width;
  ctx.clearRect(0, 0, W, W);
  ctx.strokeStyle = 'rgba(255,255,255,0.08)';
  ctx.lineWidth = 1;
  for (let i = 1; i < 4; i++) {
    ctx.beginPath(); ctx.moveTo((i * W) / 4, 0); ctx.lineTo((i * W) / 4, W); ctx.stroke();
    ctx.beginPath(); ctx.moveTo(0, (i * W) / 4); ctx.lineTo(W, (i * W) / 4); ctx.stroke();
  }
  if (lastHist) {
    const bytes = Uint8Array.from(atob(lastHist), (c) => c.charCodeAt(0));
    ctx.fillStyle = 'rgba(255,255,255,0.07)';
    ctx.beginPath();
    ctx.moveTo(0, W);
    for (let i = 0; i < 128; i++) ctx.lineTo((i / 127) * W, W - (bytes[384 + i] / 255) * W * 0.5);
    ctx.lineTo(W, W);
    ctx.fill();
  }
  ctx.setLineDash([6, 6]);
  ctx.strokeStyle = 'rgba(255,255,255,0.18)';
  ctx.beginPath(); ctx.moveTo(0, W); ctx.lineTo(W, 0); ctx.stroke();
  ctx.setLineDash([]);
  if (!n) return;
  const pts = curvePoints(n);
  const f = spline(pts);
  const par = curveChannel === 'rgb' ? parametric(n.settings.curve) : (v) => v;
  const col = { rgb: '#e8e8ea', red: '#ff6b6b', green: '#5fd38a', blue: '#6aa0ff' }[curveChannel];
  ctx.strokeStyle = col;
  ctx.lineWidth = 3;
  ctx.beginPath();
  for (let i = 0; i <= 128; i++) {
    const x = i / 128;
    const y = clamp(f(clamp(par(x), 0, 1)), 0, 1);
    if (i === 0) ctx.moveTo(x * W, W - y * W);
    else ctx.lineTo(x * W, W - y * W);
  }
  ctx.stroke();
  for (const p of pts) {
    ctx.fillStyle = '#111';
    ctx.strokeStyle = col;
    ctx.lineWidth = 3;
    ctx.beginPath();
    ctx.arc((p[0] / 255) * W, W - (p[1] / 255) * W, 9, 0, Math.PI * 2);
    ctx.fill();
    ctx.stroke();
  }
}

function bindCurve(cv) {
  let drag = null;
  const pos = (e) => {
    const r = cv.getBoundingClientRect();
    return [clamp(((e.clientX - r.left) / r.width) * 255, 0, 255), clamp((1 - (e.clientY - r.top) / r.height) * 255, 0, 255), (e.clientY - r.top) / r.height];
  };
  const commit = (pts, gesture, final) => adjust(nested('curve.' + curveChannel, pts.map((p) => [Math.round(p[0]), Math.round(p[1])])), { gesture, label: 'Tone curve', final });
  cv.addEventListener('pointerdown', (e) => {
    const n = activePhoto();
    if (!n) return;
    const [x, y] = pos(e);
    const pts = curvePoints(n);
    let idx = pts.findIndex((p) => Math.hypot(p[0] - x, p[1] - y) < 12);
    if (idx < 0) {
      const f = spline(pts);
      pts.push([x, f(x / 255) * 255]);
      pts.sort((a, b) => a[0] - b[0]);
      idx = pts.findIndex((p) => p[0] === x);
    }
    drag = { pts, idx, gesture: gestureId() };
    cv.setPointerCapture(e.pointerId);
    commit(pts, drag.gesture, false);
  });
  cv.addEventListener('pointermove', (e) => {
    if (!drag) return;
    const [x, y, ry] = pos(e);
    const { pts, idx } = drag;
    const lo = idx > 0 ? pts[idx - 1][0] + 2 : 0;
    const hi = idx < pts.length - 1 ? pts[idx + 1][0] - 2 : 255;
    pts[idx] = [clamp(x, lo, hi), y];
    drag.remove = pts.length > 2 && idx > 0 && idx < pts.length - 1 && (ry < -0.12 || ry > 1.12);
    const shown = drag.remove ? pts.filter((_, i) => i !== idx) : pts;
    commit(shown, drag.gesture, false);
  });
  cv.addEventListener('pointerup', () => {
    if (!drag) return;
    const pts = drag.remove ? drag.pts.filter((_, i) => i !== drag.idx) : drag.pts;
    commit(pts, drag.gesture, true);
    drag = null;
  });
  cv.addEventListener('dblclick', (e) => {
    const n = activePhoto();
    if (!n) return;
    const [x, y] = pos(e);
    const pts = curvePoints(n);
    const idx = pts.findIndex((p) => Math.hypot(p[0] - x, p[1] - y) < 12);
    if (idx > 0 && idx < pts.length - 1) {
      pts.splice(idx, 1);
      commit(pts, gestureId(), true);
    }
  });
}

// ---------- grading wheels ----------

let wheelImage = null;
function wheelBitmap(size) {
  if (wheelImage) return wheelImage;
  const c = document.createElement('canvas');
  c.width = c.height = size;
  const ctx = c.getContext('2d');
  const img = ctx.createImageData(size, size);
  const r = size / 2;
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const dx = x - r + 0.5, dy = r - y - 0.5;
      const d = Math.hypot(dx, dy) / r;
      const i = (y * size + x) * 4;
      if (d > 1) { img.data[i + 3] = 0; continue; }
      const hue = ((Math.atan2(dy, dx) * 180) / Math.PI + 360) % 360;
      const rgb = oklchCss(0.62 + 0.12 * (1 - d), 0.16 * d, hue).match(/\d+/g).map(Number);
      img.data[i] = rgb[0]; img.data[i + 1] = rgb[1]; img.data[i + 2] = rgb[2];
      img.data[i + 3] = d > 0.985 ? Math.round((1 - d) / 0.015 * 255) : 255;
    }
  }
  ctx.putImageData(img, 0, 0);
  wheelImage = c;
  return c;
}

function wheel(zone) {
  const cv = h('canvas', { width: 160, height: 160 });
  const lbl = h('div', { class: 'wl' }, zone[0].toUpperCase() + zone.slice(1));
  const el = h('div', { class: 'wheel' }, cv, lbl);
  const draw = () => {
    const n = activePhoto();
    const ctx = cv.getContext('2d');
    ctx.clearRect(0, 0, 160, 160);
    ctx.drawImage(wheelBitmap(160), 0, 0);
    if (!n) return;
    const w = n.settings.grading[zone];
    const a = (w.hue * Math.PI) / 180, rr = (w.sat / 100) * 78;
    const x = 80 + Math.cos(a) * rr, y = 80 - Math.sin(a) * rr;
    ctx.strokeStyle = '#fff';
    ctx.lineWidth = 2.5;
    ctx.fillStyle = 'rgba(0,0,0,.35)';
    ctx.beginPath(); ctx.arc(x, y, 7, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
    lbl.replaceChildren(h('b', {}, zone[0].toUpperCase() + zone.slice(1)), w.sat ? `${Math.round(w.hue)}°  ${Math.round(w.sat)}%` : 'Neutral');
  };
  let g = null;
  const set = (e, final) => {
    const r = cv.getBoundingClientRect();
    const dx = (e.clientX - r.left) / r.width - 0.5, dy = 0.5 - (e.clientY - r.top) / r.height;
    const hue = Math.round(((Math.atan2(dy, dx) * 180) / Math.PI + 360) % 360);
    const sat = Math.round(clamp(Math.hypot(dx, dy) / 0.49, 0, 1) * 100);
    adjust({ grading: { [zone]: { hue, sat } } }, { gesture: g, label: `Grading ${zone} ${hue}°/${sat}`, final });
    draw();
  };
  cv.addEventListener('pointerdown', (e) => { g = gestureId(); cv.setPointerCapture(e.pointerId); set(e, false); });
  cv.addEventListener('pointermove', (e) => { if (g) set(e, false); });
  cv.addEventListener('pointerup', (e) => { if (g) set(e, true); g = null; });
  cv.addEventListener('dblclick', () => { adjust({ grading: { [zone]: { hue: 0, sat: 0 } } }, { label: `Reset ${zone} wheel` }); draw(); });
  wheels.push(draw);
  return { el, draw };
}

// ---------- panel state ----------

export function updatePanel(animate = false) {
  const n = activePhoto();
  panel.hidden = !n;
  empty.hidden = !!n;
  setInspector(!!n);
  if (!n) return;
  document.getElementById('photo-title').textContent = n.name;
  drawHeader(n);
  drawRating(n);
  for (const [key, c] of controls) {
    const v = getPath(n.settings, key);
    c.set(v ?? getPath(defaultsFor(n), key) ?? 0, animate);
  }
  profileSel.value = n.settings.profile;
  // Standard is the camera's own rendering of this shot: name the style it used.
  const std = [...profileSel.options].find((o) => o.value === 'standard');
  const style = n.info?.meta?.style;
  if (std) std.textContent = n.info?.raw && style ? `Camera ${style}` : 'Standard';
  [...treatSeg.children].forEach((b) => b.classList.toggle('active', b.dataset.v === n.settings.treatment));
  wbSel.value = n.settings.wb;
  aspectSel.value = n.settings.crop.aspect || 'free';
  pickBtn.classList.toggle('on', pickMode.wb);
  drawCurve();
  wheels.forEach((d) => d());
  markSections();
  emit('panel');
}

// setInspector slides the develop panel in or out; when it opens, the
// selected photo is scrolled clear of it.
let inspectorOpen = false;
function setInspector(open) {
  if (open === inspectorOpen) return;
  inspectorOpen = open;
  document.getElementById('app').classList.toggle('inspector-open', open);
  setTimeout(() => emit(open ? 'inspector-open' : 'inspector-close'), 300);
}

export function shutter(t) {
  return t >= 0.3 ? `${+t.toFixed(1)}s` : `1/${Math.round(1 / t)}`;
}

export function aperture(f) {
  return 'f/' + (+f.toFixed(1));
}

function drawHeader(n) {
  const m = n.info?.meta || {};
  const kind = n.info ? [`${n.info.kind.toUpperCase()}, ${n.info.width} × ${n.info.height}`, m.style ? `${m.style} style` : ''].filter(Boolean).join(', ') : '';
  const camera = [m.make && m.model && !m.model.toLowerCase().startsWith(m.make.toLowerCase().split(' ')[0]) ? m.make.split(' ')[0] + ' ' + m.model : m.model, m.lens ? 'with ' + m.lens : ''].filter(Boolean).join(' ');
  const preview = /preview$/.test(n.info?.kind || '');
  const meta = [h('div', {}, camera || 'No camera data'), h('div', {}, kind)];
  if (preview) meta.push(h('div', { class: 'warn' }, 'Shown from the embedded preview: the RAW data could not be read (damaged or truncated file?)'));
  document.getElementById('photo-meta').replaceChildren(...meta);
  const cells = [
    ['ISO', m.iso ? String(m.iso) : '–'],
    ['Shutter', m.exposureTime ? shutter(m.exposureTime) : '–'],
    ['Aperture', m.fNumber ? aperture(m.fNumber) : '–'],
    ['Focal', m.focalLength ? Math.round(m.focalLength) + ' mm' : '–'],
  ];
  document.getElementById('photo-exif').replaceChildren(...cells.map(([k, v]) => h('div', {}, h('dt', {}, k), h('dd', {}, v))));
}

function drawRating(n) {
  const el = document.getElementById('photo-rate');
  const stars = [1, 2, 3, 4, 5].map((i) => h('button', { class: 'star' + (n.rating >= i ? ' on' : ''), title: `${i} star${i > 1 ? 's' : ''} (${i})`, onclick: () => setMeta({ rating: n.rating === i ? 0 : i }) }, icon('star', '')));
  const pick = h('button', { class: 'flagb' + (n.flag === 'pick' ? ' on' : ''), onclick: () => setMeta({ flag: n.flag === 'pick' ? '' : 'pick' }), title: n.flag === 'pick' ? 'Picked (P to unflag with U)' : 'Pick (P)' }, icon('flag', 'ico sm'));
  const rej = h('button', { class: 'flagb' + (n.flag === 'reject' ? ' on' : ''), onclick: () => setMeta({ flag: n.flag === 'reject' ? '' : 'reject' }), title: n.flag === 'reject' ? 'Rejected' : 'Reject (X)' }, icon('reject', 'ico sm'));
  el.replaceChildren(...stars, pick, rej);
}

export function setMeta(meta, ids) {
  ids = ids || [...S.selection];
  if (!ids.length) return;
  post('meta', { ids, meta })
    .then((ns) => { ns.forEach(upsert); ns.forEach((x) => renderNode(x.id)); updatePanel(false); emit('edited', ids); })
    .catch((err) => toast(err.message, true));
}

export function autoTone(ids) {
  ids = ids || [...S.selection].filter((id) => S.nodes.get(id)?.type === 'photo');
  if (!ids.length) return;
  post('auto', { ids }).then((ns) => { ns.forEach(upsert); ns.forEach((x) => refreshImage(x.id)); updatePanel(true); }).catch((err) => toast(err.message, true));
}

export function resetGroups(groups, ids) {
  ids = ids || [...S.selection].filter((id) => S.nodes.get(id)?.type === 'photo');
  if (!ids.length) return;
  post('reset', { ids, groups }).then((ns) => { ns.forEach(upsert); renderAll(); ns.forEach((x) => refreshImage(x.id)); updatePanel(true); }).catch((err) => toast(err.message, true));
}

export function togglePick(force) {
  pickMode.wb = force ?? !pickMode.wb;
  pickBtn?.classList.toggle('on', pickMode.wb);
  document.getElementById('viewport').style.cursor = pickMode.wb ? 'crosshair' : '';
}

// ---------- presets tab ----------

export function buildPresets() {
  const tab = document.getElementById('tab-presets');
  const mk = (p) => h('div', { class: 'preset', role: 'button', tabindex: '0', onclick: () => applyPreset(p.name), title: 'Apply to the selected photos: their edits are reset first (masks included, the crop stays)' }, h('b', {}, p.name), p.desc ? h('span', {}, p.desc) : null);
  const builtin = S.presets.filter((p) => p.builtin).map(mk);
  const mine = S.presets.filter((p) => !p.builtin).map(mk);
  const name = h('input', { placeholder: 'Name for the current look' });
  name.addEventListener('keydown', (e) => { e.stopPropagation(); if (e.key === 'Enter') save.click(); });
  const save = h('button', { class: 'btn small', onclick: () => {
    if (!S.active || !name.value.trim()) return toast('Select a photo and type a name');
    post('presets/save', { name: name.value.trim(), from: S.active }).then(() => { toast('Preset saved'); refreshPresets(); }).catch((err) => toast(err.message, true));
  } }, 'Save');
  tab.replaceChildren(h('div', { class: 'section plain' }, h('div', { class: 'sb' },
    h('div', { class: 'hint', style: { marginTop: 0 } }, 'A preset applies its settings to every selected photo. Crop, masks and white balance stay as they are.'),
    h('div', { class: 'list-head' }, 'Built in'),
    ...builtin,
    h('div', { class: 'list-head' }, 'Yours'),
    ...(mine.length ? mine : [h('div', { class: 'hint' }, 'Save a look you like to reuse it on other photos.')]),
    h('div', { class: 'preset-save' }, name, save))));
}

async function refreshPresets() {
  const st = await fetch('/api/state').then((r) => r.json());
  S.presets = st.presets || [];
  buildPresets();
}

function applyPreset(name) {
  const ids = [...S.selection].filter((id) => S.nodes.get(id)?.type === 'photo');
  if (!ids.length) return toast('Select photos first');
  post('presets/apply', { name, ids }).then((ns) => { ns.forEach(upsert); ns.forEach((x) => refreshImage(x.id)); updatePanel(true); }).catch((err) => toast(err.message, true));
}

// ---------- tabs ----------

export function setTab(t) {
  S.tab = t;
  const tabs = [...document.querySelectorAll('#tabs button')];
  tabs.forEach((b) => b.classList.toggle('active', b.dataset.tab === t));
  document.querySelector('#tabs .tab-ind').style.transform = `translateX(${Math.max(0, tabs.findIndex((b) => b.dataset.tab === t)) * 100}%)`;
  for (const id of ['edit', 'masks', 'presets']) document.getElementById('tab-' + id).hidden = id !== t;
  emit('tab', t);
}

export function initDevelop() {
  for (const p of S.schema.params) S.params.set(p.key, p);
  buildEdit();
  buildPresets();
  for (const b of document.querySelectorAll('#tabs button')) b.onclick = () => setTab(b.dataset.tab);
  on('selection', () => {
    togglePick(false);
    updatePanel(false);
    const n = activePhoto();
    if (n) {
      const hc = histFor(n.id);
      if (hc) emit('histogram', hc);
      refreshImage(n.id);
    }
  });
}
