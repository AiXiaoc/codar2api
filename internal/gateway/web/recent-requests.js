'use strict';
/* 最近请求的错误记录独立保存；切换、翻页和刷新都从完整保留期查询。 */
let RECENT_ERRORS = false, RECENT_PAGE = 1, RECENT_PAGES = 1, RECENT_HOURS = null;
let RECENT_GENERATION = 0, RECENT_PENDING = null, RECENT_DRAW_KEY = '', RECENT_BUSY = false;
try { RECENT_ERRORS = localStorage.getItem('gateway-recent-errors') === '1'; } catch(_) {}

function recentControls(){
  document.querySelectorAll('#statRecentMode button').forEach(b => b.setAttribute('aria-pressed', String((b.dataset.errors === '1') === RECENT_ERRORS)));
  $('#statRecentPrev').disabled = RECENT_BUSY || RECENT_PAGE <= 1;
  $('#statRecentNext').disabled = RECENT_BUSY || RECENT_PAGE >= RECENT_PAGES;
  $('#statRecent').setAttribute('aria-busy', String(RECENT_BUSY));
}

function recentRequestRow(r){
  const failed = r.error_attempts || [], recovered = r.recovered || (r.ok && failed.length);
  let result = recovered ? chip('warn sm', '重试后成功') + ' ' + chip('ok sm', r.status) : r.ok ? chip('ok sm', r.status) : chip('bad sm', r.status || '失败', r.error);
  const causes = failed.length ? failed : r.error ? [{error:r.error}] : [];
  result += causes.map(a => {
    const cause = (a.account ? a.account + '：' : '') + (a.status ? 'HTTP ' + a.status + ' · ' : '') + (a.error || '请求失败');
    return '<div class="bad-t small recent-error" title="' + esc(cause) + '">' + esc(cause.slice(0, 240)) + '</div>';
  }).join('');
  return '<tr><td class="nowrap muted" title="' + fmt(r.ts) + '">' + fmtT(r.ts) + '</td><td class="mono">' + requestModelLabel(r) + '</td><td class="nowrap">' + esc(API_NAME[r.path] || r.path)
    + (r.stream ? ' ' + chip('sm', '流式') : '') + (r.source === 'panel' ? ' ' + chip('info sm', '面板') : '') + '</td><td>' + esc(r.account || '-') + (r.attempts > 1 ? '<div class="muted small">换号 ' + (r.attempts - 1) + ' 次</div>' : '') + '</td><td>' + result
    + (r.diagnostic_id ? ' <button type="button" class="link" data-request-detail="' + esc(r.diagnostic_id) + '">查看详情</button>' : '') + '</td>'
    + '<td class="num">' + (r.prompt || r.completion ? n(r.prompt) + ' / ' + n(r.cached) + ' / ' + n(r.completion) : '-') + '</td><td class="num">' + (r.ttft_ms != null ? r.ttft_ms + ' ms' : '-')
    + '</td><td class="num">' + (r.ms != null ? n(r.ms) + ' ms' : '-') + '</td><td class="num">' + yuan(r.price_cost) + '</td></tr>';
}

function loadRecentRequests(){
  if(RECENT_HOURS !== STAT_H){ RECENT_HOURS = STAT_H; RECENT_PAGE = 1; }
  const key = [STAT_H, RECENT_ERRORS, RECENT_PAGE].join('/');
  if(RECENT_PENDING && RECENT_PENDING.key === key) return RECENT_PENDING.promise;
  const generation = ++RECENT_GENERATION;
  const url = '/stats/recent?hours=' + STAT_H + '&errors_only=' + (RECENT_ERRORS ? '1' : '0') + '&page=' + RECENT_PAGE + '&page_size=30';
  RECENT_BUSY = true; recentControls();
  $('#statRecentNotice').className = 'muted small';
  $('#statRecentNotice').textContent = RECENT_ERRORS ? '错误记录独立保存 7 天，包含重试后成功的请求。正在读取…' : '正在读取…';
  if(RECENT_DRAW_KEY !== key){
    $('#statRecent').innerHTML = '<tr><td colspan="9" class="empty">加载中…</td></tr>';
    $('#statRecentPage').textContent = '';
  }
  const promise = (async () => {
    try {
      const d = await api(url);
      if(generation !== RECENT_GENERATION) return;
      RECENT_PAGE = d.page; RECENT_PAGES = d.pages;
      RECENT_DRAW_KEY = [STAT_H, RECENT_ERRORS, RECENT_PAGE].join('/');
      $('#statRecent').innerHTML = (d.items || []).map(recentRequestRow).join('') || '<tr><td colspan="9" class="empty">' + (RECENT_ERRORS ? '这段时间没有错误请求' : '这段时间没有请求') + '</td></tr>';
      $('#statRecentPage').textContent = '共 ' + n(d.total) + ' 条 · 第 ' + d.page + ' / ' + d.pages + ' 页';
      $('#statRecentNotice').textContent = RECENT_ERRORS ? '错误记录独立保存 7 天，包含重试后成功的请求。' : '保留最近 7 天的请求，可切换到错误请求单独查看。';
      return d;
    } catch(err){
      if(generation !== RECENT_GENERATION) return;
      $('#statRecentNotice').className = 'bad-t small';
      $('#statRecentNotice').textContent = '读取失败：' + err.message + (RECENT_DRAW_KEY === key ? '（显示上次结果）' : '');
      if(RECENT_DRAW_KEY !== key) $('#statRecent').innerHTML = '<tr><td colspan="9" class="empty">读取失败，请重新切换或等待刷新</td></tr>';
    } finally {
      if(generation === RECENT_GENERATION){ RECENT_PENDING = null; RECENT_BUSY = false; recentControls(); }
    }
  })();
  RECENT_PENDING = {key, promise};
  return promise;
}

$('#statRecentMode').addEventListener('click', e => {
  const b = e.target.closest('[data-errors]'); if(!b) return;
  const errors = b.dataset.errors === '1';
  if(errors !== RECENT_ERRORS){ RECENT_ERRORS = errors; RECENT_PAGE = 1; }
  try { localStorage.setItem('gateway-recent-errors', RECENT_ERRORS ? '1' : '0'); } catch(_) {}
  loadRecentRequests();
});
$('#statRecentPrev').onclick = () => { if(!RECENT_BUSY && RECENT_PAGE > 1){ RECENT_PAGE--; loadRecentRequests(); } };
$('#statRecentNext').onclick = () => { if(!RECENT_BUSY && RECENT_PAGE < RECENT_PAGES){ RECENT_PAGE++; loadRecentRequests(); } };
