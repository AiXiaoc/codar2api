'use strict';
/* 账号最大并发：全部账号或打开弹窗时勾选的账号，整批保存。 */
let CONCURRENCY_TARGET = null, CONCURRENCY_SAVING = false;
function openConcurrency(all = false){
  closeMenu();
  if(BATCH_BUSY) return;
  const uids = Array.from(SELECTED_ACCOUNTS), accounts = all ? ROWS : ROWS.filter(a => SELECTED_ACCOUNTS.has(a.uid));
  if(!accounts.length) return toast(all ? '当前没有账号' : '请先选择账号', true);
  CONCURRENCY_TARGET = all ? {all: true} : {uids};
  $('#concurrencyTitle').textContent = all ? '全部账号设置并发数' : '所选账号设置并发数';
  $('#concurrencyScope').textContent = all ? '作用于全部 ' + accounts.length + ' 个账号，包含已禁用和受限账号，不受当前筛选影响。'
    : '作用于已勾选的 ' + uids.length + ' 个账号，包含已勾选但被筛选隐藏的账号。';
  const limits = new Set(accounts.map(a => a.max_concurrency || 0));
  $('#concurrencyLimit').value = limits.size === 1 ? String(limits.values().next().value) : '';
  $('#concurrencyLimit').placeholder = limits.size === 1 ? '' : '当前并发上限不一致，请输入统一值';
  $('#concurrencyMsg').hidden = true;
  openModal('#concurrencyModal', '#concurrencyLimit');
}
$('#concurrencyAll').onclick = () => openConcurrency(true);
$('#concurrencySelected').onclick = () => openConcurrency(false);
MODAL_CLOSE['#concurrencyModal'] = id => {
  if(CONCURRENCY_SAVING) return;
  CONCURRENCY_TARGET = null; closeModal(id);
};
$('#concurrencyLimit').oninput = () => { $('#concurrencyMsg').hidden = true; };
$('#concurrencyForm').onsubmit = async e => {
  e.preventDefault();
  if(CONCURRENCY_SAVING || BATCH_BUSY || !CONCURRENCY_TARGET || !$('#concurrencyForm').reportValidity()) return;
  const input = $('#concurrencyLimit'), limit = Number(input.value);
  if(!input.value.trim() || !Number.isInteger(limit) || limit < 0 || limit > 1000000) return;
  const target = CONCURRENCY_TARGET;
  CONCURRENCY_SAVING = true; BATCH_BUSY = true; drawSelection();
  input.disabled = true; $('#concurrencySave').disabled = true; $('#concurrencySave').textContent = '保存中…';
  $('#concurrencyMsg').hidden = true;
  try {
    const result = await api('/accounts/batch', {action: 'set_concurrency', max_concurrency: limit, ...target});
    if(result.ok === false) throw new Error(result.error || '保存失败');
    ROWS.forEach(a => { if(target.all || target.uids.includes(a.uid)) a.max_concurrency = limit; });
    if(SCHED_EDIT && (target.all || target.uids.includes(SCHED_EDIT.uid))) SCHED_EDIT.max_concurrency = limit;
    CONCURRENCY_TARGET = null; closeModal('#concurrencyModal');
    toast('已将 ' + result.count + ' 个账号的最大并发设为' + (limit ? ' ' + limit : '不限') + '，立即生效');
    try { await load(); } catch(err){ toast('并发数已保存，但刷新列表失败：' + err.message, true); }
  } catch(err){
    $('#concurrencyMsg').textContent = err.message; $('#concurrencyMsg').hidden = false;
  } finally {
    CONCURRENCY_SAVING = false; BATCH_BUSY = false;
    input.disabled = false; $('#concurrencySave').disabled = false; $('#concurrencySave').textContent = '保存并发数';
    drawSelection();
  }
};
