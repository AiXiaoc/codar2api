'use strict';
/* 账号详情抽屉：概览、请求记录、签到兑换、活动邀请、用量、代理 */
const VIA = {wechat: '微信扫码', phone: '手机号验证码', local: '本机客户端登录', token: '粘贴令牌'};
const kv = rows => '<dl class="kv">' + rows.filter(Boolean).map(r => '<div><dt>' + r[0] + '</dt><dd>' + r[1] + '</dd></div>').join('') + '</dl>';
function renewCell(a){
  const r = a.renew || {}, auto = SET_CUR.auto_renew_token === false ? '自动续期已关闭' : '剩不到 ' + (SET_CUR.renew_before_hours || 48) + ' 小时自动续期';
  return '<div>' + auto + '</div>' + (r.at ? '<div class="small ' + (r.ok ? 'ok-t' : 'bad-t') + '">' + (r.ok ? '上次续期成功' : '上次续期失败：' + esc(r.error)) + ' · ' + fmtS(r.at)
    + (r.count ? '（共 ' + r.count + ' 次）' : '') + '</div>' : '') + '<button type="button" class="btn soft sm" data-act="renew" style="margin-top:6px">' + ic('refresh') + '立即续期</button>';
}
const ipNotice = a => '<div class="notice bad" style="margin-bottom:14px"><b>出口 IP 封控</b>（' + esc(exitName(a)) + '）：' + esc(a.proxy.blocked_reason || '上游拒绝了这个 IP')
  + (a.proxy.blocked_at ? '，' + fmtS(a.proxy.blocked_at) + ' 标记' : '') + '。已停止经这个 IP 调度，用同一出口的账号都受影响。<div style="margin-top:8px"><button type="button" class="btn soft sm" data-act="unblock">解除封控</button></div></div>';
