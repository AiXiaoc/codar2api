'use strict';
/* 账号页：汇总卡、筛选、列表、批量操作、代理绑定 */
let ROWS = [], SET_CUR = {}, SEL = null, DTAB = 'overview', FILTER = 'all', KW = '';
const SELECTED_ACCOUNTS = new Set();
let BATCH_BUSY = false;
const PX = {proxies: [], loaded: false, error: '', edit: null};
const NOW = () => Date.now() / 1000;
const proxyName = a => a.proxy ? a.proxy.name || (a.proxy.mode === 'direct' ? '直连' : '指定代理') : '直连';
function proxyOptions(value){
  const id = Number(value || 0);
  return '<option value="0"' + (value !== 'round_robin' && !id ? ' selected' : '') + '>直连</option>'
    + '<option value="round_robin"' + (value === 'round_robin' ? ' selected' : '') + '>轮询代理（每次请求换代理）</option>'
    + PX.proxies.map(p => '<option value="' + p.id + '"' + (id === p.id ? ' selected' : '') + (!p.enabled ? ' disabled' : '') + '>'
      + esc(p.name) + ' · #' + p.id + (!p.enabled ? '（已停用）' : '') + '</option>').join('')
    + (Number.isFinite(id) && id && !PX.proxies.some(p => p.id === id) ? '<option value="' + id + '" selected disabled>已删除的代理 #' + id + '</option>' : '');
}
const proxyValue = a => (a.proxy || {}).mode === 'round_robin' ? 'round_robin' : ((a.proxy || {}).ids || [])[0] || 0;
const proxySelection = value => value === 'round_robin' ? {proxy_mode: 'round_robin'} : {proxy_id: Number(value)};
async function loadProxyPool(){
  try { const d = await api('/proxy-pool'); PX.proxies = d.proxies; PX.loaded = true; PX.error = d.error || ''; }
  catch(err){ PX.error = err.message; throw err; }
}
/* retry_after = 查询时距下次可签的秒数，按上次签到滚动计算，不是零点重置 */
const nextSign = s => s.signed && typeof s.retry_after === 'number' && s.at ? s.at + s.retry_after : 0;
const cooling = a => a.cooldown_until > NOW();
const bar = p => { p = Math.max(0, Math.min(100, p)); return '<div class="bar' + (p <= 10 ? ' bad' : p <= 30 ? ' warn' : '') + '" role="progressbar" aria-label="Agent 余额" aria-valuemin="0" aria-valuemax="100" aria-valuenow="'
  + Math.round(p) + '">' + (p > 0 ? '<i style="width:' + Math.max(3, p) + '%"></i>' : '') + '</div>'; };
