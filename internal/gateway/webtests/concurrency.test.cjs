'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const code = fs.readFileSync(path.join(__dirname, '../web/concurrency.js'), 'utf8');
const plain = value => JSON.parse(JSON.stringify(value));

function harness(){
  const elements = new Map(), calls = [], messages = [], closes = [];
  const $ = selector => {
    if(!elements.has(selector)) elements.set(selector, {value:'', hidden:true, disabled:false, textContent:'', valid:true, reportValidity(){ return this.valid; }});
    return elements.get(selector);
  };
  const context = vm.createContext({
    $, ROWS:[{uid:'a', max_concurrency:2}, {uid:'b', max_concurrency:4, enabled:false}, {uid:'c', max_concurrency:6}],
    SELECTED_ACCOUNTS:new Set(['a', 'b']), BATCH_BUSY:false, SCHED_EDIT:{uid:'a', max_concurrency:2, weight:7},
    MODAL_CLOSE:{}, closeMenu:() => {}, drawSelection:() => {},
    openModal:id => { $(id).hidden = false; }, closeModal:id => { $(id).hidden = true; closes.push(id); },
    load:async () => {}, toast:(...args) => messages.push(args),
    api:(url, body) => new Promise((resolve, reject) => calls.push({url, body:plain(body), resolve, reject}))
  });
  vm.runInContext(code, context);
  return {$, calls, messages, closes, context,
    open:all => $(all ? '#concurrencyAll' : '#concurrencySelected').onclick(),
    save:value => { $('#concurrencyLimit').value = String(value); return $('#concurrencyForm').onsubmit({preventDefault(){}}); },
    close:() => context.MODAL_CLOSE['#concurrencyModal']('#concurrencyModal')};
}

test('selected concurrency snapshots all checked accounts and retains unrelated drafts', async () => {
  const h = harness(); h.open(false);
  assert.equal(h.$('#concurrencyLimit').value, '', 'mixed limits require explicit input');
  assert.match(h.$('#concurrencyScope').textContent, /筛选隐藏/);
  h.context.SELECTED_ACCOUNTS.clear(); h.context.SELECTED_ACCOUNTS.add('c');
  const saving = h.save(5);
  assert.deepEqual(h.calls[0].body, {action:'set_concurrency', max_concurrency:5, uids:['a', 'b']});
  assert.equal(h.calls[0].url, '/accounts/batch');
  await h.save(8); h.close();
  assert.equal(h.calls.length, 1, 'double submit must not duplicate save');
  assert.equal(h.$('#concurrencyModal').hidden, false, 'saving cannot be dismissed');
  h.calls[0].resolve({ok:true, count:2, updated:2}); await saving;
  assert.deepEqual(plain(h.context.ROWS).map(a => a.max_concurrency), [5,5,6]);
  assert.deepEqual(plain(h.context.SCHED_EDIT), {uid:'a', max_concurrency:5, weight:7});
  assert.equal(h.context.BATCH_BUSY, false);
  assert.equal(h.$('#concurrencyModal').hidden, true);
});

test('all accounts ignores selection and supports unlimited even for disabled accounts', async () => {
  const h = harness(); h.context.SELECTED_ACCOUNTS.clear(); h.open(true);
  assert.match(h.$('#concurrencyScope').textContent, /全部 3 个账号/);
  const saving = h.save(0);
  assert.deepEqual(h.calls[0].body, {action:'set_concurrency', max_concurrency:0, all:true});
  h.calls[0].resolve({ok:true, count:3, updated:3}); await saving;
  assert.deepEqual(plain(h.context.ROWS).map(a => a.max_concurrency), [0,0,0]);
  assert.match(h.messages[0][0], /3 个账号.*不限/);
});

test('invalid limits do not submit; errors preserve the input and allow retry', async () => {
  const h = harness(); h.open(false);
  for(const value of ['', ' ', -1, 1.5, 1000001, 'abc']) await h.save(value);
  assert.equal(h.calls.length, 0);
  h.$('#concurrencyForm').valid = false; await h.save(3);
  assert.equal(h.calls.length, 0);
  h.$('#concurrencyForm').valid = true;
  const saving = h.save(3);
  h.calls[0].reject(new Error('账号已不存在')); await saving;
  assert.equal(h.$('#concurrencyMsg').textContent, '账号已不存在');
  assert.equal(h.$('#concurrencyMsg').hidden, false);
  assert.equal(h.$('#concurrencyLimit').value, '3');
  assert.equal(h.$('#concurrencyLimit').disabled, false);
  assert.equal(h.$('#concurrencySave').disabled, false);
  assert.equal(h.context.BATCH_BUSY, false);
  assert.deepEqual(plain(h.context.ROWS).map(a => a.max_concurrency), [2,4,6]);
  const retry = h.save(3);
  h.calls[1].resolve({ok:true, count:2, updated:0}); await retry;
  assert.match(h.messages.at(-1)[0], /2 个账号/);
});

test('refresh failure after saving is distinguished from save failure', async () => {
  const h = harness(); h.open(false);
  h.context.load = async () => { throw new Error('断网'); };
  const saving = h.save(3); h.calls[0].resolve({ok:true, count:2}); await saving;
  assert.equal(h.$('#concurrencyModal').hidden, true);
  assert.match(h.messages.at(-1)[0], /已保存.*刷新列表失败/);
  assert.deepEqual(plain(h.context.ROWS).map(a => a.max_concurrency), [3,3,6]);
});

test('empty scope does not open and cancelling does not save', async () => {
  const h = harness(); h.context.SELECTED_ACCOUNTS.clear(); h.open(false);
  assert.equal(h.$('#concurrencyModal').hidden, true);
  h.open(true); h.close(); await h.save(3);
  assert.equal(h.calls.length, 0);
  h.context.ROWS = []; h.open(true);
  assert.equal(h.$('#concurrencyModal').hidden, true);
});