function dOverview(a){
  const b = a.balance || {}, s = a.sign || {}, st = a.stats || {};
  return (a.restricted ? '<div class="notice bad" style="margin-bottom:14px"><b>账号受限</b>：' + esc(a.restricted_reason || '上游返回 account_restricted')
      + (a.restricted_at ? '（' + fmtS(a.restricted_at) + ' 标记）' : '') + '。已停止派发对话请求，手动解除后恢复；上游再次返回受限会重新标记。</div>' : '')
    + (exitBlocked(a) ? ipNotice(a) : '')
    + quotaCell(a, true)
    + (s.granted && s.expire_at > NOW() ? '<div class="small warn-t">其中签到送的 ' + esc(s.granted) + ' 在 ' + fmtS(s.expire_at) + ' 过期</div>' : '')
    + '<div class="muted small">' + (b.at ? ago(b.at) + '查询' : '还没查询余额') + '</div>'
    + '<h3>账号</h3>' + kv([
      ['用户名', '<b>' + esc(a.username || '-') + '</b>' + copyBtn(a.username)],
      ['ID', '<span class="mono">' + esc(a.uid) + '</span>' + copyBtn(a.uid)],
      a.display_name && a.display_name !== a.username ? ['显示名', esc(a.display_name)] : null,
      ['备注', '<input class="in" data-note value="' + esc(a.note) + '" placeholder="写点备注，回车保存" aria-label="备注" maxlength="200">'],
      ['添加方式', esc(VIA[a.login_via] || VIA.token) + ' · ' + fmtS(a.added_at)],
      a.last_error ? ['最近错误', '<span class="bad-t">' + esc(a.last_error) + '</span>' + (a.last_error_at ? ' <span class="muted small">' + fmtS(a.last_error_at) + '</span>' : '')] : null])
    + '<h3>令牌</h3>' + kv([['剩余', expCell(a)], ['令牌', '<span class="mono small">' + esc(a.token_masked) + '</span>'], ['续期', renewCell(a)]])
    + '<h3>使用</h3><div class="mini3"><div><b>' + nf(st.requests || 0) + '</b><span>请求</span></div><div><b>' + (st.cost_cny ? '≈ ¥' + st.cost_cny : '-') + '</b><span>花费</span></div>'
    + '<div><b>' + (st.last_used ? ago(st.last_used) : '-') + '</b><span title="' + esc(st.last_model || '') + '">' + esc(st.last_model || '最近使用') + '</span></div></div>';
}
/* 请求记录：经本网关发给这个账号的每一次尝试，换号前失败的那次也算这个账号的 */
const RCACHE = {};
let RFAILED = false;
function fetchRequests(uid, page, force){
  const c = RCACHE[uid];
  if(c && c.page === page && c.failed === RFAILED && !force && (c.data || c.loading)) return;
  const failed = RFAILED, token = {};
  RCACHE[uid] = {page, failed, token, loading: true, data: c && c.failed === failed ? c.data : null};
  const keep = d => { if(RCACHE[uid] && RCACHE[uid].token === token){ RCACHE[uid] = {page, failed, data: d}; redrawTab('requests'); } };
  api('/accounts/requests?uid=' + encodeURIComponent(uid) + '&page=' + page + '&page_size=20' + (failed ? '&failed=1' : ''))
    .then(keep, err => keep({ok: false, error: err.message}));
}
function recItem(x){
  const status = x.status ? ' ' + x.status : '';
  const usage = x.prompt != null || x.completion != null ? '<div class="rec-b">' + tokenLine(x.prompt, x.completion, x.cached) + '</div>' : '';
  return '<li><div class="rec-h">' + (x.ok ? chip('ok sm', '成功' + status) : chip('bad sm', '失败' + status)) + '<span class="mono">' + esc(x.model || '-') + '</span>'
    + (x.switched ? chip('warn sm', '已换号', '这次失败后网关换了别的账号重试') : '') + (x.source === 'panel' ? chip('sm', '面板测试') : '') + '<span class="when">' + fmtT(x.ts) + '</span></div>'
    + usage + (!x.ok ? '<div class="rec-e">' + esc(x.error || '未知原因') + '</div>' : '') + '</li>';
}
function dRequests(a){
  fetchRequests(a.uid, (RCACHE[a.uid] || {}).page || 1);
  const d = RCACHE[a.uid].data;
  const head = '<div class="row"><h3 style="margin:0">请求记录' + tip('经本网关发给这个账号的请求，保留最近 7 天；换号前失败的那次也记在这里') + '</h3><span class="grow"></span>'
    + seg('data-rfail', [[0, '全部'], [1, '只看失败']], RFAILED ? 1 : 0) + '<button type="button" class="ibtn sm" data-rreload title="刷新" aria-label="刷新请求记录">' + ic('refresh') + '</button></div>';
  if(!d) return head + '<div style="margin-top:12px">' + spinner('正在读取…') + '</div>';
  if(d.ok === false) return head + '<div class="notice bad" style="margin-top:12px">读取失败：' + esc(d.error) + '</div>';
  const pages = Math.max(1, Math.ceil((d.total || 0) / (d.page_size || 20)));
  return head + '<p class="muted small" style="margin:8px 0 0">共 ' + nf(d.requests) + ' 次 · 成功 ' + nf(d.succeeded) + ' · 失败 <b class="' + (d.failed ? 'bad-t' : '') + '">' + nf(d.failed) + '</b></p>'
    + (d.items.length ? '<ul class="recs">' + d.items.map(recItem).join('') + '</ul>' : '<div class="empty-box" style="margin-top:12px;box-shadow:none">' + (RFAILED ? '没有失败的请求' : '最近 7 天还没有请求') + '</div>')
    + (pages > 1 ? '<div class="pager"><button type="button" class="btn ghost sm" data-rpage="' + (d.page - 1) + '"' + (d.page <= 1 ? ' disabled' : '') + '>上一页</button><span class="muted small">' + d.page + ' / ' + pages
      + '</span><button type="button" class="btn ghost sm" data-rpage="' + (d.page + 1) + '"' + (d.page >= pages ? ' disabled' : '') + '>下一页</button></div>' : '');
}
function dSign(a){
  const s = a.sign || {}, ns = nextSign(s), recs = (a.redeems || []).slice().reverse();
  const line = !s.at ? '还没查询签到状态' : s.signed ? (ns > NOW() ? dur(ns - NOW()) + '后可再签' : '已到可签时间') : '签到可领 Agent 余额';
  return '<div class="card2' + (s.signed ? ' done' : '') + '"><span class="stamp">' + ic(s.signed ? 'check' : 'cal') + '</span><div><b>' + (s.signed ? '今天已签到' : '今天还没签到') + '</b><div class="muted small">' + line + '</div>'
      + (s.granted ? '<div class="small">上次 <b class="ok-t">+' + esc(s.granted) + '</b>' + (s.expire_at ? ' · ' + fmtS(s.expire_at) + (s.expire_at <= NOW() ? ' 已过期' : ' 过期') : '') + '</div>' : '')
      + (s.error ? '<div class="small bad-t">上次失败：' + esc(s.error) + '</div>' : '') + '</div><button type="button" class="btn sm" data-act="sign">签到</button></div>'
    + '<p class="hint" style="margin-top:8px">' + (SET_CUR.auto_sign === false ? '自动签到已关闭，可在「设置」开启' : '自动签到已开启：到了可签时间自动签') + '</p>'
    + '<h3>兑换码</h3><div class="row"><label class="field grow"><input id="rdCode" class="mono" placeholder="输入兑换码" aria-label="兑换码"></label><button type="button" class="btn" data-act="redeem">兑换</button></div><div id="rdMsg" style="margin-top:8px"></div>'
    + (recs.length ? '<ul class="hist">' + recs.map(r => '<li>' + (r.ok ? chip('ok sm', '成功') : chip('bad sm', '失败')) + '<span class="mono">' + esc(r.code) + '</span><span class="muted small" style="min-width:0;overflow-wrap:anywhere">'
      + esc(r.message) + '</span><span class="when">' + fmtS(r.at) + '</span></li>').join('') + '</ul>' : '<p class="hint">还没有兑换记录</p>');
}
function dActivity(a){
  const f = a.free || {}, v = a.invite || {};
  let free;
  if(f.free_until > NOW()) free = '<b>已领取</b><div class="muted small">剩 ' + dur(f.free_until - NOW()) + '，' + fmtS(f.free_until) + ' 到期</div>' + models(a.free_models);
  else if(f.status === 'available') free = '<b>可以领取</b><div class="muted small">领取后免费 ' + dur(f.duration_seconds || 0) + '</div>' + models(f.model_ids);
  else if(f.status === 'claimed') free = '<b>已过期</b><div class="muted small">' + fmtS(f.free_until) + ' 到期</div>';
  else free = '<b>暂无活动</b><div class="muted small">' + (f.visible === false ? '活动对这个账号不可见' : f.status ? '状态：' + esc(f.status) : '还没查询') + '</div>';
  const acts = (a.activities || []).filter(x => x && x.name);
  return '<div class="card2' + (f.free_until > NOW() ? ' done' : '') + '"><span class="stamp">' + ic('gift') + '</span><div>' + free + '</div>' + (f.status === 'available' ? '<button type="button" class="btn sm" data-act="claim">领取</button>' : '') + '</div>'
    + (acts.length ? '<h3>已参加的活动</h3>' + kv(acts.map(x => [esc(x.name), ({active: chip('ok sm', '进行中'), ended: chip('sm', '已结束')}[x.status] || chip('sm', esc(x.status || '-')))
      + (x.free_until ? ' <span class="muted small">免费至 ' + fmtS(x.free_until) + '</span>' : '') + (num(x.used_quota) != null ? '<div class="muted small">已用 ' + nf(x.used_quota) + (num(x.remaining_quota) != null ? '，剩 ' + nf(x.remaining_quota) : '') + '</div>' : '')])) : '')
    + '<h3>邀请</h3>' + (v.invite_code ? kv([['邀请码', '<b class="mono">' + esc(v.invite_code) + '</b>' + copyBtn(v.invite_code)],
      ['邀请链接', v.invite_link ? '<span class="mono small">' + esc(v.invite_link) + '</span>' + copyBtn(v.invite_link) : '-'],
      ['邀请人数', (v.total_invites || 0) + ' 人（付费 ' + (v.paid_invites || 0) + '）'], ['邀请奖励', (v.total_rewards || 0) + '，可用 ' + (v.available_balance || 0)]]) : '<p class="hint">还没查询到邀请信息</p>');
}
const models = list => (list || []).length ? '<div class="m-lv" style="margin-top:6px">' + list.map(m => '<span class="lv">' + esc(m) + '</span>').join('') + '</div>' : '';
/* 上游使用明细（Codar 官方统计，含所有客户端）；统计页的「上游用量」也用这个 */
/* 输入、输出、缓存命中放在一起：输入 18,240 · 输出 812 · 缓存命中 15,100（82.8%） */
const tokenLine = (input, output, cached, f) => { f = f || nf; return '<span>输入 <b>' + f(input || 0) + '</b></span><span>输出 <b>' + f(output || 0) + '</b></span><span>缓存命中 <b>' + f(cached || 0) + '</b>'
  + (input ? '（' + pctS((cached || 0) / input) + '）' : '') + '</span>'; };
