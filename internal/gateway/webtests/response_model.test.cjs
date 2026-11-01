const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const read = name => fs.readFileSync(path.join(__dirname, '../web', name), 'utf8');
const ui = read('ui.js'), drawer = read('drawer.js'), pages = read('pages.js'), css = read('app2.css');
const helperCode = ui.slice(ui.indexOf('function requestModelLabel('), ui.indexOf('\nconst fmt ='));
const accountCode = drawer.slice(drawer.indexOf('function recItem('), drawer.indexOf('\nfunction dRequests('));
const statsCode = pages.slice(pages.indexOf('async function loadStats('), pages.indexOf('\nlet DETAIL_GENERATION'));
const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c]));

async function render(record){
  const elements = new Map();
  const element = selector => {
    if(!elements.has(selector)) elements.set(selector, {innerHTML:''});
    return elements.get(selector);
  };
  const context = vm.createContext({
    esc, $:element, document:{querySelectorAll:() => []}, STAT_H:24, API_NAME:{},
    api:async () => ({total:{requests:1, stream:0}, models:[], accounts:[], recent:[record]}),
    kpi:() => '', tip:() => '', pctS:() => '', fmtTok:() => '', round1:() => '',
    yuan:() => '', n:() => '', rateCls:() => '', fmt:() => '', fmtT:() => '',
    chip:() => '', tokenLine:() => ''
  });
  vm.runInContext(helperCode + '\n' + accountCode + '\n' + statsCode, context);
  context.record = record;
  const accountHTML = vm.runInContext('recItem(record)', context);
  await vm.runInContext('loadStats()', context);
  return [accountHTML, element('#statRecent').innerHTML];
}

test('both request lists show the exact mismatched model in yellow', async () => {
  const record = {model:'MODEL', requested_model:' MODEL ', response_model:'model', ok:true, status:200};
  for(const html of await render(record)){
    assert(html.includes('<span class="model-requested"> MODEL </span>'));
    assert(html.includes('-&gt; <span class="model-returned">model</span>'));
    assert(html.includes('（返回模型不一致）'));
  }
  assert.match(css, /\.model-returned\{[^}]*color:\s*#b47b00/i);
});

test('matching or unavailable models do not show a mismatch', async () => {
  for(const record of [
    {model:'auto', requested_model:'auto', response_model:'auto', ok:true},
    {model:'auto', requested_model:'auto', ok:true},
    {model:'auto', ok:true}
  ]) for(const html of await render(record)){
    assert(!html.includes('model-returned'));
    assert(!html.includes('返回模型不一致'));
  }
});

test('case and whitespace differences count and model text is escaped', async () => {
  for(const record of [
    {model:'A', requested_model:'A', response_model:'a', ok:true},
    {model:'A', requested_model:'A', response_model:'A ', ok:true},
    {model:'A', requested_model:'A', response_model:'', ok:true}
  ]) for(const html of await render(record)) assert(html.includes('返回模型不一致'));
  for(const html of await render({model:'<ask>', requested_model:'<ask>', response_model:'<img src=x onerror=alert(1)>', ok:true})){
    assert(html.includes('&lt;ask&gt;'));
    assert(html.includes('&lt;img src=x onerror=alert(1)&gt;'));
    assert(!html.includes('<img'));
  }
});
