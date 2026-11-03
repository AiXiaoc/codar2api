const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const Diff = require('../web/detail-diff.js');
const pages = fs.readFileSync(path.join(__dirname, '../web/pages.js'), 'utf8');
const formatterCode = pages.slice(pages.indexOf('function formatDetailBody('), pages.indexOf('function formatDetailHeaders('));
const format = vm.runInNewContext(formatterCode + '\nformatDetailBody');
const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'})[c]);

function compare(before, after){
  const a = format(before), b = format(after), result = Diff.compare(a, b);
  const marked = result.ranges.map(([start, end]) => b.slice(start, end));
  return {a, b, result, marked, html: Diff.render(b, result.ranges, esc)};
}

test('JSON formatting, property order, and equivalent escapes are not changes', () => {
  const {result, html, b} = compare('{"b":9007199254740993,"a":{"text":"<hello>"}}', '{"a":{"text":"\\u003chello\\u003e"},"b":9007199254740993}');
  assert.equal(result.changed, false);
  assert.deepEqual(result.ranges, []);
  assert.equal(html, esc(b));
});

test('marks only new text in the real identity-prefix example', () => {
  const oldText = 'You are an AI agent powered by DeepSeek Harness.\n\nYou are a coding agent powered by the deepseek-v4.1-flash model. Other DeepSeek Harness text stays.';
  const newText = 'You are an AI coding agent.\n\nYou are a coding agent powered by the deepseek-v4.1-flash model. Other DeepSeek Harness text stays.';
  const {result, marked, html} = compare(JSON.stringify({messages:[{role:'developer', content:oldText}]}), JSON.stringify({messages:[{role:'developer', content:newText}]}));
  assert.equal(result.changed, true);
  assert.deepEqual(marked, ['coding ']);
  assert(result.removed.includes('$.messages[0].content 中的文本'));
  assert(html.includes('<mark class="detail-change">coding </mark>agent.'));
  assert(html.includes('Other DeepSeek Harness text stays.'));
});

test('separate edits leave the unchanged middle unmarked', () => {
  const {marked, html} = compare(JSON.stringify({s:'before one keep this middle two after'}), JSON.stringify({s:'before FIRST keep this middle SECOND after'}));
  assert.deepEqual(marked, ['FIRST', 'SECOND']);
  assert(html.includes(' keep this middle '));
  assert(!/<mark[^>]*>[^<]*keep this middle/.test(html));
});

test('large strings with two edits retain the unchanged middle', () => {
  const middle = Array.from({length:2500}, (_, i) => 'word' + i).join(' ');
  const before = JSON.stringify({s:'head OLD1 ' + middle + ' OLD2 tail'});
  const after = JSON.stringify({s:'head NEW1 ' + middle + ' NEW2 tail'});
  const {result, marked} = compare(before, after);
  assert.equal(result.limited, false);
  assert.deepEqual(marked, ['NEW', 'NEW']);
});

test('highlights additions and reports deletions without fabricating visible characters', () => {
  const {b, result} = compare('{"keep":1,"gone":{"x":2},"text":"abc"}', '{"keep":1,"text":"ac","added":{"q":2}}');
  const marked = result.ranges.map(([start, end]) => b.slice(start, end));
  assert.equal(result.changed, true);
  assert(marked.some(s => s.includes('"added"') && s.includes('"q"')));
  assert(result.removed.includes('$.gone'));
  assert(result.removed.includes('$.text 中的文本'));
  assert(marked.every(s => !s.includes('"keep"')));
});

test('inserting an array item does not highlight the shifted unchanged items', () => {
  const before = JSON.stringify({messages:[{role:'user',content:'unchanged A'},{role:'assistant',content:'unchanged B'}]});
  const after = JSON.stringify({messages:[{role:'system',content:'new item'},{role:'user',content:'unchanged A'},{role:'assistant',content:'unchanged B'}]});
  const {marked} = compare(before, after);
  assert.equal(marked.length, 1);
  assert(marked[0].includes('new item'));
  assert(!marked[0].includes('unchanged'));
});

test('preserves numeric precision and correctly maps Unicode and escaped strings', () => {
  const nums = compare('{"x":9007199254740992}', '{"x":9007199254740993}');
  assert.deepEqual(nums.marked, ['9007199254740993']);
  const {marked, html} = compare('{"x":"same \\ud83d\\ude00 \\n end"}', '{"x":"same 😁 \\n end"}');
  assert.deepEqual(marked, ['😁']);
  assert(html.includes('same <mark class="detail-change">😁</mark>'));
});

test('escapes dangerous HTML and leaves raw text intact', () => {
  const {b, result, html} = compare(JSON.stringify({x:'safe'}), JSON.stringify({x:'<img src=x onerror=alert(1)>'}));
  assert.equal(html.replace(/<\/?mark(?: class="detail-change")?>/g, ''), esc(b));
  assert(!html.includes('<img'));
  assert(result.changed);
});

