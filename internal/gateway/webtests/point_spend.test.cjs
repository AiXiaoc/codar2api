'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const web = path.join(__dirname, '../web');
const code = fs.readFileSync(path.join(web, 'point-spend.js'), 'utf8');
const accounts = fs.readFileSync(path.join(web, 'accounts.js'), 'utf8');
const drawer = fs.readFileSync(path.join(web, 'drawer.js'), 'utf8');
const html = fs.readFileSync(path.join(web, 'dashboard.html'), 'utf8');
const sample = patch => ({uid:'a', since:100, at:1000, spent_hour:10, spent_today:20, spent_day:30, spent_week:70, spent_total:150.125,
  source:'ledger', complete:true, error:'', ...patch});

function harness(){
  let clock = 1000000;
  const calls = [], saved = new Map(), selector = {value:''};
  const cell = {dataset:{spendUid:'a'}, innerHTML:''}, second = {dataset:{spendUid:'b'}, innerHTML:''};
  const total = {dataset:{spendTotal:'a'}, innerHTML:''};
  const esc = value => String(value ?? '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('"','&quot;');
  const context = vm.createContext({
    Date:class extends Date { static now(){return clock;} }, Promise, Set, Map, console,
    ROWS:[{uid:'a', username:'A', nickname:'A', enabled:true, added_at:100}, {uid:'b', enabled:false, added_at:100}],
    $:s => s === '#spendRange' ? selector : null,
    document:{querySelectorAll:s => s === '[data-spend-uid]' ? [cell,second] : s === '[data-spend-total]' ? [total] : []},
    localStorage:{getItem:k => saved.get(k), setItem:(k,v) => saved.set(k,v)},
    esc, fmt:v => 'time-' + v, fmtS:v => 'time-' + v, nf:String, fmtTok:String, pctS:String, ago:() => '刚刚',
    num:v => typeof v === 'number' ? v : null, yuan:v => '¥' + v, tip:() => '', ic:() => '',
    SELECTED_ACCOUNTS:new Set(), SEL:null, BATCH_BUSY:false, realmBadge:() => '', proxyName:() => '',
    concurrencyCell:() => '0 / 1', stateCell:() => '正常', quotaCell:() => '100 积分',
    expCell:() => '1 天', accountSwitch:() => '启用', creditPackages:() => [], checkin:() => ({}),
    kpi:(k,v,s) => '<div>' + k + v + (s || '') + '</div>',
    api:url => new Promise((resolve,reject) => calls.push({url,resolve,reject})),
  });
  vm.runInContext(code, context, {filename:'point-spend.js'});
  return {context,calls,selector,cell,second,total,saved,advance:ms => {clock+=ms;}};
}

test('time selector changes each account without another request; pending requests coalesce', async () => {
  const h=harness();
  const first=h.context.loadPointSpending(), second=h.context.loadPointSpending();
  assert.equal(first,second); assert.equal(h.calls.length,1);
  h.calls[0].resolve({accounts:[sample(),sample({uid:'b',spent_today:2})]}); await first;
  assert.match(h.cell.innerHTML, /<b>20<\/b>/); assert.match(h.second.innerHTML, /<b>2<\/b>/);
  for(const [value,want] of [['hour',10],['day',30],['week',70],['today',20]]){
    h.selector.onchange({target:{value}});
    assert.match(h.cell.innerHTML, new RegExp('<b>'+want+'<\\/b>'));
  }
  assert.equal(h.calls.length,1);
  assert.equal(h.saved.get('gateway-point-spend-range'),'today');
  assert.match(h.total.innerHTML,/150\.125/); assert.match(h.total.innerHTML,/自 time-100 导入起/);
});

test('cache expires, manual refresh bypasses it, and errors keep the last amount', async () => {
  const h=harness(); let pending=h.context.loadPointSpending();
  h.calls[0].resolve({accounts:[sample(),sample({uid:'b'})]}); await pending;
  await h.context.loadPointSpending(); assert.equal(h.calls.length,1);
  pending=h.context.loadPointSpending(true); assert.match(h.calls[1].url,/force=1/);
  h.calls[1].reject(new Error('upstream failed')); await pending;
  assert.match(h.cell.innerHTML, /<b>20<\/b>/); assert.match(h.cell.innerHTML,/上次结果 · 查询失败/);
  assert.match(h.total.innerHTML,/upstream failed/);
  h.advance(60001); pending=h.context.loadPointSpending(); assert.equal(h.calls.length,3);
  h.calls[2].resolve({accounts:[sample({spent_today:21}),sample({uid:'b'})]}); await pending;
  assert.match(h.cell.innerHTML, /<b>21<\/b>/); assert.doesNotMatch(h.cell.innerHTML,/查询失败/);
});

test('unknown results use a dash, a true zero is shown, and incomplete history is marked', async () => {
  const h=harness(); let pending=h.context.loadPointSpending();
  h.calls[0].resolve({accounts:[sample({spent_today:null,spent_total:null,error:'page failed'}),sample({uid:'b',spent_today:0})]}); await pending;
  assert.match(h.cell.innerHTML,/—/); assert.doesNotMatch(h.cell.innerHTML,/<b>0<\/b>/);
  assert.match(h.second.innerHTML,/<b>0<\/b>/);
  pending=h.context.loadPointSpending(true);
  h.calls[1].resolve({accounts:[sample({source:'gateway',complete:false}),sample({uid:'b'})]}); await pending;
  assert.match(h.total.innerHTML,/网关扣费/); assert.match(h.total.innerHTML,/早期记录可能不完整/);
});

test('account rows and embedded page contain the points column and all four windows', async () => {
  const h=harness(); const pending=h.context.loadPointSpending();
  h.calls[0].resolve({accounts:[sample(),sample({uid:'b'})]}); await pending;
  vm.runInContext(accounts.slice(accounts.indexOf('function row(a)'),accounts.indexOf('\nconst isOk')),h.context);
  const row=h.context.row(h.context.ROWS[0]);
  assert.match(row,/data-spend-uid="a"/); assert.match(row,/<b>20<\/b>/);
  assert.equal((row.match(/<td\b/g) || []).length,8);
  assert.match(html,/积分消耗<select id="spendRange"/);
  for(const value of ['hour','today','day','week']) assert.match(html,new RegExp('value="'+value+'"'));
  assert.ok(html.indexOf('/assets/accounts.js') < html.indexOf('/assets/point-spend.js'));
  assert.ok(html.indexOf('/assets/point-spend.js') < html.indexOf('/assets/drawer.js'));
});

test('account usage renders lifetime points without estimated currency', async () => {
  const h=harness(); const pending=h.context.loadPointSpending();
  h.calls[0].resolve({accounts:[sample(),sample({uid:'b'})]}); await pending;
  let rendered;
  if(drawer.includes('function usageView(')){
    const start=drawer.indexOf('const tokenLine =');
    vm.runInContext(drawer.slice(start,drawer.indexOf('const TYPE_NAME',start)),h.context);
    rendered=h.context.usageView({summary:{calls:2,input_tokens:10,output_tokens:4,cost:999},
      models:[{model:'m',calls:2,input_tokens:10,output_tokens:4,cost:999}],daily:[]},undefined,h.context.ROWS[0]);
  } else {
    vm.runInContext(drawer.slice(drawer.indexOf('function dUsage(a)'),drawer.indexOf('\nfunction dProxy(')),h.context);
    rendered=h.context.dUsage(h.context.ROWS[0]);
  }
  assert.match(rendered,/累计消耗积分/); assert.match(rendered,/150\.125/);
  assert.match(rendered,/自 time-100 导入起/); assert.doesNotMatch(rendered,/¥|花费/);
});
