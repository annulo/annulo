const escapeHTML = text => String(text).replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]))

function renderSplash({ title, detail, failed, dark, labels }) {
  const content = failed
    ? `<pre>${escapeHTML(detail)}</pre>`
    : `<p role="status" class="loading"><span class="spinner" aria-hidden="true"></span>${escapeHTML(detail)}</p>`
  const actions = failed ? `<button onclick="window.shuttleDesktop.retry()">${escapeHTML(labels.retry)}</button><button onclick="window.shuttleDesktop.copyDiagnostics()">${escapeHTML(labels.copy)}</button><button onclick="window.shuttleDesktop.openLogs()">${escapeHTML(labels.logs)}</button>` : ''
  return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Annulo</title><style>body{margin:0;padding:48px;font:16px/1.6 system-ui;background:${dark ? '#111' : '#fff'};color:${dark ? '#eee' : '#222'}}main{max-width:850px;margin:10vh auto}h1{font-size:26px}.loading{display:flex;align-items:center;gap:12px;opacity:.7}.spinner{width:16px;height:16px;border:2px solid currentColor;border-right-color:transparent;border-radius:50%;animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(prefers-reduced-motion:reduce){.spinner{animation:none}}pre{padding:20px;border:1px solid #777;border-radius:8px;white-space:pre-wrap;overflow-wrap:anywhere;max-height:45vh;overflow:auto;font-size:13px}button{font:inherit;padding:8px 18px;margin:8px 10px 0 0;border-radius:8px;border:1px solid #888;cursor:pointer}</style></head><body><main><h1>${escapeHTML(title)}</h1>${content}${actions}</main></body></html>`
}

module.exports = { renderSplash }