/* 额度：Agent 余额的分母取最近一次签到送的额度（余额更高就按余额）；免费模型显示剩余时长；通用余额没有上限，只显示数字 */
function quotaCell(a, big){
  const b = a.balance || {}, s = a.sign || {}, f = a.free || {}, ag = num(b.agent_token), general = num(b.token), extra = [];
  if(f.free_until > NOW()) extra.push('<span class="free-t">' + ic('gift') + '免费 ' + dur(f.free_until - NOW()) + '</span>');
  if(general != null) extra.push('<span>通用 ' + nf(general) + '</span>');
  const base = Math.max(ag || 0, num(s.granted) || 0);
  const headLine = ag == null ? '<div class="q-l"><span>' + (b.at ? 'Agent' : '余额还没查询') + '</span><b>-</b></div>'
    : '<div class="q-l"><span>Agent</span><b>' + nf(ag) + '</b>' + (base > ag ? '<small>/ ' + nf(base) + '</small>' : '') + '</div>' + bar(base > 0 ? ag / base * 100 : 0);
  return '<div class="q' + (big ? ' big' : '') + '">' + headLine + (extra.length ? '<div class="q-t">' + extra.join('') + '</div>' : '') + '</div>';
}
/* 出口 IP 封控：上游拦截的是 IP，不是账号；设置里关掉「IP 封控时停止调度该 IP」后不再按封控处理 */
const exitBlocked = a => !!(a.proxy && a.proxy.blocked) && SET_CUR.ip_block !== false;
const exitID = a => a.proxy && a.proxy.mode === 'round_robin' ? -1 : a.proxy && a.proxy.mode === 'pick' ? Number((a.proxy.ids || [])[0]) || 0 : 0;
const exitName = a => exitID(a) ? proxyName(a) : '直连';
/* 账号状态只有「正常」「账号受限」两种；冷却、余额、令牌、出口 IP 等调度条件写在下面一行 */
function stateCell(a){
  const s = a.sign || {}, expired = a.token_exp && a.token_exp <= NOW(), notes = [], nt = html => notes.push('<span class="nt">' + html + '</span>');
  const main = a.restricted ? chip('bad', '<i class="pt"></i>账号受限', a.restricted_reason || '上游返回 account_restricted')
    : chip('ok', '<i class="pt"></i>正常', a.last_error ? '最近错误：' + a.last_error : '');
  if(a.restricted) nt('<button type="button" class="link" data-act="unrestrict">解除受限</button>');
  if(exitBlocked(a)) nt('<span class="bad-t" title="' + esc(a.proxy.blocked_reason || '') + '">IP 封控 · ' + esc(exitName(a)) + '</span><button type="button" class="link" data-act="' + (a.proxy.mode === 'round_robin' ? 'proxy-pool' : 'unblock') + '">' + (a.proxy.mode === 'round_robin' ? '管理代理池' : '解除') + '</button>');
  if(cooling(a)) nt('<span class="warn-t">' + (a.cooldown_kind === 'balance' ? '余额冷却 · 仅免费模型' : esc(a.cooldown_reason || '冷却中')) + '</span><span class="cd" data-cd="' + a.cooldown_until + '">还有 '
    + dur2s(a.cooldown_until - NOW()) + '</span><button type="button" class="link" data-act="reset">立即恢复</button>');
  else if(a.paid_block) nt('<span class="muted" title="收费模型暂停派发，免费模型照常；签到、兑换或充值后下次刷新即恢复">仅免费模型 · ' + esc(a.paid_block) + '</span>');
  const token = a.token_invalid ? '令牌失效' : expired ? '令牌过期' : a.reason === '没有令牌' ? '没有令牌' : '';
  if(token) nt('<span class="bad-t">' + token + '</span><button type="button" class="link" data-act="relogin">重新导入</button>');
  if(a.enabled && a.ready && s.at && !s.signed) nt('<span class="warn-t">今天还没签到</span>');
  return main + (notes.length ? '<div class="sub2">' + notes.join('') + '</div>' : '');
}
function expCell(a){
  if(!a.token_exp) return '<span class="muted">-</span>';
  if(a.token_exp <= NOW()) return '<b class="bad-t">已过期</b>';
  return '<b' + (a.token_exp - NOW() < 86400 ? ' class="warn-t"' : '') + '>' + dur(a.token_exp - NOW()) + '</b><div class="muted">' + fmtS(a.token_exp) + '</div>';
}
const accountSwitch = a => sw(a.enabled, '启用账号 ' + (a.username || a.uid), 'data-act="toggle" title="' + (a.enabled ? '已启用：点击关闭后停止自动调度和后台任务，仍可手动调用和测试' : '已关闭：仍可手动调用和测试，点击启用自动调度') + '"');
const concurrencyText = a => (a.current_concurrency || 0) + ' / ' + (a.max_concurrency || '∞');
const concurrencyCell = a => '<span class="mono' + (a.max_concurrency > 0 && a.current_concurrency >= a.max_concurrency ? ' warn-t' : '') + '" data-concurrency-uid="' + esc(a.uid) + '" title="当前并发 / 最大并发，∞ 表示无上限">' + concurrencyText(a) + '</span>';
function row(a){
  const px = a.proxy && a.proxy.mode !== 'direct' ? '<div class="px' + (a.proxy.available === false ? ' bad-t' : '') + '">' + ic('globe') + esc(proxyName(a)) + '</div>' : '';
  const picked = SELECTED_ACCOUNTS.has(a.uid), cls = [SEL === a.uid && 'sel', picked && 'checked', !a.enabled && 'is-off'].filter(Boolean).join(' ');
  return '<tr class="' + cls + '" data-uid="' + esc(a.uid) + '"><td class="pick"><label><input type="checkbox" data-select-account="' + esc(a.uid) + '" aria-label="选择账号 ' + esc(a.username || a.uid) + '"'
    + (picked ? ' checked' : '') + (BATCH_BUSY ? ' disabled' : '') + '></label></td>'
    + '<td><button type="button" class="nm"><b>' + esc(a.username || a.uid) + '</b>' + (a.note ? '<span class="note" title="' + esc(a.note) + '">' + esc(a.note) + '</span>' : '') + '</button>' + px + '<div class="muted small">并发 ' + concurrencyCell(a) + '</div></td>'
    + '<td>' + stateCell(a) + '</td><td class="w-q">' + quotaCell(a) + '</td><td class="exp">' + expCell(a) + '</td><td>' + accountSwitch(a) + '</td>'
    + '<td class="row-act"><button type="button" class="ibtn sm" data-act="call" title="' + (a.restricted ? '请先解除受限' : '调用：选模型、写提示词测试') + '" aria-label="调用"' + (a.restricted ? ' disabled' : '') + '>' + ic('chat') + '</button>'
    + '<button type="button" class="ibtn sm" data-act="test" title="' + (a.restricted ? '请先解除受限' : '测试') + '" aria-label="测试"' + (a.restricted ? ' disabled' : '') + '>' + ic('play') + '</button>'
    + '<button type="button" class="ibtn sm" data-act="refresh" title="刷新" aria-label="刷新">' + ic('refresh') + '</button><span class="chev">' + ic('right') + '</span></td></tr>';
}
const isOk = a => a.enabled && a.ready;
function visible(){
  const kw = KW.toLowerCase();
  const pass = {all: () => true, ok: a => !a.restricted, restricted: a => a.restricted, ip: exitBlocked, off: a => !a.enabled}[FILTER] || (() => true);
  return ROWS.filter(a => (!kw || [a.username, a.uid, a.note, a.display_name].join(' ').toLowerCase().includes(kw)) && pass(a));
}
function drawSelection(){
  const shown = visible(), selectedVisible = shown.filter(a => SELECTED_ACCOUNTS.has(a.uid)).length, hidden = SELECTED_ACCOUNTS.size - selectedVisible;
  $('#batchBar').hidden = !SELECTED_ACCOUNTS.size;
  $('#selectedCount').textContent = '已选 ' + SELECTED_ACCOUNTS.size + ' 个' + (hidden ? '（含筛选外 ' + hidden + ' 个）' : '');
  const head = $('#selectVisibleAccounts');
  head.checked = !!shown.length && selectedVisible === shown.length;
  head.indeterminate = selectedVisible > 0 && selectedVisible < shown.length;
  head.disabled = BATCH_BUSY || !shown.length;
  $('#selectAllAccounts').textContent = '全选 ' + ROWS.length + ' 个';
  $('#selectAllAccounts').disabled = BATCH_BUSY || SELECTED_ACCOUNTS.size === ROWS.length;
  $('#clearAccountSelection').disabled = BATCH_BUSY;
  const restricted = ROWS.some(a => SELECTED_ACCOUNTS.has(a.uid) && a.restricted);
  document.querySelectorAll('[data-batch]').forEach(b => { b.disabled = BATCH_BUSY || !SELECTED_ACCOUNTS.size || (b.dataset.batch === 'clear_restriction' && !restricted); });
  $('#restrictionAll').disabled = BATCH_BUSY;
  document.querySelectorAll('#accBody tr[data-uid]').forEach(tr => {
    const picked = SELECTED_ACCOUNTS.has(tr.dataset.uid), box = tr.querySelector('[data-select-account]');
    tr.classList.toggle('checked', picked); box.checked = picked; box.disabled = BATCH_BUSY;
  });
}
/* 积分 = Agent 余额 + 通用余额，只算启用的账号（上游先扣 Agent，扣完扣通用） */
function drawPool(all, ok, on){
  const sum = k => on.reduce((s, a) => s + (num((a.balance || {})[k]) || 0), 0), agent = sum('agent_token'), general = sum('token');
  const card = (k, v, sub) => '<div class="stat-k">' + k + '</div><div class="stat-v">' + v + '</div><div class="stat-s">' + sub + '</div>';
  const paid = ROWS.filter(a => isOk(a) && !a.paid_block).length, signed = on.filter(a => (a.sign || {}).signed).length;
  $('#cardPool').innerHTML = card('可用账号', ok + '<small>/ ' + all + '</small>', !all ? '还没有账号' : ['收费模型可用 ' + paid + ' 个', on.length && '已签到 ' + signed + ' / ' + on.length].filter(Boolean).join(' · '));
  $('#cardCredit').innerHTML = card('积分余额', nf(agent + general), 'Agent ' + nf(agent) + ' · 通用 ' + nf(general) + (SET_CUR.auto_sign === false ? ' · 自动签到已关' : ''));
}
/* 消耗速度：最近 1 小时从额度明细扣掉的积分（所有启用账号），换算成所选单位 */
let RATE = null, RATE_UNIT = 60;
try { RATE_UNIT = +localStorage.getItem('codar-rate-unit') || 60; } catch(e) {}
const UNIT_NAME = {1: '秒', 60: '分', 3600: '时', 86400: '天', 604800: '周'};
const rateFmt = v => v >= 100 ? nf(Math.round(v)) : v >= 10 ? String(Math.round(v * 10) / 10) : v > 0 ? String(Math.round(v * 100) / 100) : '0';
function drawRate(){
  if(!RATE) return;
  const r = RATE, left = r.per_second > 0 && r.balance > 0 ? r.balance / r.per_second : 0;
  $('#rateUnit').value = String(RATE_UNIT);
  $('#rateVal').innerHTML = rateFmt(r.per_second * RATE_UNIT) + '<small>/ ' + UNIT_NAME[RATE_UNIT] + '</small>';
  $('#rateSub').innerHTML = (left ? '余额约可用 ' + dur(left) : r.last_spend ? '上次扣费 ' + fmtS(r.last_spend) : '还没有扣费记录')
    + (r.errors.length ? ' · <span class="bad-t">' + r.errors.length + ' 个账号查询失败</span>' : '');
  $('#rateSub').title = '近 1 小时 ' + nf(r.spent_window) + ' · 24 小时 ' + nf(r.spent_day) + '；按各账号额度明细计算，免费模型不扣费、不计入；' + fmt(r.at) + ' 更新'
    + (r.errors.length ? '\n' + r.errors.map(e => e.username + '：' + e.error).join('\n') : '');
}
async function loadRate(force){
  try { RATE = await api('/credit-rate' + (force ? '?force=1' : '')); drawRate(); load().catch(() => {}); }
  catch(err){ $('#rateSub').textContent = '读取失败：' + err.message; }
}
$('#rateUnit').onchange = e => { RATE_UNIT = +e.target.value; try { localStorage.setItem('codar-rate-unit', RATE_UNIT); } catch(_) {} drawRate(); };
function draw(){
  const all = ROWS.length, ok = ROWS.filter(isOk).length, on = ROWS.filter(a => a.enabled), restricted = ROWS.filter(a => a.restricted).length, ncd = ROWS.filter(cooling).length;
  const blocked = ROWS.filter(exitBlocked).length;
  if(FILTER === 'ip' && !blocked) FILTER = 'all';
  $('#accFilter').innerHTML = [['all', '全部', all], ['ok', '正常', all - restricted], ['restricted', '账号受限', restricted], blocked && ['ip', 'IP 封控', blocked], ['off', '已关闭', all - on.length]].filter(Boolean).map(f =>
    '<button type="button" data-f="' + f[0] + '" aria-pressed="' + (FILTER === f[0]) + '">' + f[1] + '<em>' + f[2] + '</em></button>').join('');
  drawPool(all, ok, on);
  $('#cdAll').hidden = !ncd; $('#cdAll').innerHTML = ic('clock') + ncd + ' 个冷却中 · 全部恢复';
  $('#restrictionAll').hidden = !restricted; $('#restrictionAll').innerHTML = ic('shield') + restricted + ' 个账号受限 · 全部解除';
  $('#ipAll').hidden = !blocked; $('#ipAll').innerHTML = ic('globe') + blocked + ' 个账号的出口 IP 被封控 · 去代理池';
  $('#accBody').innerHTML = visible().map(row).join('') || '<tr class="empty-row"><td colspan="7" class="empty">' + (all ? '没有匹配的账号' : '还没有账号，点「添加账号」开始') + '</td></tr>';
  drawSelection();
}
async function load(){
  const d = await api('/accounts');
  ROWS = d.accounts || [];
  const existing = new Set(ROWS.map(a => a.uid));
  SELECTED_ACCOUNTS.forEach(uid => { if(!existing.has(uid)) SELECTED_ACCOUNTS.delete(uid); });
  SET_CUR = d.settings || {};
  draw();
  if(SEL){ const a = ROWS.find(x => x.uid === SEL); a ? drawDrawer(a) : closeDrawer(); }
}
let CONCURRENCY_LOADING = false;
async function loadConcurrency(){
  if(document.hidden || CONCURRENCY_LOADING || (document.querySelector('[data-page="accounts"]').hidden && !SEL)) return;
  CONCURRENCY_LOADING = true;
  try {
    const d = await api('/accounts/concurrency');
    const live = new Map((d.accounts || []).map(a => [a.uid, a]));
    ROWS.forEach(a => { const fresh = live.get(a.uid); if(fresh) Object.assign(a, fresh); });
    document.querySelectorAll('[data-concurrency-uid]').forEach(el => {
      const a = live.get(el.dataset.concurrencyUid); if(!a) return;
      el.textContent = concurrencyText(a);
      el.classList.toggle('warn-t', a.max_concurrency > 0 && a.current_concurrency >= a.max_concurrency);
    });
  } finally { CONCURRENCY_LOADING = false; }
}
/* ---- 批量操作 ---- */
async function batchAccounts(action, all = false){
  if(BATCH_BUSY) return;
  const uids = Array.from(SELECTED_ACCOUNTS);
  if(!all && !uids.length) return;
  const names = {enable: '启用', disable: '禁用', delete: '删除', clear_restriction: '解除受限标记'};
  if(action === 'delete' && !confirm('删除所选 ' + uids.length + ' 个账号？包含已选但被筛选隐藏的账号，令牌会一起删除。')) return;
  BATCH_BUSY = true; drawSelection();
  try {
    const r = await api('/accounts/batch', all ? {action, all: true} : {action, uids});
    toast('已' + names[action] + ' ' + r.updated + ' 个账号' + (action === 'enable' ? '，有受限标记的需要单独解除' : ''));
    if(action === 'delete') SELECTED_ACCOUNTS.clear();
    await load();
  } catch(err){ toast(err.message, true); }
  finally { BATCH_BUSY = false; drawSelection(); }
}
$('#selectAllAccounts').onclick = () => { ROWS.forEach(a => SELECTED_ACCOUNTS.add(a.uid)); drawSelection(); };
$('#clearAccountSelection').onclick = () => { SELECTED_ACCOUNTS.clear(); drawSelection(); };
$('#selectVisibleAccounts').onchange = e => { visible().forEach(a => { if(e.target.checked) SELECTED_ACCOUNTS.add(a.uid); else SELECTED_ACCOUNTS.delete(a.uid); }); drawSelection(); };
$('#batchBar').addEventListener('click', e => { const b = e.target.closest('[data-batch]'); if(b && !b.disabled) batchAccounts(b.dataset.batch); });
$('#restrictionAll').onclick = () => batchAccounts('clear_restriction', true);
$('#ipAll').onclick = () => showTab('proxies');
$('#accBody').addEventListener('change', e => {
  const cb = e.target.closest('[data-select-account]'); if(!cb) return;
  if(cb.checked) SELECTED_ACCOUNTS.add(cb.dataset.selectAccount); else SELECTED_ACCOUNTS.delete(cb.dataset.selectAccount);
  drawSelection();
});
$('#accBody').addEventListener('click', async e => {
  if(e.target.closest('.pick')) return;
  const tr = e.target.closest('tr[data-uid]'); if(!tr) return;
  if(await onAct(e, tr.dataset.uid)) return;
  SEL === tr.dataset.uid ? closeDrawer() : openDrawer(tr.dataset.uid);
});
$('#accFilter').addEventListener('click', e => { const f = e.target.closest('[data-f]'); if(f){ FILTER = f.dataset.f; draw(); } });
$('#accSearch').oninput = e => { KW = e.target.value; draw(); };
$('#cdAll').onclick = async e => {
  const b = e.currentTarget; b.disabled = true;
  try { const r = await api('/accounts/cooldown/clear', {}); toast('已恢复 ' + r.cleared.length + ' 个账号，立即参与派发'); }
  catch(err){ toast(err.message, true); }
  b.disabled = false; load().catch(() => {});
};
/* 冷却倒计时每秒走，到点自动重新拉一次列表 */
setInterval(() => {
  let due = false;
  document.querySelectorAll('[data-cd]').forEach(el => { const left = +el.dataset.cd - NOW(); if(left <= 0) due = true; else el.textContent = '还有 ' + dur2s(left); });
  if(due) load().catch(() => {});
}, 1000);
/* ---- 「批量」菜单：全部签到、刷新全部、全部设置代理 ---- */
function closeMenu(){ $('#bulkList').hidden = true; $('#bulkMenu').setAttribute('aria-expanded', 'false'); }
$('#bulkMenu').onclick = () => { const open = $('#bulkList').hidden; $('#bulkList').hidden = !open; $('#bulkMenu').setAttribute('aria-expanded', String(open)); if(open) $('#bulkList button').focus(); };
document.addEventListener('click', e => { if(!e.target.closest('.menu-wrap')) closeMenu(); });
async function bulk(btn, path, done){
  closeMenu(); btn.disabled = true; $('#bulkMenu').disabled = true; toast('正在处理全部启用的账号…');
  try { done(await api(path, {})); } catch(err){ toast(err.message, true); }
  btn.disabled = false; $('#bulkMenu').disabled = false; load().catch(() => {});
}
$('#signAll').onclick = e => bulk(e.currentTarget, '/accounts/sign', r => {
  const ok = r.results.filter(x => x.ok), got = ok.reduce((s, x) => s + (+x.granted || 0), 0), bad = r.results.length - ok.length;
  toast(r.results.length ? '签到：' + ok.length + ' 个成功' + (got ? '（共 +' + got + '）' : '') + (bad ? '，' + bad + ' 个未成功（多半还没到可再签时间）' : '') : '没有可签到的账号', !ok.length && r.results.length > 0);
});
$('#refreshAll').onclick = e => bulk(e.currentTarget, '/accounts/refresh', r => {
  const bad = r.results.filter(x => !x.ok).length; toast('刷新完成：' + (r.results.length - bad) + ' 个成功' + (bad ? '，' + bad + ' 个失败' : ''), bad > 0);
});
$('#proxyAll').onclick = async () => {
  closeMenu();
  try {
    await loadProxyPool();
    const first = proxyValue(ROWS[0] || {});
    const value = ROWS.length && ROWS.every(a => proxyValue(a) === first) ? first : 0;
    $('#proxyBody').innerHTML = '<p style="margin:0">应用到全部 <b>' + ROWS.length + '</b> 个账号，包括筛选隐藏和已关闭的账号。</p>'
      + '<label class="field"><select id="allProxy" aria-label="统一使用的出口">' + proxyOptions(value) + '</select></label>'
      + '<p class="hint">轮询代理使用代理池中已启用的代理，每次上游请求切换，账号之间也接续轮询。至少需要两条不同代理，没有可用出口时直接报错。</p><button type="button" class="btn block" data-proxy-action="save-all"' + (!ROWS.length ? ' disabled' : '') + '>应用到全部账号</button>';
    openModal('#proxyModal', '#allProxy');
  } catch(err){ toast(err.message, true); }
};
