'use strict';
/* 公共：选择器、格式化、图标、请求封装、页签、弹窗、公告 */
const $ = s => document.querySelector(s);
const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const fmt = ts => ts ? new Date(ts * 1000).toLocaleString('zh-CN', {hour12: false}) : '-';
const fmtS = ts => ts ? new Date(ts * 1000).toLocaleString('zh-CN', {hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit'}) : '-';
const fmtT = ts => ts ? new Date(ts * 1000).toLocaleString('zh-CN', {hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit'}) : '-';
const dur = s => { s = Math.max(0, Math.round(s)); return s >= 86400 ? Math.floor(s / 86400) + ' 天 ' + Math.floor(s % 86400 / 3600) + ' 时'
  : s >= 3600 ? Math.floor(s / 3600) + ' 时 ' + Math.floor(s % 3600 / 60) + ' 分' : Math.floor(s / 60) + ' 分'; };
const dur2s = s => { s = Math.max(0, Math.ceil(s)); return s >= 3600 ? Math.floor(s / 3600) + ' 时 ' + Math.floor(s % 3600 / 60) + ' 分'
  : s >= 60 ? Math.floor(s / 60) + ' 分 ' + (s % 60) + ' 秒' : s + ' 秒'; };
const ago = t => { const s = Date.now() / 1000 - t; return !t ? '-' : s < 60 ? '刚刚' : s < 3600 ? Math.floor(s / 60) + ' 分钟前' : s < 86400 ? Math.floor(s / 3600) + ' 小时前' : Math.floor(s / 86400) + ' 天前'; };
const num = v => typeof v === 'number' && isFinite(v) ? v : null;
const nf = v => v == null ? '-' : Number(v).toLocaleString('en-US');
const fmtTok = v => { v = Number(v) || 0; const t = (x, d) => String(Math.round(x * 10 ** d) / 10 ** d);
  return v >= 1e8 ? t(v / 1e8, 2) + ' 亿' : v >= 1e4 ? t(v / 1e4, 1) + ' 万' : String(Math.round(v)); };
const pctS = v => v == null ? '-' : (Math.round(v * 1000) / 10) + '%';
const yuan = v => v == null ? '-' : '¥' + (v >= 100 ? nf(Math.round(v)) : v >= 1 ? (Math.round(v * 100) / 100).toFixed(2) : (Math.round(v * 10000) / 10000));
const I = {
  users: '<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>',
  chart: '<line x1="18" y1="20" x2="18" y2="10"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="6" y1="20" x2="6" y2="14"/>',
  cube: '<path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/>',
  key: '<path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.78 7.78 5.5 5.5 0 0 1 7.78-7.78zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>',
  globe: '<circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>',
  gear: '<line x1="4" y1="21" x2="4" y2="14"/><line x1="4" y1="10" x2="4" y2="3"/><line x1="12" y1="21" x2="12" y2="12"/><line x1="12" y1="8" x2="12" y2="3"/><line x1="20" y1="21" x2="20" y2="16"/><line x1="20" y1="12" x2="20" y2="3"/><line x1="1" y1="14" x2="7" y2="14"/><line x1="9" y1="8" x2="15" y2="8"/><line x1="17" y1="16" x2="23" y2="16"/>',
  out: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/>',
  plus: '<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>', search: '<circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/>',
  refresh: '<polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/>',
  more: '<circle cx="12" cy="12" r="1.3"/><circle cx="19" cy="12" r="1.3"/><circle cx="5" cy="12" r="1.3"/>', x: '<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>',
  copy: '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  gift: '<polyline points="20 12 20 22 4 22 4 12"/><rect x="2" y="7" width="20" height="5"/><line x1="12" y1="22" x2="12" y2="7"/><path d="M12 7H7.5a2.5 2.5 0 0 1 0-5C11 2 12 7 12 7z"/><path d="M12 7h4.5a2.5 2.5 0 0 0 0-5C13 2 12 7 12 7z"/>',
  cal: '<rect x="3" y="4" width="18" height="18" rx="2"/><line x1="16" y1="2" x2="16" y2="6"/><line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/>',
  clock: '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>', info: '<circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/>',
  check: '<polyline points="20 6 9 17 4 12"/>', play: '<polygon points="6 4 19 12 6 20 6 4"/>', right: '<polyline points="9 18 15 12 9 6"/>',
  lock: '<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>', shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>',
  chat: '<path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"/>',
  phone: '<rect x="5" y="2" width="14" height="20" rx="2"/><line x1="12" y1="18" x2="12.01" y2="18"/>', pc: '<rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/>',
  paste: '<path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><rect x="8" y="2" width="8" height="4" rx="1"/>',
  trash: '<polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>', edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/>',
};
const ic = name => '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true">' + I[name] + '</svg>';
const chip = (cls, html, title) => '<span class="chip ' + cls + '"' + (title ? ' title="' + esc(title) + '"' : '') + '>' + html + '</span>';
const tip = t => '<span class="tip" tabindex="0" data-tip="' + esc(t) + '">' + ic('info') + '</span>';
const sw = (on, label, attrs) => '<button type="button" class="sw" role="switch" aria-checked="' + !!on + '" aria-label="' + esc(label) + '" ' + attrs + '><i></i></button>';
const copyBtn = v => v ? '<button type="button" class="ibtn xs" data-copy="' + esc(v) + '" title="复制" aria-label="复制">' + ic('copy') + '</button>' : '';
const seg = (attr, items, cur) => '<div class="seg sm" role="group">' + items.map(x => '<button type="button" ' + attr + '="' + x[0] + '" aria-pressed="' + (String(x[0]) === String(cur)) + '">' + x[1] + '</button>').join('') + '</div>';
const spinner = text => '<div class="muted small"><span class="spin"></span>' + esc(text || '正在向上游查询…') + '</div>';
function decorate(root){
  (root || document).querySelectorAll('[data-ic]').forEach(el => { if(!el.querySelector(':scope > .ic')) el.insertAdjacentHTML('afterbegin', ic(el.dataset.ic)); });
}
let toastT = 0;
function toast(msg, bad){
  const t = $('#toast'); t.textContent = msg; t.classList.toggle('bad', !!bad); t.classList.add('show');
  clearTimeout(toastT); toastT = setTimeout(() => t.classList.remove('show'), bad ? 5000 : 3200);
}
async function api(path, body, keepalive = false){
  const r = await fetch(path, body === undefined ? {cache: 'no-store'} :
    {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body), keepalive});
  let d = null; try { d = await r.json(); } catch(e) {}
  if(r.status === 401) location.replace('/login');
  if(!r.ok) throw new Error((d && d.error && (d.error.message || d.error)) || ('HTTP ' + r.status));
  return d;
}
/* 弹窗：打开时记住焦点，关闭时还回去；个别弹窗关闭前要做清理，登记在 MODAL_CLOSE */
const MODAL_CLOSE = {};
let MODAL_FOCUS = null;
function openModal(id, focus){
  MODAL_FOCUS = document.activeElement; $(id).hidden = false;
  setTimeout(() => { const el = (focus && $(focus)) || $(id + ' input:not([type=hidden]),' + id + ' button'); if(el) el.focus(); }, 30);
}
function closeModal(id){ const m = $(id); if(!m || m.hidden) return; m.hidden = true; if(MODAL_FOCUS && MODAL_FOCUS.isConnected) MODAL_FOCUS.focus(); }
const requestClose = (id, how) => (MODAL_CLOSE[id] || closeModal)(id, how);
document.addEventListener('click', e => {
  const back = e.target.closest('.modal-back'); if(!back) return;
  if(e.target === back) requestClose('#' + back.id, 'backdrop');
  else if(e.target.closest('[data-close-modal]')) requestClose('#' + back.id, 'button');
});
document.addEventListener('keydown', e => {
  if(e.key !== 'Escape') return;
  const open = [...document.querySelectorAll('.modal-back:not([hidden])')].pop();
  if(open) return requestClose('#' + open.id, 'escape');
  if(!$('#bulkList').hidden) return closeMenu();
  if(SEL) closeDrawer();
});
/* 页签 */
function showTab(t){
  if(!document.querySelector('[data-page="' + t + '"]')) t = 'accounts';
  document.querySelectorAll('.nav [data-tab]').forEach(b => b.setAttribute('aria-selected', String(b.dataset.tab === t)));
  document.querySelectorAll('[data-page]').forEach(p => p.hidden = p.dataset.page !== t);
  try { localStorage.setItem('codar-tab', t); } catch(e) {}
  if(t === 'models') loadCatalog().catch(err => toast(err.message, true));
  if(t === 'settings') loadSettings().catch(err => toast(err.message, true));
  if(t === 'keys') loadKeys().catch(err => toast(err.message, true));
  if(t === 'proxies') loadProxies().catch(err => toast(err.message, true));
  if(t === 'stats'){ loadStats().catch(err => toast(err.message, true)); loadUpstream().catch(err => { $('#upBody').innerHTML = '<div class="notice bad">' + esc(err.message) + '</div>'; }); }
}
document.addEventListener('click', e => { const b = e.target.closest('[data-tab]'); if(b) showTab(b.dataset.tab); });
/* 官方公告：有新公告才出现，关掉后同一版本不再显示 */
let ANN_KEY = '';
async function loadAnnouncements(){
  const d = await api('/announcements'), items = (d.items || []).slice(0, 3);
  const title = x => x.title || x.name || x.subject || '', body = x => { const v = x.content || x.body || x.text || x.summary || ''; return typeof v === 'object' ? (v.zh || JSON.stringify(v)) : String(v); };
  ANN_KEY = (d.version || '') + ':' + items.map(title).join('|');
  let seen = ''; try { seen = localStorage.getItem('codar-ann') || ''; } catch(e) {}
  $('#annBar').innerHTML = !items.length || seen === ANN_KEY ? '' : '<div class="ann" role="note">' + ic('info') + '<div><b>Codar 公告</b>' + items.map(x => '<p>' + (title(x) ? '<b>' + esc(title(x)) + '</b> ' : '')
    + esc(body(x).slice(0, 300)) + '</p>').join('') + '</div><button type="button" class="ibtn sm" id="annClose" title="不再显示这条公告" aria-label="关闭公告">' + ic('x') + '</button></div>';
}
document.addEventListener('click', e => { if(!e.target.closest('#annClose')) return; try { localStorage.setItem('codar-ann', ANN_KEY); } catch(_) {} $('#annBar').innerHTML = ''; });
