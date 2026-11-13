'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const pages = fs.readFileSync(path.join(__dirname, '../web/pages.js'), 'utf8');
const settingsCode = pages.slice(pages.indexOf('/* ---- 设置：'), pages.indexOf('/* ---- 模型价格 ---- */'));
const flush = () => new Promise(resolve => setImmediate(resolve));

function harness(){
  const elements = new Map(), controls = [], calls = [], messages = [];
  const element = selector => {
    if(!elements.has(selector)) elements.set(selector, {
      disabled:false, innerHTML:'', listeners:{},
      addEventListener(type, handler){ this.listeners[type] = handler; }
    });
    return elements.get(selector);
  };
  const addControl = (key, value, type = 'switch') => {
    const attrs = {'aria-checked':String(value)};
    const el = {
      dataset:{k:key}, type, value:String(value), disabled:false,
      classList:{contains:name => name === 'sw' && type === 'switch'},
      getAttribute:name => attrs[name], setAttribute:(name, value) => { attrs[name] = value; },
      closest:selector => selector === '.sw[data-k]' && type === 'switch' || selector === 'select[data-k="request_debug_scope"]' && key === 'request_debug_scope' ? el : null
    };
    controls.push(el);
    return el;
  };
  const initial = {
    use_codar_ua:true, auto_passthrough:true, dsh_identity_rewrite:true,
    identity_first_line_fill:false, historical_tool_as_user:false, detailed_debug:false, request_debug_scope:'errors', allow_remote_management:true,
    auto_sign:true, auto_claim:true, auto_renew_token:true, ip_block:true, restricted_request_block:false, allow_general_balance:true,
    codar_ua:'saved agent', max_attempts:3, account_scheduling_strategy:'default', scheduling_strategy:'round_robin', proxy_connect_retry:false, proxy_connect_switch:false
  };
  Object.entries(initial).forEach(([key, value]) => addControl(key, value,
    ['request_debug_scope', 'account_scheduling_strategy', 'scheduling_strategy'].includes(key) ? 'select' : typeof value === 'boolean' ? 'switch' : typeof value === 'number' ? 'number' : 'text'));
  let saved = {...initial};
  const context = vm.createContext({
    $:element, esc:String, SET_CUR:{}, localStorage:{getItem:() => null, setItem:() => {}},
    document:{querySelector:element, querySelectorAll:selector => selector === '#setGrid [data-k]' ? controls : []},
    toast:(...args) => messages.push(args),
    api:(url, patch) => new Promise((resolve, reject) => {
      calls.push({url, patch:patch === undefined ? undefined : JSON.parse(JSON.stringify(patch)), resolve, reject});
    })
  });
  vm.runInContext(settingsCode, context);
  context.initial = initial;
  vm.runInContext('SET = {settings:initial, schema:[]}', context);
  return {
    calls, messages, context, element,
    control:key => controls.find(el => el.dataset.k === key),
    click:key => element('#setGrid').listeners.click({target:controls.find(el => el.dataset.k === key)}),
    change:key => element('#setGrid').listeners.change({target:controls.find(el => el.dataset.k === key)}),
    save:() => element('#setSave').onclick(),
    resolve(index, normalized = {}){
      if(calls[index].patch !== undefined) saved = {...saved, ...calls[index].patch, ...normalized};
      calls[index].resolve({settings:{...saved, ...normalized}});
    },
    current:() => JSON.parse(vm.runInContext('JSON.stringify(SET.settings)', context))
  };
}

