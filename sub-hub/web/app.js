const state = {
  token: '',
  page: 'dashboard',
  tokenTab: 'children',
  overview: null,
  nodes: [],
  collections: [],
  masters: [],
  children: [],
  logs: [],
  settings: {},
  selectedNodes: new Set(),
  nodeSearch: '',
  enabledOnly: false,
};

const pageMeta = {
  dashboard: ['概览', '节点、集合与订阅状态'],
  nodes: ['节点池', '维护可组合的节点资源'],
  collections: ['集合', '将节点组合授权给不同 Token'],
  tokens: ['Token', '管理主 Token 与受限子 Token'],
  links: ['订阅链接', '生成各客户端的真实订阅地址'],
  logs: ['访问日志', '查看订阅请求与消耗'],
  settings: ['设置与备份', '配置订阅输出并迁移数据'],
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => Array.from(document.querySelectorAll(selector));

function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>"']/g, (char) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[char]);
}

function formatDate(value) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).format(date);
}

function formatNumber(value) {
  return new Intl.NumberFormat('zh-CN').format(Number(value || 0));
}

function shortToken(token) {
  if (!token) return '-';
  if (token.length <= 18) return token;
  return `${token.slice(0, 9)}...${token.slice(-6)}`;
}

function toast(message, error = false) {
  const element = document.createElement('div');
  element.className = `toast${error ? ' error' : ''}`;
  element.textContent = message;
  $('#toast-root').appendChild(element);
  setTimeout(() => element.remove(), 2600);
}