const kpi = (k, v, s, cls) => '<div class="kpi ' + (cls || '') + '"><div class="kpi-k">' + k + '</div><div class="kpi-v">' + v + '</div><div class="kpi-s">' + (s || '&nbsp;') + '</div></div>';
function usageModels(list){
  return '<div class="tbl"><table class="t"><thead><tr><th>模型</th><th class="num">调用</th><th>输入 · 输出 · 缓存命中</th><th class="num">花费</th></tr></thead><tbody>'
    + ((list || []).map(m => '<tr><td class="mono">' + esc(m.model) + '</td><td class="num">' + nf(m.calls) + '</td><td><div class="toks">' + tokenLine(m.input_tokens, m.output_tokens, m.cached_tokens, fmtTok)
      + '</div></td><td class="num">' + yuan(m.cost) + '</td></tr>').join('') || '<tr><td colspan="4" class="empty">这段时间没有调用</td></tr>') + '</tbody></table></div>';
}
function usageView(d, extra){
  const s = d.summary || {}, daily = d.daily || [], max = Math.max(1, ...daily.map(x => x.calls || 0)), every = daily.length > 16 ? Math.ceil(daily.length / 10) : 1;
  return '<div class="kpis sm usage3">' + kpi('调用', nf(s.calls || 0)) + kpi('输入 / 输出', fmtTok(s.input_tokens) + '<small>/</small> ' + fmtTok(s.output_tokens), '缓存命中 ' + fmtTok(s.cached_tokens) + '（' + pctS(s.cache_hit_rate) + '）', 'wide')
    + kpi('花费', yuan(s.cost), (s.unpriced || []).length ? s.unpriced.length + ' 个模型无价格' : '') + '</div>'
    + '<div class="chart" role="img" aria-label="每日调用次数">' + daily.map((x, i) => '<div class="col" title="' + esc(x.date) + '：' + nf(x.calls) + ' 次，输入 ' + fmtTok(x.input_tokens)
      + '，输出 ' + fmtTok(x.output_tokens) + '，缓存 ' + fmtTok(x.cached_tokens) + (x.cost != null ? '，约 ' + yuan(x.cost) : '') + '"><i style="height:' + Math.max(x.calls ? 3 : 0, (x.calls || 0) / max * 100) + '%"></i>'
      + '<span>' + (i % every === 0 || i === daily.length - 1 ? esc(String(x.date).slice(5)) : '') + '</span></div>').join('') + '</div>'
    + (extra === undefined ? usageModels(d.models) : extra);
}
const TYPE_NAME = {agent: 'Agent', general: '通用'};
function ledgerView(d){
  const items = d.items || [], pages = Math.max(1, Math.ceil((d.total || 0) / (d.page_size || 20)));
  return '<ul class="ledger">' + (items.map(x => '<li><b class="' + (x.change < 0 ? 'bad-t' : 'ok-t') + '">' + (x.change > 0 ? '+' : '') + nf(x.change) + '</b><div>'
      + (x.model ? '<span class="mono">' + esc(x.model) + '</span> ' : esc(x.note) + ' ') + chip('sm', esc(TYPE_NAME[x.type] || x.type || '-'))
      + (x.model ? '<div class="rec-b" style="margin-top:0">' + tokenLine(x.input, x.output, x.cached) + '</div>' : '')
      + (x.expire_at ? '<div class="muted">' + fmtS(x.expire_at) + ' 过期</div>' : '') + '</div><span class="when">' + fmtS(x.at) + '</span></li>').join('') || '<li class="muted">没有记录</li>') + '</ul>'
    + (pages > 1 ? '<div class="pager"><button type="button" class="btn ghost sm" data-lpage="' + (d.page - 1) + '"' + (d.page <= 1 ? ' disabled' : '') + '>上一页</button><span class="muted small">' + d.page + ' / ' + pages
      + '</span><button type="button" class="btn ghost sm" data-lpage="' + (d.page + 1) + '"' + (d.page >= pages ? ' disabled' : '') + '>下一页</button></div>' : '');
}
/* 用量数据按账号缓存，列表定时刷新时不重新请求上游 */
const UCACHE = {}, LCACHE = {};
let UDAYS = 7;
const ymd = d => d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
const rangeQS = days => 'start=' + ymd(new Date(Date.now() - (days - 1) * 86400000)) + '&end=' + ymd(new Date());
function fetchUsage(uid, force){
  const c = UCACHE[uid];
  if(c && c.days === UDAYS && !force && (c.data || c.loading)) return;
  const days = UDAYS; UCACHE[uid] = {days, loading: true, data: c && c.days === days ? c.data : null};
  api('/accounts/usage?uid=' + encodeURIComponent(uid) + '&' + rangeQS(days)).then(d => { UCACHE[uid] = {days, data: d}; }, err => { UCACHE[uid] = {days, data: {ok: false, error: err.message}}; }).then(() => redrawTab('usage'));
}
function fetchLedger(uid, page, force){
  const c = LCACHE[uid];
  if(c && c.page === page && !force && (c.data || c.loading)) return;
  LCACHE[uid] = {page, loading: true, data: c ? c.data : null};
  api('/accounts/ledger?uid=' + encodeURIComponent(uid) + '&page=' + page + '&page_size=20').then(d => { LCACHE[uid] = {page, data: d}; }, err => { LCACHE[uid] = {page, data: {ok: false, error: err.message}}; }).then(() => redrawTab('usage'));
}
function dUsage(a){
  fetchUsage(a.uid); fetchLedger(a.uid, (LCACHE[a.uid] || {}).page || 1);
  const u = UCACHE[a.uid] || {}, l = LCACHE[a.uid] || {}, fail = d => '<div class="notice bad">查询失败：' + esc(d.error) + '</div>';
  return '<div class="row"><h3 style="margin:0">使用明细' + tip('Codar 官方统计，含这个账号在所有客户端的调用，免费模型也计入') + '</h3><span class="grow"></span>'
      + seg('data-udays', [[7, '7 天'], [14, '14 天'], [30, '30 天']], UDAYS) + '<button type="button" class="ibtn sm" data-ureload title="刷新" aria-label="刷新用量">' + ic('refresh') + '</button></div><div style="margin-top:12px">'
    + (u.data ? (u.data.ok === false ? fail(u.data) : usageView(u.data)) : spinner()) + '</div>'
    + '<h3>额度明细' + tip('每笔积分收支来自上游账本；Agent 余额扣完接着扣通用余额，同一请求可能拆成两笔；免费模型不记账') + '</h3>'
    + (l.data ? (l.data.ok === false ? fail(l.data) : ledgerView(l.data)) : spinner());
}
function dProxy(a){
  const value = PX.edit && PX.edit.uid === a.uid ? PX.edit.value : ((a.proxy || {}).ids || [])[0] || 0, err = PX.error || (a.proxy || {}).error;
  return (exitBlocked(a) ? ipNotice(a) : '') + '<h3>出口</h3><p class="hint">对话、签到、刷新和令牌续期都走这里。指定代理不可用时直接报错，不会改走直连。</p>'
    + '<div class="row" style="margin-top:10px;flex-wrap:nowrap"><label class="field grow"><select id="accountProxy" aria-label="这个账号的出口"' + (!PX.loaded ? ' disabled' : '') + '>' + proxyOptions(value) + '</select></label>'
    + '<button type="button" class="ibtn" data-proxy-action="reload" title="刷新代理池" aria-label="刷新代理池">' + ic('refresh') + '</button></div>'
    + (!PX.loaded ? '<p class="hint">代理池还没读取，刷新后再保存</p>' : !PX.proxies.length ? '<p class="hint">代理池里还没有代理，可以在「代理池」页添加</p>' : '')
    + (err ? '<p class="small bad-t">' + esc(err) + '</p>' : '') + '<button type="button" class="btn" data-proxy-action="save" style="margin-top:12px"' + (!PX.loaded ? ' disabled' : '') + '>保存</button>';
}
const DRAW = {overview: dOverview, requests: dRequests, sign: dSign, activity: dActivity, usage: dUsage, proxy: dProxy};
function drawDrawer(a){
  $('#drwTitle').textContent = a.username || a.uid;
  $('#drwState').innerHTML = stateCell(a);
  $('#drwSw').innerHTML = accountSwitch(a);
  document.querySelectorAll('#drawer [data-dtab]').forEach(b => b.setAttribute('aria-selected', String(b.dataset.dtab === DTAB)));
  /* 正在抽屉里输入（备注 / 兑换码 / 代理）时，定时刷新不重画内容，免得吞掉输入 */
  const el = document.activeElement, typing = $('#drawer').contains(el) && /INPUT|TEXTAREA|SELECT/.test(el.tagName);
  if(!typing) $('#drwBody').innerHTML = DRAW[DTAB](a);
  $('#drwFoot').innerHTML = '<button type="button" class="btn soft sm" data-act="call"' + (a.restricted ? ' disabled title="请先解除受限"' : '') + '>' + ic('chat') + '调用</button>'
    + '<button type="button" class="btn soft sm" data-act="test"' + (a.restricted ? ' disabled title="请先解除受限"' : '') + '>' + ic('play') + '测试</button>'
    + '<button type="button" class="btn soft sm" data-act="refresh">' + ic('refresh') + '刷新</button>'
    + (a.restricted ? '<button type="button" class="btn soft sm" data-act="unrestrict">解除受限</button>' : '')
    + (cooling(a) ? '<button type="button" class="btn soft sm" data-act="reset">取消冷却</button>' : a.last_error ? '<button type="button" class="btn soft sm" data-act="reset">清除错误</button>' : '')
    + '<span class="grow"></span><button type="button" class="btn danger sm" data-act="delete">' + ic('trash') + '删除</button>';
}
function redrawTab(tab){ if(SEL && DTAB === tab){ const a = ROWS.find(x => x.uid === SEL); if(a) drawDrawer(a); } }
let DRAWER_FOCUS = null;
function openDrawer(uid, tab){
  const a = ROWS.find(x => x.uid === uid); if(!a) return;
  if(SEL !== uid) DTAB = tab || 'overview';
  if(!SEL) DRAWER_FOCUS = document.activeElement;
  SEL = uid; drawDrawer(a); draw();
  $('#scrim').hidden = false; $('#drawer').hidden = false; $('#drawer [data-close]').focus();
}
function closeDrawer(){
  const uid = SEL; SEL = null; $('#scrim').hidden = true; $('#drawer').hidden = true; draw();
  const back = DRAWER_FOCUS && DRAWER_FOCUS.isConnected ? DRAWER_FOCUS : uid && document.querySelector('#accBody tr[data-uid="' + CSS.escape(uid) + '"] .nm');
  if(back) back.focus();
}
$('#scrim').onclick = closeDrawer;
/* ---- 列表和抽屉里的账号操作 ---- */
async function act(name, a){
  const uid = a.uid, who = a.username || uid;
  if(name === 'relogin'){ if(SEL) closeDrawer(); return openAdd(); }
  if(name === 'call') return openCall(a);
  if(name === 'refresh'){ const x = (await api('/accounts/refresh', {uid})).results[0]; toast(x.ok ? '已刷新 ' + who : '刷新失败：' + x.error, !x.ok); }
  if(name === 'test'){ const r = await api('/accounts/test', {uid}); toast(r.ok ? '测试通过（' + r.model + '，' + r.elapsed_ms + ' ms）：' + r.reply : '测试失败：' + r.error, !r.ok); delete RCACHE[uid]; }
  if(name === 'claim'){ const r = await api('/accounts/claim', {uid}); toast(r.ok ? '领取成功，免费至 ' + fmt((r.free || {}).free_until) : '领取失败：' + r.error, !r.ok); }
  if(name === 'renew'){ const r = await api('/accounts/renew', {uid}); toast(r.ok ? '令牌已续期，新令牌 ' + fmtS(r.token_exp) + ' 到期' : '续期失败：' + r.error, !r.ok); }
  if(name === 'sign'){ const r = await api('/accounts/sign', {uid}); toast(r.ok ? '签到成功：+' + r.granted + '，' + fmtS(r.expire_at) + ' 过期' : '签到没成功：' + r.error, !r.ok); }
  if(name === 'redeem'){
    const code = ($('#rdCode') || {}).value.trim(); if(!code) return toast('请输入兑换码', true);
    const r = await api('/accounts/redeem', {uid, code});
    await load();
    const m = $('#rdMsg');
    if(m) m.innerHTML = r.ok ? '<div class="notice ok"><b>兑换成功</b>：' + esc(r.message) + (r.account && r.account.balance ? '，Agent 余额现在是 ' + esc(r.account.balance.agent_token) : '') + '</div>'
      : '<div class="notice bad"><b>兑换失败</b>：' + esc(r.error) + '</div>';
    if(r.ok && $('#rdCode')) $('#rdCode').value = '';
    return;
  }
  if(name === 'toggle'){ await api('/accounts/update', {uid, enabled: !a.enabled}); toast(a.enabled ? '已关闭 ' + who + '：不再派发请求，也不参与批量和后台任务' : '已启用 ' + who); }
  if(name === 'unblock'){ await api('/proxy-pool/manage', {action: 'unblock', id: exitID(a)}); toast('已解除 ' + exitName(a) + ' 的封控，用这个出口的账号恢复调度'); }
  if(name === 'unrestrict'){ const r = await api('/accounts/batch', {action: 'clear_restriction', uids: [uid]}); toast(r.updated ? '已解除受限：' + who : '这个账号已经没有受限标记'); }
  if(name === 'reset'){ const was = cooling(a); await api('/accounts/cooldown/clear', {uid});
    toast(a.restricted ? '已清除冷却，账号仍有受限标记' : was ? '已恢复 ' + who + '，立即参与派发' : '已清除错误'); }
  if(name === 'delete'){ if(!confirm('删除账号 ' + who + '？令牌会一起删除。')) return;
    await api('/accounts/delete', {uid}); toast('已删除 ' + who); SELECTED_ACCOUNTS.delete(uid); if(SEL === uid) closeDrawer(); }
  await load();
}
async function onAct(e, uid){
  const btn = e.target.closest('[data-act]'); if(!btn) return false; if(btn.disabled) return true;
  const a = ROWS.find(x => x.uid === uid); if(!a) return true;
  btn.disabled = true;
  try { await act(btn.dataset.act, a); } catch(err){ toast(err.message, true); }
  btn.disabled = false;
  return true;
}
$('#drawer').addEventListener('click', async e => {
  if(e.target.closest('[data-close]')) return closeDrawer();
  const cp = e.target.closest('[data-copy]');
  if(cp){ (await copyText(cp.dataset.copy)) ? toast('已复制') : toast(cp.dataset.copy); return; }
  const t = e.target.closest('[data-dtab]');
  if(t){ DTAB = t.dataset.dtab; const a = ROWS.find(x => x.uid === SEL); if(!a) return; t.focus();
    if(DTAB === 'requests') fetchRequests(SEL, (RCACHE[SEL] || {}).page || 1, true);
    drawDrawer(a);
    if(DTAB === 'proxy') loadProxyPool().then(() => drawDrawer(a), err => { drawDrawer(a); toast(err.message, true); });
    return; }
  const rf = e.target.closest('[data-rfail]'); if(rf){ RFAILED = rf.dataset.rfail === '1'; fetchRequests(SEL, 1, true); return redrawTab('requests'); }
  if(e.target.closest('[data-rreload]')){ fetchRequests(SEL, (RCACHE[SEL] || {}).page || 1, true); return redrawTab('requests'); }
  const rp = e.target.closest('[data-rpage]'); if(rp){ fetchRequests(SEL, +rp.dataset.rpage); return redrawTab('requests'); }
  const ud = e.target.closest('[data-udays]'); if(ud){ UDAYS = +ud.dataset.udays; fetchUsage(SEL); return redrawTab('usage'); }
  if(e.target.closest('[data-ureload]')){ fetchUsage(SEL, true); fetchLedger(SEL, (LCACHE[SEL] || {}).page || 1, true); return redrawTab('usage'); }
  const lp = e.target.closest('[data-lpage]'); if(lp){ fetchLedger(SEL, +lp.dataset.lpage); return redrawTab('usage'); }
  if(SEL) onAct(e, SEL);
});
$('#drawer').addEventListener('change', async e => {
  if(e.target.id === 'accountProxy'){ PX.edit = {uid: SEL, value: Number(e.target.value)}; return; }
  const inp = e.target.closest('[data-note]'); if(!inp || !SEL) return;
  try { await api('/accounts/update', {uid: SEL, note: inp.value}); inp.blur(); toast('备注已保存'); load().catch(() => {}); } catch(err){ toast(err.message, true); }
});
$('#drawer').addEventListener('keydown', e => {
  if(e.key === 'Enter' && e.target.id === 'rdCode'){ e.preventDefault(); $('#drawer [data-act="redeem"]').click(); }
  if(e.key === 'Enter' && e.target.hasAttribute('data-note')) e.target.blur();
});
/* 代理选择：抽屉里单个账号保存，或弹窗里应用到全部账号 */
document.addEventListener('click', async e => {
  const b = e.target.closest('[data-proxy-action]'); if(!b || b.disabled) return;
  const action = b.dataset.proxyAction;
  b.disabled = true;
  try {
    if(action === 'reload'){ await loadProxyPool(); redrawTab('proxy'); toast('已刷新代理池'); }
    else {
      const all = action === 'save-all', r = await api('/accounts/proxy', {all, uid: SEL, proxy_id: Number($(all ? '#allProxy' : '#accountProxy').value)});
      PX.edit = null;
      if(all) closeModal('#proxyModal');
      toast('已更新 ' + r.updated + ' 个账号的出口'); await load();
    }
  } catch(err){ toast(err.message, true); }
  finally { b.disabled = false; }
});
