'use strict';
/* 模型、统计、设置页，以及启动 */
const VENDORS = [[/^claude/i, 'Claude', '#FFB070'], [/^gpt|^o\d/i, 'OpenAI', '#8BE0A4'], [/^deepseek/i, 'DeepSeek', '#8AC9FF'], [/^glm/i, '智谱 GLM', '#C6A8FF'],
  [/^qwen/i, '通义千问', '#FFD84D'], [/^kimi/i, 'Kimi', '#BDE3FF'], [/^minimax/i, 'MiniMax', '#FF8FB7'], [/^gemini/i, 'Gemini', '#B6EFC4'], [/^auto$/i, '自动路由', '#FFE27A']];
const vendor = id => VENDORS.find(v => v[0].test(id)) || [null, '其他', '#EDE2CC'];
const fmtK = v => { if(!v) return '-'; const k = Math.round(v / 1000); return k >= 1000 ? (k / 1000) + 'M' : k + 'K'; };
function freeNow(){ const out = new Set(); ROWS.filter(a => a.ready).forEach(a => (a.free_models || []).forEach(m => out.add(m))); return out; }
async function loadCatalog(force){
  const d = await api('/catalog' + (force ? '?force=1' : '')), free = freeNow();
  $('#catAt').innerHTML = d.models.length + ' 个' + (d.at ? ' · ' + ago(d.at) + '更新' : '') + (d.warning ? ' · <span class="bad-t">刷新失败：' + esc(d.warning) + '</span>' : '');
  $('#catBody').innerHTML = d.models.map(m => {
    const p = m.pricing || {}, v = vendor(m.id), lv = m.reasoning_levels || [];
    const caps = ((m.input_modalities || []).includes('image') ? chip('ok sm', '视觉') : chip('sm', '文本')) + (m.tools ? chip('sm', '工具') : '') + (lv.length ? chip('think sm', '推理') : '');
    const proto = {openai_chat_completions: 'OpenAI Chat', anthropic_messages: 'Anthropic', openai: 'OpenAI'}[m.protocol] || m.protocol || '-';
    return '<article class="model"><div class="m-top"><span class="vdot" style="background:' + v[2] + '" title="' + esc(v[1]) + '"></span><div class="m-name"><b>' + esc(m.name || m.id) + '</b><span class="mono muted">' + esc(m.id) + '</span></div>'
      + (free.has(m.id) ? chip('ok', ic('gift') + '限时免费', '有账号领了免费模型活动，这些请求不扣余额') : '') + (m.rate_badge ? chip('info', esc(m.rate_badge)) : '') + '</div>'
      + '<div class="m-caps">' + caps + '</div><dl class="m-spec"><div><dt>上下文</dt><dd>' + fmtK(m.context_window) + '</dd></div><div><dt>最大输出</dt><dd>' + fmtK(m.max_output) + '</dd></div><div><dt>协议</dt><dd>' + esc(proto) + '</dd></div></dl>'
      + (lv.length ? '<div class="m-lv">' + lv.map(l => '<span class="lv' + (l === m.reasoning_default ? ' def' : '') + '" title="' + esc((m.reasoning_labels || {})[l] || l) + (l === m.reasoning_default ? '（默认）' : '') + '">' + esc(l) + '</span>').join('') + '</div>' : '')
      + '<div class="m-price">' + (p.input != null ? '<span>入 <b>' + p.input + '</b></span><span>出 <b>' + p.output + '</b></span>' + (p.cached_input != null ? '<span>缓存 <b>' + p.cached_input + '</b></span>' : '') + '<em>元 / 百万 token</em>'
        : '<em style="margin-left:0">没有目录价</em>') + '</div></article>';
  }).join('') || '<div class="empty-box">上游还没有返回模型</div>';
}
$('#catRefresh').onclick = () => loadCatalog(true).then(() => toast('模型目录已刷新')).catch(err => toast(err.message, true));
/* ---- 统计 ---- */
let STAT_H = 24, UPDAYS = 7;
try { STAT_H = +localStorage.getItem('codar-stat-hours') || 24; } catch(e) {}
const n = v => v == null ? '-' : Number(v).toLocaleString('en-US');
const round1 = v => v == null ? '-' : String(Math.round(v * 10) / 10);
const rateCls = v => v == null ? '' : v >= 0.95 ? 'ok-t' : v >= 0.8 ? 'warn-t' : 'bad-t';
const API_NAME = {'/v1/chat/completions': 'Chat', '/v1/messages': 'Messages', '/v1/responses': 'Responses'};
async function loadStats(){
  const d = await api('/stats?hours=' + STAT_H), t = d.total;
  document.querySelectorAll('#statWin button').forEach(b => b.setAttribute('aria-pressed', String(+b.dataset.h === STAT_H)));
  $('#statCards').innerHTML = kpi('请求', n(t.requests), t.stream + ' 次流式', 'k1') + kpi('成功率', '<span class="' + rateCls(t.success_rate) + '">' + pctS(t.success_rate) + '</span>', t.failed + ' 次失败', 'k2')
    + kpi('缓存命中' + tip('上游报告的缓存命中输入 token ÷ 输入 token'), pctS(t.cache_hit_rate), fmtTok(t.cached) + ' / ' + fmtTok(t.prompt), 'k3')
    + kpi('TPS' + tip('输出 token ÷ 生成时间；流式从首字算起，非流式按总耗时'), round1(t.tps), '输出 ' + fmtTok(t.completion), 'k4')
    + kpi('平均首字', t.ttft_ms == null ? '-' : Math.round(t.ttft_ms) + '<small>ms</small>', '仅流式', 'k5')
    + kpi('估算花费' + tip('按「设置 → 模型价格」估算，实际以上游账单为准'), yuan(t.price_cost), [t.unpriced && t.unpriced + ' 次无价格', t.cost_cny && '上游账单 ¥' + t.cost_cny].filter(Boolean).join(' · '), 'k6');
  $('#statModels').innerHTML = d.models.map(m => '<tr><td class="mono"><b>' + esc(m.model) + '</b></td><td class="num">' + n(m.requests) + '</td><td class="num ' + rateCls(m.success_rate) + '">' + pctS(m.success_rate)
    + '</td><td class="num">' + fmtTok(m.prompt) + ' / ' + fmtTok(m.completion) + '</td><td class="num">' + pctS(m.cache_hit_rate) + '</td><td class="num">' + round1(m.tps) + '</td><td class="num">'
    + (m.ttft_ms == null ? '-' : Math.round(m.ttft_ms) + ' ms') + '</td><td class="num">' + yuan(m.price_cost) + '</td></tr>').join('') || '<tr><td colspan="8" class="empty">这段时间还没有请求</td></tr>';
  $('#statAccounts').innerHTML = d.accounts.map(a => '<tr><td>' + esc(a.account) + '</td><td class="num">' + n(a.requests) + '</td><td class="num ' + rateCls(a.success_rate) + '">' + pctS(a.success_rate)
    + '</td><td class="num">' + pctS(a.cache_hit_rate) + '</td><td class="num">' + yuan(a.price_cost) + '</td></tr>').join('') || '<tr><td colspan="5" class="empty">这段时间还没有请求</td></tr>';
  $('#statRecent').innerHTML = d.recent.slice(0, 30).map(r => '<tr><td class="nowrap muted" title="' + fmt(r.ts) + '">' + fmtT(r.ts) + '</td><td class="mono">' + requestModelLabel(r) + '</td><td class="nowrap">' + esc(API_NAME[r.path] || r.path)
    + (r.stream ? ' ' + chip('sm', '流式') : '') + (r.source === 'panel' ? ' ' + chip('info sm', '面板') : '') + '</td><td>' + esc(r.account || '-') + (r.attempts > 1 ? '<div class="muted small">换号 ' + (r.attempts - 1) + ' 次</div>' : '') + '</td>'
    + '<td>' + (r.ok ? chip('ok sm', r.status) : chip('bad sm', r.status || '失败', r.error) + '<div class="bad-t small" style="max-width:240px">' + esc((r.error || '').slice(0, 80)) + '</div>')
    + (r.diagnostic_id ? ' <button type="button" class="link" data-request-detail="' + esc(r.diagnostic_id) + '">查看详情</button>' : '') + '</td>'
    + '<td class="num">' + (r.prompt || r.completion ? n(r.prompt) + ' / ' + n(r.cached) + ' / ' + n(r.completion) : '-') + '</td><td class="num">' + (r.ttft_ms != null ? r.ttft_ms + ' ms' : '-')
    + '</td><td class="num">' + (r.ms != null ? n(r.ms) + ' ms' : '-') + '</td><td class="num">' + yuan(r.price_cost) + '</td></tr>').join('') || '<tr><td colspan="9" class="empty">还没有请求</td></tr>';
}
let DETAIL_GENERATION = 0, DETAIL_COPY = [];
// Format JSON without parsing and re-encoding values, so large numbers and
// escaped strings stay exactly as the sender wrote them.
function formatDetailBody(value, encoding){
  if(value == null) return '（未记录）';
  if(typeof value !== 'string') return JSON.stringify(value, null, 2);
  if(value === '') return '（空）';
  if(String(encoding || '').toLowerCase() === 'base64') return value;
  const raw = value.trim();
  try { JSON.parse(raw); } catch(_) {
    if(!/(?:^|\n)(?:data|event|id|retry):/.test(value)) return value;
    return value.split('\n').map(line => {
      if(!line.startsWith('data:')) return line;
      const payload = line.slice(5).trim();
      try { JSON.parse(payload); } catch(_) { return line; }
      return 'data: ' + formatDetailBody(payload).replace(/\n/g, '\n  ');
    }).join('\n');
  }
  let out = '', indent = 0, quoted = false, escaped = false, previous = '';
  const newline = () => '\n' + '  '.repeat(indent);
  for(let i = 0; i < raw.length; i++){
    const c = raw[i];
    if(quoted){
      out += c;
      if(escaped) escaped = false;
      else if(c === '\\') escaped = true;
      else if(c === '"'){ quoted = false; previous = c; }
    } else if(c === '"'){ quoted = true; out += c; }
    else if(c === '{' || c === '['){
      out += c;
      let next = i + 1; while(next < raw.length && /\s/.test(raw[next])) next++;
      if(raw[next] !== '}' && raw[next] !== ']'){ indent++; out += newline(); }
      else indent++;
      previous = c;
    } else if(c === '}' || c === ']'){
      indent = Math.max(0, indent - 1);
      if(previous !== '{' && previous !== '[') out += newline();
      out += c; previous = c;
    } else if(c === ','){ out += ',' + newline(); previous = c; }
    else if(c === ':'){ out += ': '; previous = c; }
    else if(!/\s/.test(c)){ out += c; previous = c; }
  }
  return out;
}
function formatDetailHeaders(headers){
  if(headers == null) return '（未记录）';
  if(typeof headers === 'string') return headers || '（空）';
  const lines = [];
  Object.entries(headers).forEach(([name, values]) => {
    (Array.isArray(values) ? values : [values]).forEach(value => lines.push(name + ': ' + String(value)));
  });
  return lines.join('\n') || '（空）';
}
function detailPart(title, value, copy, highlighted){
  const id = DETAIL_COPY.push(copy === undefined ? value : copy) - 1;
  return '<section class="request-detail-part"><div class="request-detail-head"><b>' + esc(title) + '</b>'
    + '<button type="button" class="btn ghost sm" data-detail-copy="' + id + '">复制</button></div><pre class="request-detail-pre">' + (highlighted === undefined ? esc(value) : highlighted) + '</pre></section>';
}
function detailHeaders(title, headers){
  return '<details class="request-detail-fold"><summary>' + esc(title) + '</summary>' + detailPart(title, formatDetailHeaders(headers)) + '</details>';
}
function detailBodyLabel(part, label){
  return label + (String((part || {}).body_encoding || '').toLowerCase() === 'base64' ? '（二进制 / Base64）' : '');
}
function detailBody(part, label, highlighted){
  part = part || {};
  return detailPart(detailBodyLabel(part, label), formatDetailBody(part.body, part.body_encoding), typeof part.body === 'string' ? part.body : undefined, highlighted);
}
function detailMeta(part){
  const meta = [part.method, part.url].filter(Boolean).join(' ');
  return meta ? '<div class="request-detail-meta mono">' + esc(meta) + '</div>' : '';
}
function detailForward(original, attempt, index, total){
  const request = (attempt || {}).request;
  if(!request || !request.method) return '<div class="request-detail-attempt"><h4>第 ' + (index + 1) + ' 次</h4><p class="muted small">未发送到上游</p></div>';
  const before = original && original.body, after = request.body;
  const display = formatDetailBody(after, request.body_encoding);
  let comparison;
  try {
    comparison = DetailDiff.compare(before === '' ? '' : before == null ? null : formatDetailBody(before, original.body_encoding),
      after === '' ? '' : after == null ? null : display,
      {beforeBinary: String((original || {}).body_encoding || '').toLowerCase() === 'base64', afterBinary: String(request.body_encoding || '').toLowerCase() === 'base64'});
  } catch(_) { comparison = {ranges: [], removed: [], changed: false, reason: '无法比较正文；以下显示原始转发内容。'}; }
  const note = comparison.reason || (comparison.changed ? '黄色高亮表示相对原始请求体新增或修改的内容。' : '正文内容与原始请求体一致（忽略 JSON 排序和空白）。');
  const deletions = comparison.removed.length ? '<details class="request-detail-fold"><summary>原始请求中有 ' + comparison.removed.length + ' 处内容被删除</summary><pre class="request-detail-pre">' + esc(comparison.removed.join('\n')) + '</pre></details>' : '';
  const highlighted = after === '' || after == null ? esc(display) : DetailDiff.render(display, comparison.ranges, esc);
  return '<div class="request-detail-attempt">' + (total > 1 ? '<h4>第 ' + (index + 1) + ' 次转发</h4>' : '') + detailMeta(request)
    + '<p class="request-detail-hint muted">' + esc(note) + '</p>' + detailBody(request, '转发正文', highlighted) + deletions + detailHeaders('转发请求头', request.headers) + '</div>';
}
function detailUpstreamResponse(attempt, index){
  const response = (attempt || {}).response || {}, error = (attempt || {}).error;
  const status = response.status ? ' · HTTP ' + response.status : '';
  return '<details class="request-detail-fold"><summary>第 ' + (index + 1) + ' 次上游响应' + esc(status) + '</summary>'
    + (error ? '<p class="bad-t small">' + esc(error) + '</p>' : '')
    + (response.status ? detailHeaders('上游响应头', response.headers) + detailBody(response, '上游响应正文') : '<p class="muted small">未收到上游响应</p>') + '</details>';
}
async function openRequestDetail(id){
  const generation = ++DETAIL_GENERATION;
  DETAIL_COPY = [];
  $('#requestDetailBody').innerHTML = spinner('正在读取详细记录…');
  openModal('#requestDetailModal');
  try {
    const result = await api('/stats/request-detail?id=' + encodeURIComponent(id));
    if(generation !== DETAIL_GENERATION) return;
    const d = result.detail || {}, attempts = Array.isArray(d.attempts) ? d.attempts : [], original = d.request || {}, response = d.response || {};
    $('#requestDetailBody').innerHTML = '<section class="request-detail-group"><h3>原始请求体</h3>' + detailMeta(original)
      + detailBody(original, '原始正文') + detailHeaders('原始请求头', original.headers) + '</section>'
      + '<section class="request-detail-group"><h3>转发请求体</h3>'
      + (attempts.length ? attempts.map((a, i) => detailForward(original, a, i, attempts.length)).join('') : '<p class="muted small">未发送到上游</p>') + '</section>'
      + '<section class="request-detail-group"><h3>响应体</h3>' + (response.status ? '<div class="request-detail-meta">返回客户端 · HTTP ' + esc(response.status) + '</div>' : '<p class="muted small">未记录客户端响应</p>')
      + detailBody(response, '返回客户端的正文') + detailHeaders('返回客户端的响应头', response.headers)
      + attempts.map((a, i) => detailUpstreamResponse(a, i)).join('') + '</section>';
  } catch(err){
    if(generation === DETAIL_GENERATION) $('#requestDetailBody').innerHTML = '<div class="notice bad">读取失败：' + esc(err.message) + '</div>';
  }
}
document.addEventListener('click', e => {
  const b = e.target.closest('[data-request-detail]');
  if(b) openRequestDetail(b.dataset.requestDetail);
});
$('#requestDetailBody').addEventListener('click', async e => {
  const b = e.target.closest('[data-detail-copy]'); if(!b) return;
  const ok = await copyText(DETAIL_COPY[Number(b.dataset.detailCopy)] || '');
  toast(ok ? '已复制' : '复制失败', !ok);
});
MODAL_CLOSE['#requestDetailModal'] = id => {
  DETAIL_GENERATION++; DETAIL_COPY = []; $('#requestDetailBody').textContent = ''; closeModal(id);
};
$('#statWin').addEventListener('click', e => { const b = e.target.closest('[data-h]'); if(!b) return;
  STAT_H = +b.dataset.h; try { localStorage.setItem('codar-stat-hours', STAT_H); } catch(_) {} loadStats().catch(err => toast(err.message, true)); });
