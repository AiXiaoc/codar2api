'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const web = path.join(__dirname, '../web');
const code = fs.readFileSync(path.join(web, 'recent-requests.js'), 'utf8');
const html = fs.readFileSync(path.join(web, 'dashboard.html'), 'utf8');
const pages = fs.readFileSync(path.join(web, 'pages.js'), 'utf8');
const row = (error = '', extra = {}) => ({ts:123, model:'test-model', path:'/v1/chat/completions', account:'account', ok:!error, status:error ? 502 : 200, error, ...extra});
const response = (items = [], extra = {}) => ({items, page:1, pages:1, total:items.length, ...extra});

function harness(saved = '0'){
  const elements = new Map(), calls = [], storage = new Map([['gateway-recent-errors', saved]]);
  const element = selector => {
    if(!elements.has(selector)) elements.set(selector, {innerHTML:'', textContent:'', disabled:false, attrs:{}, listeners:{},
      setAttribute(k,v){this.attrs[k]=v;}, addEventListener(k,v){this.listeners[k]=v;}});
    return elements.get(selector);
  };
  const buttons = ['0','1'].map(errors => ({dataset:{errors}, attrs:{}, setAttribute(k,v){this.attrs[k]=v;}}));
  const esc = value => String(value ?? '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;');
  const context = vm.createContext({
    STAT_H:24, $:element, document:{querySelectorAll:() => buttons},
    localStorage:{getItem:k => storage.get(k),setItem:(k,v) => storage.set(k,v)},
    api:url => new Promise((resolve,reject) => calls.push({url,resolve,reject})),
    esc, chip:(cls,value) => '<b class="' + cls + '">' + esc(value) + '</b>', fmt:String, fmtT:String, n:String, yuan:String,
    API_NAME:{'/v1/chat/completions':'Chat'}, requestModelLabel:r => esc(r.model),
  });
  vm.runInContext(code,context,{filename:'recent-requests.js'});
  return {context,calls,element,buttons,storage,
    load:() => context.loadRecentRequests(), pending:() => vm.runInContext('RECENT_PENDING.promise',context),
    mode:value => element('#statRecentMode').listeners.click({target:{closest:() => ({dataset:{errors:value}})}}),
  };
}

test('queries the complete error history and preserves its page during automatic refresh', async () => {
  const h=harness('1'), first=h.load();
  assert.equal(first,h.load()); assert.equal(h.calls.length,1);
  assert.match(h.calls[0].url,/errors_only=1&page=1&page_size=30/);
  h.calls[0].resolve(response([row('older failure')],{pages:3,total:65})); await first;
  assert.match(h.element('#statRecentPage').textContent,/共 65 条 · 第 1 \/ 3 页/);
  assert.equal(h.buttons[1].attrs['aria-pressed'],'true');
  assert.equal(h.element('#statRecentPrev').disabled,true);
  h.element('#statRecentNext').onclick(); const next=h.pending();
  assert.match(h.calls[1].url,/errors_only=1&page=2/);
  assert.equal(h.element('#statRecentNext').disabled,true);
  h.calls[1].resolve(response([row('page two')],{page:2,pages:3,total:65})); await next;
  const refresh=h.load(); assert.match(h.calls[2].url,/errors_only=1&page=2/);
  h.calls[2].resolve(response([row('page two refreshed')],{page:2,pages:3,total:66})); await refresh;
  assert.match(h.element('#statRecent').innerHTML,/page two refreshed/);
  assert.equal(h.element('#statRecentPrev').disabled,false);
});

test('switching mode or time range resets the page and ignores stale responses', async () => {
  const h=harness(), all=h.load();
  h.mode('1'); const errors=h.pending();
  h.calls[1].resolve(response([row('only errors')],{pages:2,total:35})); await errors;
  h.calls[0].resolve(response([row('',{account:'stale normal'})])); await all;
  assert.match(h.element('#statRecent').innerHTML,/only errors/);
  assert.doesNotMatch(h.element('#statRecent').innerHTML,/stale normal/);
  h.element('#statRecentNext').onclick(); const page=h.pending();
  h.calls[2].resolve(response([row('page two')],{page:2,pages:2,total:35})); await page;
  h.context.STAT_H=1; const hour=h.load();
  assert.match(h.calls[3].url,/hours=1&errors_only=1&page=1/);
  h.calls[3].resolve(response()); await hour;
  assert.match(h.element('#statRecent').innerHTML,/这段时间没有错误请求/);
  h.mode('0'); const normal=h.pending();
  assert.match(h.calls[4].url,/errors_only=0&page=1/);
  h.calls[4].resolve(response([row()])); await normal;
  assert.equal(h.storage.get('gateway-recent-errors'),'0');
});

test('retry errors keep the failing account, error message, detail link and escaped text', () => {
  const h=harness();
  const rendered=h.context.recentRequestRow(row('',{recovered:true, account:'success-account', diagnostic_id:'detail-id',
    error_attempts:[{account:'failed-account',status:502,error:'<img src=x onerror=alert(1)>'}]}));
  assert.match(rendered,/重试后成功/); assert.match(rendered,/success-account/); assert.match(rendered,/failed-account：HTTP 502/);
  assert.match(rendered,/&lt;img/); assert.doesNotMatch(rendered,/<img/);
  assert.match(rendered,/data-request-detail="detail-id"/);
});

test('refresh failures preserve the last result and remain visible; another mode has no stale rows', async () => {
  const h=harness('1'), first=h.load();
  h.calls[0].resolve(response([row('known failure')])); await first;
  const refresh=h.load(); h.calls[1].reject(new Error('network down')); await refresh;
  assert.match(h.element('#statRecent').innerHTML,/known failure/);
  assert.match(h.element('#statRecentNotice').textContent,/network down（显示上次结果）/);
  h.mode('0'); const switched=h.pending();
  h.calls[2].reject(new Error('still down')); await switched;
  assert.doesNotMatch(h.element('#statRecent').innerHTML,/known failure/);
  assert.match(h.element('#statRecent').innerHTML,/读取失败/);
  assert.equal(h.element('#statRecent').attrs['aria-busy'],'false');
});

test('a shrinking history uses the server clamped page for following requests', async () => {
  const h=harness('1'), first=h.load();
  h.calls[0].resolve(response([row('first')],{pages:2,total:31})); await first;
  h.element('#statRecentNext').onclick(); const next=h.pending();
  h.calls[1].resolve(response([row('retention shrank')],{page:1,pages:1,total:1})); await next;
  const refresh=h.load(); assert.match(h.calls[2].url,/page=1&page_size/);
  h.calls[2].resolve(response()); await refresh;
  assert.equal(h.element('#statRecentNext').disabled,true);
});

test('embedded statistics page loads the independent paged API and accessible controls', () => {
  assert.match(html,/id="statRecentMode" role="group" aria-label="最近请求类型"/);
  assert.match(html,/data-errors="1"[^>]*>错误请求/);
  assert.match(html,/id="statRecentNext"/);
  assert(html.indexOf('/assets/recent-requests.js') < html.indexOf('/assets/pages.js'));
  assert.match(pages,/loadRecentRequests\(\)/);
  assert.doesNotMatch(pages,/d\.recent\.slice\(0, 30\)/);
});
