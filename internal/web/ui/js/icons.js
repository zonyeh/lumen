// A small, consistent icon set (16px grid, 1.4 stroke).

const P = {
  brush: '<path d="M10.5 2.5 13.5 5.5 7.5 11.5 4.5 8.5Z"/><path d="M4.5 8.5c-1.6.2-2.5 1.4-2.5 3.3V13.5h1.7c1.9 0 3.1-.9 3.3-2.5"/>',
  linear: '<path d="M2.5 3.5h11"/><path d="M2.5 8h11" opacity=".6"/><path d="M2.5 12.5h11" opacity=".3"/>',
  radial: '<circle cx="8" cy="8" r="5.5"/><circle cx="8" cy="8" r="2.5" stroke-dasharray="2 1.6"/>',
  luminance: '<circle cx="8" cy="8" r="5.5"/><path d="M8 2.5v11" /><path d="M8 2.5a5.5 5.5 0 0 1 0 11Z" class="fill"/>',
  color: '<path d="M8 2.2c2.3 2.9 3.8 5.1 3.8 7a3.8 3.8 0 0 1-7.6 0c0-1.9 1.5-4.1 3.8-7Z"/>',
  all: '<rect x="2.5" y="3.5" width="11" height="9" rx="1.5"/>',
  eye: '<path d="M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8Z"/><circle cx="8" cy="8" r="2"/>',
  eyeOff: '<path d="M2 2l12 12"/><path d="M6.4 3.7A6.7 6.7 0 0 1 8 3.5C12 3.5 14.5 8 14.5 8a12 12 0 0 1-1.9 2.5M10.2 12A6 6 0 0 1 8 12.5C4 12.5 1.5 8 1.5 8a12 12 0 0 1 2.4-3"/>',
  trash: '<path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5"/>',
  x: '<path d="m4.5 4.5 7 7m0-7-7 7"/>',
  remove: '<rect x="2.5" y="3.5" width="11" height="9" rx="1.5"/><path d="M5.5 8h5"/>',
  invert: '<circle cx="8" cy="8" r="5.5"/><path d="M8 2.5a5.5 5.5 0 0 0 0 11Z" class="fill"/>',
  reset: '<path d="M3 8a5 5 0 1 0 1.5-3.6"/><path d="M3 2.5v2.5h2.5"/>',
  chevron: '<path d="M4.5 6.5 8 10l3.5-3.5"/>',
  picker: '<path d="M10.2 2.8a1.8 1.8 0 0 1 2.6 2.6l-1.4 1.4-2.6-2.6Z"/><path d="M8.8 4.2 3.5 9.5V12.5h3l5.3-5.3"/>',
  star: '<path d="M8 1.8l1.8 3.9 4.2.5-3.1 2.9.8 4.2L8 11.2l-3.7 2.1.8-4.2L2 6.2l4.2-.5Z" class="fill"/>',
  flag: '<path d="M3.5 14V2.5M3.5 3h8l-1.6 3 1.6 3h-8"/>',
  reject: '<circle cx="8" cy="8" r="5.5"/><path d="m4.2 11.8 7.6-7.6"/>',
  folder: '<path d="M2 4.5c0-.6.4-1 1-1h3.2l1.3 1.5H13c.6 0 1 .4 1 1v6.5c0 .6-.4 1-1 1H3c-.6 0-1-.4-1-1Z"/>',
  image: '<rect x="2.5" y="3" width="11" height="10" rx="1.5"/><path d="m3 11.5 3.2-3.2 2.3 2.3 1.6-1.6 2.9 2.9"/><circle cx="10.3" cy="5.8" r="1"/>',
  home: '<path d="M2.5 7.2 8 2.8l5.5 4.4V13a.5.5 0 0 1-.5.5H9.5V10h-3v3.5H3a.5.5 0 0 1-.5-.5Z"/>',
  drive: '<rect x="2" y="5" width="12" height="6.5" rx="1.5"/><path d="M11 8.3h.01"/>',
  desktop: '<rect x="2" y="3" width="12" height="8" rx="1"/><path d="M6 13.5h4M8 11v2.5"/>',
  note: '<path d="M3 3.5h10M3 8h10M3 12.5h6"/>',
  plus: '<path d="M8 3.5v9M3.5 8h9"/>',
  sparkle: '<path d="M8 2v3M8 11v3M2 8h3M11 8h3"/>',
};

export function icon(name, cls = 'ico') {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  s.setAttribute('viewBox', '0 0 16 16');
  s.setAttribute('class', cls);
  s.setAttribute('aria-hidden', 'true');
  s.innerHTML = P[name] || '';
  return s;
}
