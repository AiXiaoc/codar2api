'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const pages = fs.readFileSync(path.join(__dirname, '../web/pages.js'), 'utf8');
const code = pages.slice(0, pages.indexOf('/* ---- 统计 ---- */'));
const flush = () => new Promise(resolve => setImmediate(resolve));
const esc = value => String(value == null ? '' : value).replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c]));
function harness(){
  const elements = new Map(), calls = [], messages = [];
  const $ = selector => {
    if(!elements.has(selector)) elements.set(selector, {value:'', innerHTML:'', textContent:'', disabled:false});
    return elements.get(selector);
  };
  const context = vm.createContext({
    $, ROWS:[], esc, ago:() => '刚刚', chip:() => '', ic:() => '',
    toast:(...args) => messages.push(args),
    api:(url, body) => new Promise((resolve, reject) => calls.push({url, body:body && JSON.parse(JSON.stringify(body)), resolve, reject}))
  });
  vm.runInContext(code, context);
  return {
    $, calls, messages, load:() => vm.runInContext('loadCatalog()', context),
    change:id => { $('#catProxy').value = String(id); return $('#catProxy').onchange(); },
    pool(index, id = 0, proxies = [{id:1, name:'<出口一>', enabled:true}, {id:2, name:'出口二', enabled:true}]){
      calls[index].resolve({catalog_proxy_id:id, proxies});
    },
    models(index, id = 'model', warning){ calls[index].resolve({models:[{id, name:id}], at:1, warning}); }
  };
}

test('model page loads the saved proxy even when the catalog fails and escapes proxy names', async () => {
  const h = harness(), loading = h.load();
  assert.equal(h.calls[0].url, '/proxy-pool');
  h.pool(0, 1, [{id:1, name:'<出口一>', enabled:false}]);
  await flush();
  assert.equal(h.$('#catProxy').value, '1');
  assert(h.$('#catProxy').innerHTML.includes('&lt;出口一&gt;'));
  assert(h.$('#catProxy').innerHTML.includes('已停用'));
  assert.equal(h.$('#catProxy').disabled, false, 'allow choosing another exit while the request is pending');
  const rejected = assert.rejects(loading, /指定代理已停用/);
  h.calls[1].reject(new Error('指定代理已停用'));
  await rejected;
  assert(h.$('#catAt').textContent.includes('指定代理已停用'));
  assert.equal(h.$('#catProxy').disabled, false);
  const reload = h.load();
  h.pool(2, 9, []);
  await flush();
  assert(h.$('#catProxy').innerHTML.includes('已删除的代理 #9'));
  assert.equal(h.$('#catProxy').value, '9');
  h.models(3);
  await reload;
});

test('changing proxy saves immediately and refreshes without showing an earlier response', async () => {
  const h = harness(), old = h.load();
  h.pool(0);
  await flush();
  const changing = h.change(2);
  assert.equal(h.calls[2].url, '/catalog/proxy');
  assert.deepEqual(h.calls[2].body, {proxy_id:2});
  assert.equal(h.$('#catProxy').disabled, true);
  assert.equal(h.$('#catBody').innerHTML, '');
  h.calls[2].resolve({catalog_proxy_id:2});
  await flush();
  h.pool(3, 2);
  await flush();
  assert.equal(h.calls[4].url, '/catalog?force=1');
  h.models(4, 'new-proxy-model');
  await changing;
  h.models(1, 'stale-direct-model');
  await old;
  assert(h.$('#catBody').innerHTML.includes('new-proxy-model'));
  assert(!h.$('#catBody').innerHTML.includes('stale-direct-model'));
  assert.equal(h.$('#catProxy').value, '2');
  assert.equal(h.$('#catRefresh').disabled, false);
  assert(h.messages.some(([message, error]) => message.includes('已保存') && !error));
});

test('failed saves restore the previous selection and do not fetch through another exit', async () => {
  const h = harness(), loading = h.load();
  h.pool(0, 1);
  await flush(); h.models(1); await loading;
  const changing = h.change(2);
  h.calls[2].reject(new Error('写入失败'));
  await changing;
  assert.equal(h.$('#catProxy').value, '1');
  assert.equal(h.calls.length, 3);
  assert(h.messages.some(([message, error]) => message.includes('保存失败') && error));
  assert.equal(h.$('#catProxy').disabled, false);
});

test('failed fetches keep the successfully saved proxy and never request direct fallback', async () => {
  const h = harness(), changing = h.change(2);
  h.calls[0].resolve({catalog_proxy_id:2});
  await flush(); h.pool(1, 2); await flush();
  h.calls[2].reject(new Error('连接代理失败'));
  await changing;
  assert.equal(h.$('#catProxy').value, '2');
  assert.equal(h.calls.length, 3);
  assert(h.messages.some(([message, error]) => message.includes('代理已保存，读取失败') && error));
  assert(h.$('#catBody').innerHTML.includes('请检查获取代理后重试'));
});

test('a stale cache warning is not reported as a successful refresh', async () => {
  const h = harness(), refreshing = h.$('#catRefresh').onclick();
  h.pool(0, 1); await flush();
  h.models(1, 'cached', '<上游失败>');
  await refreshing;
  assert(h.$('#catAt').innerHTML.includes('&lt;上游失败&gt;'));
  assert(h.messages.some(([message, error]) => message.includes('刷新失败') && error));
  assert(!h.messages.some(([message]) => message === '模型目录已刷新'));
});
