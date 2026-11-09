'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const read = name => fs.readFileSync(path.join(__dirname, '../web', name), 'utf8');
const manage = read('manage.js');
const code = manage.slice(manage.indexOf('/* 代理池 */'), manage.indexOf('function openPoolModal('));
const formatters = read('ui.js').split('\n').filter(line => line.startsWith('const nf =') || line.startsWith('const pctS =')).join('\n');
const esc = value => String(value == null ? '' : value).replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c]));

function harness(){
  const elements = new Map(), stats = new Map(), calls = [];
  const $ = selector => {
    if(!elements.has(selector)) elements.set(selector, {innerHTML:'', hidden:false});
    return elements.get(selector);
  };
  const document = {
    hidden:false,
    querySelector:$,
    querySelectorAll:() => [...stats.values()]
  };
  const context = vm.createContext({
    $, document, ROWS:[], MANAGED_PROXIES:[], esc, ago:() => '刚刚', chip:() => '', ic:() => '', sw:() => '', exitID:() => 0,
    loadProxyPool:async () => {},
    api:async url => { calls.push(url); return context.pool; }
  });
  vm.runInContext(formatters + '\n' + code, context);
  context.pool = {direct:{stats:{requests:2, succeeded:2, failed:0, success_rate:1}}, proxies:[
    {id:1, name:'代理 <one>', address:'http://127.0.0.1:8080', enabled:true, stats:{requests:4, succeeded:3, failed:1, success_rate:.75}}
  ]};
  return {
    context, $, document, calls,
    node(id){ const node = {dataset:{proxyStatsId:String(id)}, innerHTML:''}; stats.set(id, node); return node; },
    load:() => vm.runInContext('loadProxies()', context),
    refresh:() => vm.runInContext('refreshProxyStats()', context)
  };
}

test('proxy and direct cards display all four metrics without exposing raw names as HTML', async () => {
  const h = harness();
  await h.load();
  const html = h.$('#proxiesBody').innerHTML;
  for(const label of ['请求数', '成功数', '失败数', '成功率']) assert.equal(html.split('title="' + label + '"').length - 1, 2);
  assert.match(html, /data-proxy-stats-id="0"/);
  assert.match(html, /data-proxy-stats-id="1"/);
  assert.match(html, /75%/);
  assert.match(html, /100%/);
  assert.match(html, /代理 &lt;one&gt;/);
  assert.doesNotMatch(html, /代理 <one>/);
});

test('exits without requests show zero counts and no invented success percentage', async () => {
  const h = harness();
  h.context.pool = {direct:{}, proxies:[{id:1, name:'new', enabled:true, stats:{requests:0, succeeded:0, failed:0, success_rate:null}}]};
  await h.load();
  const html = h.$('#proxiesBody').innerHTML;
  assert.equal(html.split('title="请求数">请求 <b>0</b>').length - 1, 2);
  assert.equal(html.split('title="成功率">成功率 <b>-</b>').length - 1, 2);
});

test('automatic refresh only changes the statistics and retains existing controls', async () => {
  const h = harness(), direct = h.node(0), proxy = h.node(1);
  await h.load();
  const html = h.$('#proxiesBody').innerHTML;
  await h.refresh();
  assert.match(direct.innerHTML, /100%/);
  assert.match(proxy.innerHTML, /75%/);
  assert.equal(h.$('#proxiesBody').innerHTML, html);
  h.document.hidden = true;
  await h.refresh();
  h.document.hidden = false;
  h.$('[data-page="proxies"]').hidden = true;
  await h.refresh();
  assert.equal(h.calls.length, 2);
  assert.match(read('pages.js'), /setInterval\(\(\) => refreshProxyStats\(\)\.catch\(\(\) => \{\}\), 10000\)/);
});

test('overlapping refreshes are skipped and a failed request does not prevent later updates', async () => {
  const h = harness(), proxy = h.node(1);
  let reject;
  h.context.api = url => { h.calls.push(url); return new Promise((_, fail) => { reject = fail; }); };
  const pending = h.refresh();
  await h.refresh();
  assert.equal(h.calls.length, 1);
  reject(new Error('offline'));
  await assert.rejects(pending, /offline/);
  h.context.api = async () => h.context.pool;
  await h.refresh();
  assert.match(proxy.innerHTML, /75%/);
});
