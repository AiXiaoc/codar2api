'use strict';
/* 添加账号弹窗：微信扫码 / 手机号 / 粘贴令牌 / 本机登录 */
let QR_GEN = 0, ADD_WAY = 'wechat';
const WAYS = [['wechat', '微信扫码', 'chat'], ['phone', '手机号', 'phone'], ['token', '粘贴令牌', 'paste'], ['local', '本机登录', 'pc']];
const addedText = r => (r.updated ? '已更新账号 ' : '已添加账号 ') + r.account.username + (r.claimed ? '，已领取免费模型' : '') + (r.signed ? '，已签到' : '');
function addStep(way){
  QR_GEN++;                                       // 离开扫码页就停止轮询
  ADD_WAY = way;
  const note = ($('#addNote') || {}).value || '';
  $('#addSeg').innerHTML = WAYS.map(w => '<button type="button" role="tab" data-way="' + w[0] + '" aria-selected="' + (w[0] === way) + '">' + ic(w[2]) + '<span>' + esc(w[1]) + '</span>'
    + (w[0] === 'wechat' ? '<em>推荐</em>' : '') + '</button>').join('');
  const body = $('#addBody'), noteRow = btn => '<div class="row" style="flex-wrap:nowrap"><label class="field grow"><input id="addNote" placeholder="备注（可选）" aria-label="备注" maxlength="200" value="' + esc(note) + '"></label>' + btn + '</div>';
  if(way === 'wechat'){
    body.innerHTML = '<button type="button" class="qr-box loading" id="qrWrap" title="点击刷新二维码" aria-label="刷新二维码"><span class="qrimg"><span class="qrph">正在获取二维码…</span></span>'
      + '<span class="re">' + ic('refresh') + '刷新二维码</span>' + '</button><div id="qrMsg"></div>' + noteRow('')
      + '<p class="hint center">没绑手机号或需要新 IP 验证时，会转到手机号页完成验证</p>';
    qrStart();
  }
  if(way === 'token'){
    body.innerHTML = '<textarea id="tokenIn" class="in" placeholder="粘贴 Codar 请求里 Authorization: Bearer 后面的令牌（eyJ…），多个每行一个" aria-label="令牌"></textarea>'
      + noteRow('<button type="button" class="btn" id="addBtn">导入</button>') + '<div id="tokMsg"></div>'
      + '<p class="hint">网关和 Codar 客户端在同一台机器上时，可以直接<a href="#" data-way="local">读取本机登录</a>。</p>';
    $('#tokenIn').addEventListener('input', countTokens);
    setTimeout(() => $('#tokenIn').focus(), 30);
  }
  if(way === 'phone'){
    /* PH.ctx：微信扫码转过来的绑定手机号 / 新 IP 验证；没有就是普通手机号登录 */
    const ctx = PH.ctx; PH.ctx = null;
    PH.mode = ctx ? ctx.mode : 'login'; PH.flow = ctx ? ctx.flow : ''; PH.needPhone = !!ctx; PH.until = ctx ? 0 : PH.until;
    const lead = !ctx ? '' : '<div class="notice info"><b>' + (ctx.mode === 'bind' ? '这个微信账号还没绑定手机号' : '需要新 IP 手机验证') + '</b>（' + esc(ctx.username) + '）：'
      + (ctx.mode === 'bind' ? '填写要绑定的手机号，获取验证码后点「绑定并登录」。' : '填写账号绑定的手机号' + (ctx.tail ? '（尾号 ' + esc(ctx.tail) + '）' : '') + '，获取验证码后点「验证并登录」。') + '</div>';
    body.innerHTML = lead + '<label class="field">' + ic('phone') + '<input id="phNum" inputmode="numeric" maxlength="11" autocomplete="tel" placeholder="11 位手机号" aria-label="手机号" value="' + esc(ctx ? '' : PH.phone) + '"></label>'
      + '<div class="row" style="flex-wrap:nowrap"><label class="field grow"><input id="phCode" inputmode="numeric" maxlength="8" autocomplete="one-time-code" placeholder="短信验证码" aria-label="短信验证码"></label>'
      + '<button type="button" class="btn ghost" id="phSend" style="min-width:118px">获取验证码</button></div><div id="phMsg"></div>'
      + noteRow('<button type="button" class="btn" id="phLogin">' + (PH.mode === 'bind' ? '绑定并登录' : PH.mode === 'ip' ? '验证并登录' : '登录') + '</button>')
      + '<p class="hint">获取验证码前要手动完成一次人机验证。登录请求从网关服务器发出，上游要求新 IP 验证时会再发一条验证码到同一手机号。</p>';
    phTick();
    setTimeout(() => (PH.phone && !ctx ? $('#phCode') : $('#phNum')).focus(), 30);
  }
  if(way === 'local'){
    body.innerHTML = '<div id="locBox">' + spinner('正在读取…') + '</div>' + noteRow('<button type="button" class="btn ghost" id="locReload">重新读取</button>')
      + '<p class="hint">读取网关所在机器的 <span class="mono">~/.codar/config.json</span>（可用 CODAR_CONFIG 指定），令牌只在网关内使用，不发给浏览器。</p>';
    loadLocal();
  }
}
async function loadLocal(){
  const box = $('#locBox'); if(!box) return;
  box.innerHTML = spinner('正在读取…');
  let d;
  try { d = await api('/local-login'); } catch(err){ d = {ok: false, error: err.message, logins: []}; }
  if(ADD_WAY !== 'local' || !$('#locBox')) return;
  box.innerHTML = d.logins.length ? d.logins.map(x => '<div class="loc">' + ic('pc') + '<div><b>' + esc(x.username || '(未知用户名)') + '</b> '
      + (x.in_pool ? chip(x.same_token ? 'sm' : 'info sm', x.same_token ? '已在账号池' : '已在账号池，令牌不同') : '')
      + '<div class="muted small">' + (x.login_at ? ago(x.login_at) + '登录 · ' : '') + (x.token_exp ? (x.expired ? '<span class="bad-t">令牌已过期</span>' : '令牌剩 ' + dur(x.token_exp - NOW())) : '') + '</div></div>'
      + '<button type="button" class="btn sm' + (x.expired || x.same_token ? ' ghost' : '') + '" data-local="' + esc(x.profile) + '"' + (x.expired ? ' disabled' : '') + '>'
      + (x.same_token ? '重新导入' : x.in_pool ? '更新令牌' : '导入') + '</button></div>').join('')
    : '<div class="notice ' + (d.ok ? 'info' : 'bad') + '">' + esc(d.error) + '</div>';
}
async function importLocal(btn){
  btn.disabled = true;
  try {
    const r = await api('/local-login/import', {profile: btn.dataset.local, note: ($('#addNote') || {}).value.trim()});
    if(!r.ok) throw new Error(r.error);
    toast(addedText(r)); closeAdd(); await load(); openDrawer(r.account.uid);
  } catch(err){
    $('#locBox').insertAdjacentHTML('beforeend', '<div class="notice bad" style="margin-top:10px"><b>导入失败</b>：' + esc(err.message) + '</div>');
    btn.disabled = false;
  }
}
/* ---- 手机号登录：人机验证在浏览器里由人完成（官方组件），结果交给网关去发短信 ---- */
const PH = {phone: '', mode: 'login', flow: '', needPhone: false, ctx: null, until: 0, cap: null, capReady: null, pending: null};
function phNote(cls, html){ const m = $('#phMsg'); if(m) m.innerHTML = html ? '<div class="notice ' + cls + '">' + html + '</div>' : ''; }
function phTick(){
  const b = $('#phSend'); if(!b) return;
  const left = Math.ceil((PH.until - Date.now()) / 1000);
  if(left > 0){ b.disabled = true; b.textContent = left + ' 秒后重发'; setTimeout(phTick, 1000); }
  else { b.disabled = false; b.textContent = PH.mode === 'ip' && !PH.needPhone ? '获取安全验证码' : '获取验证码'; }
}
function loadScript(src){
  return new Promise((resolve, reject) => {
    if(window.initAlicom4) return resolve();
    const el = document.createElement('script');
    el.src = src; el.async = true;
    el.onload = () => resolve();
    el.onerror = () => reject(new Error('人机验证组件加载失败（' + src + '）'));
    document.head.appendChild(el);
  });
}
/* 官方验证码组件只初始化一次（product=bind：平时不占位置，showCaptcha 时弹出）；每次发短信前人工过一次 */
function captchaReady(){
  if(PH.capReady) return PH.capReady;
  const settle = (fn, v) => { const p = PH.pending; PH.pending = null; if(p) p[fn](v); };
  const failMsg = err => new Error('人机验证出错：' + ((err && (err.msg || err.code)) || '未知错误'));
  PH.capReady = api('/phone/captcha').then(cfg => loadScript(cfg.script).then(() => new Promise((resolve, reject) => {
    if(typeof window.initAlicom4 !== 'function') return reject(new Error('人机验证组件不可用'));
    const timer = setTimeout(() => reject(new Error('人机验证初始化超时')), 35000);
    window.initAlicom4({captchaId: cfg.captcha_id, product: cfg.product || 'bind', language: 'zh', protocol: 'https://', https: true, onError: err => settle('reject', failMsg(err))}, obj => {
      obj.onReady(() => { clearTimeout(timer); PH.cap = obj; resolve(obj); })
        .onSuccess(() => { const r = obj.getValidate(); r ? settle('resolve', r) : settle('reject', new Error('人机验证结果为空，请重试')); })
        .onClose(() => settle('reject', new Error('已取消人机验证')))
        .onError(err => settle('reject', failMsg(err)));
    });
  }))).catch(err => { PH.capReady = null; throw err; });
  return PH.capReady;
}
async function solveCaptcha(){
  if(PH.pending) throw new Error('已有人机验证正在进行，请先完成或关闭它');
  const obj = await captchaReady();
  return new Promise((resolve, reject) => {
    PH.pending = {resolve, reject};
    try { obj.reset && obj.reset(); } catch(e) {}
    obj.showCaptcha();
  });
}
/* 普通登录、微信转过来的绑定 / 新 IP 验证要填手机号；手机号登录后的新 IP 验证用原号 */
const phNeedPhone = () => PH.mode === 'login' || PH.needPhone;
async function phSend(btn){
  const phone = phNeedPhone() ? $('#phNum').value.trim() : '';
  if(phNeedPhone() && !/^1[3-9]\d{9}$/.test(phone)) return phNote('bad', '请填写正确的 11 位手机号');
  PH.phone = phone || PH.phone;
  btn.disabled = true; btn.textContent = '人机验证中…';
  phNote('info', '请在弹出的窗口里完成人机验证');
  try {
    const captcha = await solveCaptcha();
    btn.textContent = '发送中…';
    const r = await api('/phone/code', PH.flow ? {flow: PH.flow, captcha, phone} : {phone, captcha});
    if(!r.ok) throw new Error(r.error);
    PH.until = Date.now() + 60000;
    phNote('ok', esc(r.message || '验证码已发送') + '（' + esc(r.phone) + '）');
    setTimeout(() => $('#phCode') && $('#phCode').focus(), 30);
  } catch(err){ phNote('bad', esc(err.message)); }
  phTick();
}
async function phLogin(btn){
  const code = $('#phCode').value.trim();
  if(phNeedPhone() && !/^1[3-9]\d{9}$/.test($('#phNum').value.trim())) return phNote('bad', '请填写正确的 11 位手机号');
  if(!/^\d{4,8}$/.test(code)) return phNote('bad', '请填写短信验证码');
  const note = ($('#addNote') || {}).value.trim();
  btn.disabled = true;
  try {
    const r = PH.mode === 'bind' ? await api('/phone/bind', {flow: PH.flow, code, note})
      : PH.mode === 'ip' ? await api('/phone/verify-ip', {flow: PH.flow, code, note})
      : await api('/phone/login', {phone: $('#phNum').value.trim(), code, note});
    if(!r.ok) throw new Error(r.error);
    if(r.state === 'ip_verify'){
      PH.mode = 'ip'; PH.flow = r.flow; PH.until = 0; PH.needPhone = false;
      $('#phNum').disabled = true; $('#phCode').value = ''; btn.textContent = '验证并登录';
      phNote('info', '<b>需要安全验证</b>：' + esc(r.message) + '。点「获取安全验证码」，验证码会发到 ' + esc(r.phone) + '，填好后点「验证并登录」。');
      phTick(); btn.disabled = false; return;
    }
    toast(addedText(r)); PH.phone = ''; PH.until = 0;
    closeAdd(); await load(); openDrawer(r.account.uid);
  } catch(err){ phNote('bad', '<b>登录失败</b>：' + esc(err.message)); btn.disabled = false; }
}
function openAdd(way){ openModal('#addModal'); addStep(typeof way === 'string' ? way : ADD_WAY); setTimeout(() => $('#addSeg [aria-selected="true"]').focus(), 40); }
function closeAdd(){ QR_GEN++; closeModal('#addModal'); }
MODAL_CLOSE['#addModal'] = closeAdd;
function qrNote(cls, html){ const m = $('#qrMsg'); if(m) m.innerHTML = html ? '<div class="notice ' + cls + '">' + html + '</div>' : ''; }
function qrDone(mark){
  const w = $('#qrWrap'); if(!w) return;
  w.classList.add('done');
  if(!w.querySelector('.qrmark')) w.insertAdjacentHTML('beforeend', '<span class="qrmark" aria-hidden="true">' + mark + '</span>');
}
async function qrPoll(gen, scene, until){
  if(gen !== QR_GEN) return;
  let r;
  try { r = await api('/wechat/status', {scene, note: ($('#addNote') || {}).value || ''}); } catch(err){ r = {state: 'pending', warn: err.message}; }
  if(gen !== QR_GEN) return;
  if(r.state === 'success'){
    const a = r.account || {};
    qrNote('ok', '<b>登录成功</b>：' + esc(addedText(r)) + '，令牌 ' + fmtS(a.token_exp) + ' 到期。<div style="margin-top:6px"><button type="button" class="btn sm" data-view="' + esc(a.uid) + '">查看账号</button></div>');
    qrDone('✅'); return load();
  }
  if(r.state === 'bind_phone' || r.state === 'ip_verify'){
    /* 扫码成功但没绑手机号 / 新 IP：转到手机号页，填手机号和短信验证码后自动入池 */
    qrDone('📱');
    PH.ctx = {mode: r.state === 'bind_phone' ? 'bind' : 'ip', flow: r.flow, username: r.username, tail: r.tail || ''};
    return addStep('phone');
  }
  if(r.state === 'failed'){ qrNote('bad', '<b>登录没完成</b>' + (r.username ? '（' + esc(r.username) + '）' : '') + '：' + esc(r.reason)); return qrDone('⚠️'); }
  const left = Math.max(0, Math.round((until - Date.now()) / 1000));
  qrNote('info', '请用微信扫码，并在手机上确认 <span class="muted">· ' + Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0') + ' 后过期</span>'
    + (r.warn ? '<div class="bad-t small">' + esc(r.warn) + '</div>' : ''));
  setTimeout(() => qrPoll(gen, scene, until), 2000);
}
async function qrStart(){
  const gen = ++QR_GEN, w = $('#qrWrap'), img = w.querySelector('.qrimg');
  w.classList.remove('done'); w.classList.add('loading'); (w.querySelector('.qrmark') || {remove(){}}).remove();
  img.innerHTML = '<span class="qrph">正在获取二维码…</span>'; qrNote('', '');
  try {
    const r = await api('/wechat/qrcode', {note: ($('#addNote') || {}).value || ''});
    if(!r.ok) throw new Error(r.error);
    if(gen !== QR_GEN) return;
    w.classList.remove('loading');
    img.innerHTML = '<img alt="微信登录二维码" referrerpolicy="no-referrer" src="' + esc(r.qr_url) + '">';
    qrPoll(gen, r.scene, Date.now() + (r.expires_in || 600) * 1000);
  } catch(err){
    if(gen !== QR_GEN) return;
    w.classList.remove('loading'); img.innerHTML = '<span class="qrph">获取失败，点击重试</span>'; qrNote('bad', esc(err.message));
  }
}
/* 粘贴令牌：一个就直接导入并打开详情；多个就批量导入，结果列成表 */
const JWT_RE = /eyJ[\w-]+\.[\w-]+\.[\w-]+/g;
function countTokens(){ const n = new Set(($('#tokenIn').value.match(JWT_RE) || [])).size; $('#addBtn').textContent = n > 1 ? '批量导入 ' + n + ' 个' : '导入'; }
async function importBatch(btn, text, note){
  btn.disabled = true; btn.textContent = '导入中…';
  $('#tokMsg').innerHTML = spinner('正在逐个校验并导入…');
  try {
    const r = await api('/accounts/import-batch', {text, note});
    if(!r.ok) throw new Error(r.error);
    const line = x => '<tr><td class="num">' + x.index + '</td><td class="mono">' + esc(x.token_masked) + '</td><td>' + (x.ok ? '<b>' + esc(x.username) + '</b>' : '<span class="muted">-</span>') + '</td><td>'
      + (x.ok ? chip(x.updated ? 'info sm' : 'ok sm', x.updated ? '已更新令牌' : '新增') : chip('bad sm', '失败') + ' <span class="bad-t small">' + esc(x.error) + '</span>') + '</td></tr>';
    $('#tokMsg').innerHTML = '<div class="notice ' + (r.failed ? 'info' : 'ok') + '"><b>导入完成</b>：共 ' + r.total + ' 个，新增 ' + r.added + '，更新 ' + r.updated + '，失败 ' + r.failed + '</div>'
      + '<div class="tbl" style="max-height:240px;margin-top:8px"><table class="t"><thead><tr><th class="num">#</th><th>令牌</th><th>账号</th><th>结果</th></tr></thead><tbody>' + r.results.map(line).join('') + '</tbody></table></div>';
    if(!r.failed) $('#tokenIn').value = '';
    load().catch(() => {});
  } catch(err){ $('#tokMsg').innerHTML = '<div class="notice bad"><b>批量导入失败</b>：' + esc(err.message) + '</div>'; }
  btn.disabled = false; countTokens();
}
async function importToken(btn){
  const token = $('#tokenIn').value.trim(); if(!token) return toast('请先粘贴令牌', true);
  if(new Set(token.match(JWT_RE) || []).size > 1) return importBatch(btn, token, ($('#addNote') || {}).value.trim());
  btn.disabled = true;
  try {
    const r = await api('/accounts/add', {token, note: ($('#addNote') || {}).value.trim()});
    toast(addedText(r)); closeAdd(); await load(); openDrawer(r.account.uid);
  } catch(err){ $('#tokMsg').innerHTML = '<div class="notice bad"><b>导入失败</b>：' + esc(err.message) + '</div>'; btn.disabled = false; }
}
$('#addOpen').onclick = openAdd;
$('#addModal').addEventListener('click', e => {
  const w = e.target.closest('[data-way]'); if(w){ e.preventDefault(); if(w.dataset.way !== ADD_WAY) addStep(w.dataset.way); return; }
  if(e.target.closest('#qrWrap')) return qrStart();
  if(e.target.closest('#addBtn')) return importToken(e.target.closest('#addBtn'));
  if(e.target.closest('#locReload')) return loadLocal();
  const lb = e.target.closest('[data-local]'); if(lb) return importLocal(lb);
  if(e.target.closest('#phSend')) return phSend(e.target.closest('#phSend'));
  if(e.target.closest('#phLogin')) return phLogin(e.target.closest('#phLogin'));
  const v = e.target.closest('[data-view]'); if(v){ closeAdd(); openDrawer(v.dataset.view); }
});
/* 添加方式支持左右方向键切换 */
$('#addSeg').addEventListener('keydown', e => {
  if(e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
  const i = WAYS.findIndex(w => w[0] === ADD_WAY), n = WAYS[(i + (e.key === 'ArrowRight' ? 1 : WAYS.length - 1)) % WAYS.length][0];
  addStep(n); $('#addSeg [data-way="' + n + '"]').focus();
});
