const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const code = fs.readFileSync(path.join(__dirname, '../web/accounts.js'), 'utf8');
const context = vm.createContext({chip:() => '', esc:String, dur2s:() => '60 秒'});
vm.runInContext(code.slice(0, code.indexOf('\nfunction expCell(')), context);
function render(fields){
  context.account = {enabled:true, paid_block:'余额已用完', free_models:[], ...fields};
  return vm.runInContext('stateCell(account)', context);
}

test('depleted account without free models shows suspended scheduling', () => {
  const html = render({});
  assert.match(html, /余额不足，已暂停调度/);
  assert.doesNotMatch(html, /仅免费模型|免费模型照常/);
});

test('depleted account with active free models remains free-only', () => {
  const html = render({free_models:['free-model']});
  assert.match(html, /仅免费模型 · 余额已用完/);
  assert.doesNotMatch(html, /已暂停调度/);
});

test('balance cooldown distinguishes accounts with and without free models', () => {
  const cooldown = {cooldown_kind:'balance', cooldown_until:Date.now() / 1000 + 60};
  assert.match(render(cooldown), /余额冷却 · 已暂停调度/);
  assert.match(render({...cooldown, free_models:['free-model']}), /余额冷却 · 仅免费模型/);
});
