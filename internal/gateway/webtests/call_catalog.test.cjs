'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const tools = fs.readFileSync(path.join(__dirname, '../web/tools.js'), 'utf8');
const code = tools.slice(tools.indexOf('/* ---- 调用测试：'));
const esc = value => String(value == null ? '' : value).replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c]));
function harness(){
  const elements = new Map(), calls = [], MODAL_CLOSE = {};
  const $ = selector => {
    if(!elements.has(selector)) elements.set(selector, {value:'', innerHTML:'', textContent:'', disabled:false, listeners:{}, addEventListener(type, fn){ this.listeners[type] = fn; }});
    return elements.get(selector);
  };
  const context = vm.createContext({
    $, esc, MODAL_CLOSE, URL, encodeURIComponent, openModal:() => {}, closeModal:() => {},
    RCACHE:{}, spinner:esc, nf:String, tokenLine:() => '', localStorage:{getItem:() => ''},
    api:(url, body) => new Promise((resolve,reject) => calls.push({url, body:body && JSON.parse(JSON.stringify(body)), resolve,reject}))
  });
  vm.runInContext(code, context);
  return {$, calls, submit:() => $('#callForm').onsubmit({preventDefault(){}}), close:() => MODAL_CLOSE['#callModal']('#callModal'), open:account => {
    context.account = account;
    return vm.runInContext('openCall(account)', context);
  }};
}
test('each account call reads models for its own account, including after reopening', async () => {
  const h = harness();
  for(const uid of ['a/+?中文', 'b', 'a/+?中文']){
    const loading = h.open({uid, username:uid});
    const call = h.calls.at(-1);
    assert.equal(call.url, '/accounts/catalog?uid=' + encodeURIComponent(uid));
    assert.equal(h.$('#callModel').disabled, true);
    assert.equal(h.$('#callGo').disabled, true);
    call.resolve({models:[{id:'model-' + uid, name:'<Model>'}]});
    await loading;
    assert(h.$('#callModel').innerHTML.includes(esc('model-' + uid)));
    assert(h.$('#callModel').innerHTML.includes('&lt;Model&gt;'));
    assert.equal(h.$('#callModel').disabled, false);
    assert.equal(h.$('#callGo').disabled, false);
  }
  assert.equal(h.calls.length, 3, 'do not share the global catalog cache');
});
test('late catalog responses cannot overwrite a different account or a reopened dialog', async () => {
  const h = harness();
  const first = h.open({uid:'a'});
  const second = h.open({uid:'b'});
  h.calls[1].resolve({models:[{id:'b-model'}]});
  await second;
  h.calls[0].resolve({models:[{id:'a-stale'}]});
  await first;
  assert(h.$('#callModel').innerHTML.includes('b-model'));
  assert(!h.$('#callModel').innerHTML.includes('a-stale'));
  const beforeClose = h.open({uid:'b'});
  h.close();
  const reopened = h.open({uid:'b'});
  h.calls[3].resolve({models:[{id:'b-fresh'}]});
  await reopened;
  h.calls[2].reject(new Error('stale failure'));
  await beforeClose;
  assert(h.$('#callModel').innerHTML.includes('b-fresh'));
  assert(!h.$('#callOut').innerHTML.includes('stale failure'));
});
test('proxy errors are shown without requesting a global or direct catalog', async () => {
  const h = harness(), loading = h.open({uid:'proxied'});
  h.calls[0].reject(new Error('指定代理不可用 <proxy>'));
  await loading;
  assert.equal(h.calls.length, 1);
  assert(h.$('#callOut').innerHTML.includes('指定代理不可用 &lt;proxy&gt;'));
  assert(!h.$('#callOut').innerHTML.includes('<proxy>'));
  const retry = h.open({uid:'proxied', free_models:['free-id']});
  h.calls[1].resolve({models:[{id:'paid-id'}, {id:'free-id', name:'免费模型'}]});
  await retry;
  const options = h.$('#callModel').innerHTML;
  assert(options.indexOf('free-id') < options.indexOf('paid-id'));
});

test('choosing a generation model sends the selected account and previews URL or Base64 results', async () => {
  const h = harness(), loading = h.open({uid:'selected'});
  const model = 'gemini-nano-banana-2.1';
  h.calls[0].resolve({models:[{id:'auto'}, {id:model, type:'image'}]});
  await loading;
  assert(h.$('#callModel').innerHTML.includes('<optgroup label="生图">'));
  assert.equal(h.$('#callImageOptions').hidden, true);
  h.$('#callModel').value = model;
  h.$('#callModel').listeners.change();
  assert.equal(h.$('#callImageOptions').hidden, false);
  assert.equal(h.$('#callGoText').textContent, '生成图片');
  await h.submit();
  assert.equal(h.calls.length, 1, 'empty prompt must not submit a billable image');
  h.$('#callPrompt').value = '一张海报'; h.$('#callSize').value = '3:2'; h.$('#callResolution').value = '2K';
  const requestingURL = h.submit();
  assert.equal(h.calls[1].url, '/accounts/test');
  assert.deepEqual(h.calls[1].body, {uid:'selected', model, prompt:'一张海报', size:'3:2', resolution:'2K'});
  h.calls[1].resolve({ok:true, model, elapsed_ms:1000, data:[{url:'https://cdn.example.test/p.png?a=1&b=2'}]});
  await requestingURL;
  assert(h.$('#callOut').innerHTML.includes('src="https://cdn.example.test/p.png?a=1&amp;b=2"'));
  const requestingBase64 = h.submit();
  h.calls[2].resolve({ok:true, model, elapsed_ms:1000, data:[{mime_type:'image/webp', b64_json:'AAAA'}]});
  await requestingBase64;
  assert(h.$('#callOut').innerHTML.includes('src="data:image/webp;base64,AAAA"'));
  assert(!h.$('#callOut').innerHTML.includes('上游返回了空内容'));
});
