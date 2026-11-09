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
  $('#importMsg').innerHTML = spinner('正在直接导入账号…');
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
let CALL_UID = '', CALL_GENERATION = 0, CALL_BUSY = false;
let CALL_IMAGE_MODELS = new Set();
function updateCallMode(){
  const image = CALL_IMAGE_MODELS.has($('#callModel').value), prompt = $('#callPrompt');
  $('#callImageOptions').hidden = !image;
  $('#callGoText').textContent = image ? '生成图片' : '发送';
  $('#callHint').textContent = image ? 'Ctrl + Enter 生成 · 经该账号的出口发出，完成后显示图片' : 'Ctrl + Enter 发送 · 最多输出 1024 token，经该账号的出口发出';
  prompt.maxLength = image ? 16000 : 8000;
  prompt.placeholder = image ? '描述要生成的图片' : '';
  if(image && prompt.value === 'Hi') prompt.value = '';
  if(!image && !prompt.value) prompt.value = 'Hi';
}
async function openCall(a){
  const generation = ++CALL_GENERATION;
  CALL_UID = a.uid; CALL_BUSY = false; CALL_IMAGE_MODELS = new Set();
  $('#callTitle').textContent = '调用 · ' + (a.username || a.uid);
  $('#callPrompt').value = 'Hi'; $('#callOut').innerHTML = ''; $('#callGo').disabled = true;
  $('#callSize').value = '16:9'; $('#callResolution').value = '1K';
  const sel = $('#callModel');
  sel.innerHTML = '<option>正在读取模型…</option>'; sel.disabled = true;
  updateCallMode();
  openModal('#callModal', '#callPrompt');
  let catalog;
  try { catalog = await api('/accounts/catalog?uid=' + encodeURIComponent(a.uid)); }
  catch(err){ catalog = {warning:err.message}; }
  if(generation !== CALL_GENERATION || CALL_UID !== a.uid) return;
  if(catalog.warning) $('#callOut').innerHTML = '<div class="notice bad">读取对话目录失败：' + esc(catalog.warning) + '</div>';
  const models = catalog.models || [], images = models.filter(m => m.type === 'image');
  CALL_IMAGE_MODELS = new Set(images.map(m => m.id));
  const free = (a.free_models || []).filter(id => !CALL_IMAGE_MODELS.has(id));
  const list = models.filter(m => m.type !== 'image');
  free.filter(id => !list.some(m => m.id === id)).forEach(id => list.push({id, name: id}));
  list.sort((x, y) => free.includes(y.id) - free.includes(x.id));
  if(!list.length) list.push({id:'auto', name:'auto'});
  let last = ''; try { last = localStorage.getItem('codar-call-model') || ''; } catch(e) {}
  const pick = list.some(m => m.id === last) || CALL_IMAGE_MODELS.has(last) ? last : free[0] || (list.some(m => m.id === 'auto') ? 'auto' : list[0].id);
  const options = rows => rows.map(m => {
    const name = m.name || m.id;
    return '<option value="' + esc(m.id) + '"' + (m.id === pick ? ' selected' : '') + '>' + (free.includes(m.id) ? '免费 · ' : '') + esc(name) + (name !== m.id ? '（' + esc(m.id) + '）' : '') + '</option>';
  }).join('');
  sel.innerHTML = '<optgroup label="对话">' + options(list) + '</optgroup>' + (images.length ? '<optgroup label="生图">' + options(images) + '</optgroup>' : '');
  sel.value = pick;
  sel.disabled = false;
  updateCallMode();
  $('#callGo').disabled = false;
}
function callImagePreview(result){
  const image = (result.data || [])[0] || {};
  if(/^image\/(png|jpeg|gif|webp|bmp|x-icon|vnd\.microsoft\.icon|avif)$/.test(image.mime_type || '')
      && typeof image.b64_json === 'string' && /^[A-Za-z0-9+/]+={0,2}$/.test(image.b64_json)) {
    return '<img class="call-image" alt="生成的图片" decoding="async" src="data:' + image.mime_type + ';base64,' + image.b64_json + '">';
  }
  try {
    const link = new URL(image.url);
    if((link.protocol === 'https:' || link.protocol === 'http:') && !link.username && !link.password){
      const url = esc(link.href);
      return '<img class="call-image" alt="生成的图片" decoding="async" referrerpolicy="no-referrer" src="' + url + '"><a class="link" href="' + url + '" target="_blank" rel="noopener noreferrer">打开原图</a>';
    }
  } catch(_) {}
  return '<div class="notice bad">未收到可预览的图片</div>';
}
$('#callModel').addEventListener('change', updateCallMode);
$('#callForm').onsubmit = async e => {
  e.preventDefault();
  const sel = $('#callModel');
  if(CALL_BUSY || !CALL_UID || sel.disabled) return;
  const model = sel.value, uid = CALL_UID, generation = CALL_GENERATION, image = CALL_IMAGE_MODELS.has(model);
  const input = $('#callPrompt').value.trim();
  if(image && !input){ $('#callOut').innerHTML = '<div class="notice bad">请输入生图提示词</div>'; return; }
  const p = {uid, model, prompt:input || 'Hi'};
  if(image){ p.size = $('#callSize').value.trim() || '16:9'; p.resolution = $('#callResolution').value; }
  else p.max_tokens = 1024;
  try { localStorage.setItem('codar-call-model', model); } catch(_) {}
  CALL_BUSY = true; sel.disabled = $('#callGo').disabled = true; $('#callOut').innerHTML = spinner(image ? '正在生成图片…' : '正在调用 ' + model + '…');
  try {
    const r = await api('/accounts/test', p);
    delete RCACHE[uid];
    if(generation !== CALL_GENERATION) return;
    const u = r.usage || {};
    $('#callOut').innerHTML = r.ok ? '<div class="call-meta"><span><b class="ok-t">成功</b></span><span class="mono">' + esc(r.model) + '</span><span>耗时 <b>' + nf(r.elapsed_ms) + '</b> ms</span>'
        + (r.usage ? tokenLine(u.prompt, u.completion, u.cached) : '') + '</div>' + (image ? callImagePreview(r) : '<pre class="reply">' + esc(r.reply || '（上游返回了空内容）') + '</pre>')
      : '<div class="notice bad"><b>调用失败</b>（' + esc(r.model || model) + '）：' + esc(r.error) + '</div>';
  } catch(err){ if(generation === CALL_GENERATION) $('#callOut').innerHTML = '<div class="notice bad"><b>调用失败</b>：' + esc(err.message) + '</div>'; }
  finally { if(generation === CALL_GENERATION){ CALL_BUSY = false; sel.disabled = $('#callGo').disabled = false; } }
};
$('#callPrompt').addEventListener('keydown', e => { if(e.key === 'Enter' && (e.ctrlKey || e.metaKey)){ e.preventDefault(); $('#callForm').requestSubmit(); } });
MODAL_CLOSE['#callModal'] = id => { CALL_UID = ''; CALL_GENERATION++; $('#callOut').innerHTML = ''; closeModal(id); };
