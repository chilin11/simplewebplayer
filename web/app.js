(function () {
  'use strict';

  const U = window.SWPUtil;
  const Api = window.SWPApi;

  const gate = U.createRequestGate();

  const state = {
    currentUser: null,
    activeView: 'player',
    roots: [],
    selectedRootId: null,
    currentPath: '',
    currentEntries: [],
    filterQuery: '',
    isBrowsing: false,
    libraryDirty: false,
    // 授权列表版本：每次发起 /api/library 请求都会领取一个递增版本号，
    // 只有版本号大于已应用版本的响应才能覆盖 state.roots，防止旧响应失序覆盖新数据。
    rootsIssuedVersion: 0,
    rootsAppliedVersion: 0,
    // 管理台标记“片库数据已变”时的版本水位；只有在此之后发起的请求才能清除脏标记。
    libraryDirtySince: 0,
    activeVideo: null,
    playbackToken: 0,
    isUnloadingVideo: false,
    // 当前影片的下载预检是否进行中（仅绑定 state.activeVideo，切换 / 关闭即取消）
    playerDownloadBusy: false,
    // 列表行下载预检：键为 rootId + path，各自独立，防止重复点击
    entryDownloads: Object.create(null),
    loggingIn: false,
    loggingOut: false,
  };

  // 视图容器
  const bootView = document.getElementById('boot-view');
  const bootMessage = document.getElementById('boot-message');
  const bootRetryBtn = document.getElementById('boot-retry-btn');
  const loginView = document.getElementById('login-view');
  const appShell = document.getElementById('app-shell');

  // 登录表单
  const loginForm = document.getElementById('login-form');
  const loginUsername = document.getElementById('login-username');
  const loginPassword = document.getElementById('login-password');
  const loginSubmitBtn = document.getElementById('login-submit-btn');
  const loginAlert = document.getElementById('login-alert');

  // 顶栏与导航
  const navPlayerBtn = document.getElementById('nav-player-btn');
  const navAdminBtn = document.getElementById('nav-admin-btn');
  const currentUsernameEl = document.getElementById('current-username');
  const currentRoleBadge = document.getElementById('current-role-badge');
  const logoutBtn = document.getElementById('logout-btn');
  const playerView = document.getElementById('player-view');
  const adminView = document.getElementById('admin-view');

  // 播放台：根快捷入口与目录浏览
  const rootsBar = document.getElementById('roots-bar');
  const rootsAlert = document.getElementById('roots-alert');
  const refreshRootsBtn = document.getElementById('refresh-roots-btn');
  const browserSummary = document.getElementById('browser-summary');
  const browseUpBtn = document.getElementById('browse-up-btn');
  const browseRefreshBtn = document.getElementById('browse-refresh-btn');
  const breadcrumbsNav = document.getElementById('breadcrumbs');
  const browserToolbar = document.getElementById('browser-toolbar');
  const browserFilterInput = document.getElementById('browser-filter');
  const browserAlert = document.getElementById('browser-alert');
  const browserLoading = document.getElementById('browser-loading');
  const browserEmpty = document.getElementById('browser-empty');
  const browserList = document.getElementById('browser-list');

  // 原生播放器
  const videoEl = document.getElementById('video-player');
  const playerPlaceholder = document.getElementById('player-placeholder');
  const playerHeading = document.getElementById('player-heading');
  const playerCurrentMeta = document.getElementById('player-current-meta');
  const playerNowTitle = document.getElementById('player-now-title');
  const playerLocation = document.getElementById('player-location');
  const playerLocationText = document.getElementById('player-location-text');
  const playerActions = document.getElementById('player-actions');
  const playerDownloadBtn = document.getElementById('player-download-btn');
  const playerDownloadLabel = document.getElementById('player-download-label');
  const playerDownloadAlert = document.getElementById('player-download-alert');
  const playerReloadBtn = document.getElementById('player-reload-btn');
  const playerCloseBtn = document.getElementById('player-close-btn');
  const playerErrorBox = document.getElementById('player-error');

  const adminController = window.SWPAdmin.create({
    getCurrentUser: function () {
      return state.currentUser;
    },
    onLibraryDataChanged: function () {
      state.libraryDirty = true;
      state.libraryDirtySince = state.rootsIssuedVersion;
    },
    onCurrentUserUpdated: function (updatedUser) {
      if (!state.currentUser || !updatedUser) {
        return;
      }
      if (String(updatedUser.id) !== String(state.currentUser.id)) {
        return;
      }
      state.currentUser = updatedUser;
      renderUserBadge();
      if (updatedUser.role !== 'admin' && state.activeView === 'admin') {
        switchView('player');
        U.showToast('当前账号角色已变更，已返回片库', 'error');
      }
    },
  });

  function findRootById(rootId) {
    if (rootId === null || rootId === undefined) {
      return null;
    }
    const target = String(rootId);
    for (let i = 0; i < state.roots.length; i += 1) {
      if (String(state.roots[i].id) === target) {
        return state.roots[i];
      }
    }
    return null;
  }

  function nextRootsVersion() {
    state.rootsIssuedVersion += 1;
    return state.rootsIssuedVersion;
  }

  // 仅当响应版本更新时才写入授权列表；返回是否实际应用
  function applyRootsResponse(version, res) {
    if (version <= state.rootsAppliedVersion) {
      return false;
    }
    state.rootsAppliedVersion = version;
    state.roots = Array.isArray(res && res.roots) ? res.roots : [];
    if (version > state.libraryDirtySince) {
      state.libraryDirty = false;
    }
    return true;
  }

  function stopPlayerIfRootRevoked() {
    if (state.activeVideo && !findRootById(state.activeVideo.rootId)) {
      stopAndResetPlayer();
      U.showToast('当前视频所属目录授权已撤销，已停止播放', 'error');
    }
  }

  function renderUserBadge() {
    if (!state.currentUser) {
      currentUsernameEl.textContent = '';
      currentRoleBadge.textContent = '';
      navAdminBtn.hidden = true;
      return;
    }
    currentUsernameEl.textContent = state.currentUser.username || '';
    const isAdmin = state.currentUser.role === 'admin';
    currentRoleBadge.textContent = U.formatRoleLabel(state.currentUser.role);
    currentRoleBadge.className = isAdmin ? 'badge badge-primary' : 'badge';
    navAdminBtn.hidden = !isAdmin;
  }

  function stopAndResetPlayer() {
    // 若焦点在即将隐藏的影片操作区，先把焦点安置到播放区标题，避免丢到 body
    const hadActionFocus = playerActions.contains(document.activeElement);
    resetPlayerDownload();
    state.playbackToken += 1;
    state.activeVideo = null;
    state.isUnloadingVideo = true;
    try {
      videoEl.pause();
      videoEl.removeAttribute('src');
      videoEl.load();
    } finally {
      window.setTimeout(function () {
        state.isUnloadingVideo = false;
      }, 0);
    }
    videoEl.hidden = true;
    playerPlaceholder.hidden = false;
    playerActions.hidden = true;
    playerNowTitle.textContent = '尚未选择视频';
    playerLocation.hidden = true;
    playerLocationText.textContent = '';
    playerCurrentMeta.textContent = '在目录中点选视频即可播放或下载';
    playerCurrentMeta.hidden = false;
    U.clearAlert(playerErrorBox);
    renderBrowserEntries();
    if (hadActionFocus) {
      playerHeading.focus({ preventScroll: true });
    }
  }

  // 只展示接口提供的真实信息：扩展名标签与文件大小（不推测时长 / 编码）
  function renderNowMeta(entry) {
    const nodes = [];
    const ext = getFileExtLabel(entry.name);
    if (ext) {
      nodes.push(U.el('span', { className: 'ext-chip', text: ext }));
    }
    if (typeof entry.size === 'number') {
      nodes.push(U.el('span', { className: 'now-size', text: U.formatBytes(entry.size) }));
    }
    playerCurrentMeta.replaceChildren.apply(playerCurrentMeta, nodes);
    playerCurrentMeta.hidden = nodes.length === 0;
  }

  function playVideo(root, entry) {
    if (!root || !entry) {
      return;
    }
    const streamUrl = Api.buildStreamUrl(root.id, entry.path);
    state.playbackToken += 1;
    state.activeVideo = {
      rootId: root.id,
      rootLabel: root.label,
      name: entry.name,
      path: entry.path,
      size: entry.size,
      streamUrl: streamUrl,
    };

    // 切换影片：取消上一部的下载预检，下载状态只跟随当前真实选中的素材
    resetPlayerDownload();
    U.clearAlert(playerErrorBox);
    // 文件名与路径原样展示（样式中保留空白），只显示接口提供的真实信息
    const parentPath = U.getParentPath(entry.path);
    playerNowTitle.textContent = entry.name;
    playerLocationText.textContent = parentPath ? root.label + ' / ' + parentPath : root.label;
    playerLocation.hidden = false;
    renderNowMeta(entry);

    playerPlaceholder.hidden = true;
    videoEl.hidden = false;
    playerActions.hidden = false;

    state.isUnloadingVideo = false;
    videoEl.src = streamUrl;
    videoEl.load();

    const playPromise = videoEl.play();
    if (playPromise && typeof playPromise.catch === 'function') {
      playPromise.catch(function () {
        // 浏览器拦截自动播放时保持就绪状态，由用户点击播放按钮
      });
    }

    renderBrowserEntries();

    if (window.innerWidth <= 1024) {
      videoEl.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
  }

  function buildMediaErrorMessage(mediaError) {
    const code = mediaError ? mediaError.code : 0;
    if (code === 2) {
      return '视频流加载中断：可能是网络连接异常、NAS 暂不可用，或登录状态 / 目录访问权限已失效。';
    }
    if (code === 3) {
      return '视频解码失败：文件可能损坏，或该视频的音视频编码（如 HEVC/H.265、AC3/DTS）不被当前浏览器原生支持。';
    }
    return '当前浏览器无法播放该视频：可能是封装容器或编码（如 MKV、HEVC、AC3 等）不被浏览器原生支持，也可能是文件已被移除或授权已撤销。请优先使用 MP4（H.264/AAC）或更换支持的浏览器。';
  }

  videoEl.addEventListener('loadedmetadata', function () {
    if (state.activeVideo) {
      U.clearAlert(playerErrorBox);
    }
  });

  videoEl.addEventListener('playing', function () {
    if (state.activeVideo) {
      U.clearAlert(playerErrorBox);
    }
  });

  videoEl.addEventListener('error', async function () {
    if (state.isUnloadingVideo || !state.activeVideo) {
      return;
    }
    const mediaError = videoEl.error;
    if (mediaError && mediaError.code === 1) {
      return;
    }

    const token = state.playbackToken;
    const message = buildMediaErrorMessage(mediaError);
    const retryBtn = U.el('button', {
      className: 'btn btn-secondary btn-sm',
      text: '重试播放',
      props: { type: 'button' },
      on: {
        click: function () {
          if (state.activeVideo) {
            const root = findRootById(state.activeVideo.rootId) || {
              id: state.activeVideo.rootId,
              label: state.activeVideo.rootLabel,
            };
            playVideo(root, state.activeVideo);
          }
        },
      },
    });
    U.setAlert(playerErrorBox, message, 'error', retryBtn);

    // 若为网络或资源错误，静默核验会话，避免将 401 误判为编码不支持
    if (!mediaError || mediaError.code === 2 || mediaError.code === 4) {
      try {
        const sessionRes = await Api.getSession();
        if (token !== state.playbackToken) {
          return;
        }
        if (!sessionRes || !sessionRes.user) {
          handleUnauthorized(new Api.ApiError('登录状态已失效，请重新登录。', 401, 'UNAUTHORIZED', false));
        }
      } catch (err) {
        if (token !== state.playbackToken) {
          return;
        }
        if (err && err.status === 401) {
          handleUnauthorized(err);
        }
      }
    }
  });

  playerReloadBtn.addEventListener('click', function () {
    if (!state.activeVideo) {
      return;
    }
    const root = findRootById(state.activeVideo.rootId) || {
      id: state.activeVideo.rootId,
      label: state.activeVideo.rootLabel,
    };
    playVideo(root, state.activeVideo);
  });

  playerCloseBtn.addEventListener('click', stopAndResetPlayer);

  // ---------- 下载：HEAD 预检 + 浏览器原生下载 ----------
  // 前端不把文件读进 JS 内存；预检通过后交给浏览器发起 GET，由服务端独立鉴权并以附件返回。
  function downloadKey(rootId, relPath) {
    return String(rootId) + '\u0000' + String(relPath);
  }

  function triggerNativeDownload(url) {
    // 同源链接 + download 属性：文件名以服务端 Content-Disposition 为准，当前页面与播放不受影响
    const link = U.el('a', { attrs: { href: url, download: '' }, props: { hidden: true } });
    document.body.appendChild(link);
    try {
      link.click();
    } finally {
      link.remove();
    }
  }

  function describeDownloadError(err) {
    const status = err && typeof err.status === 'number' ? err.status : 0;
    if (status === 403) {
      return '无法下载：当前账号已无权访问该视频，目录授权可能已被撤销。';
    }
    if (status === 404) {
      return '无法下载：找不到该视频文件，可能已被移动、删除或授权已撤销。';
    }
    return '无法下载：' + ((err && err.message) || '准备下载时出现异常，请稍后重试。');
  }

  function setPlayerDownloadBusy(busy) {
    state.playerDownloadBusy = busy;
    // 不使用 disabled，避免键盘焦点在预检期间丢失
    playerDownloadBtn.setAttribute('aria-disabled', busy ? 'true' : 'false');
    playerDownloadBtn.setAttribute('aria-busy', busy ? 'true' : 'false');
    playerDownloadBtn.classList.toggle('is-busy', busy);
    playerDownloadLabel.textContent = busy ? '正在核验…' : '下载原视频';
  }

  function resetPlayerDownload() {
    gate.abort('playerDownload');
    setPlayerDownloadBusy(false);
    U.clearAlert(playerDownloadAlert);
  }

  async function downloadActiveVideo() {
    const video = state.activeVideo;
    if (!video || state.playerDownloadBusy) {
      return;
    }
    const token = state.playbackToken;
    const req = gate.begin('playerDownload');
    U.clearAlert(playerDownloadAlert);
    setPlayerDownloadBusy(true);

    try {
      await Api.checkDownload(video.rootId, video.path, req.signal);
      if (!req.isCurrent() || token !== state.playbackToken) {
        return;
      }
      triggerNativeDownload(Api.buildDownloadUrl(video.rootId, video.path));
      U.showToast('已交给浏览器下载：' + video.name, 'success');
    } catch (err) {
      if (!req.isCurrent() || token !== state.playbackToken || (err && err.isAbort)) {
        return;
      }
      if (err && err.status === 401) {
        // 统一的未登录处理已接管（返回登录页）
        return;
      }
      U.setAlert(
        playerDownloadAlert,
        describeDownloadError(err),
        'error',
        U.el('button', {
          className: 'btn btn-secondary btn-sm',
          text: '重试下载',
          props: { type: 'button' },
          on: {
            click: function () {
              playerDownloadBtn.focus();
              downloadActiveVideo();
            },
          },
        })
      );
      if (err && (err.status === 403 || err.status === 404)) {
        recheckRootsAfterDownloadDenied();
      }
    } finally {
      if (req.isCurrent()) {
        setPlayerDownloadBusy(false);
      }
      req.finish();
    }
  }

  function syncEntryDownloadButtons(key) {
    const busy = Boolean(state.entryDownloads[key]);
    const buttons = browserList.querySelectorAll('.entry-dl');
    for (let i = 0; i < buttons.length; i += 1) {
      if (buttons[i].dataset.dlKey === key) {
        applyEntryDownloadBusy(buttons[i], busy);
      }
    }
  }

  function applyEntryDownloadBusy(button, busy) {
    button.classList.toggle('is-busy', busy);
    button.setAttribute('aria-busy', busy ? 'true' : 'false');
    button.setAttribute('aria-disabled', busy ? 'true' : 'false');
  }

  async function downloadEntry(root, entry) {
    const key = downloadKey(root.id, entry.path);
    if (state.entryDownloads[key]) {
      return;
    }
    // 每个文件独立通道：连续下载多个文件互不取消；退出登录时由 abortAll 统一取消
    const req = gate.begin('entryDownload:' + key);
    state.entryDownloads[key] = true;
    syncEntryDownloadButtons(key);

    try {
      await Api.checkDownload(root.id, entry.path, req.signal);
      if (!req.isCurrent()) {
        return;
      }
      triggerNativeDownload(Api.buildDownloadUrl(root.id, entry.path));
      U.showToast('已交给浏览器下载：' + entry.name, 'success');
    } catch (err) {
      if (!req.isCurrent() || (err && err.isAbort) || (err && err.status === 401)) {
        return;
      }
      U.showToast(describeDownloadError(err) + '（' + entry.name + '）', 'error');
      if (err && (err.status === 403 || err.status === 404)) {
        recheckRootsAfterDownloadDenied();
      }
    } finally {
      if (req.isCurrent()) {
        delete state.entryDownloads[key];
        syncEntryDownloadButtons(key);
      }
      req.finish();
    }
  }

  // 下载预检返回 403/404 时复核授权列表：独立通道，不取消浏览复核或手动刷新；按版本择新写入。
  async function recheckRootsAfterDownloadDenied() {
    const req = gate.begin('downloadRecheck');
    const version = nextRootsVersion();
    try {
      const res = await Api.getLibraryRoots(req.signal);
      if (!req.isCurrent() || !applyRootsResponse(version, res)) {
        return;
      }
      stopPlayerIfRootRevoked();
      if (state.selectedRootId !== null && !findRootById(state.selectedRootId)) {
        clearBrowseSelection();
        U.setAlert(rootsAlert, '当前浏览目录的访问授权已被撤销，已刷新最新授权列表。', 'error');
        if (state.roots.length > 0) {
          browseDirectory(state.roots[0].id, '');
        }
        return;
      }
      renderRootsBar();
    } catch (_err) {
      // 复核失败不影响播放；下载错误提示已展示，401 由统一处理接管
    } finally {
      req.finish();
    }
  }

  function clearBrowseSelection() {
    gate.abort('libraryBrowse');
    state.isBrowsing = false;
    state.selectedRootId = null;
    state.currentPath = '';
    state.currentEntries = [];
    state.filterQuery = '';
    browserFilterInput.value = '';
    browserFilterInput.disabled = true;
    browserLoading.hidden = true;
    U.clearAlert(browserAlert);
    renderRootsBar();
    renderBreadcrumbs();
    renderBrowserEntries();
  }

  playerDownloadBtn.addEventListener('click', downloadActiveVideo);

  // ---------- 目录浏览与面包屑 ----------
  function renderRootsBar() {
    rootsBar.replaceChildren();
    if (state.roots.length === 0) {
      const isAdmin = state.currentUser && state.currentUser.role === 'admin';
      const emptyNodes = [
        U.el('p', { className: 'empty-state-title', text: '暂无可访问的授权目录' }),
        U.el('p', {
          className: 'empty-state-desc',
          text: isAdmin
            ? '请前往「管理台」完成 NAS 连接配置、添加授权目录并为当前账号分配权限。'
            : '当前账号尚未获得任何目录授权，请联系管理员为您分配目录权限。',
        }),
      ];
      if (isAdmin) {
        emptyNodes.push(
          U.el('button', {
            className: 'btn btn-primary btn-sm',
            text: '前往管理台',
            props: { type: 'button' },
            on: {
              click: function () {
                switchView('admin');
              },
            },
          })
        );
      }
      rootsBar.appendChild(U.el('div', { className: 'empty-state empty-state-wide' }, emptyNodes));
      return;
    }

    for (let i = 0; i < state.roots.length; i += 1) {
      const root = state.roots[i];
      const isActive =
        state.selectedRootId !== null && String(root.id) === String(state.selectedRootId);
      const chip = U.el(
        'button',
        {
          className: 'root-chip' + (isActive ? ' is-active' : ''),
          props: { type: 'button' },
          attrs: isActive ? { 'aria-current': 'true' } : null,
          on: {
            click: function () {
              selectRoot(root.id);
            },
          },
        },
        [
          U.el('span', { className: 'root-chip-icon' }, U.createIcon('harddrive', 18)),
          U.el('span', { className: 'root-chip-label', text: root.label }),
        ]
      );
      rootsBar.appendChild(chip);
    }
  }

  function renderBreadcrumbs() {
    breadcrumbsNav.replaceChildren();
    const root = findRootById(state.selectedRootId);
    if (!root) {
      breadcrumbsNav.hidden = true;
      browseUpBtn.hidden = true;
      browseRefreshBtn.hidden = true;
      browserToolbar.hidden = true;
      return;
    }

    const crumbs = U.buildBreadcrumbs(root.label, state.currentPath);
    for (let i = 0; i < crumbs.length; i += 1) {
      if (i > 0) {
        breadcrumbsNav.appendChild(
          U.el('span', { className: 'crumb-sep', text: '/', attrs: { 'aria-hidden': 'true' } })
        );
      }
      const crumb = crumbs[i];
      if (i === crumbs.length - 1) {
        breadcrumbsNav.appendChild(
          U.el('span', { className: 'crumb-current', text: crumb.label, attrs: { 'aria-current': 'location' } })
        );
      } else {
        breadcrumbsNav.appendChild(
          U.el('button', {
            className: 'crumb-btn',
            text: crumb.label,
            props: { type: 'button' },
            on: {
              click: function () {
                browseDirectory(root.id, crumb.path);
              },
            },
          })
        );
      }
    }

    breadcrumbsNav.hidden = false;
    browseUpBtn.hidden = U.normalizePathSegments(state.currentPath).length === 0;
    browseRefreshBtn.hidden = false;
    browserToolbar.hidden = false;
  }

  function renderBrowserEntries() {
    if (state.isBrowsing) {
      return;
    }
    // 重绘列表前记住键盘焦点所在的行 / 下载按钮，重绘后恢复，避免点播后焦点丢失
    const activeEl = document.activeElement;
    const focusKey =
      activeEl && browserList.contains(activeEl) && activeEl.dataset ? activeEl.dataset.focusKey || '' : '';
    let focusTarget = null;
    browserList.replaceChildren();
    const root = findRootById(state.selectedRootId);
    if (!root) {
      browserSummary.textContent =
        state.roots.length === 0 ? '暂无已授权目录可浏览' : '请选择一个已授权目录';
      browserEmpty.replaceChildren(
        U.el('p', {
          className: 'empty-state-title',
          text: state.roots.length === 0 ? '暂无可浏览内容' : '请先在上方选择一个授权目录',
        })
      );
      browserEmpty.hidden = false;
      return;
    }

    const query = state.filterQuery.trim().toLowerCase();
    const allEntries = state.currentEntries;
    const filtered = query
      ? allEntries.filter(function (item) {
          return String(item.name || '').toLowerCase().indexOf(query) !== -1;
        })
      : allEntries;

    let dirCount = 0;
    let videoCount = 0;
    for (let i = 0; i < allEntries.length; i += 1) {
      if (allEntries[i].kind === 'directory') {
        dirCount += 1;
      } else {
        videoCount += 1;
      }
    }

    if (query) {
      browserSummary.textContent = '筛选结果：' + filtered.length + ' / ' + allEntries.length + ' 项';
    } else {
      browserSummary.textContent = '共 ' + dirCount + ' 个文件夹、' + videoCount + ' 个视频';
    }

    if (allEntries.length === 0) {
      browserEmpty.replaceChildren(
        U.el('p', { className: 'empty-state-title', text: '当前目录为空' }),
        U.el('p', { className: 'empty-state-desc', text: '此目录下暂无子目录或视频文件。' })
      );
      browserEmpty.hidden = false;
      return;
    }

    if (filtered.length === 0) {
      browserEmpty.replaceChildren(
        U.el('p', { className: 'empty-state-title', text: '没有找到匹配项' }),
        U.el('p', { className: 'empty-state-desc', text: '当前目录中没有名称包含该关键词的文件夹或视频。' }),
        U.el('button', {
          className: 'btn btn-secondary btn-sm',
          text: '清空筛选',
          props: { type: 'button' },
          on: {
            click: function () {
              state.filterQuery = '';
              browserFilterInput.value = '';
              renderBrowserEntries();
              browserFilterInput.focus();
            },
          },
        })
      );
      browserEmpty.hidden = false;
      return;
    }

    browserEmpty.hidden = true;

    for (let i = 0; i < filtered.length; i += 1) {
      const entry = filtered[i];
      const isDir = entry.kind === 'directory';
      const isPlaying =
        !isDir &&
        state.activeVideo &&
        String(state.activeVideo.rootId) === String(root.id) &&
        state.activeVideo.path === entry.path;

      const ext = isDir ? '' : getFileExtLabel(entry.name);
      const metaNodes = [];
      if (isDir) {
        metaNodes.push(U.el('span', { text: '文件夹' }));
      } else {
        if (isPlaying) {
          metaNodes.push(
            U.el('span', { className: 'playing-tag' }, [
              U.el('span', { className: 'eq', attrs: { 'aria-hidden': 'true' } }, [
                U.el('i'),
                U.el('i'),
                U.el('i'),
              ]),
              '正在播放',
            ])
          );
        }
        if (ext) {
          metaNodes.push(U.el('span', { className: 'ext-chip', text: ext }));
        }
        metaNodes.push(U.el('span', { text: U.formatBytes(entry.size) }));
      }

      const rowFocusKey = (isDir ? 'dir:' : 'row:') + entry.path;
      const btn = U.el(
        'button',
        {
          className: 'entry-btn' + (isDir ? ' is-dir' : ' is-video') + (isPlaying ? ' is-playing' : ''),
          props: { type: 'button' },
          attrs: { 'aria-current': isPlaying ? 'true' : null, 'data-focus-key': rowFocusKey },
          on: {
            click: function () {
              if (isDir) {
                browseDirectory(root.id, entry.path);
              } else {
                playVideo(root, entry);
              }
            },
          },
        },
        [
          U.el(
            'span',
            { className: 'entry-icon ' + (isDir ? 'entry-icon-dir' : 'entry-icon-video') },
            U.createIcon(isDir ? 'folder' : 'film', 20)
          ),
          U.el('span', { className: 'entry-main' }, [
            U.el('span', { className: 'entry-name', text: entry.name }),
            U.el('span', { className: 'entry-meta' }, metaNodes),
          ]),
          U.el(
            'span',
            { className: 'entry-tail', attrs: { 'aria-hidden': 'true' } },
            U.createIcon(isDir ? 'chevron' : 'play', 16)
          ),
        ]
      );

      const item = U.el('li', { className: 'entry-item' + (isDir ? '' : ' has-action') }, btn);
      if (focusKey === rowFocusKey) {
        focusTarget = btn;
      }

      if (!isDir) {
        // 下载按钮与行按钮为同级元素（不嵌套），点击只下载、不切换播放
        const dlKey = downloadKey(root.id, entry.path);
        const dlFocusKey = 'dl:' + entry.path;
        const dlBtn = U.el(
          'button',
          {
            className: 'entry-dl',
            props: { type: 'button', title: '下载原视频' },
            attrs: {
              'aria-label': '下载「' + entry.name + '」',
              'data-dl-key': dlKey,
              'data-focus-key': dlFocusKey,
            },
            on: {
              click: function () {
                downloadEntry(root, entry);
              },
            },
          },
          U.createIcon('download', 18)
        );
        applyEntryDownloadBusy(dlBtn, Boolean(state.entryDownloads[dlKey]));
        item.appendChild(dlBtn);
        if (focusKey === dlFocusKey) {
          focusTarget = dlBtn;
        }
      }

      browserList.appendChild(item);
    }

    if (focusTarget) {
      focusTarget.focus({ preventScroll: true });
    }
  }

  // 从真实文件名取扩展名作为格式标签（不推测编码、不伪造时长）
  function getFileExtLabel(name) {
    const text = String(name || '');
    const dot = text.lastIndexOf('.');
    if (dot <= 0 || dot === text.length - 1) {
      return '';
    }
    const ext = text.slice(dot + 1);
    if (ext.length > 5 || /[^A-Za-z0-9]/.test(ext)) {
      return '';
    }
    return ext.toUpperCase();
  }

  async function browseDirectory(rootId, relPath) {
    const req = gate.begin('libraryBrowse');
    let loadSucceeded = false;
    state.selectedRootId = rootId;
    state.currentPath = relPath || '';
    state.currentEntries = [];
    state.filterQuery = '';
    state.isBrowsing = true;
    browserFilterInput.value = '';
    browserFilterInput.disabled = true;

    renderRootsBar();
    renderBreadcrumbs();
    U.clearAlert(browserAlert);
    browserLoading.hidden = false;
    browserEmpty.hidden = true;
    browserList.replaceChildren();
    browserSummary.textContent = '正在加载目录…';

    try {
      const data = await Api.browseLibrary(rootId, relPath, req.signal);
      if (!req.isCurrent()) {
        return;
      }
      state.currentPath = data && typeof data.path === 'string' ? data.path : relPath || '';
      state.currentEntries = U.sortEntries(data && data.entries ? data.entries : []);
      loadSucceeded = true;
      state.isBrowsing = false;
      browserFilterInput.disabled = false;
      renderBreadcrumbs();
      renderBrowserEntries();
    } catch (err) {
      if (!req.isCurrent() || (err && err.isAbort)) {
        return;
      }
      state.currentEntries = [];

      if (err.status === 404 || err.status === 403) {
        await handleBrowseRevokedOrMissing(rootId, relPath, err, req);
        return;
      }

      browserSummary.textContent = '目录加载失败';
      U.setAlert(
        browserAlert,
        err.message || '读取目录内容失败。',
        'error',
        U.el('button', {
          className: 'btn btn-secondary btn-sm',
          text: '重试',
          props: { type: 'button' },
          on: {
            click: function () {
              browseDirectory(rootId, relPath);
            },
          },
        })
      );
    } finally {
      if (req.isCurrent()) {
        state.isBrowsing = false;
        browserLoading.hidden = true;
        browserFilterInput.disabled = !loadSucceeded;
      }
      req.finish();
    }
  }

  async function handleBrowseRevokedOrMissing(failedRootId, failedPath, browseError, browseReq) {
    // 404/403 时自动复核已授权根列表，区分“授权被撤销”与“子目录被移除”。
    // 使用独立的 libraryRecheck 通道：不会取消用户手动刷新（libraryRoots），也不会被其取消；
    // 列表数据按版本号择新写入，界面结论仅在原浏览令牌仍有效时呈现。
    const checkReq = gate.begin('libraryRecheck');
    const version = nextRootsVersion();
    browserSummary.textContent = '正在核对目录授权…';
    try {
      const res = await Api.getLibraryRoots(checkReq.signal);
      if (!checkReq.isCurrent()) {
        return;
      }
      const applied = applyRootsResponse(version, res);
      if (!browseReq.isCurrent()) {
        // 用户已切换到别的目录：只同步更新后的授权列表，不改动当前浏览区
        if (applied) {
          stopPlayerIfRootRevoked();
          renderRootsBar();
        }
        return;
      }
      const stillHasRoot = Boolean(findRootById(failedRootId));

      if (!stillHasRoot) {
        if (state.activeVideo && !findRootById(state.activeVideo.rootId)) {
          stopAndResetPlayer();
        }
        state.isBrowsing = false;
        state.selectedRootId = null;
        state.currentPath = '';
        state.currentEntries = [];
        browserFilterInput.value = '';
        browserFilterInput.disabled = true;
        renderRootsBar();
        renderBreadcrumbs();
        renderBrowserEntries();
        U.setAlert(
          rootsAlert,
          (browseError.message || '目标目录访问权限已被撤销或已删除。') + ' 已自动刷新最新授权列表。',
          'error'
        );
        if (state.roots.length > 0) {
          selectRoot(state.roots[0].id);
        }
        return;
      }

      // 根目录仍有效，说明仅当前子路径被移除或变动
      renderRootsBar();
      browserSummary.textContent = '路径不可用';
      const backBtn = failedPath
        ? U.el('button', {
            className: 'btn btn-secondary btn-sm',
            text: '返回根目录',
            props: { type: 'button' },
            on: {
              click: function () {
                browseDirectory(failedRootId, '');
              },
            },
          })
        : U.el('button', {
            className: 'btn btn-secondary btn-sm',
            text: '重新加载',
            props: { type: 'button' },
            on: {
              click: function () {
                browseDirectory(failedRootId, '');
              },
            },
          });

      U.setAlert(
        browserAlert,
        (browseError.message || '该路径不存在或已被移除。') + ' 请返回上级或刷新目录。',
        'error',
        backBtn
      );
    } catch (refreshErr) {
      if (!checkReq.isCurrent() || !browseReq.isCurrent() || (refreshErr && refreshErr.isAbort)) {
        return;
      }
      browserSummary.textContent = '目录不可访问';
      U.setAlert(browserAlert, browseError.message || '目标目录不存在或授权已撤销。', 'error');
    } finally {
      checkReq.finish();
    }
  }

  function selectRoot(rootId) {
    U.clearAlert(rootsAlert);
    browseDirectory(rootId, '');
  }

  async function loadLibraryRoots(options) {
    const opts = options || {};
    const req = gate.begin('libraryRoots');
    const version = nextRootsVersion();
    U.clearAlert(rootsAlert);
    setRootsRefreshing(true);

    try {
      const res = await Api.getLibraryRoots(req.signal);
      if (!req.isCurrent()) {
        return;
      }
      // 若自动复核已写入更新版本，则沿用较新的列表继续后续流程，不回退为旧数据
      applyRootsResponse(version, res);
      stopPlayerIfRootRevoked();

      if (state.roots.length === 0) {
        gate.abort('libraryBrowse');
        state.isBrowsing = false;
        state.selectedRootId = null;
        state.currentPath = '';
        state.currentEntries = [];
        state.filterQuery = '';
        browserFilterInput.value = '';
        browserFilterInput.disabled = true;
        renderRootsBar();
        renderBreadcrumbs();
        U.clearAlert(browserAlert);
        browserLoading.hidden = true;
        renderBrowserEntries();
        return;
      }

      const existingSelected = opts.keepSelection ? findRootById(state.selectedRootId) : null;
      if (existingSelected) {
        renderRootsBar();
        browseDirectory(existingSelected.id, state.currentPath);
      } else {
        selectRoot(state.roots[0].id);
      }
    } catch (err) {
      if (!req.isCurrent() || (err && err.isAbort)) {
        return;
      }
      U.setAlert(
        rootsAlert,
        err.message || '加载已授权目录失败。',
        'error',
        U.el('button', {
          className: 'btn btn-secondary btn-sm',
          text: '重试',
          props: { type: 'button' },
          on: {
            click: function () {
              loadLibraryRoots(opts);
            },
          },
        })
      );
    } finally {
      if (req.isCurrent()) {
        setRootsRefreshing(false);
      }
      req.finish();
    }
  }

  function setRootsRefreshing(busy) {
    refreshRootsBtn.disabled = busy;
    refreshRootsBtn.setAttribute('aria-busy', busy ? 'true' : 'false');
    refreshRootsBtn.classList.toggle('is-busy', busy);
  }

  refreshRootsBtn.addEventListener('click', function () {
    loadLibraryRoots({ keepSelection: true });
  });

  browseUpBtn.addEventListener('click', function () {
    if (state.selectedRootId !== null) {
      browseDirectory(state.selectedRootId, U.getParentPath(state.currentPath));
    }
  });

  browseRefreshBtn.addEventListener('click', function () {
    if (state.selectedRootId !== null) {
      browseDirectory(state.selectedRootId, state.currentPath);
    }
  });

  browserFilterInput.addEventListener('input', function () {
    if (state.isBrowsing || browserFilterInput.disabled) {
      return;
    }
    state.filterQuery = browserFilterInput.value;
    renderBrowserEntries();
  });

  // ---------- 视图切换 ----------
  function switchView(viewName) {
    const canAdmin = Boolean(state.currentUser && state.currentUser.role === 'admin');
    const target = viewName === 'admin' && canAdmin ? 'admin' : 'player';
    state.activeView = target;

    playerView.hidden = target !== 'player';
    adminView.hidden = target !== 'admin';

    navPlayerBtn.classList.toggle('is-active', target === 'player');
    navAdminBtn.classList.toggle('is-active', target === 'admin');
    if (target === 'player') {
      navPlayerBtn.setAttribute('aria-current', 'page');
      navAdminBtn.removeAttribute('aria-current');
      if (state.libraryDirty) {
        loadLibraryRoots({ keepSelection: true });
      }
    } else {
      navAdminBtn.setAttribute('aria-current', 'page');
      navPlayerBtn.removeAttribute('aria-current');
      adminController.activate();
    }
  }

  navPlayerBtn.addEventListener('click', function () {
    switchView('player');
  });

  navAdminBtn.addEventListener('click', function () {
    switchView('admin');
  });

  // ---------- 登录 / 退出 / 401 处理 ----------
  function clearAuthenticatedState() {
    gate.abortAll();
    adminController.reset();
    U.closeModal(true);
    stopAndResetPlayer();
    state.currentUser = null;
    state.activeView = 'player';
    state.roots = [];
    state.selectedRootId = null;
    state.currentPath = '';
    state.currentEntries = [];
    state.filterQuery = '';
    state.isBrowsing = false;
    state.libraryDirty = false;
    state.libraryDirtySince = state.rootsIssuedVersion;
    state.entryDownloads = Object.create(null);
    setRootsRefreshing(false);
    browserLoading.hidden = true;
    browserFilterInput.value = '';
    browserFilterInput.disabled = true;
    rootsBar.replaceChildren();
    browserList.replaceChildren();
    U.clearAlert(rootsAlert);
    U.clearAlert(browserAlert);
    renderUserBadge();
  }

  function enterLoginState(noticeMessage) {
    clearAuthenticatedState();
    bootView.hidden = true;
    appShell.hidden = true;
    loginView.hidden = false;
    loginPassword.value = '';
    if (noticeMessage) {
      U.setAlert(loginAlert, noticeMessage, 'error');
    } else {
      U.clearAlert(loginAlert);
    }
    window.requestAnimationFrame(function () {
      loginUsername.focus();
    });
  }

  function enterAuthenticatedState(user) {
    state.currentUser = user;
    state.libraryDirty = false;
    loginPassword.value = '';
    U.clearAlert(loginAlert);

    bootView.hidden = true;
    loginView.hidden = true;
    appShell.hidden = false;

    renderUserBadge();
    switchView('player');
    loadLibraryRoots({ keepSelection: false });
  }

  function handleUnauthorized(err) {
    if (!state.currentUser && !loginView.hidden) {
      return;
    }
    const msg = err && err.message ? err.message : '登录状态已失效，请重新登录。';
    enterLoginState(msg);
  }

  Api.setUnauthorizedHandler(handleUnauthorized);

  loginForm.addEventListener('submit', async function (event) {
    event.preventDefault();
    if (state.loggingIn) {
      return;
    }
    U.clearAlert(loginAlert);

    const username = loginUsername.value.trim();
    const password = loginPassword.value;

    if (!username) {
      U.setAlert(loginAlert, '请输入用户名。', 'error');
      loginUsername.focus();
      return;
    }
    if (!password) {
      U.setAlert(loginAlert, '请输入密码。', 'error');
      loginPassword.focus();
      return;
    }

    state.loggingIn = true;
    loginSubmitBtn.disabled = true;
    loginSubmitBtn.textContent = '登录中…';

    try {
      const res = await Api.login(username, password);
      loginPassword.value = '';
      if (!res || !res.user) {
        U.setAlert(loginAlert, '登录响应异常，请重试。', 'error');
        return;
      }
      enterAuthenticatedState(res.user);
    } catch (err) {
      loginPassword.value = '';
      U.setAlert(loginAlert, (err && err.message) || '用户名或密码不正确。', 'error');
      loginPassword.focus();
    } finally {
      state.loggingIn = false;
      loginSubmitBtn.disabled = false;
      loginSubmitBtn.textContent = '登录';
    }
  });

  logoutBtn.addEventListener('click', async function () {
    if (state.loggingOut) {
      return;
    }
    state.loggingOut = true;
    logoutBtn.disabled = true;
    logoutBtn.textContent = '退出中…';

    try {
      await Api.logout();
      enterLoginState('');
      U.showToast('已安全退出登录', 'success');
    } catch (err) {
      if (err && err.status === 401) {
        enterLoginState('');
      } else {
        U.showToast((err && err.message) || '退出登录失败，请重试', 'error');
      }
    } finally {
      state.loggingOut = false;
      logoutBtn.disabled = false;
      logoutBtn.textContent = '退出登录';
    }
  });

  // ---------- 应用启动会话检查 ----------
  async function bootstrap() {
    bootView.hidden = false;
    loginView.hidden = true;
    appShell.hidden = true;
    bootRetryBtn.hidden = true;
    bootMessage.textContent = '正在检查登录状态…';

    const req = gate.begin('sessionBoot');
    try {
      const res = await Api.getSession(req.signal);
      if (!req.isCurrent()) {
        return;
      }
      if (res && res.user) {
        enterAuthenticatedState(res.user);
      } else {
        enterLoginState('');
      }
    } catch (err) {
      if (!req.isCurrent() || (err && err.isAbort)) {
        return;
      }
      if (err && err.status === 401) {
        enterLoginState('');
        return;
      }
      bootMessage.textContent = (err && err.message) || '无法连接到服务，请确认后端已启动。';
      bootRetryBtn.hidden = false;
    } finally {
      req.finish();
    }
  }

  bootRetryBtn.addEventListener('click', bootstrap);

  bootstrap();
})();
