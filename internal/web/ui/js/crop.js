// Crop & straighten tool: the card shows the whole frame and a rotated
// crop rectangle that can be moved, resized and rotated.
import { S, activePhoto } from './store.js';
import { svg, clamp } from './ui.js';
import { hooks, nodeRect, drawOverlay, localPoint } from './canvas.js';
import { adjust } from './develop.js';

let draft = null;

function dims(n) {
  const r = n.settings.crop.rotate || 0;
  const W = n.info.width, H = n.info.height;
  return r === 90 || r === 270 ? [H, W] : [W, H];
}

function state(n) {
  if (draft && draft.id === n.id) return draft;
  const c = n.settings.crop;
  const [FW, FH] = dims(n);
  return { id: n.id, cw: c.w * FW, ch: c.h * FH, cx: (c.x + c.w / 2) * FW, cy: (c.y + c.h / 2) * FH, angle: c.angle || 0 };
}

function ratio(n) {
  const a = n.settings.crop.aspect;
  const [FW, FH] = dims(n);
  if (!a || a === 'free') return 0;
  if (a === 'original') return FW / FH;
  const [x, y] = a.split(':').map(Number);
  return x && y ? x / y : 0;
}

function rot(d) {
  const a = (d.angle * Math.PI) / 180;
  return [Math.cos(a), Math.sin(a)];
}

function corner(d, lx, ly) {
  const [ca, sa] = rot(d);
  return [d.cx + lx * ca + ly * sa, d.cy - lx * sa + ly * ca];
}

function toLocal(d, fx, fy) {
  const [ca, sa] = rot(d);
  const ex = fx - d.cx, ey = fy - d.cy;
  return [ex * ca - ey * sa, ex * sa + ey * ca];
}

function scr(n, fx, fy) {
  const r = nodeRect(n);
  const [FW, FH] = dims(n);
  return [r.x + (fx / FW) * r.w, r.y + (fy / FH) * r.h];
}

function frm(n, sx, sy) {
  const r = nodeRect(n);
  const [FW, FH] = dims(n);
  return [((sx - r.x) / r.w) * FW, ((sy - r.y) / r.h) * FH];
}

const HANDLES = [[-1, -1], [0, -1], [1, -1], [1, 0], [1, 1], [0, 1], [-1, 1], [-1, 0]];

hooks.overlay.push((o) => {
  const n = activePhoto();
  if (!n || S.tool !== 'crop') return;
  const d = state(n);
  const r = nodeRect(n);
  const pts = [[-1, -1], [1, -1], [1, 1], [-1, 1]].map(([a, b]) => scr(n, ...corner(d, (a * d.cw) / 2, (b * d.ch) / 2)));
  const poly = pts.map((p) => p.join(',')).join(' ');
  o.append(svg('path', { class: 'crop-shade', d: `M${r.x},${r.y}h${r.w}v${r.h}h${-r.w}z M${pts.map((p) => p.join(',')).join(' L')} z` }));
  for (const k of [1 / 3, 2 / 3]) {
    const a = scr(n, ...corner(d, -d.cw / 2 + d.cw * k, -d.ch / 2)), b = scr(n, ...corner(d, -d.cw / 2 + d.cw * k, d.ch / 2));
    const c = scr(n, ...corner(d, -d.cw / 2, -d.ch / 2 + d.ch * k)), e = scr(n, ...corner(d, d.cw / 2, -d.ch / 2 + d.ch * k));
    o.append(svg('line', { class: 'crop-grid', x1: a[0], y1: a[1], x2: b[0], y2: b[1] }));
    o.append(svg('line', { class: 'crop-grid', x1: c[0], y1: c[1], x2: e[0], y2: e[1] }));
  }
  o.append(svg('polygon', { class: 'crop-rect', points: poly }));
  for (const [hx, hy] of HANDLES) {
    const [x, y] = scr(n, ...corner(d, (hx * d.cw) / 2, (hy * d.ch) / 2));
    o.append(svg('rect', { class: 'crop-h', x: x - 4, y: y - 4, width: 8, height: 8 }));
  }
});