test('points priority uses its own options on the routing tab and saves with the form', async () => {
  const h = harness();
  vm.runInContext(`
    SET.schema = [
      {key:'account_scheduling_strategy', label:'账号调度策略', type:'select', default:'default'},
      {key:'scheduling_strategy', label:'调度策略', type:'select', default:'round_robin'}
    ];
    SET.account_scheduling_options = [
      {value:'default', label:'默认'}, {value:'least_points', label:'最少积分优先'}, {value:'most_points', label:'最多积分优先'}
    ];
    SET.scheduling_options = [{value:'round_robin', label:'轮询'}];
    drawSettings(SET.settings);
  `, h.context);
  const routing = h.element('[data-setting-fields="routing"]').innerHTML;
  assert.match(routing, /data-k="account_scheduling_strategy"[^>]*><option value="default" selected>默认<\/option><option value="least_points">最少积分优先<\/option><option value="most_points">最多积分优先<\/option><\/select>/);
  assert.match(routing, /data-k="scheduling_strategy"[^>]*><option value="round_robin" selected>轮询<\/option><\/select>/);
  h.control('account_scheduling_strategy').value = 'most_points';
  await h.change('account_scheduling_strategy');
  assert.equal(h.calls.length, 0);
  const save = h.save();
  await flush();
  assert.equal(h.calls[0].patch.account_scheduling_strategy, 'most_points');
  assert.equal(h.calls[0].patch.scheduling_strategy, 'round_robin');
  h.resolve(0);
  await save;
  assert.equal(h.current().account_scheduling_strategy, 'most_points');
});

test('every settings switch saves only its own value without submitting text drafts', async () => {
  for(const key of ['use_codar_ua', 'auto_passthrough', 'dsh_identity_rewrite', 'identity_first_line_fill',
    'historical_tool_as_user', 'detailed_debug', 'allow_remote_management', 'auto_sign', 'auto_claim', 'auto_renew_token', 'ip_block', 'restricted_request_block', 'allow_general_balance', 'proxy_connect_retry', 'proxy_connect_switch']){
    const h = harness(), control = h.control(key), before = control.getAttribute('aria-checked') === 'true';
    h.control('codar_ua').value = 'unsaved agent';
    h.control('max_attempts').value = '5';
    const clicking = h.click(key);
    assert.equal(control.disabled, true);
    assert.equal(h.element('#setSave').disabled, true);
    await flush();
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].url, '/settings');
    assert.deepEqual(h.calls[0].patch, {[key]:!before});
    h.resolve(0);
    await clicking;
    assert.equal(h.current()[key], !before);
    assert.equal(control.getAttribute('aria-checked'), String(!before));
    assert.equal(control.disabled, false);
    assert.equal(h.element('#setSave').disabled, false);
    assert.equal(h.control('codar_ua').value, 'unsaved agent');
    assert.equal(h.control('max_attempts').value, '5');
    assert.equal(h.current().codar_ua, 'saved agent');
    assert.equal(h.calls.length, 1, 'saving must not require a follow-up request');
  }
});

test('request blocklist can be cleared and failed clears remain retryable', async () => {
  const h = harness();
  vm.runInContext('SET.request_blocklist_count = 2', h.context);
  const button = {disabled:false, textContent:'清空黑名单（2）'};
  const event = {target:{closest:selector => selector === '[data-clear-request-blocklist]' ? button : null}};
  let clearing = h.element('#setGrid').listeners.click(event);
  assert.equal(button.disabled, true);
  assert.equal(h.calls[0].url, '/request-blocklist/clear');
  h.calls[0].reject(new Error('disk unavailable'));
  await clearing;
  assert.equal(button.disabled, false);
  assert.equal(vm.runInContext('SET.request_blocklist_count', h.context), 2);
  clearing = h.element('#setGrid').listeners.click(event);
  h.calls[1].resolve({ok:true});
  await clearing;
  assert.equal(button.disabled, true);
  assert.equal(button.textContent, '清空黑名单（0）');
  assert.equal(vm.runInContext('SET.request_blocklist_count', h.context), 0);
});

test('request debug scope saves immediately and restores on failure', async () => {
  const h = harness(), scope = h.control('request_debug_scope');
  scope.value = 'all';
  const saving = h.change('request_debug_scope');
  await flush();
  assert.equal(scope.disabled, true);
  assert.deepEqual(h.calls[0].patch, {request_debug_scope:'all'});
  h.resolve(0);
  await saving;
  assert.equal(scope.disabled, false);
  assert.equal(h.current().request_debug_scope, 'all');
  scope.value = 'success';
  const failing = h.change('request_debug_scope');
  await flush();
  h.calls[1].reject(new Error('disk unavailable'));
  await failing;
  assert.equal(scope.value, 'all');
  assert.equal(h.current().request_debug_scope, 'all');
});

