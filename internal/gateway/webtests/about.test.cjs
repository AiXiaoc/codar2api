'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const read = file => fs.readFileSync(path.join(__dirname, '../web', file), 'utf8');
const code = read('about.js'), html = read('dashboard.html'), pages = read('pages.js');

function harness(){
  const elements = new Map(), calls = [], navigation = [];
  const $ = selector => {
    if(!elements.has(selector)) elements.set(selector, {textContent:'', innerHTML:'', hidden:true, disabled:false});
    return elements.get(selector);
  };
  const context = vm.createContext({
    $, fmt:ts => 'time:' + ts,
    showTab:tab => navigation.push(['page', tab]),
    showSettingsTab:tab => navigation.push(['settings', tab]),
    api:(url, body) => new Promise((resolve, reject) => calls.push({url, body, resolve, reject}))
  });
  vm.runInContext(code, context);
  return {$, calls, context, navigation, check:() => $('#aboutCheck').onclick()};
}

test('opening About reads the running version without checking GitHub', async () => {
  const h = harness(), loading = vm.runInContext('loadAbout()', h.context);
  assert.equal(h.calls.length, 1);
  assert.equal(h.calls[0].url, '/about');
  assert.equal(h.calls[0].body, undefined);
  h.calls[0].resolve({current_version:'0.1.3'});
  await loading;
  assert.equal(h.$('#aboutVersion').textContent, 'v0.1.3');
  assert.equal(h.calls.length, 1);
});

test('manual detection displays update, matching, newer and unknown versions', async () => {
  for(const [status, message, link] of [
    ['available', '发现新版本', true], ['current', '已是最新', false],
    ['newer', '高于', false], ['unknown', '无法自动比较', true]
  ]){
    const h = harness(), checking = h.check();
    assert.equal(h.check(), checking, 'duplicate clicks should share the pending request');
    assert.equal(h.calls.length, 1, 'duplicate clicks must not start another check');
    assert.equal(h.calls[0].url, '/about/check-update');
    assert.equal(h.$('#aboutCheck').disabled, true);
    assert.equal(h.$('#aboutCheckText').textContent, '检测中…');
    h.calls[0].resolve({current_version:'0.1.3', latest_version:'v0.2.0', status,
      release_url:'https://github.com/AiXiaoc/codar2api/releases/tag/v0.2.0', checked_at:123});
    await checking;
    assert.equal(h.$('#aboutCheck').disabled, false);
    assert.equal(h.$('#aboutCheckText').textContent, '检测更新');
    assert.equal(h.$('#aboutLatest').textContent, 'v0.2.0');
    assert(h.$('#aboutStatus').textContent.includes(message));
    assert.equal(h.$('#aboutRelease').hidden, !link);
    assert.equal(h.$('#brandUpdate').hidden, status !== 'available');
    assert.equal(h.$('#aboutCheckedAt').textContent, '上次检测：time:123');
  }
});

test('automatic checks reveal the badge and clicking it opens About', async () => {
  const h = harness(), checking = vm.runInContext('checkAboutUpdate(true)', h.context);
  assert.equal(h.$('#aboutStatus').hidden, true, 'background checks should not interrupt the page');
  assert.equal(h.$('#brandUpdate').hidden, true);
  h.calls[0].resolve({current_version:'0.1.3', latest_version:'v0.1.4', status:'available', checked_at:123});
  await checking;
  assert.equal(h.$('#brandUpdate').hidden, false);
  assert(h.$('#brandUpdate').title.includes('v0.1.4'));
  h.$('#brandUpdate').onclick();
  assert.deepEqual(h.navigation, [['page', 'settings'], ['settings', 'about']]);
  assert(h.$('#aboutStatus').textContent.includes('发现新版本 v0.1.4'));
  const rechecking = h.check();
  h.calls[1].resolve({current_version:'0.1.4', latest_version:'v0.1.4', status:'current', checked_at:456});
  await rechecking;
  assert.equal(h.$('#brandUpdate').hidden, true, 'hide a previous badge when the version is current');
});

test('failed automatic checks stay quiet, retain a known update and allow retries', async () => {
  const h = harness(), checking = vm.runInContext('checkAboutUpdate(true)', h.context);
  h.calls[0].reject(new Error('连接失败'));
  await checking;
  assert.equal(h.$('#aboutStatus').hidden, true);
  assert.equal(h.$('#brandUpdate').hidden, true);
  assert.equal(h.$('#aboutCheck').disabled, false);
  const retry = h.check();
  h.calls[1].resolve({current_version:'0.1.3', latest_version:'v0.1.4', status:'available'});
  await retry;
  const previousMessage = h.$('#aboutStatus').textContent;
  const refreshing = vm.runInContext('checkAboutUpdate(true)', h.context);
  h.calls[2].reject(new Error('GitHub 限流'));
  await refreshing;
  assert.equal(h.$('#brandUpdate').hidden, false);
  assert.equal(h.$('#aboutStatus').textContent, previousMessage);
});

