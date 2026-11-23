(function () {
  'use strict';

  const SVG_NS = 'http://www.w3.org/2000/svg';

  const ICON_SPECS = {
    folder: [
      {
        tag: 'path',
        attrs: {
          d: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z',
        },
      },
    ],
    video: [
      { tag: 'rect', attrs: { x: '2', y: '4', width: '20', height: '16', rx: '2' } },
      { tag: 'polygon', attrs: { points: '10 9 15 12 10 15 10 9' } },
    ],
    play: [{ tag: 'polygon', attrs: { points: '6 4 20 12 6 20 6 4' } }],
    film: [
      { tag: 'rect', attrs: { x: '3', y: '4', width: '18', height: '16', rx: '2' } },
      { tag: 'line', attrs: { x1: '7', y1: '4', x2: '7', y2: '20' } },
      { tag: 'line', attrs: { x1: '17', y1: '4', x2: '17', y2: '20' } },
      { tag: 'line', attrs: { x1: '3', y1: '9', x2: '7', y2: '9' } },
      { tag: 'line', attrs: { x1: '3', y1: '15', x2: '7', y2: '15' } },
      { tag: 'line', attrs: { x1: '17', y1: '9', x2: '21', y2: '9' } },
      { tag: 'line', attrs: { x1: '17', y1: '15', x2: '21', y2: '15' } },
    ],
    chevron: [{ tag: 'polyline', attrs: { points: '9 6 15 12 9 18' } }],
    tv: [
      { tag: 'rect', attrs: { x: '2', y: '4', width: '20', height: '13', rx: '2' } },
      { tag: 'line', attrs: { x1: '8', y1: '21', x2: '16', y2: '21' } },
      { tag: 'line', attrs: { x1: '12', y1: '17', x2: '12', y2: '21' } },
    ],
    download: [
      { tag: 'path', attrs: { d: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4' } },
      { tag: 'polyline', attrs: { points: '7 10 12 15 17 10' } },
      { tag: 'line', attrs: { x1: '12', y1: '15', x2: '12', y2: '3' } },
    ],
    alert: [
      {
        tag: 'path',
        attrs: { d: 'M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z' },
      },
      { tag: 'line', attrs: { x1: '12', y1: '9', x2: '12', y2: '13' } },
      { tag: 'line', attrs: { x1: '12', y1: '17', x2: '12.01', y2: '17' } },
    ],
    harddrive: [
      { tag: 'line', attrs: { x1: '22', y1: '12', x2: '2', y2: '12' } },
      {
        tag: 'path',
        attrs: {
          d: 'M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z',
        },
      },
      { tag: 'line', attrs: { x1: '6', y1: '16', x2: '6.01', y2: '16' } },
      { tag: 'line', attrs: { x1: '10', y1: '16', x2: '10.01', y2: '16' } },
    ],
  };

  function createIcon(name, size) {
    const dim = String(size || 18);
    const svg = document.createElementNS(SVG_NS, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('width', dim);
    svg.setAttribute('height', dim);
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '1.9');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.setAttribute('aria-hidden', 'true');

    const shapes = ICON_SPECS[name] || [];
    for (let i = 0; i < shapes.length; i += 1) {
      const shape = shapes[i];
      const node = document.createElementNS(SVG_NS, shape.tag);
      const keys = Object.keys(shape.attrs);
      for (let j = 0; j < keys.length; j += 1) {
        node.setAttribute(keys[j], shape.attrs[keys[j]]);
      }
      svg.appendChild(node);
    }
    return svg;
  }

  function el(tag, options, children) {
    const node = document.createElement(tag);
    if (options && typeof options === 'object') {
      if (options.className) {
        node.className = options.className;
      }
      if (options.id) {
        node.id = options.id;
      }
      if (options.text !== undefined && options.text !== null) {
        node.textContent = String(options.text);
      }
      if (options.attrs) {
        const attrKeys = Object.keys(options.attrs);
        for (let i = 0; i < attrKeys.length; i += 1) {
          const key = attrKeys[i];
          const val = options.attrs[key];
          if (val !== undefined && val !== null) {
            node.setAttribute(key, String(val));
          }
        }
      }
      if (options.props) {
        const propKeys = Object.keys(options.props);
        for (let i = 0; i < propKeys.length; i += 1) {
          const key = propKeys[i];
          node[key] = options.props[key];
        }
      }
      if (options.on) {
        const eventKeys = Object.keys(options.on);
        for (let i = 0; i < eventKeys.length; i += 1) {
          const evt = eventKeys[i];
          node.addEventListener(evt, options.on[evt]);
        }
      }
    }

    if (children !== undefined && children !== null) {
      const list = Array.isArray(children) ? children : [children];
      for (let i = 0; i < list.length; i += 1) {
        const child = list[i];
        if (child === null || child === undefined || child === false) {
          continue;
        }
        if (typeof child === 'string' || typeof child === 'number') {
          node.appendChild(document.createTextNode(String(child)));
        } else {
          node.appendChild(child);
        }
      }
    }
    return node;
  }

  function formatBytes(bytes) {
    if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) {
      return '—';
    }
    if (bytes < 1024) {
      return bytes + ' B';
    }
    const units = ['KB', 'MB', 'GB', 'TB'];
    let value = bytes / 1024;
    let unitIndex = 0;
    while (value >= 1024 && unitIndex < units.length - 1) {
      value /= 1024;
      unitIndex += 1;
    }
    const fixed = value >= 100 ? value.toFixed(0) : value >= 10 ? value.toFixed(1) : value.toFixed(2);
    return fixed + ' ' + units[unitIndex];
  }

  function normalizePathSegments(rawPath) {
    if (!rawPath || typeof rawPath !== 'string') {
      return [];
    }
    return rawPath
      .replace(/\\/g, '/')
      .replace(/^\/+|\/+$/g, '')
      .split('/')
      .filter(function (seg) {
        return seg.length > 0;
      });
  }

  function buildBreadcrumbs(rootLabel, rawPath) {
    const segments = normalizePathSegments(rawPath);
    const items = [{ label: rootLabel, path: '' }];
    let acc = '';
    for (let i = 0; i < segments.length; i += 1) {
      acc = acc ? acc + '/' + segments[i] : segments[i];
      items.push({ label: segments[i], path: acc });
    }
    return items;
  }

  function getParentPath(rawPath) {
    const segments = normalizePathSegments(rawPath);
    if (segments.length <= 1) {
      return '';
    }
    return segments.slice(0, segments.length - 1).join('/');
  }

  function isWholeSharePath(path) {
    return path === null || path === undefined || String(path) === '';
  }

  function formatRootPathDisplay(path) {
    if (isWholeSharePath(path)) {
      return '（整个共享根目录）';
    }
    return String(path);
  }

  function formatRoleLabel(role) {
    return role === 'admin' ? '管理员' : '普通用户';
  }

  function sortEntries(entries) {
    if (!Array.isArray(entries)) {
      return [];
    }
    return entries.slice().sort(function (a, b) {
      const aDir = a && a.kind === 'directory' ? 0 : 1;
      const bDir = b && b.kind === 'directory' ? 0 : 1;
      if (aDir !== bDir) {
        return aDir - bDir;
      }
      const aName = a && a.name ? String(a.name) : '';
      const bName = b && b.name ? String(b.name) : '';
      return aName.localeCompare(bName, 'zh-Hans-CN', { numeric: true, sensitivity: 'base' });
    });
  }

  function setAlert(box, message, variant, actionButton) {
    if (!box) {
      return;
    }
    box.replaceChildren();
    box.className = 'alert ' + (variant ? 'alert-' + variant : 'alert-error');
    if (!message) {
      box.hidden = true;
      return;
    }
    const textNode = el('span', { className: 'alert-text', text: message });
    box.appendChild(textNode);
    if (actionButton) {
      box.appendChild(actionButton);
    }
    box.hidden = false;
  }

  function clearAlert(box) {
    if (!box) {
      return;
    }
    box.replaceChildren();
    box.hidden = true;
  }

  function createRequestGate() {
    const seqMap = Object.create(null);
    const controllerMap = Object.create(null);

    function begin(channel) {
      if (controllerMap[channel]) {
        controllerMap[channel].abort();
      }
      const nextSeq = (seqMap[channel] || 0) + 1;
      seqMap[channel] = nextSeq;
      const controller = new AbortController();
      controllerMap[channel] = controller;

      return {
        signal: controller.signal,
        isCurrent: function () {
          return seqMap[channel] === nextSeq;
        },
        finish: function () {
          if (controllerMap[channel] === controller) {
            delete controllerMap[channel];
          }
        },
      };
    }

    function abort(channel) {
      seqMap[channel] = (seqMap[channel] || 0) + 1;
      if (controllerMap[channel]) {
        controllerMap[channel].abort();
        delete controllerMap[channel];
      }
    }

    function abortAll() {
      const channels = Object.keys(seqMap);
      for (let i = 0; i < channels.length; i += 1) {
        abort(channels[i]);
      }
      const activeControllers = Object.keys(controllerMap);
      for (let i = 0; i < activeControllers.length; i += 1) {
        abort(activeControllers[i]);
      }
    }

    return {
      begin: begin,
      abort: abort,
      abortAll: abortAll,
    };
  }

  function showToast(message, variant) {
    const container = document.getElementById('toast-container');
    if (!container || !message) {
      return;
    }
    while (container.children.length >= 4) {
      container.removeChild(container.firstChild);
    }
    const toast = el('div', {
      className: 'toast' + (variant ? ' toast-' + variant : ''),
      text: message,
      attrs: { role: variant === 'error' ? 'alert' : 'status' },
    });
    container.appendChild(toast);
    window.setTimeout(function () {
      if (toast.parentNode === container) {
        container.removeChild(toast);
      }
    }, 3800);
  }

  // 无障碍通用弹窗控制器
  const modalState = {
    isOpen: false,
    locked: false,
    previousFocus: null,
    onClose: null,
  };

  function getFocusableElements(root) {
    if (!root) {
      return [];
    }
    const nodes = root.querySelectorAll(
      'button:not([disabled]), [href], input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
    );
    const list = [];
    for (let i = 0; i < nodes.length; i += 1) {
      const item = nodes[i];
      if (!item.hidden && item.offsetParent !== null) {
        list.push(item);
      }
    }
    return list;
  }

  function handleModalKeydown(event) {
    if (!modalState.isOpen) {
      return;
    }
    if (event.key === 'Escape') {
      if (!modalState.locked) {
        event.preventDefault();
        closeModal();
      }
      return;
    }
    if (event.key === 'Tab') {
      const dialog = document.getElementById('modal-dialog');
      const focusables = getFocusableElements(dialog);
      if (focusables.length === 0) {
        event.preventDefault();
        return;
      }
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (event.shiftKey) {
        if (document.activeElement === first || !dialog.contains(document.activeElement)) {
          event.preventDefault();
          last.focus();
        }
      } else if (document.activeElement === last || !dialog.contains(document.activeElement)) {
        event.preventDefault();
        first.focus();
      }
    }
  }

  function initModalEvents() {
    const backdrop = document.getElementById('modal-root');
    const closeBtn = document.getElementById('modal-close-btn');
    if (backdrop) {
      backdrop.addEventListener('click', function (event) {
        if (event.target === backdrop && !modalState.locked) {
          closeModal();
        }
      });
    }
    if (closeBtn) {
      closeBtn.addEventListener('click', function () {
        if (!modalState.locked) {
          closeModal();
        }
      });
    }
    document.addEventListener('keydown', handleModalKeydown);
  }

  function setModalLocked(locked) {
    modalState.locked = Boolean(locked);
    const closeBtn = document.getElementById('modal-close-btn');
    if (closeBtn) {
      closeBtn.disabled = modalState.locked;
    }
  }

  function closeModal(force) {
    if (!modalState.isOpen) {
      return;
    }
    if (modalState.locked && !force) {
      return;
    }
    const backdrop = document.getElementById('modal-root');
    const body = document.getElementById('modal-body');
    const footer = document.getElementById('modal-footer');
    const onClose = modalState.onClose;
    const prevFocus = modalState.previousFocus;

    modalState.isOpen = false;
    modalState.locked = false;
    modalState.onClose = null;
    modalState.previousFocus = null;

    if (backdrop) {
      backdrop.hidden = true;
    }
    if (body) {
      body.replaceChildren();
    }
    if (footer) {
      footer.replaceChildren();
    }
    const closeBtn = document.getElementById('modal-close-btn');
    if (closeBtn) {
      closeBtn.disabled = false;
    }

    if (typeof onClose === 'function') {
      try {
        onClose();
      } catch (_err) {
        // 忽略关闭钩子中的非致命错误
      }
    }
    if (prevFocus && typeof prevFocus.focus === 'function' && document.contains(prevFocus)) {
      prevFocus.focus();
    }
  }

  function openModal(options) {
    if (modalState.isOpen) {
      closeModal(true);
    }
    const backdrop = document.getElementById('modal-root');
    const dialog = document.getElementById('modal-dialog');
    const titleEl = document.getElementById('modal-title');
    const bodyEl = document.getElementById('modal-body');
    const footerEl = document.getElementById('modal-footer');

    modalState.previousFocus = document.activeElement;
    modalState.isOpen = true;
    modalState.locked = false;
    modalState.onClose = options && typeof options.onClose === 'function' ? options.onClose : null;

    dialog.className =
      'modal-dialog' +
      (options && options.size === 'lg' ? ' modal-lg' : '') +
      (options && options.tone === 'danger' ? ' modal-danger' : '');
    titleEl.textContent = options && options.title ? String(options.title) : '';
    bodyEl.replaceChildren();
    footerEl.replaceChildren();

    if (options && options.content) {
      const contentItems = Array.isArray(options.content) ? options.content : [options.content];
      for (let i = 0; i < contentItems.length; i += 1) {
        if (contentItems[i]) {
          bodyEl.appendChild(contentItems[i]);
        }
      }
    }

    if (options && options.footer && options.footer.length > 0) {
      footerEl.hidden = false;
      for (let i = 0; i < options.footer.length; i += 1) {
        if (options.footer[i]) {
          footerEl.appendChild(options.footer[i]);
        }
      }
    } else {
      footerEl.hidden = true;
    }

    backdrop.hidden = false;

    window.requestAnimationFrame(function () {
      if (options && options.initialFocus && typeof options.initialFocus.focus === 'function') {
        options.initialFocus.focus();
        return;
      }
      const focusables = getFocusableElements(dialog);
      if (focusables.length > 0) {
        focusables[0].focus();
      } else {
        dialog.focus();
      }
    });
  }

  function confirmDialog(options) {
    return new Promise(function (resolve) {
      let settled = false;
      function finish(result) {
        if (settled) {
          return;
        }
        settled = true;
        resolve(result);
      }

      const messageNode = el('p', { className: 'confirm-message', text: options.message || '确认执行此操作吗？' });
      const content = [
        options.danger
          ? el('div', { className: 'confirm-danger-row' }, [
              el('span', { className: 'confirm-danger-icon' }, createIcon('alert', 20)),
              messageNode,
            ])
          : messageNode,
      ];
      if (options.detail) {
        // 详情中可能含 NAS 路径，样式保留全部空白以原样展示
        content.push(
          el('div', {
            className: 'confirm-detail',
            text: options.detail,
          })
        );
      }

      const cancelBtn = el('button', {
        className: 'btn btn-secondary',
        text: options.cancelText || '取消',
        props: { type: 'button' },
        on: {
          click: function () {
            finish(false);
            closeModal();
          },
        },
      });

      const confirmBtn = el('button', {
        className: options.danger ? 'btn btn-danger' : 'btn btn-primary',
        text: options.confirmText || '确认',
        props: { type: 'button' },
        on: {
          click: function () {
            finish(true);
            closeModal();
          },
        },
      });

      openModal({
        title: options.title || '确认操作',
        content: content,
        footer: [cancelBtn, confirmBtn],
        initialFocus: options.danger ? cancelBtn : confirmBtn,
        tone: options.danger ? 'danger' : '',
        onClose: function () {
          finish(false);
        },
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initModalEvents);
  } else {
    initModalEvents();
  }

  window.SWPUtil = {
    createIcon: createIcon,
    el: el,
    formatBytes: formatBytes,
    normalizePathSegments: normalizePathSegments,
    buildBreadcrumbs: buildBreadcrumbs,
    getParentPath: getParentPath,
    isWholeSharePath: isWholeSharePath,
    formatRootPathDisplay: formatRootPathDisplay,
    formatRoleLabel: formatRoleLabel,
    sortEntries: sortEntries,
    setAlert: setAlert,
    clearAlert: clearAlert,
    createRequestGate: createRequestGate,
    showToast: showToast,
    openModal: openModal,
    closeModal: closeModal,
    setModalLocked: setModalLocked,
    confirmDialog: confirmDialog,
  };
})();
