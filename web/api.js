(function () {
  'use strict';

  const WRITE_METHODS = {
    POST: true,
    PUT: true,
    PATCH: true,
    DELETE: true,
  };

  let unauthorizedCallback = null;

  class ApiError extends Error {
    constructor(message, status, code, isAbort) {
      super(message);
      this.name = 'ApiError';
      this.status = typeof status === 'number' ? status : 0;
      this.code = code || '';
      this.isAbort = Boolean(isAbort);
    }
  }

  function defaultStatusMessage(status) {
    switch (status) {
      case 400:
        return '请求参数有误，请检查填写内容。';
      case 401:
        return '登录状态已失效，请重新登录。';
      case 403:
        return '当前账号没有执行该操作的权限。';
      case 404:
        return '目标资源不存在，可能已被删除或撤销授权。';
      case 409:
        return '操作发生冲突，请刷新后重试。';
      case 422:
        return '提交的数据格式不符合要求。';
      case 429:
        return '请求过于频繁，请稍后再试。';
      case 500:
        return '服务器处理请求时出现异常。';
      case 502:
      case 503:
      case 504:
        return '后端服务或 NAS 存储暂时不可用，请稍后重试。';
      default:
        return '请求失败（HTTP ' + status + '）。';
    }
  }

  function setUnauthorizedHandler(handler) {
    unauthorizedCallback = typeof handler === 'function' ? handler : null;
  }

  async function request(url, options) {
    const opts = options || {};
    const method = (opts.method || 'GET').toUpperCase();
    const isWrite = Boolean(WRITE_METHODS[method]);
    const headers = {
      Accept: 'application/json',
    };

    const fetchInit = {
      method: method,
      credentials: 'same-origin',
      headers: headers,
      signal: opts.signal,
    };

    if (!isWrite) {
      fetchInit.cache = 'no-store';
    }

    if (isWrite) {
      headers['X-Player-Request'] = '1';
      headers['Content-Type'] = 'application/json';
      if (opts.body !== undefined) {
        fetchInit.body = JSON.stringify(opts.body);
      } else if (method !== 'DELETE') {
        fetchInit.body = '{}';
      }
    }

    let response;
    try {
      response = await fetch(url, fetchInit);
    } catch (err) {
      if (err && err.name === 'AbortError') {
        throw new ApiError('请求已取消', 0, 'ABORTED', true);
      }
      throw new ApiError('网络连接失败，请确认服务在线后重试。', 0, 'NETWORK_ERROR', false);
    }

    if (response.status === 204) {
      return null;
    }

    const rawText = await response.text();
    let parsed = null;
    if (rawText) {
      try {
        parsed = JSON.parse(rawText);
      } catch (_err) {
        parsed = null;
      }
    }

    if (!response.ok) {
      const errObj = parsed && parsed.error && typeof parsed.error === 'object' ? parsed.error : null;
      const code = errObj && errObj.code ? String(errObj.code) : '';
      const message =
        errObj && errObj.message ? String(errObj.message) : defaultStatusMessage(response.status);
      const apiError = new ApiError(message, response.status, code, false);

      if (response.status === 401 && !opts.skipUnauthorizedHandler && unauthorizedCallback) {
        unauthorizedCallback(apiError);
      }
      throw apiError;
    }

    return parsed;
  }

  function buildBrowseQuery(path) {
    const params = new URLSearchParams();
    params.set('path', path ? String(path) : '');
    return params.toString();
  }

  function buildStreamUrl(rootId, relPath) {
    const params = new URLSearchParams();
    params.set('path', relPath ? String(relPath) : '');
    return '/api/library/' + encodeURIComponent(String(rootId)) + '/stream?' + params.toString();
  }

  // 下载与播放共用同一套身份 / 权限校验；成功时服务端以附件形式返回原文件
  function buildDownloadUrl(rootId, relPath) {
    const params = new URLSearchParams();
    params.set('path', relPath ? String(relPath) : '');
    return '/api/library/' + encodeURIComponent(String(rootId)) + '/download?' + params.toString();
  }

  window.SWPApi = {
    ApiError: ApiError,
    setUnauthorizedHandler: setUnauthorizedHandler,
    buildStreamUrl: buildStreamUrl,
    buildDownloadUrl: buildDownloadUrl,

    // 仅用 HEAD 预检下载是否仍可用（不读取文件内容）；真正的下载交给浏览器原生 GET，并由服务端再次鉴权
    checkDownload: function (rootId, relPath, signal) {
      return request(buildDownloadUrl(rootId, relPath), {
        method: 'HEAD',
        signal: signal,
      });
    },

    // 投屏（Google Cast / DLNA）：由服务端在同一 IPv4 局域网内发现并控制电视，浏览器不直接联系电视
    discoverCastDevices: function (signal) {
      return request('/api/cast/discover', {
        method: 'POST',
        body: {},
        signal: signal,
      });
    },

    startCast: function (rootId, deviceId, relPath, startSeconds, signal) {
      return request('/api/library/' + encodeURIComponent(String(rootId)) + '/cast', {
        method: 'POST',
        body: {
          deviceId: String(deviceId),
          path: relPath ? String(relPath) : '',
          startSeconds: startSeconds,
        },
        signal: signal,
      });
    },

    stopCast: function (castId, signal) {
      return request('/api/cast/stop', {
        method: 'POST',
        body: { castId: String(castId) },
        signal: signal,
      });
    },

    // 会话与认证
    getSession: function (signal) {
      return request('/api/session', {
        method: 'GET',
        signal: signal,
        skipUnauthorizedHandler: true,
      });
    },

    login: function (username, password) {
      return request('/api/login', {
        method: 'POST',
        body: { username: username, password: password },
        skipUnauthorizedHandler: true,
      });
    },

    logout: function () {
      return request('/api/logout', {
        method: 'POST',
        body: {},
        skipUnauthorizedHandler: true,
      });
    },

    // 普通用户与管理员共用播放库接口
    getLibraryRoots: function (signal) {
      return request('/api/library', {
        method: 'GET',
        signal: signal,
      });
    },

    browseLibrary: function (rootId, path, signal) {
      const query = buildBrowseQuery(path);
      return request('/api/library/' + encodeURIComponent(String(rootId)) + '/browse?' + query, {
        method: 'GET',
        signal: signal,
      });
    },

    // 管理员：NAS 连接配置、测试与浏览
    getAdminNas: function (signal) {
      return request('/api/admin/nas', {
        method: 'GET',
        signal: signal,
      });
    },

    updateAdminNas: function (payload) {
      return request('/api/admin/nas', {
        method: 'PUT',
        body: payload,
      });
    },

    testAdminNas: function () {
      return request('/api/admin/nas/test', {
        method: 'POST',
        body: {},
      });
    },

    browseAdminNas: function (path, signal) {
      const query = buildBrowseQuery(path);
      return request('/api/admin/nas/browse?' + query, {
        method: 'GET',
        signal: signal,
      });
    },

    // 管理员：授权根目录
    getAdminRoots: function (signal) {
      return request('/api/admin/roots', {
        method: 'GET',
        signal: signal,
      });
    },

    createAdminRoot: function (label, path) {
      return request('/api/admin/roots', {
        method: 'POST',
        body: { label: label, path: path },
      });
    },

    updateAdminRoot: function (rootId, label) {
      return request('/api/admin/roots/' + encodeURIComponent(String(rootId)), {
        method: 'PATCH',
        body: { label: label },
      });
    },

    deleteAdminRoot: function (rootId) {
      return request('/api/admin/roots/' + encodeURIComponent(String(rootId)), {
        method: 'DELETE',
      });
    },

    // 管理员：账号与目录授权
    getAdminUsers: function (signal) {
      return request('/api/admin/users', {
        method: 'GET',
        signal: signal,
      });
    },

    createAdminUser: function (payload) {
      return request('/api/admin/users', {
        method: 'POST',
        body: payload,
      });
    },

    updateAdminUser: function (userId, payload) {
      return request('/api/admin/users/' + encodeURIComponent(String(userId)), {
        method: 'PATCH',
        body: payload,
      });
    },

    deleteAdminUser: function (userId) {
      return request('/api/admin/users/' + encodeURIComponent(String(userId)), {
        method: 'DELETE',
      });
    },

    getUserGrants: function (userId, signal) {
      return request('/api/admin/users/' + encodeURIComponent(String(userId)) + '/grants', {
        method: 'GET',
        signal: signal,
      });
    },

    updateUserGrants: function (userId, rootIds) {
      return request('/api/admin/users/' + encodeURIComponent(String(userId)) + '/grants', {
        method: 'PUT',
        body: { rootIds: rootIds },
      });
    },
  };
})();
