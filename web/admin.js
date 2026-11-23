(function () {
  'use strict';

  const U = window.SWPUtil;
  const Api = window.SWPApi;

  function createAdminController(hooks) {
    const callbacks = hooks || {};
    const gate = U.createRequestGate();

    const state = {
      activeTab: 'nas',
      loadedNas: null,
      nasFetched: false,
      nasSaving: false,
      nasTesting: false,
      roots: [],
      users: [],
    };

    // DOM 引用
    const tabButtons = Array.prototype.slice.call(document.querySelectorAll('[data-admin-tab]'));
    const tabPanels = {
      nas: document.getElementById('admin-panel-nas'),
      roots: document.getElementById('admin-panel-roots'),
      users: document.getElementById('admin-panel-users'),
    };

    // NAS DOM
    const nasForm = document.getElementById('nas-form');
    const nasHostInput = document.getElementById('nas-host');
    const nasPortInput = document.getElementById('nas-port');
    const nasShareInput = document.getElementById('nas-share');
    const nasDomainInput = document.getElementById('nas-domain');
    const nasUsernameInput = document.getElementById('nas-username');
    const nasPasswordInput = document.getElementById('nas-password');
    const nasPasswordHint = document.getElementById('nas-password-hint');
    const nasStatusBadge = document.getElementById('nas-status-badge');
    const nasDirtyBadge = document.getElementById('nas-dirty-badge');
    const nasAlert = document.getElementById('nas-alert');
    const nasTestResult = document.getElementById('nas-test-result');
    const nasSaveBtn = document.getElementById('nas-save-btn');
    const nasTestBtn = document.getElementById('nas-test-btn');
    const nasPreviewBrowseBtn = document.getElementById('nas-preview-browse-btn');

    // Roots DOM
    const rootCreateForm = document.getElementById('root-create-form');
    const rootCreateLabel = document.getElementById('root-create-label');
    const rootCreatePath = document.getElementById('root-create-path');
    const rootCreateSubmitBtn = document.getElementById('root-create-submit-btn');
    const rootCreateAlert = document.getElementById('root-create-alert');
    const openNasPickerBtn = document.getElementById('open-nas-picker-btn');
    const adminRootsRefreshBtn = document.getElementById('admin-roots-refresh-btn');
    const adminRootsAlert = document.getElementById('admin-roots-alert');
    const adminRootsLoading = document.getElementById('admin-roots-loading');
    const adminRootsEmpty = document.getElementById('admin-roots-empty');
    const adminRootsList = document.getElementById('admin-roots-list');

    // Users DOM
    const userCreateForm = document.getElementById('user-create-form');
    const userCreateUsername = document.getElementById('user-create-username');
    const userCreatePassword = document.getElementById('user-create-password');
    const userCreateRole = document.getElementById('user-create-role');
    const userCreateSubmitBtn = document.getElementById('user-create-submit-btn');
    const userCreateAlert = document.getElementById('user-create-alert');
    const adminUsersRefreshBtn = document.getElementById('admin-users-refresh-btn');
    const adminUsersAlert = document.getElementById('admin-users-alert');
    const adminUsersLoading = document.getElementById('admin-users-loading');
    const adminUsersEmpty = document.getElementById('admin-users-empty');
    const adminUsersList = document.getElementById('admin-users-list');

    function notifyLibraryChanged() {
      if (typeof callbacks.onLibraryDataChanged === 'function') {
        callbacks.onLibraryDataChanged();
      }
    }

    function getCurrentUser() {
      return typeof callbacks.getCurrentUser === 'function' ? callbacks.getCurrentUser() : null;
    }

    function formatNowTime() {
      const now = new Date();
      const pad = function (n) {
        return n < 10 ? '0' + n : String(n);
      };
      return pad(now.getHours()) + ':' + pad(now.getMinutes()) + ':' + pad(now.getSeconds());
    }

    // ---------- 子标签切换 ----------
    function switchTab(tabName) {
      if (!tabPanels[tabName]) {
        return;
      }
      state.activeTab = tabName;
      for (let i = 0; i < tabButtons.length; i += 1) {
        const btn = tabButtons[i];
        const isSelected = btn.getAttribute('data-admin-tab') === tabName;
        btn.classList.toggle('is-active', isSelected);
        btn.setAttribute('aria-selected', isSelected ? 'true' : 'false');
      }
      const panelKeys = Object.keys(tabPanels);
      for (let i = 0; i < panelKeys.length; i += 1) {
        const key = panelKeys[i];
        tabPanels[key].hidden = key !== tabName;
      }

      if (tabName === 'nas') {
        loadNasConfig();
      } else if (tabName === 'roots') {
        loadRoots();
      } else if (tabName === 'users') {
        loadUsers();
      }
    }

    for (let i = 0; i < tabButtons.length; i += 1) {
      tabButtons[i].addEventListener('click', function (event) {
        const targetTab = event.currentTarget.getAttribute('data-admin-tab');
        switchTab(targetTab);
      });
      tabButtons[i].addEventListener('keydown', function (event) {
        if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') {
          return;
        }
        event.preventDefault();
        const idx = tabButtons.indexOf(event.currentTarget);
        const dir = event.key === 'ArrowRight' ? 1 : -1;
        const nextIdx = (idx + dir + tabButtons.length) % tabButtons.length;
        const nextBtn = tabButtons[nextIdx];
        nextBtn.focus();
        switchTab(nextBtn.getAttribute('data-admin-tab'));
      });
    }

    // ---------- NAS SMB 配置与测试 ----------
    function nasIdentityChanged() {
      const base = state.loadedNas || {};
      return nasHostInput.value.trim() !== (base.host || '') ||
        Number(nasPortInput.value) !== (base.port || 445) ||
        nasShareInput.value.trim() !== (base.share || '') ||
        nasUsernameInput.value.trim() !== (base.username || '') ||
        nasDomainInput.value.trim() !== (base.domain || '');
    }

    function isNasFormDirty() {
      return state.nasFetched && (nasIdentityChanged() || nasPasswordInput.value.length > 0);
    }

    function updateNasIndicators() {
      const nas = state.loadedNas;
      if (!state.nasFetched) {
        nasStatusBadge.textContent = '加载中…';
        nasStatusBadge.className = 'badge';
      } else if (!nas) {
        nasStatusBadge.textContent = '未配置';
        nasStatusBadge.className = 'badge badge-warning';
        nasPasswordInput.placeholder = '请输入只读 SMB 账号的密码';
        nasPasswordHint.textContent = '首次配置必须输入密码，不支持匿名共享。';
      } else if (nas.hasPassword) {
        nasStatusBadge.textContent = '已配置（已保存密码）';
        nasStatusBadge.className = 'badge badge-success';
        const changed = nasIdentityChanged();
        nasPasswordInput.placeholder = changed ? '重新输入 SMB 密码' : '留空则保留已保存密码';
        nasPasswordHint.textContent = changed ? '连接信息已更改，原密码不会用于新的连接。' : '同一连接可留空保留原密码，或输入新密码。已保存的密码不会回显。';
      } else {
        nasStatusBadge.textContent = '已配置（未设置密码）';
        nasStatusBadge.className = 'badge badge-primary';
        nasPasswordInput.placeholder = '请输入 SMB 密码';
        nasPasswordHint.textContent = '当前未保存密码；输入密码并保存后不会再次显示。';
      }

      nasPasswordInput.required = !nas || !nas.hasPassword || nasIdentityChanged();
      nasDirtyBadge.hidden = !isNasFormDirty();
    }

    function applyNasToForm(nas) {
      state.loadedNas = nas || null;
      state.nasFetched = true;
      nasHostInput.value = nas && nas.host ? String(nas.host) : '';
      nasPortInput.value = nas && nas.port ? String(nas.port) : '445';
      nasShareInput.value = nas && nas.share ? String(nas.share) : '';
      nasUsernameInput.value = nas && nas.username ? String(nas.username) : '';
      nasDomainInput.value = nas && nas.domain ? String(nas.domain) : '';
      nasPasswordInput.value = '';
      updateNasIndicators();
    }

    async function loadNasConfig() {
      const req = gate.begin('adminNas');
      U.clearAlert(nasAlert);
      nasStatusBadge.textContent = '加载中…';
      nasStatusBadge.className = 'badge';

      try {
        const res = await Api.getAdminNas(req.signal);
        if (!req.isCurrent()) {
          return;
        }
        applyNasToForm(res && res.nas ? res.nas : null);
      } catch (err) {
        if (!req.isCurrent() || (err && err.isAbort)) {
          return;
        }
        nasStatusBadge.textContent = '加载失败';
        nasStatusBadge.className = 'badge badge-danger';
        U.setAlert(
          nasAlert,
          err.message || '加载 NAS 配置失败。',
          'error',
          U.el('button', {
            className: 'btn btn-secondary btn-sm',
            text: '重试',
            props: { type: 'button' },
            on: { click: loadNasConfig },
          })
        );
      } finally {
        req.finish();
      }
    }

    const nasWatchInputs = [
      nasHostInput,
      nasPortInput,
      nasShareInput,
      nasDomainInput,
      nasUsernameInput,
      nasPasswordInput,
    ];
    for (let i = 0; i < nasWatchInputs.length; i += 1) {
      nasWatchInputs[i].addEventListener('input', updateNasIndicators);
    }

    nasForm.addEventListener('submit', async function (event) {
      event.preventDefault();
      if (state.nasSaving) {
        return;
      }

      U.clearAlert(nasAlert);
      U.clearAlert(nasTestResult);

      const host = nasHostInput.value.trim();
      const portRaw = nasPortInput.value.trim();
      const port = Number(portRaw);
      const share = nasShareInput.value.trim();
      const username = nasUsernameInput.value.trim();
      const domain = nasDomainInput.value.trim();
      const password = nasPasswordInput.value;

      if (!host) {
        U.setAlert(nasAlert, '请填写 NAS 服务器地址（Host / IP）。', 'error');
        nasHostInput.focus();
        return;
      }
      if (!Number.isInteger(port) || port < 1 || port > 65535) {
        U.setAlert(nasAlert, '端口必须是 1 到 65535 之间的整数（SMB 默认 445）。', 'error');
        nasPortInput.focus();
        return;
      }
      if (!share) {
        U.setAlert(nasAlert, '请填写 SMB 共享名称（Share）。', 'error');
        nasShareInput.focus();
        return;
      }

      if (!username) {
        U.setAlert(nasAlert, '请填写访问共享的只读 SMB 账号，不支持匿名共享。', 'error');
        nasUsernameInput.focus();
        return;
      }
      if (!password && nasPasswordInput.required) {
        U.setAlert(nasAlert, '首次配置或更改连接信息时，请输入该 SMB 账号的密码。', 'error');
        nasPasswordInput.focus();
        return;
      }

      const payload = {
        host: host,
        port: port,
        share: share,
        username: username,
        domain: domain,
      };
      // 修改时留空密码则不发送，保留原密码
      if (password !== '') {
        payload.password = password;
      }

      state.nasSaving = true;
      nasSaveBtn.disabled = true;
      nasSaveBtn.textContent = '保存中…';

      try {
        const res = await Api.updateAdminNas(payload);
        applyNasToForm(res && res.nas ? res.nas : null);
        U.setAlert(nasAlert, 'SMB 连接配置已保存。可点击“测试已保存连接”验证连通性。', 'success');
        U.showToast('NAS 配置已保存', 'success');
      } catch (err) {
        if (err && err.isAbort) {
          return;
        }
        U.setAlert(nasAlert, err.message || '保存 NAS 配置失败。', 'error');
      } finally {
        state.nasSaving = false;
        nasSaveBtn.disabled = false;
        nasSaveBtn.textContent = '保存配置';
      }
    });

    nasTestBtn.addEventListener('click', async function () {
      if (state.nasTesting) {
        return;
      }
      U.clearAlert(nasTestResult);

      if (!state.loadedNas) {
        U.setAlert(nasTestResult, '请先填写并保存 SMB 连接配置，然后再执行连接测试。', 'warning');
        return;
      }

      const dirtyAtTest = isNasFormDirty();
      state.nasTesting = true;
      nasTestBtn.disabled = true;
      nasTestBtn.textContent = '测试中…';

      try {
        const res = await Api.testAdminNas();
        const timeStr = formatNowTime();
        if (res && res.ok) {
          const suffix = dirtyAtTest
            ? '（注意：测试基于已保存配置，当前表单尚有未保存更改。）'
            : '';
          U.setAlert(
            nasTestResult,
            '连接测试成功（' + timeStr + '）：已成功连通 SMB 共享。' + suffix,
            'success'
          );
          U.showToast('SMB 连接测试通过', 'success');
        } else {
          U.setAlert(nasTestResult, '连接测试未返回成功状态，请检查 NAS 配置。', 'error');
        }
      } catch (err) {
        if (err && err.isAbort) {
          return;
        }
        const suffix = dirtyAtTest
          ? '（提示：测试基于上次已保存配置，请先保存最新修改再测。）'
          : '';
        U.setAlert(
          nasTestResult,
          '连接测试失败（' + formatNowTime() + '）：' + (err.message || '无法连接 SMB 共享。') + suffix,
          'error'
        );
      } finally {
        state.nasTesting = false;
        nasTestBtn.disabled = false;
        nasTestBtn.textContent = '测试已保存连接';
      }
    });

    nasPreviewBrowseBtn.addEventListener('click', function () {
      if (!state.loadedNas) {
        U.setAlert(nasTestResult, '请先保存 SMB 连接配置后再浏览共享目录。', 'warning');
        return;
      }
      openNasBrowserModal({
        mode: 'preview',
        initialPath: '',
      });
    });

    // ---------- NAS 共享目录浏览与挑选弹窗 ----------
    function openNasBrowserModal(options) {
      const mode = options && options.mode === 'pick' ? 'pick' : 'preview';
      let currentPath = options && typeof options.initialPath === 'string' ? options.initialPath : '';

      const alertBox = U.el('div', { className: 'alert alert-error', attrs: { role: 'alert' }, props: { hidden: true } });
      const currentBar = U.el('div', { className: 'picker-current-bar' });
      const navRow = U.el('div', { className: 'panel-header panel-header-compact' });
      const crumbsNav = U.el('nav', { className: 'breadcrumbs', attrs: { 'aria-label': 'NAS 目录面包屑' } });
      const navBtns = U.el('div', { className: 'btn-row' });

      const upBtn = U.el('button', {
        className: 'btn btn-secondary btn-sm',
        text: '上一级',
        props: { type: 'button', hidden: true },
        on: {
          click: function () {
            browsePath(U.getParentPath(currentPath));
          },
        },
      });
      const refreshBtn = U.el('button', {
        className: 'btn btn-secondary btn-sm',
        text: '刷新',
        props: { type: 'button' },
        on: {
          click: function () {
            browsePath(currentPath);
          },
        },
      });
      navBtns.append(upBtn, refreshBtn);
      navRow.append(crumbsNav, navBtns);

      const loadingBox = U.el(
        'div',
        { className: 'inline-loading', attrs: { role: 'status', 'aria-live': 'polite' }, props: { hidden: true } },
        [U.el('span', { className: 'spinner spinner-sm', attrs: { 'aria-hidden': 'true' } }), U.el('span', { text: '正在读取共享目录…' })]
      );
      const emptyBox = U.el('div', { className: 'empty-state', props: { hidden: true } });
      const listEl = U.el('ul', { className: 'picker-list', attrs: { 'aria-label': 'NAS 共享目录条目' } });

      function updateCurrentPathBar(pathValue) {
        currentBar.replaceChildren(
          '当前位置：',
          U.el('strong', { text: U.formatRootPathDisplay(pathValue) })
        );
      }

      function renderCrumbs(pathValue) {
        crumbsNav.replaceChildren();
        const crumbs = U.buildBreadcrumbs('共享根目录', pathValue);
        for (let i = 0; i < crumbs.length; i += 1) {
          if (i > 0) {
            crumbsNav.appendChild(U.el('span', { className: 'crumb-sep', text: '/', attrs: { 'aria-hidden': 'true' } }));
          }
          const crumb = crumbs[i];
          if (i === crumbs.length - 1) {
            crumbsNav.appendChild(U.el('span', { className: 'crumb-current', text: crumb.label }));
          } else {
            crumbsNav.appendChild(
              U.el('button', {
                className: 'crumb-btn',
                text: crumb.label,
                props: { type: 'button' },
                on: {
                  click: function () {
                    browsePath(crumb.path);
                  },
                },
              })
            );
          }
        }
        upBtn.hidden = U.normalizePathSegments(pathValue).length === 0;
      }

      function selectPathAndClose(selectedPath) {
        if (typeof options.onSelect === 'function') {
          options.onSelect(selectedPath);
        }
        U.closeModal();
      }

      const confirmActionBtn =
        mode === 'pick'
          ? U.el('button', {
              className: 'btn btn-primary',
              text: '选择当前位置',
              props: { type: 'button', disabled: true },
              on: {
                click: function () {
                  selectPathAndClose(currentPath);
                },
              },
            })
          : U.el('button', {
              className: 'btn btn-primary',
              text: '以此目录新建授权根',
              props: { type: 'button', disabled: true },
              on: {
                click: function () {
                  const picked = currentPath;
                  U.closeModal();
                  switchTab('roots');
                  applyPickedRootPath(picked);
                },
              },
            });

      async function browsePath(targetPath) {
        const req = gate.begin('nasBrowse');
        U.clearAlert(alertBox);
        loadingBox.hidden = false;
        emptyBox.hidden = true;
        confirmActionBtn.disabled = true;
        listEl.replaceChildren();

        try {
          const data = await Api.browseAdminNas(targetPath, req.signal);
          if (!req.isCurrent()) {
            return;
          }
          currentPath = data && typeof data.path === 'string' ? data.path : targetPath || '';
          updateCurrentPathBar(currentPath);
          renderCrumbs(currentPath);
          confirmActionBtn.disabled = false;

          const entries = U.sortEntries(data && data.entries ? data.entries : []);
          if (entries.length === 0) {
            emptyBox.replaceChildren(
              U.el('p', { className: 'empty-state-title', text: '该目录下暂无子目录或视频文件' }),
              U.el('p', {
                className: 'empty-state-desc',
                text: mode === 'pick' ? '您仍可点击下方“选择当前位置”将此目录设为授权根。' : '当前共享路径为空。',
              })
            );
            emptyBox.hidden = false;
            return;
          }

          for (let i = 0; i < entries.length; i += 1) {
            const entry = entries[i];
            const isDir = entry.kind === 'directory';
            if (isDir) {
              const enterBtn = U.el(
                'button',
                {
                  className: 'picker-dir-btn',
                  props: { type: 'button' },
                  on: {
                    click: function () {
                      browsePath(entry.path);
                    },
                  },
                },
                [
                  U.el('span', { className: 'entry-icon entry-icon-dir' }, U.createIcon('folder', 18)),
                  U.el('span', { className: 'entry-name', text: entry.name }),
                ]
              );

              const rowChildren = [enterBtn];
              if (mode === 'pick') {
                rowChildren.push(
                  U.el('button', {
                    className: 'btn btn-secondary btn-sm',
                    text: '选择此目录',
                    props: { type: 'button' },
                    on: {
                      click: function () {
                        selectPathAndClose(entry.path);
                      },
                    },
                  })
                );
              }
              listEl.appendChild(U.el('li', { className: 'picker-row' }, rowChildren));
            } else {
              listEl.appendChild(
                U.el('li', { className: 'picker-row picker-row-muted' }, [
                  U.el('span', { className: 'entry-icon entry-icon-video' }, U.createIcon('video', 18)),
                  U.el('div', { className: 'entry-main' }, [
                    U.el('span', { className: 'entry-name', text: entry.name }),
                    U.el('span', { className: 'entry-meta', text: '视频文件（仅供确认目录内容）' }),
                  ]),
                  U.el('span', { className: 'entry-tail', text: U.formatBytes(entry.size) }),
                ])
              );
            }
          }
        } catch (err) {
          if (!req.isCurrent() || (err && err.isAbort)) {
            return;
          }
          updateCurrentPathBar(currentPath);
          renderCrumbs(currentPath);
          const actionBtn =
            err.status === 404 && targetPath
              ? U.el('button', {
                  className: 'btn btn-secondary btn-sm',
                  text: '返回共享根目录',
                  props: { type: 'button' },
                  on: {
                    click: function () {
                      browsePath('');
                    },
                  },
                })
              : U.el('button', {
                  className: 'btn btn-secondary btn-sm',
                  text: '重试',
                  props: { type: 'button' },
                  on: {
                    click: function () {
                      browsePath(targetPath);
                    },
                  },
                });
          U.setAlert(alertBox, err.message || '读取 NAS 共享目录失败。', 'error', actionBtn);
        } finally {
          if (req.isCurrent()) {
            loadingBox.hidden = true;
          }
          req.finish();
        }
      }

      const footerButtons = [
        U.el('button', {
          className: 'btn btn-secondary',
          text: mode === 'pick' ? '取消' : '关闭',
          props: { type: 'button' },
          on: {
            click: function () {
              U.closeModal();
            },
          },
        }),
        confirmActionBtn,
      ];

      updateCurrentPathBar(currentPath);
      renderCrumbs(currentPath);

      U.openModal({
        title: mode === 'pick' ? '选择 NAS 授权目录' : '浏览 NAS 共享目录',
        size: 'lg',
        content: [currentBar, navRow, alertBox, loadingBox, emptyBox, listEl],
        footer: footerButtons,
        onClose: function () {
          gate.abort('nasBrowse');
        },
      });

      browsePath(currentPath);
    }

    function applyPickedRootPath(pickedPath) {
      const exactPath = typeof pickedPath === 'string' ? pickedPath : '';
      rootCreatePath.value = exactPath;
      if (!rootCreateLabel.value.trim()) {
        if (U.isWholeSharePath(exactPath)) {
          rootCreateLabel.value = '共享根目录';
        } else {
          const segs = U.normalizePathSegments(exactPath);
          const candidate = segs.length > 0 ? segs[segs.length - 1].trim() : '';
          if (candidate) {
            rootCreateLabel.value = candidate;
          }
        }
      }
      rootCreateLabel.focus();
    }

    // ---------- 授权目录（根）管理 ----------
    function renderRootsList() {
      adminRootsList.replaceChildren();
      if (state.roots.length === 0) {
        adminRootsEmpty.replaceChildren(
          U.el('p', { className: 'empty-state-title', text: '暂无授权目录' }),
          U.el('p', { className: 'empty-state-desc', text: '请先在「新增授权目录」中添加一个目录；路径完全留空表示授权整个共享根目录。' })
        );
        adminRootsEmpty.hidden = false;
        return;
      }

      adminRootsEmpty.hidden = true;
      for (let i = 0; i < state.roots.length; i += 1) {
        const root = state.roots[i];
        const isWholeShare = U.isWholeSharePath(root.path);

        const titleRow = U.el('div', { className: 'record-title-row' }, [
          U.el('span', { className: 'record-title', text: root.label }),
          isWholeShare ? U.el('span', { className: 'badge badge-primary', text: '整个共享' }) : null,
        ]);

        const pathLine = U.el('div', {
          className: isWholeShare ? 'record-sub record-sub-plain' : 'record-sub',
          text: '路径：' + U.formatRootPathDisplay(root.path),
        });

        const renameBtn = U.el('button', {
          className: 'btn btn-secondary btn-sm',
          text: '重命名',
          props: { type: 'button' },
          on: {
            click: function () {
              openRenameRootModal(root);
            },
          },
        });

        const deleteBtn = U.el('button', {
          className: 'btn btn-danger-outline btn-sm',
          text: '删除',
          props: { type: 'button' },
          on: {
            click: function () {
              confirmDeleteRoot(root);
            },
          },
        });

        adminRootsList.appendChild(
          U.el('div', { className: 'record-card' }, [
            U.el('div', { className: 'record-info' }, [titleRow, pathLine]),
            U.el('div', { className: 'record-actions' }, [renameBtn, deleteBtn]),
          ])
        );
      }
    }

    async function loadRoots() {
      const req = gate.begin('adminRoots');
      U.clearAlert(adminRootsAlert);
      adminRootsLoading.hidden = false;
      adminRootsEmpty.hidden = true;

      try {
        const res = await Api.getAdminRoots(req.signal);
        if (!req.isCurrent()) {
          return;
        }
        state.roots = Array.isArray(res && res.roots) ? res.roots : [];
        renderRootsList();
      } catch (err) {
        if (!req.isCurrent() || (err && err.isAbort)) {
          return;
        }
        U.setAlert(
          adminRootsAlert,
          err.message || '加载授权目录列表失败。',
          'error',
          U.el('button', {
            className: 'btn btn-secondary btn-sm',
            text: '重试',
            props: { type: 'button' },
            on: { click: loadRoots },
          })
        );
      } finally {
        if (req.isCurrent()) {
          adminRootsLoading.hidden = true;
        }
        req.finish();
      }
    }

    function openRenameRootModal(root) {
      const modalAlert = U.el('div', { className: 'alert alert-error', attrs: { role: 'alert' }, props: { hidden: true } });
      const labelInput = U.el('input', {
        id: 'modal-root-label',
        className: 'input',
        props: { type: 'text', value: root.label || '', required: true },
      });
      const pathReadonly = U.el('input', {
        id: 'modal-root-path',
        className: 'input',
        props: { type: 'text', value: U.formatRootPathDisplay(root.path), readOnly: true },
      });

      const cancelBtn = U.el('button', {
        className: 'btn btn-secondary',
        text: '取消',
        props: { type: 'button' },
        on: {
          click: function () {
            U.closeModal();
          },
        },
      });
      const saveBtn = U.el('button', {
        className: 'btn btn-primary',
        text: '保存名称',
        attrs: { form: 'modal-rename-root-form' },
        props: { type: 'submit' },
      });

      let submitting = false;
      const form = U.el(
        'form',
        {
          id: 'modal-rename-root-form',
          className: 'form-stack',
          attrs: { novalidate: '' },
          on: {
            submit: async function (event) {
              event.preventDefault();
              if (submitting) {
                return;
              }
              U.clearAlert(modalAlert);
              const nextLabel = labelInput.value.trim();
              if (!nextLabel) {
                U.setAlert(modalAlert, '请输入新的显示名称。', 'error');
                labelInput.focus();
                return;
              }
              if (nextLabel === root.label) {
                U.closeModal();
                return;
              }

              submitting = true;
              U.setModalLocked(true);
              saveBtn.disabled = true;
              saveBtn.textContent = '保存中…';

              try {
                await Api.updateAdminRoot(root.id, nextLabel);
                U.setModalLocked(false);
                U.closeModal();
                notifyLibraryChanged();
                U.showToast('授权目录名称已更新', 'success');
                loadRoots();
              } catch (err) {
                U.setModalLocked(false);
                if (err && err.status === 404) {
                  loadRoots();
                }
                U.setAlert(modalAlert, err.message || '更新授权目录失败。', 'error');
              } finally {
                submitting = false;
                saveBtn.disabled = false;
                saveBtn.textContent = '保存名称';
              }
            },
          },
        },
        [
          modalAlert,
          U.el('div', { className: 'field' }, [
            U.el('label', { className: 'field-label', attrs: { for: 'modal-root-label' }, text: '显示名称' }),
            labelInput,
          ]),
          U.el('div', { className: 'field' }, [
            U.el('label', { className: 'field-label', attrs: { for: 'modal-root-path' }, text: '共享内相对路径（不可修改）' }),
            pathReadonly,
          ]),
        ]
      );

      U.openModal({
        title: '重命名授权目录',
        content: form,
        footer: [cancelBtn, saveBtn],
        initialFocus: labelInput,
      });
    }

    async function confirmDeleteRoot(root) {
      const confirmed = await U.confirmDialog({
        title: '确认删除授权目录',
        message: '删除后，已分配给该目录的所有用户授权将立即失效，且不可撤销。确认继续吗？',
        detail: '名称：' + root.label + ' ｜ 路径：' + U.formatRootPathDisplay(root.path),
        confirmText: '确认删除',
        danger: true,
      });
      if (!confirmed) {
        return;
      }

      U.clearAlert(adminRootsAlert);
      try {
        await Api.deleteAdminRoot(root.id);
        notifyLibraryChanged();
        U.showToast('已删除授权目录“' + root.label + '”', 'success');
        loadRoots();
      } catch (err) {
        if (err && err.isAbort) {
          return;
        }
        if (err && err.status === 404) {
          U.showToast('该授权目录已不存在，已刷新列表', 'error');
          notifyLibraryChanged();
          loadRoots();
          return;
        }
        U.setAlert(adminRootsAlert, err.message || '删除授权目录失败。', 'error');
      }
    }

    openNasPickerBtn.addEventListener('click', function () {
      U.clearAlert(rootCreateAlert);
      openNasBrowserModal({
        mode: 'pick',
        initialPath: rootCreatePath.value,
        onSelect: applyPickedRootPath,
      });
    });

    adminRootsRefreshBtn.addEventListener('click', loadRoots);

    let rootCreating = false;
    rootCreateForm.addEventListener('submit', async function (event) {
      event.preventDefault();
      if (rootCreating) {
        return;
      }
      U.clearAlert(rootCreateAlert);

      const label = rootCreateLabel.value.trim();
      const path = rootCreatePath.value;

      if (!label) {
        U.setAlert(rootCreateAlert, '请输入授权目录的显示名称。', 'error');
        rootCreateLabel.focus();
        return;
      }

      rootCreating = true;
      rootCreateSubmitBtn.disabled = true;
      rootCreateSubmitBtn.textContent = '添加中…';

      try {
        await Api.createAdminRoot(label, path);
        rootCreateLabel.value = '';
        rootCreatePath.value = '';
        notifyLibraryChanged();
        U.setAlert(rootCreateAlert, '授权目录“' + label + '”已创建。请前往「账号与权限」为用户分配访问权限。', 'success');
        U.showToast('授权目录已添加', 'success');
        loadRoots();
      } catch (err) {
        if (err && err.isAbort) {
          return;
        }
        U.setAlert(rootCreateAlert, err.message || '添加授权目录失败。', 'error');
      } finally {
        rootCreating = false;
        rootCreateSubmitBtn.disabled = false;
        rootCreateSubmitBtn.textContent = '添加授权目录';
      }
    });

    // ---------- 账号与多选目录授权管理 ----------
    function renderUsersList() {
      adminUsersList.replaceChildren();
      if (state.users.length === 0) {
        adminUsersEmpty.replaceChildren(
          U.el('p', { className: 'empty-state-title', text: '暂无账号记录' })
        );
        adminUsersEmpty.hidden = false;
        return;
      }

      adminUsersEmpty.hidden = true;
      const currentUser = getCurrentUser();

      for (let i = 0; i < state.users.length; i += 1) {
        const user = state.users[i];
        const isSelf = Boolean(currentUser && String(currentUser.id) === String(user.id));

        const titleRow = U.el('div', { className: 'record-title-row' }, [
          U.el('span', { className: 'record-title', text: user.username }),
          U.el('span', {
            className: user.role === 'admin' ? 'badge badge-primary' : 'badge',
            text: U.formatRoleLabel(user.role),
          }),
          U.el('span', {
            className: user.enabled ? 'badge badge-success' : 'badge badge-danger',
            text: user.enabled ? '已启用' : '已停用',
          }),
          isSelf ? U.el('span', { className: 'badge badge-warning', text: '当前账号' }) : null,
        ]);

        const grantsBtn = U.el('button', {
          className: 'btn btn-primary btn-sm',
          text: '目录授权',
          props: { type: 'button' },
          on: {
            click: function () {
              openGrantsModal(user);
            },
          },
        });

        const editBtn = U.el('button', {
          className: 'btn btn-secondary btn-sm',
          text: '编辑 / 改密',
          props: { type: 'button' },
          on: {
            click: function () {
              openEditUserModal(user);
            },
          },
        });

        const toggleBtn = U.el('button', {
          className: user.enabled ? 'btn btn-ghost btn-sm' : 'btn btn-secondary btn-sm',
          text: user.enabled ? '停用' : '启用',
          props: { type: 'button' },
          on: {
            click: function () {
              toggleUserEnabled(user);
            },
          },
        });

        const deleteBtn = U.el('button', {
          className: 'btn btn-danger-outline btn-sm',
          text: '删除',
          props: { type: 'button' },
          on: {
            click: function () {
              confirmDeleteUser(user);
            },
          },
        });

        adminUsersList.appendChild(
          U.el('div', { className: 'record-card' }, [
            U.el('div', { className: 'record-info' }, [titleRow]),
            U.el('div', { className: 'record-actions' }, [grantsBtn, editBtn, toggleBtn, deleteBtn]),
          ])
        );
      }
    }

    async function loadUsers() {
      const req = gate.begin('adminUsers');
      U.clearAlert(adminUsersAlert);
      adminUsersLoading.hidden = false;
      adminUsersEmpty.hidden = true;

      try {
        const res = await Api.getAdminUsers(req.signal);
        if (!req.isCurrent()) {
          return;
        }
        state.users = Array.isArray(res && res.users) ? res.users : [];
        renderUsersList();
      } catch (err) {
        if (!req.isCurrent() || (err && err.isAbort)) {
          return;
        }
        U.setAlert(
          adminUsersAlert,
          err.message || '加载账号列表失败。',
          'error',
          U.el('button', {
            className: 'btn btn-secondary btn-sm',
            text: '重试',
            props: { type: 'button' },
            on: { click: loadUsers },
          })
        );
      } finally {
        if (req.isCurrent()) {
          adminUsersLoading.hidden = true;
        }
        req.finish();
      }
    }

    async function toggleUserEnabled(user) {
      const targetEnabled = !Boolean(user.enabled);
      if (!targetEnabled) {
        const confirmed = await U.confirmDialog({
          title: '确认停用账号',
          message: '停用后，该账号将无法继续登录或播放视频。确认停用吗？',
          detail: '账号：' + user.username + '（' + U.formatRoleLabel(user.role) + '）',
          confirmText: '确认停用',
          danger: true,
        });
        if (!confirmed) {
          return;
        }
      }

      U.clearAlert(adminUsersAlert);
      try {
        const res = await Api.updateAdminUser(user.id, { enabled: targetEnabled });
        const updated = res && res.user ? res.user : null;
        if (updated && typeof callbacks.onCurrentUserUpdated === 'function') {
          callbacks.onCurrentUserUpdated(updated);
        }
        U.showToast(
          targetEnabled ? '已启用账号“' + user.username + '”' : '已停用账号“' + user.username + '”',
          'success'
        );
        loadUsers();
      } catch (err) {
        if (err && err.isAbort) {
          return;
        }
        if (err && err.status === 404) {
          loadUsers();
        }
        U.setAlert(adminUsersAlert, err.message || '更新账号状态失败。', 'error');
      }
    }

    async function confirmDeleteUser(user) {
      const confirmed = await U.confirmDialog({
        title: '确认删除账号',
        message: '删除账号及其目录授权后无法恢复，确认永久删除该账号吗？',
        detail: '账号：' + user.username + '（' + U.formatRoleLabel(user.role) + '）',
        confirmText: '确认删除',
        danger: true,
      });
      if (!confirmed) {
        return;
      }

      U.clearAlert(adminUsersAlert);
      try {
        await Api.deleteAdminUser(user.id);
        U.showToast('已删除账号“' + user.username + '”', 'success');
        loadUsers();
      } catch (err) {
        if (err && err.isAbort) {
          return;
        }
        if (err && err.status === 404) {
          U.showToast('该账号已不存在，已刷新列表', 'error');
          loadUsers();
          return;
        }
        U.setAlert(adminUsersAlert, err.message || '删除账号失败。', 'error');
      }
    }

    function openEditUserModal(user) {
      const modalAlert = U.el('div', { className: 'alert', attrs: { role: 'alert' }, props: { hidden: true } });
      const currentUser = getCurrentUser();
      const isSelf = Boolean(currentUser && String(currentUser.id) === String(user.id));

      const usernameReadonly = U.el('input', {
        id: 'modal-edit-username',
        className: 'input',
        props: { type: 'text', value: user.username || '', readOnly: true },
      });

      const roleSelect = U.el(
        'select',
        { id: 'modal-edit-role', className: 'input' },
        [
          U.el('option', { props: { value: 'viewer' }, text: '普通用户' }),
          U.el('option', { props: { value: 'admin' }, text: '管理员' }),
        ]
      );
      roleSelect.value = user.role === 'admin' ? 'admin' : 'viewer';

      const enabledCheckbox = U.el('input', {
        id: 'modal-edit-enabled',
        className: 'grant-checkbox',
        props: { type: 'checkbox', checked: Boolean(user.enabled) },
      });

      const passwordInput = U.el('input', {
        id: 'modal-edit-password',
        className: 'input',
        attrs: { autocomplete: 'new-password' },
        props: { type: 'password', value: '', placeholder: '留空则不修改密码' },
      });

      const cancelBtn = U.el('button', {
        className: 'btn btn-secondary',
        text: '取消',
        props: { type: 'button' },
        on: {
          click: function () {
            U.closeModal();
          },
        },
      });
      const saveBtn = U.el('button', {
        className: 'btn btn-primary',
        text: '保存修改',
        attrs: { form: 'modal-edit-user-form' },
        props: { type: 'submit' },
      });

      let submitting = false;
      const formChildren = [
        modalAlert,
        isSelf
          ? U.el('div', {
              className: 'alert alert-warning',
              text: '您正在编辑当前登录的管理员账号；如降低角色将立即退出管理台。',
            })
          : null,
        U.el('div', { className: 'field' }, [
          U.el('label', { className: 'field-label', attrs: { for: 'modal-edit-username' }, text: '用户名' }),
          usernameReadonly,
        ]),
        U.el('div', { className: 'field' }, [
          U.el('label', { className: 'field-label', attrs: { for: 'modal-edit-role' }, text: '账号角色' }),
          roleSelect,
        ]),
        U.el('div', { className: 'field' }, [
          U.el('label', { className: 'checkbox-row' }, [enabledCheckbox, U.el('span', { text: '启用该账号（取消勾选将停用账号）' })]),
        ]),
        U.el('div', { className: 'field' }, [
          U.el('label', { className: 'field-label', attrs: { for: 'modal-edit-password' }, text: '重置密码（可选）' }),
          passwordInput,
          U.el('p', { className: 'field-hint', text: '不会显示原密码；留空保存将保留原密码。' }),
        ]),
      ];

      const form = U.el(
        'form',
        {
          id: 'modal-edit-user-form',
          className: 'form-stack',
          attrs: { novalidate: '' },
          on: {
            submit: async function (event) {
              event.preventDefault();
              if (submitting) {
                return;
              }
              U.clearAlert(modalAlert);

              const payload = {};
              if (roleSelect.value !== user.role) {
                payload.role = roleSelect.value;
              }
              if (enabledCheckbox.checked !== Boolean(user.enabled)) {
                payload.enabled = enabledCheckbox.checked;
              }
              if (passwordInput.value !== '') {
                payload.password = passwordInput.value;
              }

              if (Object.keys(payload).length === 0) {
                U.setAlert(modalAlert, '当前未做任何修改。', 'info');
                return;
              }

              submitting = true;
              U.setModalLocked(true);
              saveBtn.disabled = true;
              saveBtn.textContent = '保存中…';

              try {
                const res = await Api.updateAdminUser(user.id, payload);
                passwordInput.value = '';
                U.setModalLocked(false);
                U.closeModal();

                const updated = res && res.user ? res.user : null;
                const demotedSelf = Boolean(isSelf && updated && updated.role !== 'admin');
                if (updated && typeof callbacks.onCurrentUserUpdated === 'function') {
                  callbacks.onCurrentUserUpdated(updated);
                }
                if (!demotedSelf) {
                  U.showToast('已更新账号“' + user.username + '”', 'success');
                  loadUsers();
                }
              } catch (err) {
                U.setModalLocked(false);
                if (err && err.status === 404) {
                  loadUsers();
                }
                U.setAlert(modalAlert, err.message || '修改账号信息失败。', 'error');
              } finally {
                submitting = false;
                saveBtn.disabled = false;
                saveBtn.textContent = '保存修改';
              }
            },
          },
        },
        formChildren
      );

      U.openModal({
        title: '编辑账号：' + user.username,
        content: form,
        footer: [cancelBtn, saveBtn],
        initialFocus: roleSelect,
        onClose: function () {
          passwordInput.value = '';
        },
      });
    }

    function openGrantsModal(user) {
      const modalAlert = U.el('div', { className: 'alert alert-error', attrs: { role: 'alert' }, props: { hidden: true } });
      const loadingBox = U.el(
        'div',
        { className: 'inline-loading', attrs: { role: 'status', 'aria-live': 'polite' } },
        [U.el('span', { className: 'spinner spinner-sm', attrs: { 'aria-hidden': 'true' } }), U.el('span', { text: '正在加载目录与当前授权…' })]
      );
      const countLabel = U.el('span', { className: 'grant-count', text: '' });
      const selectAllBtn = U.el('button', {
        className: 'btn btn-secondary btn-sm',
        text: '全选',
        props: { type: 'button', disabled: true },
      });
      const clearAllBtn = U.el('button', {
        className: 'btn btn-ghost btn-sm',
        text: '清空',
        props: { type: 'button', disabled: true },
      });
      const toolbar = U.el('div', { className: 'grant-toolbar', props: { hidden: true } }, [
        countLabel,
        U.el('div', { className: 'btn-row' }, [selectAllBtn, clearAllBtn]),
      ]);
      const emptyBox = U.el('div', { className: 'empty-state', props: { hidden: true } });
      const listBox = U.el('div', { className: 'grant-list' });

      const cancelBtn = U.el('button', {
        className: 'btn btn-secondary',
        text: '取消',
        props: { type: 'button' },
        on: {
          click: function () {
            U.closeModal();
          },
        },
      });
      const saveBtn = U.el('button', {
        className: 'btn btn-primary',
        text: '保存授权',
        props: { type: 'button', disabled: true },
      });

      let allRoots = [];
      const selectedKeys = new Set();
      let saving = false;

      function updateGrantCounter() {
        countLabel.textContent = '已选择 ' + selectedKeys.size + ' / ' + allRoots.length + ' 个授权目录';
      }

      function renderGrantOptions() {
        listBox.replaceChildren();
        if (allRoots.length === 0) {
          toolbar.hidden = true;
          emptyBox.replaceChildren(
            U.el('p', { className: 'empty-state-title', text: '尚未创建任何授权目录' }),
            U.el('p', { className: 'empty-state-desc', text: '请先切换到「授权目录」添加至少一个目录，再回到此处为账号分配权限。' })
          );
          emptyBox.hidden = false;
          saveBtn.disabled = false;
          return;
        }

        emptyBox.hidden = true;
        toolbar.hidden = false;
        selectAllBtn.disabled = false;
        clearAllBtn.disabled = false;
        saveBtn.disabled = false;
        updateGrantCounter();

        for (let i = 0; i < allRoots.length; i += 1) {
          const root = allRoots[i];
          const key = String(root.id);
          const checkbox = U.el('input', {
            className: 'grant-checkbox',
            props: { type: 'checkbox', checked: selectedKeys.has(key) },
            on: {
              change: function (event) {
                if (event.currentTarget.checked) {
                  selectedKeys.add(key);
                } else {
                  selectedKeys.delete(key);
                }
                updateGrantCounter();
              },
            },
          });

          const optionLabel = U.el('label', { className: 'grant-option' }, [
            checkbox,
            U.el('div', { className: 'grant-option-text' }, [
              U.el('span', { className: 'grant-option-label', text: root.label }),
              U.el('span', { className: 'grant-option-path', text: '路径：' + U.formatRootPathDisplay(root.path) }),
            ]),
          ]);
          listBox.appendChild(optionLabel);
        }
      }

      selectAllBtn.addEventListener('click', function () {
        for (let i = 0; i < allRoots.length; i += 1) {
          selectedKeys.add(String(allRoots[i].id));
        }
        renderGrantOptions();
      });

      clearAllBtn.addEventListener('click', function () {
        selectedKeys.clear();
        renderGrantOptions();
      });

      saveBtn.addEventListener('click', async function () {
        if (saving) {
          return;
        }
        U.clearAlert(modalAlert);

        // 保留后端返回的原始 root.id 类型（数字或字符串）
        const rootIds = [];
        for (let i = 0; i < allRoots.length; i += 1) {
          if (selectedKeys.has(String(allRoots[i].id))) {
            rootIds.push(allRoots[i].id);
          }
        }

        saving = true;
        U.setModalLocked(true);
        saveBtn.disabled = true;
        saveBtn.textContent = '保存中…';

        try {
          await Api.updateUserGrants(user.id, rootIds);
          U.setModalLocked(false);
          U.closeModal();
          notifyLibraryChanged();
          U.showToast('已更新“' + user.username + '”的目录授权（共 ' + rootIds.length + ' 个）', 'success');
        } catch (err) {
          U.setModalLocked(false);
          if (err && err.status === 404) {
            loadUsers();
            loadRoots();
          }
          U.setAlert(modalAlert, err.message || '保存目录授权失败。', 'error');
        } finally {
          saving = false;
          saveBtn.disabled = false;
          saveBtn.textContent = '保存授权';
        }
      });

      async function loadGrantData() {
        const rootsReq = gate.begin('grantsRoots');
        const grantsReq = gate.begin('userGrants');
        U.clearAlert(modalAlert);
        loadingBox.hidden = false;
        toolbar.hidden = true;
        emptyBox.hidden = true;
        listBox.replaceChildren();
        saveBtn.disabled = true;

        try {
          const results = await Promise.all([
            Api.getAdminRoots(rootsReq.signal),
            Api.getUserGrants(user.id, grantsReq.signal),
          ]);
          if (!rootsReq.isCurrent() || !grantsReq.isCurrent()) {
            return;
          }
          allRoots = Array.isArray(results[0] && results[0].roots) ? results[0].roots : [];
          state.roots = allRoots;
          const grantedIds = Array.isArray(results[1] && results[1].rootIds) ? results[1].rootIds : [];

          selectedKeys.clear();
          const validRootKeys = new Set();
          for (let i = 0; i < allRoots.length; i += 1) {
            validRootKeys.add(String(allRoots[i].id));
          }
          for (let i = 0; i < grantedIds.length; i += 1) {
            const key = String(grantedIds[i]);
            if (validRootKeys.has(key)) {
              selectedKeys.add(key);
            }
          }
          renderGrantOptions();
        } catch (err) {
          if (!rootsReq.isCurrent() || !grantsReq.isCurrent() || (err && err.isAbort)) {
            return;
          }
          if (err && err.status === 404) {
            loadUsers();
          }
          U.setAlert(
            modalAlert,
            err.message || '加载目录授权失败。',
            'error',
            U.el('button', {
              className: 'btn btn-secondary btn-sm',
              text: '重试',
              props: { type: 'button' },
              on: { click: loadGrantData },
            })
          );
        } finally {
          if (rootsReq.isCurrent() && grantsReq.isCurrent()) {
            loadingBox.hidden = true;
          }
          rootsReq.finish();
          grantsReq.finish();
        }
      }

      U.openModal({
        title: '目录授权：' + user.username,
        content: [
          U.el('p', {
            className: 'panel-desc',
            text: '勾选该账号可浏览与播放的根目录，保存后将整体替换原有授权。',
          }),
          modalAlert,
          loadingBox,
          toolbar,
          emptyBox,
          listBox,
        ],
        footer: [cancelBtn, saveBtn],
        onClose: function () {
          gate.abort('grantsRoots');
          gate.abort('userGrants');
        },
      });

      loadGrantData();
    }

    adminUsersRefreshBtn.addEventListener('click', loadUsers);

    let userCreating = false;
    userCreateForm.addEventListener('submit', async function (event) {
      event.preventDefault();
      if (userCreating) {
        return;
      }
      U.clearAlert(userCreateAlert);

      const username = userCreateUsername.value.trim();
      const password = userCreatePassword.value;
      const role = userCreateRole.value === 'admin' ? 'admin' : 'viewer';

      if (!username) {
        U.setAlert(userCreateAlert, '请输入新账号的用户名。', 'error');
        userCreateUsername.focus();
        return;
      }
      if (!password) {
        U.setAlert(userCreateAlert, '请输入新账号的初始密码。', 'error');
        userCreatePassword.focus();
        return;
      }

      userCreating = true;
      userCreateSubmitBtn.disabled = true;
      userCreateSubmitBtn.textContent = '创建中…';

      try {
        const res = await Api.createAdminUser({
          username: username,
          password: password,
          role: role,
        });
        userCreateUsername.value = '';
        userCreatePassword.value = '';
        userCreateRole.value = 'viewer';

        const created = res && res.user ? res.user : null;
        U.setAlert(
          userCreateAlert,
          '账号“' + (created ? created.username : username) + '”已创建，请在账号列表中点击「目录授权」分配访问目录。',
          'success'
        );
        U.showToast('账号创建成功', 'success');
        loadUsers();
      } catch (err) {
        if (err && err.isAbort) {
          return;
        }
        U.setAlert(userCreateAlert, err.message || '创建账号失败。', 'error');
      } finally {
        userCreating = false;
        userCreateSubmitBtn.disabled = false;
        userCreateSubmitBtn.textContent = '创建账号';
      }
    });

    function activate() {
      switchTab(state.activeTab || 'nas');
    }

    function reset() {
      gate.abortAll();
      state.loadedNas = null;
      state.nasFetched = false;
      state.nasSaving = false;
      state.nasTesting = false;
      state.roots = [];
      state.users = [];
      nasHostInput.value = '';
      nasPortInput.value = '445';
      nasShareInput.value = '';
      nasDomainInput.value = '';
      nasUsernameInput.value = '';
      nasPasswordInput.value = '';
      rootCreateLabel.value = '';
      rootCreatePath.value = '';
      userCreateUsername.value = '';
      userCreatePassword.value = '';
      userCreateRole.value = 'viewer';
      U.clearAlert(nasAlert);
      U.clearAlert(nasTestResult);
      U.clearAlert(rootCreateAlert);
      U.clearAlert(adminRootsAlert);
      U.clearAlert(userCreateAlert);
      U.clearAlert(adminUsersAlert);
      adminRootsList.replaceChildren();
      adminUsersList.replaceChildren();
    }

    return {
      activate: activate,
      reset: reset,
    };
  }

  window.SWPAdmin = {
    create: createAdminController,
  };
})();
