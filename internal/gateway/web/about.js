'use strict';
/* 设置 → 关于，以及左上角的新版本提示。 */
let ABOUT_CHECKING = null;
const aboutVersionText = value => value ? (/^\d/.test(value) ? 'v' : '') + value : '未知';
function aboutStatus(kind, message){
  const status = $('#aboutStatus');
  status.className = 'notice ' + kind; status.textContent = message; status.hidden = false;
}
async function loadAbout(){
  try {
    const d = await api('/about');
    $('#aboutVersion').textContent = aboutVersionText(d.current_version);
  } catch(err){
    $('#aboutVersion').textContent = '读取失败';
    aboutStatus('bad', '读取当前版本失败：' + err.message);
  }
}
function renderAboutUpdate(d){
  $('#aboutVersion').textContent = aboutVersionText(d.current_version);
  $('#aboutLatest').textContent = aboutVersionText(d.latest_version);
  const badge = $('#brandUpdate');
  badge.hidden = d.status !== 'available';
  badge.title = d.status === 'available' ? '发现新版本 ' + aboutVersionText(d.latest_version) + '，点击查看' : '';
  const release = $('#aboutRelease'), prefix = 'https://github.com/AiXiaoc/codar2api/releases/';
  release.href = typeof d.release_url === 'string' && d.release_url.startsWith(prefix) ? d.release_url : prefix;
  release.hidden = !['available', 'unknown'].includes(d.status);
  release.textContent = d.status === 'available' ? '查看新版本' : '查看发布页面';
  if(d.status === 'available') aboutStatus('info', '发现新版本 ' + aboutVersionText(d.latest_version) + '，可前往发布页面查看和下载。');
  else if(d.status === 'current') aboutStatus('ok', '当前已是最新正式版本。');
  else if(d.status === 'newer') aboutStatus('ok', '当前版本高于 GitHub 最新正式版。');
  else aboutStatus('info', '已获取最新版本，但当前版本号无法自动比较，请前往发布页面确认。');
  if(d.checked_at){ $('#aboutCheckedAt').textContent = '上次检测：' + fmt(d.checked_at); $('#aboutCheckedAt').hidden = false; }
}
function checkAboutUpdate(silent = false){
  if(ABOUT_CHECKING) return ABOUT_CHECKING;
  const button = $('#aboutCheck');
  button.disabled = true; $('#aboutCheckText').textContent = '检测中…';
  if(!silent){
    $('#aboutRelease').hidden = true; $('#aboutLatest').textContent = '检测中…'; $('#aboutCheckedAt').hidden = true;
    aboutStatus('info', '正在连接 GitHub，检测最新正式版本…');
  }
  ABOUT_CHECKING = (async () => {
    try {
      const d = await api('/about/check-update', {});
      renderAboutUpdate(d);
      return d;
    } catch(err){
      if(!silent){
        $('#aboutLatest').textContent = '未获取';
        aboutStatus('bad', '检测失败：' + err.message);
      }
    }
  })().finally(() => {
    ABOUT_CHECKING = null;
    button.disabled = false; $('#aboutCheckText').textContent = '检测更新';
  });
  return ABOUT_CHECKING;
}
$('#aboutCheck').onclick = () => checkAboutUpdate();
$('#brandUpdate').onclick = () => { showTab('settings'); showSettingsTab('about'); };