async function loadUpstream(){
  document.querySelectorAll('#upWin button').forEach(b => b.setAttribute('aria-pressed', String(+b.dataset.updays === UPDAYS)));
  $('#upBody').innerHTML = spinner('正在向上游查询各账号…');
  const d = await api('/accounts/usage?' + rangeQS(UPDAYS));
  if(d.ok === false){ $('#upBody').innerHTML = '<div class="notice bad">' + esc(d.error) + '</div>'; return; }
  const accounts = '<div class="tbl"><table class="t"><thead><tr><th>账号</th><th class="num">调用</th><th class="num">输入</th><th class="num">输出</th><th class="num">命中率</th><th class="num">花费</th></tr></thead><tbody>'
    + (d.accounts.map(a => '<tr><td>' + esc(a.username) + '</td><td class="num">' + nf(a.calls) + '</td><td class="num">' + fmtTok(a.input_tokens) + '</td><td class="num">' + fmtTok(a.output_tokens)
      + '</td><td class="num">' + pctS(a.cache_hit_rate) + '</td><td class="num">' + yuan(a.cost) + '</td></tr>').join('') || '<tr><td colspan="6" class="empty">没有可查询的账号</td></tr>') + '</tbody></table></div>';
  $('#upBody').innerHTML = usageView(d, '<details class="more"><summary>按模型 / 按账号明细</summary>' + usageModels(d.models) + '<div style="height:10px"></div>' + accounts + '</details>')
    + (d.errors.length ? '<div class="notice bad" style="margin-top:12px">' + d.errors.map(e => esc(e.username) + '：' + esc(e.error)).join('<br>') + '</div>' : '');
}
$('#upWin').addEventListener('click', e => { const b = e.target.closest('[data-updays]'); if(!b) return; UPDAYS = +b.dataset.updays; loadUpstream().catch(err => toast(err.message, true)); });
$('#upReload').onclick = () => loadUpstream().catch(err => toast(err.message, true));
/* ---- 设置：按用途分组，每项后面一句话说明规则 ---- */
let SET = null, PRICES = [], SETTINGS_PENDING = 0, SETTINGS_REVISION = 0, SETTINGS_SAVE_QUEUE = Promise.resolve();
const SET_GROUPS = [['relay', ['use_codar_ua', 'codar_ua', 'auto_passthrough', 'dsh_identity_rewrite', 'identity_first_line_fill', 'detailed_debug']],
  ['routing', ['scheduling_strategy', 'max_attempts', 'cooldown_429_seconds', 'cooldown_error_seconds', 'cooldown_balance_seconds', 'ip_block', 'allow_general_balance']],
  ['tasks', ['auto_sign', 'auto_claim', 'auto_renew_token', 'renew_before_hours', 'refresh_minutes', 'free_check_minutes', 'catalog_ttl_minutes']],
  ['remote', ['allow_remote_management']]];
