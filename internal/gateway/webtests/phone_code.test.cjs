'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/add.js'), 'utf8');
const phoneCode = source.slice(source.indexOf('function addProxyID()'), source.indexOf('function addStep('))
  + source.slice(source.indexOf('const PH ='), source.indexOf('async function phLogin('));

test('verification code can be requested again as soon as sending finishes', async () => {
  const button = {disabled:false, textContent:'获取验证码'};
  const number = {value:'13800138000'};
  const message = {innerHTML:''};
  const fields = {'#phSend':button, '#phNum':number, '#phMsg':message, '#phCode':{focus(){}},
    '#addProxy':{disabled:false, value:'0'}, '#addProxyMsg':{textContent:''}};
  const requests = [], pendingCaptcha = [], timers = [];
  const context = vm.createContext({
    $:selector => fields[selector], esc:value => value, button,
    api:async (url, body) => { requests.push({url, body}); return {ok:true, message:'验证码已发送', phone:'138****8000'}; },
    setTimeout:(callback, delay) => { timers.push(delay); }
  });
  vm.runInContext(phoneCode, context);
  context.nextCaptcha = () => new Promise(resolve => pendingCaptcha.push(resolve));
  vm.runInContext('solveCaptcha = nextCaptcha', context);

  const first = vm.runInContext('phSend(button)', context);
  assert.equal(button.disabled, true);
  await vm.runInContext('phSend(button)', context);
  assert.equal(pendingCaptcha.length, 1, 'a second click while sending must not start another request');
  pendingCaptcha.shift()({lot_number:'first'});
  await first;
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, '获取验证码');
  assert.equal(requests.length, 1);

  const second = vm.runInContext('phSend(button)', context);
  assert.equal(button.disabled, true);
  pendingCaptcha.shift()({lot_number:'second'});
  await second;
  assert.equal(button.disabled, false);
  assert.equal(requests.length, 2);
  assert(requests.every(request => request.url === '/phone/code' && request.body.proxy_id === 0));
  assert(timers.every(delay => delay < 1000), 'there must be no resend countdown');

  vm.runInContext("PH.mode = 'ip'; PH.needPhone = false; PH.flow = 'flow-id'; phSendReady(button)", context);
  assert.equal(button.textContent, '获取安全验证码');
  assert.equal(button.disabled, false);
});
