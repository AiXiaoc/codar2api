'use strict';
/* API Key、代理池、管理员密码与退出登录 */
let MANAGED_KEYS = [], MANAGED_PROXIES = [];
function managementError(err){ toast(err.message, true); }
async function loadKeys(){
  const d = await api('/keys'); MANAGED_KEYS = d.keys;
  $('#apiBase').textContent = location.origin + '/v1';
  $('#keysBody').innerHTML = d.keys.map(k => {
    const expired = k.expires_at && k.expires_at <= NOW(), scope = k.models.length ? k.models.slice(0, 2).join('、') + (k.models.length > 2 ? ' 等 ' + k.models.length + ' 个' : '') : '全部模型';
    return '<article class="key' + (k.enabled ? '' : ' is-off') + '"><span class="l-ico">' + ic('key') + '</span><div class="l-main"><div class="nm"><b>' + esc(k.name) + '</b>'
      + (!k.enabled ? chip('sm', '已停用') : expired ? chip('bad sm', '已过期') : '') + '</div><div class="mono">' + esc(k.prefix) + '…</div></div>'
      + '<div class="l-meta"><div title="' + esc(k.models.join('\n')) + '">' + esc(scope) + '</div><div>' + (!k.expires_at ? '长期有效' : expired ? fmtS(k.expires_at) + ' 已到期' : fmtS(k.expires_at) + ' 到期') + '</div></div>'
      + '<div class="l-use"><b>' + nf(k.requests || 0) + '</b> 次 · ' + fmtTok(k.tokens || 0) + ' token<div class="muted">' + (k.last_used ? ago(k.last_used) + '使用' : '还没用过') + '</div></div>'
      + sw(k.enabled, '启用 Key ' + k.name, 'data-key-action="toggle" data-id="' + esc(k.id) + '"')
      + '<button type="button" class="ibtn sm" data-key-action="copy" data-id="' + esc(k.id) + '"' + (k.copyable ? ' title="复制完整 Key"' : ' disabled title="旧版本创建的 Key 只保存了哈希，无法再复制，请新建一个"') + ' aria-label="复制 Key ' + esc(k.name) + '">' + ic('copy') + '</button>'
      + '<button type="button" class="ibtn sm" data-key-action="edit" data-id="' + esc(k.id) + '" title="编辑名称和模型" aria-label="编辑 Key ' + esc(k.name) + '">' + ic('edit') + '</button>'
      + '<button type="button" class="ibtn sm danger" data-key-action="delete" data-id="' + esc(k.id) + '" title="删除" aria-label="删除 Key ' + esc(k.name) + '">' + ic('trash') + '</button></article>';
  }).join('') || '<div class="empty-box">还没有 API Key，新建一个就能接入客户端</div>';
}
/* 模型范围：「全部模型」= 不限制；「指定模型」从模型目录里勾选，Key 只能调度勾选的模型 */
let KEY_EDIT = null, KEY_MODELS = [];
function keyScope(scope){
  document.querySelectorAll('#keyScope [data-scope]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.scope === scope)));
  $('#keyPick').hidden = scope !== 'pick'; keyPickCount();
}
const keyPicked = () => [...document.querySelectorAll('#keyModelList input:checked')].map(i => i.value);
function keyPickCount(){ $('#keyPickCount').textContent = '已选 ' + keyPicked().length + ' / ' + document.querySelectorAll('#keyModelList input').length + ' 个模型'; }
async function keyPicker(selected){
  $('#keyModelList').innerHTML = spinner('正在读取模型目录…');
  let models = [], warn = '';
  try { models = (await api('/catalog')).models.map(m => ({id: m.id, name: m.name || m.id})); } catch(err){ warn = err.message; }
  selected.filter(id => !models.some(m => m.id === id)).forEach(id => models.push({id, name: id + '（目录外）'}));
  KEY_MODELS = models;
  $('#keyModelList').innerHTML = (warn ? '<div class="notice bad" style="grid-column:1/-1">读取模型目录失败：' + esc(warn) + '</div>' : '')
    + models.map(m => '<label class="pk"><input type="checkbox" value="' + esc(m.id) + '"' + (selected.includes(m.id) ? ' checked' : '') + '><span><b>' + esc(m.name) + '</b><small>' + esc(m.id) + '</small></span></label>').join('')
    + (!models.length && !warn ? '<p class="hint">模型目录是空的</p>' : '');
  keyPickCount();
}
function openKeyModal(k){
  KEY_EDIT = k || null;
  $('#keyForm').reset(); $('#keyForm').hidden = false; $('#newKey').hidden = true; $('#newKeyValue').value = '';
  $('#keyTitle').textContent = k ? '编辑 Key' : '新建 API Key'; $('#keyCreate').textContent = k ? '保存' : '创建';
  $('#keyExpiryRow').hidden = !!k; $('#keyName').value = k ? k.name : '';
  const selected = k ? k.models : [];
  keyScope(selected.length ? 'pick' : 'all'); keyPicker(selected);
  openModal('#keyModal', '#keyName');
}
/* 新 Key 显示后点空白处或按 Esc 不关闭，免得没复制就关掉；之后也能在列表里复制 */
MODAL_CLOSE['#keyModal'] = (id, how) => { if(!$('#newKey').hidden && how !== 'button') return; $('#newKeyValue').value = ''; closeModal(id); };
$('#keyOpen').onclick = () => openKeyModal(null);
$('#keyScope').onclick = e => { const b = e.target.closest('[data-scope]'); if(b) keyScope(b.dataset.scope); };
$('#keyModelList').onchange = keyPickCount;
$('#keyPickAll').onclick = () => { document.querySelectorAll('#keyModelList input').forEach(i => i.checked = true); keyPickCount(); };
$('#keyPickNone').onclick = () => { document.querySelectorAll('#keyModelList input').forEach(i => i.checked = false); keyPickCount(); };
$('#keyForm').onsubmit = async e => {
  e.preventDefault();
  const pick = !$('#keyPick').hidden, models = pick ? keyPicked() : [];
  if(pick && !models.length) return toast('请至少勾选一个模型，或者选「全部模型」', true);
  const b = $('#keyCreate'); b.disabled = true;
  try {
    const name = $('#keyName').value.trim();
    if(KEY_EDIT){ await api('/keys', {action: 'update', id: KEY_EDIT.id, name, models}); closeModal('#keyModal'); toast('Key 已保存'); return await loadKeys(); }
    const expiry = $('#keyExpiry').value;
    const d = await api('/keys', {action: 'create', name, expires_at: expiry ? new Date(expiry).getTime() / 1000 : 0, models});
    $('#keyForm').hidden = true; $('#newKey').hidden = false; $('#keyTitle').textContent = 'Key 已创建'; $('#newKeyValue').value = d.token; $('#newKeyValue').select();
    await loadKeys();
  } catch(err){ managementError(err); } finally { b.disabled = false; }
};
$('#keyDismiss').onclick = () => requestClose('#keyModal', 'button');
$('#keyCopy').onclick = async () => { const input = $('#newKeyValue'); if(await copyText(input.value)) return toast('已复制'); input.select(); toast('复制失败，请手动复制已选中的 Key', true); };
$('#apiBaseCopy').onclick = async () => (await copyText($('#apiBase').textContent)) ? toast('已复制 Base URL') : toast($('#apiBase').textContent);
$('#keysBody').onclick = async e => {
  const b = e.target.closest('[data-key-action]'); if(!b) return;
  const k = MANAGED_KEYS.find(k => k.id === b.dataset.id); if(!k) return;
  if(b.dataset.keyAction === 'edit') return openKeyModal(k);
  if(b.dataset.keyAction === 'copy'){
    b.disabled = true;
    try { const r = await api('/keys', {action: 'reveal', id: k.id}); (await copyText(r.token)) ? toast('已复制 Key「' + k.name + '」') : toast('复制失败：浏览器不允许访问剪贴板', true); }
    catch(err){ managementError(err); } finally { b.disabled = false; }
    return;
  }
  const del = b.dataset.keyAction === 'delete';
  if(del && !confirm('删除 Key「' + k.name + '」？用它的客户端会马上无法请求。')) return;
  b.disabled = true;
  try { await api('/keys', {action: del ? 'delete' : 'update', id: k.id, enabled: !k.enabled}); await loadKeys(); toast(del ? '已删除 Key' : k.enabled ? '已停用 Key' : '已启用 Key'); }
  catch(err){ managementError(err); b.disabled = false; }
};
/* 代理池 */
async function loadProxies(){
  const d = await api('/proxy-pool'), direct = d.direct || {}; MANAGED_PROXIES = d.proxies;
  const used = id => {
    const n = ROWS.filter(a => exitID(a) === id).length, rotating = id > 0 && d.proxies.some(p => p.id === id && p.enabled) ? ROWS.filter(a => (a.proxy || {}).mode === 'round_robin').length : 0;
    return '<div class="l-meta">' + ([n && n + ' 个账号固定使用', rotating && rotating + ' 个账号轮询使用'].filter(Boolean).join(' · ') || '没有账号在用') + '</div>';
  };
  /* 封控是上游对出口 IP 的拦截，和连接检测是两回事：封控优先显示 */
  const blocked = (p, id) => p.blocked ? '<div class="l-use"><span class="bad-t small" style="overflow-wrap:anywhere">' + esc(p.blocked_reason || '上游拒绝了这个 IP') + '</span><div class="muted">' + ago(p.blocked_at) + '封控</div></div>' : '';
  const unblockBtn = (p, id) => p.blocked ? '<button type="button" class="btn soft sm" data-pool-action="unblock" data-id="' + id + '">解除封控</button>' : '';
  const ipChip = p => p.blocked ? chip('bad sm', '<i class="pt"></i>IP 封控', p.blocked_reason) : '';
  $('#proxiesBody').innerHTML = '<article class="proxy"><span class="l-ico">' + ic('pc') + '</span><div class="l-main"><div class="nm"><b>直连</b>' + ipChip(direct) + '</div><div class="mono">网关本机出口 IP，不经过代理</div></div>'
    + (blocked(direct, 0) || '<div class="l-use">' + chip('ok sm', '<i class="pt"></i>正常') + '</div>') + used(0) + unblockBtn(direct, 0) + '</article>'
    + d.proxies.map(p => {
    const c = p.check;
    return '<article class="proxy' + (p.enabled ? '' : ' is-off') + '"><span class="l-ico">' + ic('globe') + '</span><div class="l-main"><div class="nm"><b>' + esc(p.name) + '</b><span class="muted">#' + p.id + '</span>'
      + (p.authenticated ? chip('sm', ic('lock') + '认证') : '') + ipChip(p) + '</div><div class="mono">' + esc(p.address) + '</div></div>'
      + (blocked(p, p.id) || '<div class="l-use">' + (c ? (c.ok ? chip('ok sm', '<i class="pt"></i>' + c.latency_ms + ' ms') : chip('bad sm', '<i class="pt"></i>不可用', c.error)) + '<div class="muted">' + ago(c.at) + '检测</div>'
        + (!c.ok && c.error ? '<div class="bad-t small" style="overflow-wrap:anywhere">' + esc(c.error) + '</div>' : '') : '<span class="muted">还没检测</span>') + '</div>')
      + used(p.id) + unblockBtn(p, p.id) + sw(p.enabled, '启用代理 ' + p.name, 'data-pool-action="toggle" data-id="' + p.id + '"')
      + '<button type="button" class="btn ghost sm" data-pool-action="check" data-id="' + p.id + '"' + (!p.enabled ? ' disabled' : '') + '>检测</button>'
      + '<button type="button" class="ibtn sm" data-pool-action="edit" data-id="' + p.id + '" title="编辑" aria-label="编辑代理 ' + esc(p.name) + '">' + ic('edit') + '</button>'
      + '<button type="button" class="ibtn sm danger" data-pool-action="delete" data-id="' + p.id + '" title="删除" aria-label="删除代理 ' + esc(p.name) + '">' + ic('trash') + '</button></article>';
  }).join('') + (d.proxies.length ? '' : '<div class="empty-box">还没有代理，支持 HTTP、HTTPS、SOCKS5 和 SOCKS5H</div>');
  await loadProxyPool();
}
function openPoolModal(p){
  $('#proxyForm').reset(); $('#proxyEditID').value = p ? p.id : ''; $('#poolTitle').textContent = p ? '编辑代理' : '添加代理';
  $('#proxyAddBox').hidden = !!p; $('#proxyEditBox').hidden = !p; $('#proxyResult').innerHTML = '';
  $('#proxyName').value = p ? p.name : '';
  $('#proxyHint').textContent = p ? '地址留空保留原地址和认证信息' : '支持 HTTP、HTTPS、SOCKS5、SOCKS5H；没写名称时用「地址:端口」作名称';
  if(p) $('#proxySave').textContent = '保存修改'; else proxyCount();
  openModal('#poolModal', p ? '#proxyName' : '#proxyBatch');
}
function proxyCount(){ const n = $('#proxyBatch').value.split('\n').filter(l => l.trim()).length; $('#proxySave').textContent = n > 1 ? '添加 ' + n + ' 个代理' : '添加'; }
$('#proxyBatch').addEventListener('input', proxyCount);
$('#poolOpen').onclick = () => openPoolModal(null);
$('#proxyForm').onsubmit = async e => {
  e.preventDefault(); const b = $('#proxySave'); b.disabled = true;
  try {
    const id = Number($('#proxyEditID').value || 0);
    if(id){
      const name = $('#proxyName').value.trim(); if(!name) throw new Error('请输入代理名称');
      await api('/proxy-pool/manage', {action: 'update', id, name, url: $('#proxyURL').value.trim()});
      closeModal('#poolModal'); await loadProxies(); return toast('代理已保存');
    }
    const lines = $('#proxyBatch').value.split('\n');
    const r = await api('/proxy-pool/manage', {action: 'import', text: $('#proxyBatch').value, scheme: $('#proxyScheme').value});
    await loadProxies();
    if(!r.failed){ closeModal('#poolModal'); return toast('已添加 ' + r.added + ' 个代理'); }
    /* 只留下没加进去的行，改好后可以直接再提交 */
    const bad = r.results.filter(x => !x.ok);
    $('#proxyBatch').value = bad.map(x => lines[x.line - 1].trim()).join('\n'); proxyCount();
    $('#proxyResult').innerHTML = '<div class="notice ' + (r.added ? 'info' : 'bad') + '"><b>新增 ' + r.added + ' 个，' + r.failed + ' 个没加进去</b>，已留在上面，改好后再提交'
      + bad.map((x, i) => '<div class="small">第 ' + (i + 1) + ' 行：' + esc(x.error) + '</div>').join('') + '</div>';
  } catch(err){ managementError(err); } finally { b.disabled = false; }
};
$('#proxiesBody').onclick = async e => {
  const b = e.target.closest('[data-pool-action]'); if(!b) return;
  const action = b.dataset.poolAction, id = Number(b.dataset.id);
  if(action === 'unblock'){
    b.disabled = true;
    try { await api('/proxy-pool/manage', {action, id}); toast('已解除封控，用这个出口的账号恢复调度'); await load(); await loadProxies(); }
    catch(err){ managementError(err); b.disabled = false; }
    return;
  }
  const p = MANAGED_PROXIES.find(p => p.id === id); if(!p) return;
  if(action === 'edit') return openPoolModal(p);
  if(action === 'delete' && !confirm('删除代理「' + p.name + '」？绑定它的账号会显示代理不可用，需要重新选择出口。')) return;
  b.disabled = true;
  try {
    if(action === 'check'){ toast('正在检测 ' + p.name + '…'); const d = await api('/proxy-pool/check', {id: p.id}); toast(d.check.ok ? p.name + ' 连接正常，' + d.check.latency_ms + ' ms' : d.check.error, !d.check.ok); }
    else await api('/proxy-pool/manage', {action: action === 'delete' ? 'delete' : 'toggle', id: p.id, enabled: !p.enabled});
    await load(); await loadProxies();
  } catch(err){ managementError(err); b.disabled = false; }
};
$('#logout').onclick = async () => { try { await api('/auth/logout', {}); } finally { location.replace('/login'); } };
$('#passwordForm').onsubmit = async e => {
  e.preventDefault(); const b = e.target.querySelector('button'); b.disabled = true;
  try { await api('/auth/password', {current_password: $('#currentPassword').value, password: $('#nextPassword').value}); location.replace('/login'); }
  catch(err){ managementError(err); b.disabled = false; }
};
