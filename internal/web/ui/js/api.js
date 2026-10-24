// HTTP and live-event plumbing.

export async function post(action, body = {}) {
  const r = await fetch(`/api/${action}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Lumen': '1' },
    body: JSON.stringify(body),
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

export async function getJSON(path) {
  const r = await fetch(path, { headers: { 'X-Lumen': '1' } });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

export async function upload(file) {
  const r = await fetch(`/api/upload?name=${encodeURIComponent(file.name)}`, {
    method: 'POST', headers: { 'X-Lumen': '1' }, body: file,
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data.path;
}

// connectEvents keeps an SSE stream open; onReconnect fires after every
// (re)connection so the caller can resync.
export function connectEvents(onEvent, onReconnect) {
  let es;
  const open = () => {
    es = new EventSource('/api/events');
    es.addEventListener('hello', () => onReconnect());
    es.onmessage = (m) => {
      try { onEvent(JSON.parse(m.data)); } catch (e) { console.error(e); }
    };
    es.onerror = () => {
      es.close();
      setTimeout(open, 1500);
    };
  };
  open();
}
