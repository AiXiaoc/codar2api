'use strict';
/* Actual point charges; the cumulative amount is independent of token dates. */
const POINT_RANGES = {hour: 'spent_hour', today: 'spent_today', day: 'spent_day', week: 'spent_week'};
let POINT_RANGE = 'today', POINT_SPENDING = {}, POINT_ERROR = '', POINT_PENDING = null, POINT_AT = 0;
try { const saved = localStorage.getItem('gateway-point-spend-range'); if(POINT_RANGES[saved]) POINT_RANGE = saved; } catch(_) {}
const pointNumber = value => Number(value).toLocaleString('en-US', {maximumFractionDigits: 6});
function pointSpendingBody(uid){
  const r = POINT_SPENDING[uid], value = r && r[POINT_RANGES[POINT_RANGE]], error = r && r.error || POINT_ERROR;
  const amount = typeof value === 'number' ? '<b>' + pointNumber(value) + '</b> <span class="muted small">积分</span>' : '<span class="muted">—</span>';
  return amount + (error ? '<div class="small bad-t" title="' + esc(error) + '">' + (typeof value === 'number' ? '上次结果 · 查询失败' : '查询失败') + '</div>' : '');
}
function pointSpendingCell(a){
  return '<div class="point-spend" data-spend-uid="' + esc(a.uid) + '">' + pointSpendingBody(a.uid) + '</div>';
}
function pointTotalBody(a){
  const r = POINT_SPENDING[a.uid], value = r && r.spent_total, error = r && r.error || POINT_ERROR;
  let note = '自 ' + fmt(r && r.since || a.added_at) + ' 导入起';
  if(r) note += r.source === 'gateway' ? ' · 网关扣费' : ' · 官方账单';
  if(r && r.complete === false && typeof value === 'number') note += ' · 早期记录可能不完整';
  if(error) note += ' · ' + error;
  return '<div class="kpi-k">累计消耗积分</div><div class="kpi-v">' + (typeof value === 'number' ? pointNumber(value) + '<small> 积分</small>' : '—')
    + '</div><div class="kpi-s' + (error ? ' bad-t' : '') + '">' + esc(note) + '</div>';
}
function pointSpendingKPI(a){
  return '<div class="kpi" data-spend-total="' + esc(a.uid) + '">' + pointTotalBody(a) + '</div>';
}
function drawPointSpending(){
  document.querySelectorAll('[data-spend-uid]').forEach(el => { el.innerHTML = pointSpendingBody(el.dataset.spendUid); });
  document.querySelectorAll('[data-spend-total]').forEach(el => {
    const a = ROWS.find(a => a.uid === el.dataset.spendTotal);
    if(a) el.innerHTML = pointTotalBody(a);
  });
  const selector = $('#spendRange'); if(selector) selector.value = POINT_RANGE;
}
function loadPointSpending(force){
  if(POINT_PENDING) return POINT_PENDING;
  if(!force && Date.now() - POINT_AT < 60000 && !ROWS.some(a => !POINT_SPENDING[a.uid])) return Promise.resolve();
  POINT_PENDING = api('/accounts/spending' + (force ? '?force=1' : ''))
    .then(d => {
      POINT_SPENDING = Object.fromEntries((d.accounts || []).map(r => [r.uid, r])); POINT_ERROR = '';
    }, err => { POINT_ERROR = err.message || '积分消耗读取失败'; })
    .finally(() => { POINT_PENDING = null; POINT_AT = Date.now(); drawPointSpending(); });
  return POINT_PENDING;
}
const pointSelector = $('#spendRange');
if(pointSelector){
  pointSelector.value = POINT_RANGE;
  pointSelector.onchange = e => {
    if(!POINT_RANGES[e.target.value]) return;
    POINT_RANGE = e.target.value;
    try { localStorage.setItem('gateway-point-spend-range', POINT_RANGE); } catch(_) {}
    drawPointSpending();
  };
}
