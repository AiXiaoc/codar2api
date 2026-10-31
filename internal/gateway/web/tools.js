'use strict';
/* 账号导入导出、调用测试 */
/* ---- 导出：文件里有账号令牌，拿到文件就能用这些账号 ---- */
function saveFile(name, text){
  const url = URL.createObjectURL(new Blob([text], {type: 'application/json'})), link = document.createElement('a');
  link.href = url; link.download = name; document.body.appendChild(link); link.click(); link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
async function exportAccounts(uids){
  const n = uids ? uids.length : ROWS.length;
  if(!n) return toast('没有可导出的账号', true);
  if(!confirm('导出 ' + n + ' 个账号？\n文件里包含账号令牌，拿到文件的人可以直接使用这些账号，请妥善保管。')) return;
  try {
    const d = await api('/accounts/export', uids ? {uids} : {}), t = new Date(), pad = v => String(v).padStart(2, '0');
    delete d.ok;
    saveFile('codar2api-accounts-' + t.getFullYear() + pad(t.getMonth() + 1) + pad(t.getDate()) + '-' + pad(t.getHours()) + pad(t.getMinutes()) + '.json', JSON.stringify(d, null, 2));
    toast('已导出 ' + d.accounts.length + ' 个账号');
  } catch(err){ toast(err.message, true); }
}
$('#exportAll').onclick = () => { closeMenu(); exportAccounts(null); };
$('#exportSelected').onclick = () => exportAccounts(Array.from(SELECTED_ACCOUNTS));
/* ---- 导入：Codar2API 导出的 JSON，或任意含令牌的文本 ---- */
let IMPORT_BODY = null;
function importLoad(text, name){
  IMPORT_BODY = null;
  let body = null;
  try {
    const d = JSON.parse(text);
    if(Array.isArray(d)) body = {accounts: d};
    else if(d && Array.isArray(d.accounts)) body = d;
  } catch(e) {}
  if(!body){ const tokens = [...new Set(text.match(JWT_RE) || [])]; if(tokens.length) body = {accounts: tokens.map(token => ({token}))}; }
  const n = body ? body.accounts.length : 0;
  $('#importName').textContent = name + (n ? ' · ' + n + ' 个账号' : '');
  $('#importMsg').innerHTML = n ? '' : '<div class="notice bad">文件里没有找到账号或令牌</div>';
  $('#importGo').disabled = !n; $('#importGo').textContent = n ? '导入 ' + n + ' 个账号' : '导入';
  IMPORT_BODY = n ? body : null;
}
function openImport(){
  closeMenu(); IMPORT_BODY = null; $('#importFile').value = ''; $('#importName').textContent = '选择导出的账号文件';
  $('#importMsg').innerHTML = ''; $('#importGo').disabled = true; $('#importGo').textContent = '导入';
  openModal('#importModal', '#importFile');
}
$('#importOpen').onclick = openImport;
$('#importFile').onchange = e => {
  const f = e.target.files[0]; if(!f) return;
  if(f.size > 8 << 20) return $('#importMsg').innerHTML = '<div class="notice bad">文件太大（最多 8MB）</div>';
  f.text().then(t => importLoad(t, f.name), () => toast('读取文件失败', true));
};
$('#importGo').onclick = async () => {
  if(!IMPORT_BODY) return;
  const b = $('#importGo'); b.disabled = true; b.textContent = '导入中…';
  $('#importMsg').innerHTML = spinner('正在逐个校验并导入，账号多时需要一会儿…');
  try {
    const r = await api('/accounts/import', IMPORT_BODY);
    const line = x => '<tr><td class="num">' + x.index + '</td><td class="mono">' + esc(x.token_masked) + '</td><td>' + (x.ok ? '<b>' + esc(x.username) + '</b>' : '<span class="muted">-</span>') + '</td><td>'
      + (x.ok ? chip(x.updated ? 'info sm' : 'ok sm', x.updated ? '已更新令牌' : '新增') : chip('bad sm', '失败') + ' <span class="bad-t small">' + esc(x.error) + '</span>') + '</td></tr>';
    $('#importMsg').innerHTML = '<div class="notice ' + (r.failed ? 'info' : 'ok') + '"><b>导入完成</b>：共 ' + r.total + ' 个，新增 ' + r.added + '，更新 ' + r.updated + '，失败 ' + r.failed + '</div>'
      + '<div class="tbl" style="max-height:240px;margin-top:8px"><table class="t"><thead><tr><th class="num">#</th><th>令牌</th><th>账号</th><th>结果</th></tr></thead><tbody>' + r.results.map(line).join('') + '</tbody></table></div>';
    b.textContent = '已导入'; IMPORT_BODY = null;
    load().catch(() => {});
  } catch(err){ $('#importMsg').innerHTML = '<div class="notice bad"><b>导入失败</b>：' + esc(err.message) + '</div>'; b.disabled = false; b.textContent = '重试'; }
};
/* ---- 调用测试：选模型、写提示词，经这个账号的出口发一次非流式请求 ---- */
let CALL_UID = '', CALL_MODELS = null, CALL_BUSY = false;
async function openCall(a){
  CALL_UID = a.uid;
  $('#callTitle').textContent = '调用 · ' + (a.username || a.uid);
  $('#callPrompt').value = 'Hi'; $('#callOut').innerHTML = ''; $('#callGo').disabled = false;
  const sel = $('#callModel');
  sel.innerHTML = '<option>正在读取模型…</option>'; sel.disabled = true;
  openModal('#callModal', '#callPrompt');
  try { if(!CALL_MODELS) CALL_MODELS = (await api('/catalog')).models; }
  catch(err){ CALL_MODELS = null; $('#callOut').innerHTML = '<div class="notice bad">读取模型目录失败：' + esc(err.message) + '</div>'; }
  if(CALL_UID !== a.uid) return;
  const free = a.free_models || [], list = (CALL_MODELS || []).map(m => ({id: m.id, name: m.name || m.id}));
  free.filter(id => !list.some(m => m.id === id)).forEach(id => list.push({id, name: id}));
  list.sort((x, y) => free.includes(y.id) - free.includes(x.id));
  let last = ''; try { last = localStorage.getItem('codar-call-model') || ''; } catch(e) {}
  const pick = list.some(m => m.id === last) ? last : free[0] || (list.some(m => m.id === 'auto') ? 'auto' : (list[0] || {}).id);
  sel.innerHTML = list.map(m => '<option value="' + esc(m.id) + '"' + (m.id === pick ? ' selected' : '') + '>' + (free.includes(m.id) ? '免费 · ' : '') + esc(m.name) + (m.name !== m.id ? '（' + esc(m.id) + '）' : '') + '</option>').join('')
    || '<option value="auto">auto</option>';
  sel.disabled = false;
}
$('#callForm').onsubmit = async e => {
  e.preventDefault(); if(CALL_BUSY || !CALL_UID) return;
  const model = $('#callModel').value, prompt = $('#callPrompt').value.trim() || 'Hi', uid = CALL_UID;
  try { localStorage.setItem('codar-call-model', model); } catch(_) {}
  CALL_BUSY = true; $('#callGo').disabled = true; $('#callOut').innerHTML = spinner('正在调用 ' + model + '…');
  try {
    const r = await api('/accounts/test', {uid, model, prompt, max_tokens: 1024});
    if(uid !== CALL_UID) return;
    const u = r.usage || {};
    $('#callOut').innerHTML = r.ok ? '<div class="call-meta"><span><b class="ok-t">成功</b></span><span class="mono">' + esc(r.model) + '</span><span>耗时 <b>' + nf(r.elapsed_ms) + '</b> ms</span>'
        + (r.usage ? tokenLine(u.prompt, u.completion, u.cached) : '') + '</div><pre class="reply">' + esc(r.reply || '（上游返回了空内容）') + '</pre>'
      : '<div class="notice bad"><b>调用失败</b>（' + esc(r.model || model) + '）：' + esc(r.error) + '</div>';
    delete RCACHE[uid];
  } catch(err){ $('#callOut').innerHTML = '<div class="notice bad"><b>调用失败</b>：' + esc(err.message) + '</div>'; }
  finally { CALL_BUSY = false; $('#callGo').disabled = false; }
};
$('#callPrompt').addEventListener('keydown', e => { if(e.key === 'Enter' && (e.ctrlKey || e.metaKey)){ e.preventDefault(); $('#callForm').requestSubmit(); } });
MODAL_CLOSE['#callModal'] = id => { CALL_UID = ''; closeModal(id); };