async function api(path, options = {}) {
  const headers = { 'Authorization': `Bearer ${state.token}` };
  if (options.body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(path, {
    method: options.method || 'GET',
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (response.status === 401) {
    logout();
    throw new Error('登录已失效');
  }
  const contentType = response.headers.get('content-type') || '';
  const payload = contentType.includes('application/json') ? await response.json() : await response.text();
  if (!response.ok) {
    const message = payload && payload.error ? payload.error : (payload || response.statusText);
    throw new Error(message || '请求失败');
  }
  return payload;
}

function setLoginError(message) {
  $('#login-error').textContent = message || '';
}

async function login(token) {
  state.token = token;
  await api('/api/me');
  localStorage.setItem('subhub_token', token);
  $('#login-view').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
  await loadAll();
}

function logout() {
  localStorage.removeItem('subhub_token');
  state.token = '';
  $('#app-view').classList.add('hidden');
  $('#login-view').classList.remove('hidden');
  $('#login-token').value = '';
}

function showPage(page) {
  state.page = page;
  $$('.nav-item').forEach((item) => item.classList.toggle('active', item.dataset.page === page));
  $$('.page').forEach((item) => item.classList.toggle('active', item.id === `page-${page}`));
  const [title, subtitle] = pageMeta[page];
  $('#page-title').textContent = title;
  $('#page-subtitle').textContent = subtitle;
  if (page === 'links') renderLinks();
}

async function loadAll() {
  try {
    const [me, overview, nodes, collections, tokens, logs, settings] = await Promise.all([
      api('/api/me'),
      api('/api/overview'),
      api('/api/nodes'),
      api('/api/collections'),
      api('/api/tokens'),
      api('/api/logs?limit=200'),
      api('/api/settings'),
    ]);
    state.overview = overview.overview;
    state.logs = logs;
    state.nodes = nodes;
    state.collections = collections;
    state.masters = tokens.masters;
    state.children = tokens.children;
    state.settings = settings;
    state.selectedNodes = new Set();
    $('#principal-name').textContent = me.token.name || '主 Token';
    renderDashboard();
    renderNodes();
    renderCollections();
    renderTokens();
    renderLogs();
    renderSettings();
    renderLinkControls();
    renderLinks();
  } catch (error) {
    toast(error.message, true);
  }
}

function renderDashboard() {
  const overview = state.overview || {};
  const cards = [
    ['节点总数', overview.node_count, `${overview.enabled_nodes || 0} 个已启用`],
    ['集合数量', overview.collection_count, '按订阅场景组合'],
    ['子 Token', overview.child_count, `${overview.active_children || 0} 个当前可用`],
    ['累计请求', overview.request_count, `今日 ${overview.today_requests || 0} 次`],
  ];
  $('#stat-grid').innerHTML = cards.map(([label, value, meta]) => `
    <article class="stat-card">
      <div class="label">${escapeHTML(label)}</div>
      <div class="value">${formatNumber(value)}</div>
      <div class="meta">${escapeHTML(meta)}</div>
    </article>
  `).join('');

  $('#dashboard-logs').innerHTML = state.logs.length ? state.logs.slice(0, 8).map((log) => `
    <div class="compact-item">
      <div class="compact-title">
        <strong>${escapeHTML(log.token_name || log.token_kind)}</strong>
        <span class="badge ${log.status < 300 ? 'ok' : 'bad'}">${log.status}</span>
      </div>
      <div class="compact-meta">${escapeHTML(log.collection_slug || '全部集合')} · ${log.node_count} 节点 · ${formatDate(log.created_at)}</div>
    </div>
  `).join('') : '<div class="empty">暂无请求记录</div>';

  $('#dashboard-collections').innerHTML = state.collections.length ? state.collections.slice(0, 8).map((collection) => `
    <div class="compact-item">
      <div class="compact-title">
        <strong>${escapeHTML(collection.name)}</strong>
        <span class="badge ${collection.enabled ? 'ok' : 'off'}">${collection.enabled ? '启用' : '停用'}</span>
      </div>
      <div class="compact-meta">${collection.node_count} 节点 · ${collection.token_count} Token 授权</div>
    </div>
  `).join('') : '<div class="empty">还没有集合</div>';
}

function nodesForCollection(collectionID) {
  const set = new Set((state.collections.find((item) => item.id === collectionID)?.node_ids) || []);
  return set;
}

function renderNodes() {
  const keyword = state.nodeSearch.toLowerCase();
  const visible = state.nodes.filter((node) => {
    if (state.enabledOnly && !node.enabled) return false;
    if (!keyword) return true;
    return `${node.name} ${node.uri}`.toLowerCase().includes(keyword);
  });
  const body = $('#nodes-body');
  body.innerHTML = visible.map((node) => {
    const inCollections = state.collections.filter((collection) => nodesForCollection(collection.id).has(node.id));
    return `
      <tr>
        <td><input type="checkbox" class="node-check" data-id="${node.id}" ${state.selectedNodes.has(node.id) ? 'checked' : ''}></td>
        <td><div class="cell-title">${escapeHTML(node.name || '未命名节点')}</div><div class="cell-sub">#${node.id}</div></td>
        <td><div class="uri-cell" title="${escapeHTML(node.uri)}">${escapeHTML(node.uri)}</div></td>
        <td>${inCollections.length ? inCollections.map((item) => `<span class="tag">${escapeHTML(item.name)}</span>`).join('') : '<span class="muted">未加入集合</span>'}</td>
        <td><span class="badge ${node.enabled ? 'ok' : 'off'}">${node.enabled ? '启用' : '停用'}</span></td>
        <td>
          <div class="row-actions">
            <button class="btn ghost small" data-action="toggle-node" data-id="${node.id}">${node.enabled ? '停用' : '启用'}</button>
            <button class="btn ghost small" data-action="edit-node" data-id="${node.id}">编辑</button>
            <button class="btn danger ghost small" data-action="delete-node" data-id="${node.id}">删除</button>
          </div>
        </td>
      </tr>
    `;
  }).join('');
  $('#nodes-empty').classList.toggle('hidden', visible.length > 0);
  $('#node-bulk-delete').disabled = state.selectedNodes.size === 0;
  $('#node-select-all').checked = visible.length > 0 && visible.every((node) => state.selectedNodes.has(node.id));
}

function renderCollections() {
  const grid = $('#collection-grid');
  grid.innerHTML = state.collections.map((collection) => `
    <article class="collection-card">
      <div class="card-bottom">
        <div>
          <h3>${escapeHTML(collection.name)}</h3>
          <div class="slug">/${escapeHTML(collection.slug)}</div>
        </div>
        <span class="badge ${collection.enabled ? 'ok' : 'off'}">${collection.enabled ? '启用' : '停用'}</span>
      </div>
      <p class="description">${escapeHTML(collection.description || '暂无说明')}</p>
      <div class="card-bottom">
        <span class="metric-line">${collection.node_count} 节点 · ${collection.token_count} Token</span>
        <div class="row-actions">
          <button class="btn ghost small" data-action="edit-collection" data-id="${collection.id}">编辑</button>
          <button class="btn danger ghost small" data-action="delete-collection" data-id="${collection.id}">删除</button>
        </div>
      </div>
    </article>
  `).join('');
  $('#collections-empty').classList.toggle('hidden', state.collections.length > 0);
}

function collectionNames(ids) {
  const byID = new Map(state.collections.map((item) => [item.id, item.name]));
  return (ids || []).map((id) => byID.get(id)).filter(Boolean);
}

function childStatus(token) {
  if (!token.enabled) return ['off', '停用'];
  if (token.expires_at && new Date(token.expires_at).getTime() <= Date.now()) return ['bad', '已过期'];
  if (token.max_uses > 0 && token.use_count >= token.max_uses) return ['bad', '已耗尽'];
  return ['ok', '可用'];
}

function renderTokens() {
  const isChildren = state.tokenTab === 'children';
  const list = isChildren ? state.children : state.masters;
  const body = $('#tokens-body');
  body.innerHTML = list.map((token) => {
    const [badgeClass, badgeText] = isChildren ? childStatus(token) : [token.enabled ? 'ok' : 'off', token.enabled ? '启用' : '停用'];
    const usage = isChildren
      ? `${token.use_count}${token.max_uses > 0 ? ` / ${token.max_uses}` : ' / 不限'}`
      : '管理 Token';
    const expiry = isChildren && token.expires_at ? formatDate(token.expires_at) : (isChildren ? '永不过期' : '-');
    const names = collectionNames(token.collection_ids);
    return `
      <tr>
        <td><div class="cell-title">${escapeHTML(token.name)}</div><div class="cell-sub">${escapeHTML(token.note || token.token_prefix)}</div></td>
        <td><div class="uri-cell" title="${escapeHTML(token.token)}">${escapeHTML(shortToken(token.token))}</div></td>
        <td>${names.length ? names.map((name) => `<span class="tag">${escapeHTML(name)}</span>`).join('') : '<span class="muted">全部集合</span>'}</td>
        <td><div class="cell-title">${escapeHTML(usage)}</div><div class="cell-sub">${escapeHTML(expiry)}</div></td>
        <td><span class="badge ${badgeClass}">${badgeText}</span></td>
        <td>
          <div class="row-actions">
            <button class="btn ghost small" data-action="copy-token" data-kind="${isChildren ? 'child' : 'master'}" data-id="${token.id}">复制</button>
            <button class="btn ghost small" data-action="edit-token" data-kind="${isChildren ? 'child' : 'master'}" data-id="${token.id}">编辑</button>
            <button class="btn ghost small" data-action="regenerate-token" data-kind="${isChildren ? 'child' : 'master'}" data-id="${token.id}">轮换</button>
            <button class="btn danger ghost small" data-action="delete-token" data-kind="${isChildren ? 'child' : 'master'}" data-id="${token.id}">删除</button>
          </div>
        </td>
      </tr>
    `;
  }).join('');
  $('#tokens-empty').classList.toggle('hidden', list.length > 0);
  $('#token-add-btn').textContent = isChildren ? '新建子 Token' : '新建主 Token';
}

function renderLogs() {
  $('#logs-body').innerHTML = state.logs.map((log) => `
    <tr>
      <td>${formatDate(log.created_at)}</td>
      <td><div class="cell-title">${escapeHTML(log.token_name || '-')}</div><div class="cell-sub">${escapeHTML(log.token_kind)} · ${escapeHTML(log.token_prefix)}</div></td>
      <td>${escapeHTML(log.collection_slug || '全部')}</td>
      <td><div class="cell-title">${escapeHTML(log.target || 'base64')}</div><div class="cell-sub">${escapeHTML(shorten(log.user_agent, 46))}</div></td>
      <td>${log.node_count}</td>
      <td><span class="badge ${log.status < 300 ? 'ok' : 'bad'}">${log.status}</span></td>
      <td>${escapeHTML(log.ip || '-')}</td>
    </tr>
  `).join('');
  $('#logs-empty').classList.toggle('hidden', state.logs.length > 0);
}

function shorten(value, length) {
  value = String(value || '');
  return value.length > length ? `${value.slice(0, length - 1)}...` : value;
}

function renderSettings() {
  $('#setting-sub-name').value = state.settings.sub_name || '';
  $('#setting-subconfig').value = state.settings.subconfig_url || '';
  $('#setting-interval').value = state.settings.update_interval || '24';
  $('#setting-userinfo').value = state.settings.userinfo_header || '';
  $('#setting-legacy').checked = state.settings.legacy_fallback !== 'false';
}

function renderLinkControls() {
  const tokenOptions = [
    ...state.masters.map((token) => ({ value: token.token, label: `主 · ${token.name}` })),
    ...state.children.map((token) => ({ value: token.token, label: `子 · ${token.name}` })),
  ];
  const tokenSelect = $('#link-token');
  tokenSelect.innerHTML = tokenOptions.map((item) => `<option value="${escapeHTML(item.value)}">${escapeHTML(item.label)}</option>`).join('');
  const collectionSelect = $('#link-collection');
  collectionSelect.innerHTML = [
    '<option value="">全部可访问集合</option>',
    ...state.collections.map((collection) => `<option value="${escapeHTML(collection.slug)}">${escapeHTML(collection.name)}</option>`),
  ].join('');
  if (!$('#link-host').value) {
    $('#link-host').value = location.origin;
  }
}

function renderLinks() {
  const host = ($('#link-host').value || location.origin).replace(/\/+$/, '');
  const token = $('#link-token').value;
  const collection = $('#link-collection').value;
  if (!token) {
    $('#link-list').innerHTML = '<div class="empty">请先创建 Token</div>';
    return;
  }
  const baseURL = new URL(`${host}/sub`);
  baseURL.searchParams.set('token', token);
  if (collection) baseURL.searchParams.set('collections', collection);
  const links = [
    ['原始 Base64', baseURL.toString()],
    ['Clash / Mihomo', withTarget(baseURL, 'clash')],
    ['Clash Meta', withTarget(baseURL, 'clash&new_name=true')],
    ['Sing-Box', withTarget(baseURL, 'singbox')],
    ['Surge 4', withTarget(baseURL, 'surge&ver=4')],
    ['Quantumult X', withTarget(baseURL, 'quanx')],
    ['Loon', withTarget(baseURL, 'loon')],
    ['Surfboard', withTarget(baseURL, 'surfboard')],
  ];
  $('#link-list').innerHTML = links.map(([label, url]) => `
    <div class="link-row">
      <div class="link-top"><strong>${escapeHTML(label)}</strong><button class="btn ghost small" data-copy="${escapeHTML(url)}">复制</button></div>
      <div class="link-url">${escapeHTML(url)}</div>
    </div>
  `).join('');
}

function withTarget(baseURL, value) {
  const targetURL = new URL(baseURL.toString());
  const [target, query] = value.split('&');
  targetURL.searchParams.set('target', target);
  if (query) {
    const [key, val] = query.split('=');
    targetURL.searchParams.set(key, val || '');
  }
  return targetURL.toString();
}

function modal({ title, body, submitText = '保存', wide = false, onSubmit }) {
  const root = $('#modal-root');
  root.innerHTML = `
    <div class="modal-backdrop">
      <form class="modal${wide ? ' wide-modal' : ''}">
        <div class="modal-head"><h2>${escapeHTML(title)}</h2></div>
        <div class="modal-body">${body}</div>
        <div class="modal-actions">
          <button type="button" class="btn ghost" data-modal-close>取消</button>
          <button type="submit" class="btn primary">${escapeHTML(submitText)}</button>
        </div>
      </form>
    </div>
  `;
  const form = root.querySelector('form');
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    try {
      await onSubmit(form);
      closeModal();
    } catch (error) {
      toast(error.message, true);
    }
  });
  root.querySelector('[data-modal-close]').addEventListener('click', closeModal);
  root.querySelector('.modal-backdrop').addEventListener('click', (event) => {
    if (event.target.classList.contains('modal-backdrop')) closeModal();
  });
  root.querySelector('input, select, textarea')?.focus();
}

function closeModal() {
  $('#modal-root').innerHTML = '';
}

function nodeModal(node = null) {
  modal({
    title: node ? '编辑节点' : '添加节点',
    body: `
      <label><span>节点名称</span><input id="modal-node-name" value="${escapeHTML(node?.name || '')}" placeholder="留空则从 URI 提取"></label>
      <label><span>节点 URI</span><textarea id="modal-node-uri" required placeholder="vless://... 或 vmess://...">${escapeHTML(node?.uri || '')}</textarea></label>
      <label class="checkline"><input id="modal-node-enabled" type="checkbox" ${!node || node.enabled ? 'checked' : ''}> 启用该节点</label>
    `,
    onSubmit: async () => {
      const body = {
        name: $('#modal-node-name').value.trim(),
        uri: $('#modal-node-uri').value.trim(),
        enabled: $('#modal-node-enabled').checked,
      };
      if (!body.uri) throw new Error('节点 URI 不能为空');
      if (node) await api(`/api/nodes/${node.id}`, { method: 'PUT', body });
      else await api('/api/nodes', { method: 'POST', body });
      await loadAll();
      toast(node ? '节点已更新' : '节点已添加');
    },
  });
}

function batchModal() {
  modal({
    title: '批量导入节点',
    body: `
      <label><span>支持标准 URI、Base64 订阅、小火箭 JSON 和小火箭分享链接</span>
        <textarea id="modal-batch" rows="12" placeholder="vless://...&#10;trojan://...&#10;或粘贴小火箭 JSON / shadowrocket:// 分享链接"></textarea>
      </label>
    `,
    submitText: '导入',
    onSubmit: async () => {
      const uris = $('#modal-batch').value.trim();
      if (!uris) throw new Error('请粘贴节点内容');
      const result = await api('/api/nodes/batch', { method: 'POST', body: { uris } });
      await loadAll();
      toast(`${result.format_label || '节点'}：导入 ${result.imported} 个，跳过 ${result.skipped} 个`);
    },
  });
}

function collectionModal(collection = null) {
  const selected = new Set(collection?.node_ids || []);
  const nodeChecks = state.nodes.map((node) => `
    <label><input type="checkbox" class="modal-collection-node" value="${node.id}" ${selected.has(node.id) ? 'checked' : ''}> ${escapeHTML(node.name || `节点 #${node.id}`)}</label>
  `).join('') || '<span class="muted">暂无节点</span>';
  modal({
    title: collection ? '编辑集合' : '新建集合',
    wide: true,
    body: `
      <div class="field-row">
        <label><span>集合名称</span><input id="modal-collection-name" required value="${escapeHTML(collection?.name || '')}"></label>
        <label><span>URL 别名</span><input id="modal-collection-slug" value="${escapeHTML(collection?.slug || '')}" placeholder="留空自动生成"></label>
      </div>
      <label><span>说明</span><input id="modal-collection-description" value="${escapeHTML(collection?.description || '')}"></label>
      <label><span>包含节点</span><div class="check-grid">${nodeChecks}</div></label>
      <label class="checkline"><input id="modal-collection-enabled" type="checkbox" ${!collection || collection.enabled ? 'checked' : ''}> 启用该集合</label>
    `,
    onSubmit: async () => {
      const body = {
        name: $('#modal-collection-name').value.trim(),
        slug: $('#modal-collection-slug').value.trim(),
        description: $('#modal-collection-description').value.trim(),
        enabled: $('#modal-collection-enabled').checked,
        node_ids: $$('.modal-collection-node:checked').map((input) => Number(input.value)),
      };
      if (collection) await api(`/api/collections/${collection.id}`, { method: 'PUT', body });
      else await api('/api/collections', { method: 'POST', body });
      await loadAll();
      toast(collection ? '集合已更新' : '集合已创建');
    },
  });
}

function tokenModal(kind, token = null) {
  const isChild = kind === 'child';
  const selected = new Set(token?.collection_ids || []);
  const collectionChecks = state.collections.map((collection) => `
    <label><input type="checkbox" class="modal-token-collection" value="${collection.id}" ${selected.has(collection.id) ? 'checked' : ''}> ${escapeHTML(collection.name)}</label>
  `).join('') || '<span class="muted">请先创建集合</span>';
  const expiryValue = token?.expires_at ? new Date(token.expires_at).toISOString().slice(0, 16) : '';
  modal({
    title: token ? `编辑${isChild ? '子' : '主'} Token` : `新建${isChild ? '子' : '主'} Token`,
    wide: true,
    body: `
      <div class="field-row">
        <label><span>名称</span><input id="modal-token-name" required value="${escapeHTML(token?.name || '')}"></label>
        <label><span>状态</span><select id="modal-token-enabled"><option value="true" ${!token || token.enabled ? 'selected' : ''}>启用</option><option value="false" ${token && !token.enabled ? 'selected' : ''}>停用</option></select></label>
      </div>
      ${isChild ? `
        <div class="field-row">
          <label><span>过期时间（可选）</span><input id="modal-token-expiry" type="datetime-local" value="${expiryValue}"></label>
          <label><span>最大使用次数（0 为不限）</span><input id="modal-token-max" type="number" min="0" value="${token?.max_uses || 0}"></label>
        </div>
        <label><span>备注</span><input id="modal-token-note" value="${escapeHTML(token?.note || '')}"></label>
      ` : ''}
      <label><span>授权集合</span><div class="check-grid">${collectionChecks}</div></label>
      <label class="checkline"><input id="modal-token-all" type="checkbox" ${!isChild && selected.size === 0 ? 'checked' : ''}> ${isChild ? '授权当前全部集合（子 Token 仍需至少选择一个集合）' : '授权当前全部集合（留空表示全部）'}</label>
    `,
    onSubmit: async () => {
      const allChecked = $('#modal-token-all').checked;
      const body = {
        name: $('#modal-token-name').value.trim(),
        enabled: $('#modal-token-enabled').value === 'true',
        collection_ids: allChecked ? [] : $$('.modal-token-collection:checked').map((input) => Number(input.value)),
      };
      if (isChild && allChecked) {
        throw new Error('子 Token 必须选择至少一个集合');
      }
      if (isChild) {
        body.max_uses = Number($('#modal-token-max').value || 0);
        body.note = $('#modal-token-note').value.trim();
        const expiry = $('#modal-token-expiry').value;
        body.expires_at = expiry ? new Date(expiry).toISOString() : null;
        if (!allChecked && body.collection_ids.length === 0) throw new Error('子 Token 至少授权一个集合');
      }
      if (token) {
        await api(`/api/tokens/${kind}/${token.id}`, { method: 'PATCH', body });
      } else {
        await api(`/api/tokens/${kind}`, { method: 'POST', body });
      }
      await loadAll();
      toast(token ? 'Token 已更新' : 'Token 已创建');
    },
  });
}

async function copyText(value) {
  try {
    await navigator.clipboard.writeText(value);
  } catch {
    const area = document.createElement('textarea');
    area.value = value;
    document.body.appendChild(area);
    area.select();
    document.execCommand('copy');
    area.remove();
  }
  toast('已复制');
}

async function handleNodeAction(action, id) {
  const node = state.nodes.find((item) => item.id === id);
  if (!node) return;
  if (action === 'toggle-node') {
    await api(`/api/nodes/${id}`, { method: 'PATCH', body: { enabled: !node.enabled } });
    await loadAll();
    toast(node.enabled ? '节点已停用' : '节点已启用');
  }
  if (action === 'edit-node') nodeModal(node);
  if (action === 'delete-node') {
    if (!confirm(`删除节点「${node.name || id}」？`)) return;
    await api(`/api/nodes/${id}`, { method: 'DELETE' });
    await loadAll();
    toast('节点已删除');
  }
}

async function handleCollectionAction(action, id) {
  const collection = state.collections.find((item) => item.id === id);
  if (!collection) return;
  if (action === 'edit-collection') collectionModal(collection);
  if (action === 'delete-collection') {
    if (!confirm(`删除集合「${collection.name}」？相关 Token 授权会一并移除。`)) return;
    await api(`/api/collections/${id}`, { method: 'DELETE' });
    await loadAll();
    toast('集合已删除');
  }
}

async function handleTokenAction(action, kind, id) {
  const list = kind === 'child' ? state.children : state.masters;
  const token = list.find((item) => item.id === id);
  if (!token) return;
  if (action === 'copy-token') await copyText(token.token);
  if (action === 'edit-token') tokenModal(kind, token);
  if (action === 'regenerate-token') {
    if (!confirm(`轮换「${token.name}」的 Token？旧链接会立即失效。`)) return;
    const updated = await api(`/api/tokens/${kind}/${id}/regenerate`, { method: 'POST' });
    await loadAll();
    await copyText(updated.token);
  }
  if (action === 'delete-token') {
    if (!confirm(`删除 Token「${token.name}」？`)) return;
    await api(`/api/tokens/${kind}/${id}`, { method: 'DELETE' });
    await loadAll();
    toast('Token 已删除');
  }
}

async function refresh() {
  await loadAll();
  toast('已刷新');
}

document.addEventListener('click', async (event) => {
  const target = event.target.closest('button');
  if (!target) return;
  try {
    if (target.dataset.copy) await copyText(target.dataset.copy);
    if (target.dataset.action === 'edit-node' || target.dataset.action === 'delete-node' || target.dataset.action === 'toggle-node') {
      await handleNodeAction(target.dataset.action, Number(target.dataset.id));
    }
    if (target.dataset.action === 'edit-collection' || target.dataset.action === 'delete-collection') {
      await handleCollectionAction(target.dataset.action, Number(target.dataset.id));
    }
    if (target.dataset.action && target.dataset.action.endsWith('-token') && target.dataset.kind) {
      await handleTokenAction(target.dataset.action, target.dataset.kind, Number(target.dataset.id));
    }
  } catch (error) {
    toast(error.message, true);
  }
});

$('#login-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const token = $('#login-token').value.trim();
  if (!token) return;
  setLoginError('');
  try {
    await login(token);
  } catch (error) {
    setLoginError(error.message);
  }
});

$('#logout-btn').addEventListener('click', logout);
$('#refresh-btn').addEventListener('click', refresh);
$('#main-nav').addEventListener('click', (event) => {
  const button = event.target.closest('[data-page]');
  if (button) showPage(button.dataset.page);
});

$('#node-search').addEventListener('input', (event) => {
  state.nodeSearch = event.target.value.trim();
  renderNodes();
});
$('#node-enabled-only').addEventListener('change', (event) => {
  state.enabledOnly = event.target.checked;
  renderNodes();
});
$('#nodes-body').addEventListener('change', (event) => {
  if (!event.target.classList.contains('node-check')) return;
  const id = Number(event.target.dataset.id);
  if (event.target.checked) state.selectedNodes.add(id);
  else state.selectedNodes.delete(id);
  renderNodes();
});
$('#node-select-all').addEventListener('change', (event) => {
  const visible = state.nodes.filter((node) => (!state.enabledOnly || node.enabled) && (!state.nodeSearch || `${node.name} ${node.uri}`.toLowerCase().includes(state.nodeSearch.toLowerCase())));
  visible.forEach((node) => event.target.checked ? state.selectedNodes.add(node.id) : state.selectedNodes.delete(node.id));
  renderNodes();
});
$('#node-bulk-delete').addEventListener('click', async () => {
  if (!state.selectedNodes.size) return;
  if (!confirm(`删除选中的 ${state.selectedNodes.size} 个节点？`)) return;
  try {
    await api('/api/nodes/bulk', { method: 'POST', body: { ids: Array.from(state.selectedNodes), action: 'delete' } });
    state.selectedNodes.clear();
    await loadAll();
    toast('已批量删除');
  } catch (error) {
    toast(error.message, true);
  }
});
$('#node-add-btn').addEventListener('click', () => nodeModal());
$('#node-import-btn').addEventListener('click', batchModal);
$('#collection-add-btn').addEventListener('click', () => collectionModal());
$('#token-tabs').addEventListener('click', (event) => {
  const button = event.target.closest('[data-token-tab]');
  if (!button) return;
  state.tokenTab = button.dataset.tokenTab;
  $$('#token-tabs button').forEach((item) => item.classList.toggle('active', item === button));
  renderTokens();
});
$('#token-add-btn').addEventListener('click', () => tokenModal(state.tokenTab === 'children' ? 'child' : 'master'));
$('#link-host').addEventListener('input', renderLinks);
$('#link-token').addEventListener('change', renderLinks);
$('#link-collection').addEventListener('change', renderLinks);
$('#logs-clear-btn').addEventListener('click', async () => {
  if (!confirm('清空全部访问日志？')) return;
  try {
    await api('/api/logs', { method: 'DELETE' });
    await loadAll();
    toast('日志已清空');
  } catch (error) {
    toast(error.message, true);
  }
});
$('#settings-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  try {
    state.settings = await api('/api/settings', {
      method: 'PUT',
      body: {
        sub_name: $('#setting-sub-name').value.trim(),
        subconfig_url: $('#setting-subconfig').value.trim(),
        update_interval: $('#setting-interval').value.trim(),
        userinfo_header: $('#setting-userinfo').value.trim(),
        legacy_fallback: $('#setting-legacy').checked ? 'true' : 'false',
      },
    });
    toast('设置已保存');
  } catch (error) {
    toast(error.message, true);
  }
});
$('#backup-download').addEventListener('click', async () => {
  try {
    const backup = await api('/api/backup');
    const blob = new Blob([JSON.stringify(backup, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `sub-hub-backup-${new Date().toISOString().slice(0, 10)}.json`;
    link.click();
    URL.revokeObjectURL(url);
    toast('备份已下载');
  } catch (error) {
    toast(error.message, true);
  }
});
$('#backup-file').addEventListener('change', async (event) => {
  const file = event.target.files[0];
  event.target.value = '';
  if (!file) return;
  if (!confirm('导入备份会替换当前全部业务数据，继续吗？')) return;
  try {
    const text = await file.text();
    const payload = JSON.parse(text);
    await api('/api/restore', { method: 'POST', body: payload });
    await loadAll();
    toast('备份已恢复');
  } catch (error) {
    toast(error.message, true);
  }
});

(async () => {
  const saved = localStorage.getItem('subhub_token');
  if (saved) {
    $('#login-token').value = saved;
    try {
      await login(saved);
    } catch (error) {
      setLoginError(error.message);
      localStorage.removeItem('subhub_token');
    }
  }
})();
