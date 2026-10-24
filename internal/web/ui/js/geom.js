// Mirrors internal/develop/geometry.go: maps between a photo card (the
// cropped, straightened frame) and normalized image coordinates (used by
// masks).

function frame(n) {
  const W = n.info?.width || 3, H = n.info?.height || 2;
  const c = n.settings?.crop || { x: 0, y: 0, w: 1, h: 1, angle: 0 };
  const rot = c.rotate || 0;
  const FW = rot === 90 || rot === 270 ? H : W;
  const FH = rot === 90 || rot === 270 ? W : H;
  return { W, H, FW, FH, c, rot };
}

export function frameAspect(n) {
  const f = frame(n);
  return f.FW / f.FH;
}

function params(n, nocrop) {
  const f = frame(n);
  const c = nocrop ? { x: 0, y: 0, w: 1, h: 1, angle: 0 } : f.c;
  const a = ((c.angle || 0) * Math.PI) / 180;
  return { ...f, cw: c.w * f.FW, ch: c.h * f.FH, cx: (c.x + c.w / 2) * f.FW, cy: (c.y + c.h / 2) * f.FH, ca: Math.cos(a), sa: Math.sin(a), flipH: f.c.flipH, flipV: f.c.flipV };
}

// boxToImage maps (bx,by) in [0,1] of the card to image fractions (u,v).
export function boxToImage(n, bx, by, nocrop = false) {
  const g = params(n, nocrop);
  const dx = bx * g.cw - g.cw / 2, dy = by * g.ch - g.ch / 2;
  let fx = g.cx + dx * g.ca + dy * g.sa;
  let fy = g.cy - dx * g.sa + dy * g.ca;
  if (g.flipH) fx = g.FW - fx;
  if (g.flipV) fy = g.FH - fy;
  let x, y;
  switch (g.rot) {
    case 90: x = fy; y = g.H - fx; break;
    case 180: x = g.W - fx; y = g.H - fy; break;
    case 270: x = g.W - fy; y = fx; break;
    default: x = fx; y = fy;
  }
  return [x / g.W, y / g.H];
}

// imageToBox is the inverse of boxToImage.
export function imageToBox(n, u, v, nocrop = false) {
  const g = params(n, nocrop);
  const x = u * g.W, y = v * g.H;
  let fx, fy;
  switch (g.rot) {
    case 90: fx = g.H - y; fy = x; break;
    case 180: fx = g.W - x; fy = g.H - y; break;
    case 270: fx = y; fy = g.W - x; break;
    default: fx = x; fy = y;
  }
  if (g.flipH) fx = g.FW - fx;
  if (g.flipV) fy = g.FH - fy;
  const ex = fx - g.cx, ey = fy - g.cy;
  const dx = ex * g.ca - ey * g.sa;
  const dy = ex * g.sa + ey * g.ca;
  return [(dx + g.cw / 2) / g.cw, (dy + g.ch / 2) / g.ch];
}

// imageScale returns card pixels per image pixel along x and y.
export function imageAspect(n) {
  return (n.info?.width || 3) / (n.info?.height || 2);
}