test('handles raw text, binary and comparison limits explicitly', () => {
  const text = Diff.compare('before A middle B after', 'before X middle Y after');
  assert(text.changed && text.ranges.length === 2);
  const binary = Diff.compare('/w==', '/g==', {beforeBinary:true, afterBinary:true});
  assert(binary.changed && binary.reason.includes('二进制') && binary.ranges.length === 0);
  const deep = '['.repeat(260) + '0' + ']'.repeat(260);
  const limit = Diff.compare(deep, deep.replace('0','1'));
  assert(limit.limited && limit.reason && limit.ranges.length === 0);
});

test('unmarked text remains an ordered subsequence of the original', () => {
  let seed = 713;
  const next = n => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed % n; };
  const alphabet = 'abcd ,.!';
  const random = () => Array.from({length:next(55)}, () => alphabet[next(alphabet.length)]).join('');
  for(let i = 0; i < 1000; i++){
    const before = random(), after = random(), a = JSON.stringify({x:before}), b = JSON.stringify({x:after});
    const result = Diff.compare(a, b);
    const ranges = result.ranges.slice().sort((x, y) => x[0] - y[0]);
    let stripped = '', pos = 0;
    for(const [start, end] of ranges){ stripped += b.slice(pos, start); pos = end; }
    stripped += b.slice(pos);
    const unchanged = JSON.parse(stripped).x;
    let p = 0;
    for(const c of before) if(c === unchanged[p]) p++;
    assert.equal(p, unchanged.length, 'an unmarked character was not present in the original');
  }
});

test('detail view uses three sections, retains headers and copies raw bodies', async () => {
  const oldBody = '{"messages":[{"role":"developer","content":"hello agent"}]}';
  const newBody = '{"messages":[{"role":"developer","content":"hello coding agent"}]}';
  const detail = {
    request:{method:'POST', url:'/v1/chat/completions', headers:{'X-Custom':['in']}, body:oldBody, body_encoding:'utf-8'},
    attempts:[
      {request:{method:'POST',url:'https://upstream/v1/chat/completions',headers:{'X-Custom':['out']},body:newBody,body_encoding:'utf-8'},response:{status:403,headers:{'X-Error':['upstream']},body:'{"error":"no"}'}},
      {request:{method:'POST',url:'https://upstream/v1/chat/completions',headers:{},body:newBody,body_encoding:'utf-8'},response:{status:200,headers:{},body:'data: [DONE]\n\n'}}
    ],
    response:{status:200,headers:{'X-Request-Id':['123']},body:'{"ok":true}'}
  };
  const elements = new Map();
  const element = selector => {
    if(!elements.has(selector)) elements.set(selector, {innerHTML:'', textContent:'', addEventListener(){}});
    return elements.get(selector);
  };
  const context = vm.createContext({
    DetailDiff: Diff, esc, $:element, document:{addEventListener(){}}, MODAL_CLOSE:{},
    spinner:() => 'loading', openModal(){}, closeModal(){}, toast(){}, copyText:async()=>true,
    api:async () => ({detail})
  });
  const begin = pages.indexOf('let DETAIL_GENERATION'), end = pages.indexOf("$('#statWin').addEventListener", begin);
  vm.runInContext(pages.slice(begin, end), context);
  await vm.runInContext("openRequestDetail('fake-id')", context);
  const html = element('#requestDetailBody').innerHTML;
  assert.deepEqual([...html.matchAll(/<h3>([^<]+)<\/h3>/g)].map(m => m[1]), ['原始请求体','转发请求体','响应体']);
  assert(html.includes('<mark class="detail-change">coding </mark>'));
  for(const text of ['第 1 次转发','第 2 次转发','原始请求头','转发请求头','返回客户端的响应头','第 1 次上游响应','第 2 次上游响应']) assert(html.includes(text));
  const copies = vm.runInContext('DETAIL_COPY', context);
  assert(copies.includes(oldBody) && copies.includes(newBody) && copies.includes('{"ok":true}'));
  vm.runInContext("MODAL_CLOSE['#requestDetailModal']('#requestDetailModal')", context);
  assert.equal(vm.runInContext('DETAIL_COPY.length', context), 0);
  assert.equal(element('#requestDetailBody').textContent, '');
});