test('panel startup checks once and checks again every six hours', async () => {
  const h = harness(), timers = [];
  const resolved = () => Promise.resolve();
  Object.assign(h.context, {
    decorate:() => {}, loadAnnouncements:resolved, load:resolved, loadConcurrency:resolved, loadRate:() => {},
    localStorage:{getItem:() => null}, setInterval:(callback, ms) => timers.push({callback, ms})
  });
  vm.runInContext(pages.slice(pages.indexOf('/* ---- 启动 ---- */')), h.context);
  assert.equal(h.calls.length, 1);
  assert.equal(h.calls[0].url, '/about/check-update');
  h.calls[0].resolve({current_version:'0.1.3', latest_version:'v0.1.3', status:'current'});
  await vm.runInContext('ABOUT_CHECKING', h.context);
  const timer = timers.find(t => t.ms === 6 * 60 * 60 * 1000);
  assert(timer, 'long-running panels need periodic checks');
  const checking = timer.callback();
  assert.equal(h.calls.length, 2);
  h.calls[1].resolve({current_version:'0.1.3', latest_version:'v0.1.4', status:'available'});
  await checking;
  assert.equal(h.$('#brandUpdate').hidden, false);
});

test('a failed check clears stale results and can be retried', async () => {
  const h = harness(), checking = h.check();
  h.calls[0].reject(new Error('GitHub 暂时限制了请求'));
  await checking;
  assert.equal(h.$('#aboutLatest').textContent, '未获取');
  assert.equal(h.$('#aboutRelease').hidden, true);
  assert.equal(h.$('#aboutCheckedAt').hidden, true);
  assert.equal(h.$('#aboutCheck').disabled, false);
  assert.equal(h.$('#aboutStatus').className, 'notice bad');
  assert(h.$('#aboutStatus').textContent.includes('GitHub 暂时限制了请求'));
  const retrying = h.check();
  h.calls[1].resolve({current_version:'0.1.3', latest_version:'v0.1.3', status:'current', checked_at:456});
  await retrying;
  assert.equal(h.$('#aboutStatus').className, 'notice ok');
});

test('version values are plain text and release links stay on the project', async () => {
  const h = harness(), checking = h.check();
  const value = '<img src=x onerror=alert(1)>';
  h.calls[0].resolve({current_version:value, latest_version:value, status:'available', release_url:'javascript:alert(1)'});
  await checking;
  assert.equal(h.$('#aboutLatest').textContent, value);
  assert.equal(h.$('#aboutVersion').innerHTML, '');
  assert.equal(h.$('#aboutLatest').innerHTML, '');
  assert.equal(h.$('#aboutStatus').innerHTML, '');
  assert.equal(h.$('#aboutRelease').href, 'https://github.com/AiXiaoc/codar2api/releases/');
});

test('About is an accessible settings tab with no save controls', () => {
  assert.match(html, /id="brandUpdate"[^>]+aria-label="有新版本，查看更新详情"[^>]+hidden/);
  assert.match(html, /id="settings-tab-about"[^>]+data-settings-tab="about"[^>]+aria-controls="settings-panel-about"/);
  assert.match(html, /id="settings-panel-about"[^>]+role="tabpanel"[^>]+aria-labelledby="settings-tab-about"/);
  assert.match(html, /id="aboutStatus"[^>]+role="status"[^>]+aria-live="polite"/);
  assert(html.indexOf('/assets/about.js') < html.indexOf('/assets/pages.js'));
  const tabs = ['relay', 'about'].map(name => ({dataset:{settingsTab:name}, attrs:{}, setAttribute(k, v){ this.attrs[k] = v; }}));
  const panels = ['relay', 'about'].map(name => ({dataset:{settingsPanel:name}, hidden:true}));
  const actions = {hidden:false}; let loads = 0;
  const context = vm.createContext({
    $:() => actions, SETTINGS_TAB:'relay', SET:null, localStorage:{setItem:() => {}}, loadAbout:() => loads++,
    document:{querySelectorAll:selector => selector === '[data-settings-tab]' ? tabs : panels}
  });
  const start = pages.indexOf('function showSettingsTab('), end = pages.indexOf("\n$('#settingsNav')", start);
  vm.runInContext(pages.slice(start, end), context);
  vm.runInContext("showSettingsTab('about')", context);
  assert.equal(actions.hidden, true);
  assert.equal(panels[1].hidden, false);
  assert.equal(tabs[1].attrs['aria-selected'], 'true');
  assert.equal(loads, 1);
  vm.runInContext("showSettingsTab('relay')", context);
  assert.equal(actions.hidden, false);
});