test('rapid clicks are serialized and cannot overwrite another switch', async () => {
  const h = harness();
  const first = h.click('identity_first_line_fill');
  await h.click('identity_first_line_fill');
  const second = h.click('auto_passthrough');
  await flush();
  assert.equal(h.calls.length, 1);
  await h.save();
  assert.equal(h.calls.length, 1, 'manual save is blocked while a switch is saving');
  h.resolve(0);
  await first;
  await flush();
  assert.equal(h.element('#setSave').disabled, true);
  assert.equal(h.calls.length, 2);
  assert.deepEqual(h.calls[1].patch, {auto_passthrough:false});
  h.resolve(1);
  await second;
  assert.equal(h.current().identity_first_line_fill, true);
  assert.equal(h.current().auto_passthrough, false);
  assert.equal(h.element('#setReset').disabled, false);
  assert.equal(vm.runInContext('SET_CUR.identity_first_line_fill && !SET_CUR.auto_passthrough', h.context), true);
});

test('a failed switch is rolled back and later queued settings still save', async () => {
  const h = harness();
  const first = h.click('detailed_debug');
  const second = h.click('auto_sign');
  await flush();
  h.calls[0].reject(new Error('disk unavailable'));
  await first;
  await flush();
  assert.equal(h.control('detailed_debug').getAttribute('aria-checked'), 'false');
  assert.equal(h.control('detailed_debug').disabled, false);
  assert.equal(h.current().detailed_debug, false);
  assert(h.messages.some(([message, bad]) => bad && message.includes('disk unavailable')));
  assert.equal(h.calls.length, 2);
  h.resolve(1);
  await second;
  assert.equal(h.current().auto_sign, false);
  assert.equal(h.element('#setSave').disabled, false);
});

test('manual save persists other fields and preserves edits made during the request', async () => {
  const h = harness();
  h.control('codar_ua').value = ' submitted agent ';
  h.control('max_attempts').value = '5';
  h.control('scheduling_strategy').value = 'least_connections';
  const saving = h.save();
  await flush();
  assert.equal(h.calls[0].patch.max_attempts, 5);
  assert.equal(h.calls[0].patch.scheduling_strategy, 'least_connections');
  h.control('max_attempts').value = '7';
  const switching = h.click('identity_first_line_fill');
  h.resolve(0, {codar_ua:'submitted agent'});
  await saving;
  await flush();
  assert.equal(h.control('codar_ua').value, 'submitted agent');
  assert.equal(h.control('max_attempts').value, '7');
  assert.equal(h.control('identity_first_line_fill').getAttribute('aria-checked'), 'true');
  assert.equal(h.calls.length, 2);
  h.resolve(1);
  await switching;
  assert.equal(h.current().max_attempts, 5);
  assert.equal(h.current().identity_first_line_fill, true);
  assert.equal(h.control('max_attempts').value, '7');
});

test('reopening settings waits for pending saves and discards stale reload responses', async () => {
  const h = harness();
  vm.runInContext('let renderedSettings = null; drawSettings = values => { renderedSettings = values; }', h.context);
  const first = h.click('identity_first_line_fill');
  await flush();
  const reloading = vm.runInContext('loadSettings()', h.context);
  await flush();
  assert.equal(h.calls.length, 1, 'reload must wait for the in-flight save');
  h.resolve(0);
  await first;
  await flush();
  assert.equal(h.calls.length, 2);
  assert.equal(h.calls[1].patch, undefined);
  const second = h.click('auto_passthrough');
  await flush();
  h.resolve(2);
  await second;
  h.resolve(1, {auto_passthrough:true});
  await flush();
  assert.equal(vm.runInContext('renderedSettings', h.context), null, 'stale response must not redraw switches');
  assert.equal(h.calls.length, 4);
  assert.equal(h.calls[3].patch, undefined);
  h.resolve(3);
  await reloading;
  assert.equal(vm.runInContext('renderedSettings.identity_first_line_fill && !renderedSettings.auto_passthrough', h.context), true);
});