test('large response bodies open as pages and preserve complete copy', async () => {
  const body = 'data: ' + JSON.stringify({text:'x'.repeat(390000)}) + '\n\n';
  const detail = {
    request:{method:'POST', body:'{"messages":[]}'},
    attempts:[{request:{method:'POST', body:'{"messages":[]}'}, response:{status:200, body}}],
    response:{status:200, body}
  };
  const elements = new Map();
  const element = selector => {
    if(!elements.has(selector)) elements.set(selector, {innerHTML:'', textContent:'', addEventListener(type, handler){this[type] = handler;}});
    return elements.get(selector);
  };
  const context = vm.createContext({
    DetailDiff:Diff, esc, $:element, document:{addEventListener(){}, createTextNode(value){return value;}}, MODAL_CLOSE:{},
    spinner:()=>'', openModal(){}, closeModal(){}, toast(){}, copyText:async()=>true,
    api:async()=>({detail})
  });
  const begin = pages.indexOf('let DETAIL_GENERATION'), end = pages.indexOf("$('#statWin').addEventListener", begin);
  vm.runInContext(pages.slice(begin, end), context);
  await vm.runInContext("openRequestDetail('large-id')", context);
  const html = element('#requestDetailBody').innerHTML;
  assert(html.length < 300000, `initial HTML is ${html.length} characters`);
  assert.equal((html.match(/data-detail-more=/g) || []).length, 2);
  assert.equal(vm.runInContext('DETAIL_COPY', context).filter(value => value === body).length, 2);
  const pageLength = vm.runInContext('DETAIL_PAGES[0].value.length', context);
  let rendered = '', removed = false;
  const pre = {appendChild(value){rendered += value;}};
  const button = {
    dataset:{detailMore:'0'},
    closest(){return {querySelector(){return pre;}};},
    remove(){removed = true;}
  };
  for(let i = 0; i < 20 && !removed; i++){
    await element('#requestDetailBody').click({target:{closest(selector){return selector === '[data-detail-more]' ? button : null;}}});
  }
  assert(removed);
  assert.equal(rendered.length, pageLength - 60000);
  vm.runInContext("MODAL_CLOSE['#requestDetailModal']('#requestDetailModal')", context);
  assert.equal(vm.runInContext('DETAIL_PAGES.length', context), 0);
});

test('detail export includes every captured header and raw body', async () => {
  const id = '0123456789abcdef0123456789abcdef';
  const detail = {
    id, ts:1793610000,
    request:{method:'POST', url:'/v1/chat/completions', status:0,
      headers:{Authorization:['Bearer local-test-key'], 'X-Trace':['original']}, body_encoding:'utf-8', body:'{"messages":[{"content":"original"}]}'},
    attempts:[
      {request:{method:'POST',url:'https://upstream.test/v1/chat/completions',headers:{Authorization:['Bearer upstream-test-token']},body_encoding:'utf-8',body:'{"messages":[{"content":"forwarded"}]}'},
        response:{status:502,headers:{'X-Upstream':['first']},body_encoding:'base64',body:'/wAB'},error:'first attempt failed',proxy:{id:3,name:'<proxy-3>'}},
      {request:{method:'POST',url:'https://upstream.test/v1/chat/completions',headers:{'X-Retry':['second']},body_encoding:'utf-8',body:'{"messages":[{"content":"retry"}]}'},
        response:{status:200,headers:{'X-Upstream':['second']},body_encoding:'utf-8',body:'data: [DONE]\n\n'},error:''}
    ],
    response:{status:200,headers:{'X-Final':['client']},body_encoding:'utf-8',body:'{"ok":true}'}
  };
  const elements = new Map(), files = [];
  const element = selector => {
    if(!elements.has(selector)) elements.set(selector, {innerHTML:'', textContent:'', disabled:false, addEventListener(type, handler){this[type] = handler;}});
    return elements.get(selector);
  };
  let resolveDetail;
  const context = vm.createContext({
    DetailDiff:Diff, esc, $:element, document:{addEventListener(){}}, MODAL_CLOSE:{},
    spinner:()=>'', openModal(){}, closeModal(){}, toast(){}, copyText:async()=>true,
    api:()=>new Promise(resolve => { resolveDetail = resolve; }),
    saveFile:(name, text)=>files.push({name, text})
  });
  const begin = pages.indexOf('let DETAIL_GENERATION'), end = pages.indexOf("$('#statWin').addEventListener", begin);
  vm.runInContext(pages.slice(begin, end), context);
  const loading = vm.runInContext(`openRequestDetail('${id}')`, context);
  assert.equal(element('#requestDetailExport').disabled, true);
  resolveDetail({detail});
  await loading;
  assert.equal(element('#requestDetailExport').disabled, false);
  assert(element('#requestDetailBody').innerHTML.includes('出口：&lt;proxy-3&gt;'));
  element('#requestDetailExport').click();
  assert.equal(files[0].name, `codar2api-request-${id}.json`);
  assert.deepEqual(JSON.parse(files[0].text), detail);
  vm.runInContext("MODAL_CLOSE['#requestDetailModal']('#requestDetailModal')", context);
  assert.equal(element('#requestDetailExport').disabled, true);
  element('#requestDetailExport').click();
  assert.equal(files.length, 1, 'closed detail must not export stale data');
});
