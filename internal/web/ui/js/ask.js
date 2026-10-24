// A small dialog that asks before acting: a question, an optional name
// field, and a button that runs the action (its error shows in place).
const dlg = document.getElementById('ask-dialog');
const askInput = document.getElementById('ask-input');
const askErr = document.getElementById('ask-err');
const askOk = document.getElementById('ask-ok');
let askRun = null;

// ask shows a small dialog; run receives the typed value and closes the
// dialog when it succeeds, or shows its error.
export function ask({ title, text = '', value = null, ok, danger = false, run }) {
  document.getElementById('ask-title').textContent = title;
  const t = document.getElementById('ask-text');
  t.textContent = text;
  t.hidden = !text;
  document.getElementById('ask-field').hidden = value == null;
  askInput.value = value ?? '';
  askOk.textContent = ok;
  askOk.classList.toggle('danger', danger);
  askOk.classList.toggle('primary', !danger);
  askErr.hidden = true;
  askRun = run;
  dlg.showModal();
  if (value != null) askInput.select();
  else askOk.focus();
}

let asking = false;
askOk.onclick = async () => {
  // A second Enter or click while the action runs does nothing.
  if (!askRun || asking) return;
  asking = true;
  try {
    await askRun(askInput.value.trim());
    dlg.close();
  } catch (err) {
    askErr.textContent = err.message;
    askErr.hidden = false;
  } finally {
    asking = false;
  }
};
askInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') {
    e.preventDefault();
    askOk.click();
  }
});