hooks.pointerDown.push((e, sx, sy) => {
  const n = activePhoto();
  if (!n || S.tool !== 'crop') return false;
  const r = nodeRect(n);
  if (sx < r.x - 80 || sy < r.y - 80 || sx > r.x + r.w + 80 || sy > r.y + r.h + 80) return false;
  const d0 = { ...state(n) };
  const [fx, fy] = frm(n, sx, sy);
  const [lx, ly] = toLocal(d0, fx, fy);
  let mode = null, hx = 0, hy = 0;
  for (const [ax, ay] of HANDLES) {
    const [px, py] = scr(n, ...corner(d0, (ax * d0.cw) / 2, (ay * d0.ch) / 2));
    if (Math.hypot(px - sx, py - sy) < 10) { mode = 'resize'; hx = ax; hy = ay; }
  }
  if (!mode) mode = Math.abs(lx) <= d0.cw / 2 && Math.abs(ly) <= d0.ch / 2 ? 'move' : 'rotate';
  const [FW, FH] = dims(n);
  const ar = ratio(n);
  const a0 = Math.atan2(sy - scr(n, d0.cx, d0.cy)[1], sx - scr(n, d0.cx, d0.cy)[0]);
  const vp = document.getElementById('viewport');
  vp.setPointerCapture(e.pointerId);
  const move = (ev) => {
    const [px, py] = localPoint(ev);
    const [qx, qy] = frm(n, px, py);
    const d = { ...d0 };
    if (mode === 'move') {
      d.cx = clamp(d0.cx + qx - fx, d0.cw / 2, FW - d0.cw / 2);
      d.cy = clamp(d0.cy + qy - fy, d0.ch / 2, FH - d0.ch / 2);
    } else if (mode === 'rotate') {
      const c = scr(n, d0.cx, d0.cy);
      const a1 = Math.atan2(py - c[1], px - c[0]);
      d.angle = clamp(d0.angle - ((a1 - a0) * 180) / Math.PI, -45, 45);
    } else {
      const [mx, my] = toLocal(d0, qx, qy);
      let left = -d0.cw / 2, right = d0.cw / 2, top = -d0.ch / 2, bottom = d0.ch / 2;
      const minS = Math.min(FW, FH) * 0.03;
      if (hx < 0) left = Math.min(mx, right - minS);
      if (hx > 0) right = Math.max(mx, left + minS);
      if (hy < 0) top = Math.min(my, bottom - minS);
      if (hy > 0) bottom = Math.max(my, top + minS);
      if (ar) {
        let w = right - left, hh = bottom - top;
        if (hx && hy) {
          if (w / hh > ar) w = hh * ar; else hh = w / ar;
        } else if (hx) hh = w / ar;
        else w = hh * ar;
        if (hx < 0) left = right - w; else if (hx > 0) right = left + w; else { left = -w / 2; right = w / 2; }
        if (hy < 0) top = bottom - hh; else if (hy > 0) bottom = top + hh; else { top = -hh / 2; bottom = hh / 2; }
      }
      const cxl = (left + right) / 2, cyl = (top + bottom) / 2;
      const [ncx, ncy] = corner(d0, cxl, cyl);
      d.cx = ncx; d.cy = ncy; d.cw = right - left; d.ch = bottom - top;
    }
    draft = d;
    drawOverlay();
  };
  const up = () => {
    vp.removeEventListener('pointermove', move);
    vp.removeEventListener('pointerup', up);
    if (!draft) return;
    const d = draft;
    const crop = {
      x: +((d.cx - d.cw / 2) / FW).toFixed(5), y: +((d.cy - d.ch / 2) / FH).toFixed(5),
      w: +(d.cw / FW).toFixed(5), h: +(d.ch / FH).toFixed(5), angle: +d.angle.toFixed(2),
    };
    draft = null;
    adjust({ crop }, { label: mode === 'rotate' ? `Straighten ${crop.angle}°` : 'Crop' });
    drawOverlay();
  };
  vp.addEventListener('pointermove', move);
  vp.addEventListener('pointerup', up);
  return true;
});