let SETTINGS_TAB = 'relay';
try { SETTINGS_TAB = localStorage.getItem('codar-settings-tab') || 'relay'; } catch(e) {}
function showSettingsTab(tab){
  if(![...document.querySelectorAll('[data-settings-tab]')].some(b => b.dataset.settingsTab === tab)) tab = 'relay';
  SETTINGS_TAB = tab;
  document.querySelectorAll('[data-settings-tab]').forEach(b => {
    const active = b.dataset.settingsTab === tab;
    b.setAttribute('aria-selected', String(active)); b.tabIndex = active ? 0 : -1;
  });
  document.querySelectorAll('[data-settings-panel]').forEach(p => p.hidden = p.dataset.settingsPanel !== tab);
  $('#settingsActions').hidden = tab === 'prices' || tab === 'about';
  try { localStorage.setItem('codar-settings-tab', tab); } catch(e) {}
  if(tab === 'prices' && SET) loadPrices().catch(err => toast(err.message, true));
  if(tab === 'about') loadAbout();
}
$('#settingsNav').addEventListener('click', e => {
  const b = e.target.closest('[data-settings-tab]'); if(b) showSettingsTab(b.dataset.settingsTab);
});
$('#settingsNav').addEventListener('keydown', e => {
  if(!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
  const tabs = [...document.querySelectorAll('[data-settings-tab]')], i = tabs.indexOf(e.target);
  if(i < 0) return;
  e.preventDefault();
  const next = e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1
    : (i + (['ArrowUp', 'ArrowLeft'].includes(e.key) ? -1 : 1) + tabs.length) % tabs.length;
  showSettingsTab(tabs[next].dataset.settingsTab); tabs[next].focus();
});
function drawSettings(vals){
  const known = SET_GROUPS.flatMap(g => g[1]), groups = SET_GROUPS.map(g => [g[0], g[1].slice()]);
  SET.schema.forEach(f => { if(!known.includes(f.key)) groups.find(g => g[0] === 'tasks')[1].push(f.key); });
  const field = f => '<div class="set' + (f.type === 'string' ? ' set-text' : '') + '"><div class="set-l"><b>' + esc(f.label) + '</b>' + (f.help ? '<span class="muted">' + esc(f.help) + '</span>' : '') + '</div>'
    + (f.type === 'bool' ? sw(vals[f.key], f.label, 'data-k="' + f.key + '" title="默认' + (f.default ? '开' : '关') + '"')
      : f.type === 'select' ? '<select class="in strategy-in" data-k="' + f.key + '" aria-label="' + esc(f.label) + '">' + (SET.scheduling_options || []).map(o => '<option value="' + esc(o.value) + '"' + (o.value === vals[f.key] ? ' selected' : '') + '>' + esc(o.label) + '</option>').join('') + '</select>'
      : f.type === 'string' ? (f.key === 'codar_ua' ? '<div class="ua-controls">' + sw(vals.use_codar_ua, '启用自定义 UA', 'data-k="use_codar_ua" title="开启后替换 UA，关闭后沿用调用方 UA"') : '') + '<input class="in mono text-in" type="text" data-k="' + f.key + '" maxlength="' + f.max + '" value="' + esc(vals[f.key]) + '" aria-label="' + esc(f.label) + '" autocomplete="off" spellcheck="false">' + (f.key === 'codar_ua' ? '</div>' : '')
      : '<input class="in num-in" type="number" data-k="' + f.key + '" min="' + f.min + '" max="' + f.max + '" value="' + esc(vals[f.key]) + '" aria-label="' + esc(f.label) + '" title="默认 ' + f.default + '，范围 ' + f.min + ' 至 ' + f.max + '">') + '</div>';
  groups.forEach(g => {
    document.querySelector('[data-setting-fields="' + g[0] + '"]').innerHTML = g[1].filter(k => k !== 'use_codar_ua').map(k => SET.schema.find(f => f.key === k)).filter(Boolean).map(field).join('');
  });
}
function settingValue(el){
  return el.classList.contains('sw') ? el.getAttribute('aria-checked') === 'true' : el.type === 'number' ? Number(el.value) : el.value;
}
function formValues(){
  const out = {};
  document.querySelectorAll('#setGrid [data-k]').forEach(el => { out[el.dataset.k] = settingValue(el); });
  return out;
}
function saveSettingsPatch(patch){
  SETTINGS_PENDING++; SETTINGS_REVISION++;
  $('#setSave').disabled = $('#setReset').disabled = true;
  const save = SETTINGS_SAVE_QUEUE.then(async () => {
    const r = await api('/settings', patch);
    SET.settings = r.settings; SET_CUR = r.settings;
    document.querySelectorAll('#setGrid [data-k]').forEach(el => {
      const k = el.dataset.k;
      if(!Object.prototype.hasOwnProperty.call(patch, k) || settingValue(el) !== patch[k]) return;
      if(el.classList.contains('sw')) el.setAttribute('aria-checked', String(r.settings[k]));
      else el.value = r.settings[k];
    });
    return r;
  });
  SETTINGS_SAVE_QUEUE = save.catch(() => {});
  return save.finally(() => {
    SETTINGS_PENDING--;
    $('#setSave').disabled = $('#setReset').disabled = SETTINGS_PENDING > 0;
  });
}
async function loadSettings(){
  await SETTINGS_SAVE_QUEUE;
  const revision = SETTINGS_REVISION, d = await api('/settings');
  if(revision !== SETTINGS_REVISION || SETTINGS_PENDING) return loadSettings();
  SET = d; drawSettings(SET.settings); showSettingsTab(SETTINGS_TAB);
}
$('#setGrid').addEventListener('click', async e => {
  const s = e.target.closest('.sw[data-k]'); if(!s || s.disabled || !SET) return;
  const before = s.getAttribute('aria-checked') === 'true';
  s.disabled = true; s.setAttribute('aria-checked', String(!before));
  try { await saveSettingsPatch({[s.dataset.k]: !before}); toast('设置已保存，立即生效'); }
  catch(err){ s.setAttribute('aria-checked', String(before)); toast('保存失败：' + err.message, true); }
  finally { s.disabled = false; }
});
$('#setSave').onclick = async () => {
  if(!SET || SETTINGS_PENDING) return;
  try { await saveSettingsPatch(formValues()); toast('设置已保存，立即生效'); }
  catch(err){ toast('保存失败：' + err.message, true); }
};
$('#setReset').onclick = () => {
  if(!SET || SETTINGS_PENDING) return;
  const d = formValues(), group = SET_GROUPS.find(g => g[0] === SETTINGS_TAB);
  if(!group) return;
  const known = SET_GROUPS.flatMap(g => g[1]);
  SET.schema.filter(f => group[1].includes(f.key) || SETTINGS_TAB === 'tasks' && !known.includes(f.key)).forEach(f => d[f.key] = f.default);
  drawSettings(d); toast('已填入本页默认值，点「保存」生效');
};
/* ---- 模型价格 ---- */
function priceRow(p){
  const inp = (k, v) => '<input class="in price-in" type="number" min="0" step="any" data-pk="' + k + '" value="' + esc(v) + '" aria-label="' + esc(p.model) + ' ' + {input: '输入', cached: '缓存命中', output: '输出'}[k] + '价格">';
  return '<tr data-model="' + esc(p.model) + '"><td class="mono"><b>' + esc(p.model) + '</b>' + (p.in_catalog ? '' : ' ' + chip('sm', '目录外', '当前模型目录里没有')) + '</td>'
    + '<td class="num">' + inp('input', p.input) + '</td><td class="num">' + inp('cached', p.cached) + '</td><td class="num">' + inp('output', p.output) + '</td>'
    + '<td class="row-act"><button type="button" class="btn soft sm" data-psave>保存</button>' + (p.source === 'custom' ? ' <button type="button" class="link" data-preset>恢复默认</button>' : '') + '</td></tr>';
}
async function loadPrices(){
  const d = await api('/prices'); PRICES = d.prices || [];
  $('#priceBody').innerHTML = PRICES.map(priceRow).join('') || '<tr><td colspan="5" class="empty">没有模型</td></tr>';
}
$('#priceBody').addEventListener('click', async e => {
  const b = e.target.closest('[data-psave],[data-preset]'); if(!b) return;
  const tr = b.closest('tr'), model = tr.dataset.model, body = {model};
  if(b.hasAttribute('data-preset')) body.reset = true;
  else tr.querySelectorAll('[data-pk]').forEach(i => body[i.dataset.pk] = i.value === '' ? NaN : Number(i.value));
  b.disabled = true;
  try { await api('/prices', body); toast(body.reset ? model + ' 已恢复默认价格' : model + ' 价格已保存'); await loadPrices(); }
  catch(err){ toast('保存失败：' + err.message, true); b.disabled = false; }
});
$('#priceBody').addEventListener('keydown', e => { if(e.key === 'Enter' && e.target.matches('[data-pk]')) e.target.closest('tr').querySelector('[data-psave]').click(); });
$('#priceAdd').onclick = async () => {
  const model = $('#priceNew').value.trim(); if(!model) return toast('请输入模型 ID', true);
  if(PRICES.some(p => p.model === model)) return toast('已经有这个模型，直接改下面的价格', true);
  try { await api('/prices', {model, input: 0, output: 0, cached: 0}); $('#priceNew').value = ''; await loadPrices(); toast('已添加 ' + model + '，填好价格后点「保存」'); }
  catch(err){ toast(err.message, true); }
};
/* ---- 启动 ---- */
decorate();
checkAboutUpdate(true);
setInterval(() => checkAboutUpdate(true), 21600000);
loadAnnouncements().catch(() => {});
setInterval(() => loadAnnouncements().catch(() => {}), 1800000);
load().catch(err => toast(err.message, true));
let TAB0 = 'accounts';
try { TAB0 = localStorage.getItem('codar-tab') || 'accounts'; } catch(e) {}
showTab(TAB0);
setInterval(() => load().catch(() => {}), 20000);
setInterval(() => loadConcurrency().catch(() => {}), 2000);
loadRate();
setInterval(() => loadRate(), 60000);
setInterval(() => { if(!document.querySelector('[data-page="stats"]').hidden) loadStats().catch(() => {}); }, 10000);
