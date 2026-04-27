document.addEventListener('DOMContentLoaded', () => {
    // --- Global Variables ---
    const uploadArea = document.getElementById('upload-zone');
    const fileInput = document.getElementById('file-picker');
    const progressArea = document.getElementById('prog-zone');
    const doneArea = document.getElementById('done-zone');
    const searchInput = document.getElementById('file-search');
    const previewModal = document.getElementById('image-preview-modal');
    const previewModalImg = document.getElementById('image-preview-modal-img');
    const previewModalCaption = document.getElementById('image-preview-caption');
    const previewModalClose = document.getElementById('image-preview-close');
    const folderTree = document.getElementById('folder-tree');
    const currentFolderGrid = document.getElementById('current-folder-grid');
    const folderBreadcrumb = document.getElementById('folder-breadcrumb');
    const folderNavRootBtn = document.getElementById('folder-nav-root-btn');
    let currentFolderPath = '';

    function getActiveCopyFormat() {
        const activeFormatBtn = document.querySelector('.format-option.active');
        return activeFormatBtn ? activeFormatBtn.dataset.format : 'url';
    }

    function buildFileUrl(item) {
        let url = '';

        const downloadLink = item.querySelector('a[href^="/d/"]');
        if (downloadLink && downloadLink.href) {
            url = downloadLink.href;
        }

        if (!url) {
            const dsUrl = item.dataset.fileUrl;
            if (dsUrl && dsUrl !== 'undefined') {
                url = dsUrl.startsWith('/') ? window.location.origin + dsUrl : dsUrl;
            }
        }

        if (!url || url.includes('undefined')) {
            const shortId = item.dataset.shortId;
            const fileId = item.dataset.fileId;
            const id = (shortId && shortId !== 'None' && shortId !== '') ? shortId : fileId;
            url = window.location.origin + `/d/${id}`;
        }

        if (url.includes('undefined')) {
            console.warn('Constructed URL contained undefined, falling back to raw fileId');
            url = window.location.origin + '/d/' + (item.dataset.fileId || 'error');
        }

        return url;
    }

    function buildCopyText(item, format) {
        const url = buildFileUrl(item);
        const name = item.dataset.filename || 'file';

        if (format === 'markdown') return `![${name}](${url})`;
        if (format === 'html') return `<img src="${url}" alt="${name}">`;
        return url;
    }

    function toRfc3339FromLocalInput(value) {
        if (!value) return null;
        const date = new Date(value);
        if (isNaN(date.getTime())) return null;
        return date.toISOString();
    }

    function formatLinkSettings(item) {
        const visibility = item.dataset.linkVisibility || 'public';
        const expiresAt = item.dataset.expiresAt || '';
        return expiresAt ? `${visibility} · ${expiresAt}` : visibility;
    }

    function itemMatchesScope(item, selectedFolder, includeSubfolders) {
        const folderPath = item.dataset.folderPath || '';
        if (!selectedFolder) return false;
        if (folderPath === selectedFolder) return true;
        if (includeSubfolders && folderPath.startsWith(`${selectedFolder}/`)) return true;
        return false;
    }

    function updateItemLinkSettings(item, visibility, expiresAt) {
        if (!item) return;
        item.dataset.linkVisibility = visibility || 'public';
        item.dataset.expiresAt = expiresAt || '';
        const label = item.querySelector('.link-settings-text');
        if (label) {
            label.textContent = formatLinkSettings(item);
        }
    }

    function normalizeFolderPathClient(value) {
        return String(value || '')
            .replace(/\\/g, '/')
            .split('/')
            .map(part => part.trim())
            .filter(part => part && part !== '.' && part !== '..')
            .join('/');
    }

    function getParentFolder(path) {
        const normalized = normalizeFolderPathClient(path);
        if (!normalized) return '';
        const parts = normalized.split('/');
        parts.pop();
        return parts.join('/');
    }

    function getFolderEntries() {
        const folders = new Map();
        getAllItems().forEach(item => {
            const folderPath = normalizeFolderPathClient(item.dataset.folderPath || '');
            let acc = '';
            folderPath.split('/').filter(Boolean).forEach(part => {
                acc = acc ? `${acc}/${part}` : part;
                if (!folders.has(acc)) {
                    folders.set(acc, {
                        path: acc,
                        name: part,
                        parent: getParentFolder(acc)
                    });
                }
            });
        });
        return Array.from(folders.values()).sort((a, b) => a.path.localeCompare(b.path, 'zh-CN'));
    }

    function getVisibleItemsForCurrentFolder() {
        return getAllItems().filter(item => normalizeFolderPathClient(item.dataset.folderPath || '') === currentFolderPath);
    }

    function navigateToFolder(path) {
        currentFolderPath = normalizeFolderPathClient(path);
        renderFolderView();
    }

    function renderFolderBreadcrumb() {
        if (!folderBreadcrumb) return;
        const segments = currentFolderPath ? currentFolderPath.split('/') : [];
        const crumbs = ['<button type="button" class="btn btn-ghost btn-sm folder-crumb" data-folder-path="" style="height: 28px; font-size: 12px;">根目录</button>'];
        let acc = '';
        segments.forEach(segment => {
            acc = acc ? `${acc}/${segment}` : segment;
            crumbs.push(`<span style="color: var(--text-tertiary);">/</span>`);
            crumbs.push(`<button type="button" class="btn btn-ghost btn-sm folder-crumb" data-folder-path="${acc}" style="height: 28px; font-size: 12px;">${segment}</button>`);
        });
        folderBreadcrumb.innerHTML = crumbs.join('');
    }

    function renderFolderTree() {
        if (!folderTree) return;
        const folders = getFolderEntries();
        const html = [`
            <button type="button" class="btn btn-ghost btn-sm folder-nav-item${currentFolderPath === '' ? ' active' : ''}" data-folder-path="" style="justify-content: flex-start; height: 34px; font-size: 13px;">
                全部文件
            </button>
        `];
        folders.forEach(folder => {
            const depth = folder.path.split('/').length - 1;
            const isActive = currentFolderPath === folder.path;
            html.push(`
                <button type="button" class="btn btn-ghost btn-sm folder-nav-item${isActive ? ' active' : ''}" data-folder-path="${folder.path}" style="justify-content: flex-start; height: 34px; font-size: 13px; padding-left: ${12 + depth * 16}px;">
                    📁 ${folder.name}
                </button>
            `);
        });
        folderTree.innerHTML = html.join('');
    }

    function renderCurrentFolderGrid() {
        if (!currentFolderGrid) return;
        const folders = getFolderEntries().filter(folder => folder.parent === currentFolderPath);
        const files = getVisibleItemsForCurrentFolder();
        const cards = [];

        if (currentFolderPath) {
            cards.push(`
                <button type="button" class="btn btn-ghost folder-up-card" data-folder-path="${getParentFolder(currentFolderPath)}" style="height: auto; min-height: 92px; display: flex; flex-direction: column; align-items: flex-start; justify-content: center; gap: 8px; padding: 14px; border: 1px dashed var(--border-color);">
                    <span style="font-size: 24px;">↩</span>
                    <span style="font-size: 13px; font-weight: 600;">返回上一级</span>
                </button>
            `);
        }

        folders.forEach(folder => {
            cards.push(`
                <button type="button" class="btn btn-ghost folder-card" data-folder-path="${folder.path}" style="height: auto; min-height: 92px; display: flex; flex-direction: column; align-items: flex-start; justify-content: center; gap: 8px; padding: 14px; border: 1px solid var(--border-color);">
                    <span style="font-size: 24px;">📁</span>
                    <span style="font-size: 13px; font-weight: 600; text-align: left; word-break: break-all;">${folder.name}</span>
                </button>
            `);
        });

        files.forEach(item => {
            const fileId = item.dataset.fileId || '';
            const filename = item.dataset.filename || 'file';
            cards.push(`
                <button type="button" class="btn btn-ghost folder-file-card" data-file-id="${fileId}" style="height: auto; min-height: 92px; display: flex; flex-direction: column; align-items: flex-start; justify-content: center; gap: 8px; padding: 14px; border: 1px solid var(--border-color);">
                    <span style="font-size: 24px;">📄</span>
                    <span style="font-size: 13px; font-weight: 600; text-align: left; word-break: break-all;">${filename}</span>
                </button>
            `);
        });

        if (cards.length === 0) {
            cards.push('<div class="text-sm text-muted" style="padding: 8px 4px;">当前目录暂无内容</div>');
        }

        currentFolderGrid.innerHTML = cards.join('');
    }

    function renderFolderTableVisibility() {
        const term = (searchInput?.value || '').toLowerCase();
        const items = getAllItems();
        const hasFolderExplorer = !!(folderTree || currentFolderGrid || folderBreadcrumb || folderNavRootBtn);
        let visibleCount = 0;

        items.forEach(item => {
            const matchesTerm = !term || (item.dataset.filename || '').toLowerCase().includes(term);
            const sameFolder = normalizeFolderPathClient(item.dataset.folderPath || '') === currentFolderPath;
            const isVisible = hasFolderExplorer ? (sameFolder && matchesTerm) : matchesTerm;
            item.style.display = isVisible ? '' : 'none';
            if (isVisible) visibleCount += 1;
        });

        const container = document.getElementById('file-list-disk');
        if (!container) return;
        const existingEmpty = container.querySelector('.folder-empty-row');
        if (existingEmpty) existingEmpty.remove();

        if (visibleCount === 0 && !document.querySelector('.image-grid')) {
            container.insertAdjacentHTML('beforeend', `
                <tr class="folder-empty-row">
                    <td colspan="5" style="padding: 36px; text-align: center;">
                        <div class="text-muted">当前目录暂无文件</div>
                    </td>
                </tr>
            `);
        }
    }

    function renderFolderView() {
        if (folderTree || currentFolderGrid || folderBreadcrumb) {
            renderFolderTree();
            renderFolderBreadcrumb();
            renderCurrentFolderGrid();
        }
        renderFolderTableVisibility();
        updateBatchControls();
    }

    function openPreview(item) {
        if (!previewModal || !previewModalImg || !previewModalCaption) return;
        const url = buildFileUrl(item);
        const name = item.dataset.filename || '';
        previewModalImg.src = url;
        previewModalImg.alt = name;
        previewModalCaption.textContent = name;
        previewModal.classList.remove('hidden');
        previewModal.style.display = 'flex';
        document.body.style.overflow = 'hidden';
    }

    function closePreview() {
        if (!previewModal || !previewModalImg) return;
        previewModal.classList.add('hidden');
        previewModal.style.display = 'none';
        previewModalImg.removeAttribute('src');
        previewModalImg.alt = '';
        if (previewModalCaption) previewModalCaption.textContent = '';
        document.body.style.overflow = '';
    }

    if (previewModalClose) {
        previewModalClose.addEventListener('click', closePreview);
    }

    if (previewModal) {
        previewModal.addEventListener('click', (e) => {
            if (e.target === previewModal) closePreview();
        });
    }

    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && previewModal && !previewModal.classList.contains('hidden')) {
            closePreview();
        }
    });

    document.addEventListener('click', (e) => {
        const folderBtn = e.target.closest('.folder-nav-item, .folder-card, .folder-up-card, .folder-crumb');
        if (folderBtn) {
            e.preventDefault();
            navigateToFolder(folderBtn.dataset.folderPath || '');
            return;
        }

        const fileCard = e.target.closest('.folder-file-card');
        if (fileCard) {
            e.preventDefault();
            const fileId = fileCard.dataset.fileId || '';
            const row = document.getElementById(`file-item-${fileId.replace(/:/g, '-')}`);
            if (row) {
                row.scrollIntoView({ behavior: 'smooth', block: 'center' });
                row.style.background = 'var(--primary-light)';
                setTimeout(() => {
                    row.style.background = '';
                }, 1200);
            }
            return;
        }
    });

    if (folderNavRootBtn) {
        folderNavRootBtn.addEventListener('click', () => navigateToFolder(''));
    }

    // --- Copy Link Delegation ---
    document.addEventListener('click', (e) => {
        const previewBtn = e.target.closest('.preview-image-btn');
        if (previewBtn) {
            e.preventDefault();
            const item = previewBtn.closest('.file-item, .image-card');
            if (item) openPreview(item);
            return;
        }

        const btn = e.target.closest('.copy-link-btn');
        if (!btn) return;

        e.preventDefault();
        e.stopPropagation();

        const item = btn.closest('.file-item, .image-card');
        if (!item) return;
        if (btn.hasAttribute('onclick')) return;

        Utils.copy(buildCopyText(item, getActiveCopyFormat()));
    });

    // --- Search Functionality ---
    if (searchInput) {
        searchInput.addEventListener('input', () => {
            renderFolderView();
        });
    }

    // --- Upload Logic ---
    if (uploadArea && fileInput) {
        // Prevent double dialog by stopping propagation from input
        fileInput.addEventListener('click', (e) => e.stopPropagation());

        uploadArea.addEventListener('click', (e) => {
             // Only trigger if not clicking the input itself (though propagation stop handles it, this is extra safety)
             if (e.target !== fileInput) {
                 fileInput.click();
             }
        });

        uploadArea.addEventListener('dragover', (event) => {
            event.preventDefault();
            uploadArea.style.borderColor = 'var(--primary-color)';
            uploadArea.style.backgroundColor = 'var(--bg-surface-hover)';
        });

        uploadArea.addEventListener('dragleave', () => {
            uploadArea.style.borderColor = '';
            uploadArea.style.backgroundColor = '';
        });

        uploadArea.addEventListener('drop', (event) => {
            event.preventDefault();
            uploadArea.style.borderColor = '';
            uploadArea.style.backgroundColor = '';
            const files = event.dataTransfer.files;
            if (files.length > 0) {
                handleFiles(files);
            }
        });

        fileInput.addEventListener('change', ({ target }) => {
            if (target.files.length > 0) {
                handleFiles(target.files);
            }
        });
    }

    // Queue system for uploads
    const uploadQueue = [];
    let isUploading = false;

    function handleFiles(files) {
        if (progressArea) progressArea.innerHTML = ''; 
        
        for (const file of files) {
            uploadQueue.push(file);
        }
        processQueue();
    }

    function processQueue() {
        if (isUploading || uploadQueue.length === 0) return;
        
        isUploading = true;
        const file = uploadQueue.shift();
        uploadFile(file).then(() => {
            isUploading = false;
            processQueue();
        });
    }

    function uploadFile(file) {
        return new Promise((resolve) => {
            const formData = new FormData();
            formData.append('file', file, file.name);
            
            const xhr = new XMLHttpRequest();
            xhr.open('POST', '/api/upload', true);
            const fileId = `temp-${Date.now()}-${Math.random().toString(36).substr(2, 5)}`;

            // Initial Progress UI
            // 使用新版 UI 风格
            const progressHTML = `
                <div class="card" id="progress-${fileId}" style="padding: 16px; margin-bottom: 12px; border: 1px solid var(--border-color);">
                    <div style="display: flex; justify-content: space-between; margin-bottom: 8px;">
                        <span style="font-size: 14px; font-weight: 500;">${file.name}</span>
                        <span class="percent" style="font-size: 12px; color: var(--text-secondary);">0%</span>
                    </div>
                    <div style="height: 4px; background: var(--bg-surface-hover); border-radius: 2px; overflow: hidden;">
                        <div class="progress-bar" style="width: 0%; height: 100%; background: var(--primary-color); transition: width 0.2s;"></div>
                    </div>
                </div>`;
            
            if (progressArea) progressArea.insertAdjacentHTML('beforeend', progressHTML);
            const progressEl = document.querySelector(`#progress-${fileId} .progress-bar`);
            const percentEl = document.querySelector(`#progress-${fileId} .percent`);

            xhr.upload.onprogress = ({ loaded, total }) => {
                const percent = Math.floor((loaded / total) * 100);
                if (progressEl) progressEl.style.width = `${percent}%`;
                if (percentEl) percentEl.textContent = `${percent}%`;
            };

            xhr.onload = () => {
                const progressRow = document.getElementById(`progress-${fileId}`);
                if (progressRow) progressRow.remove();

                if (xhr.status === 200) {
                    const response = JSON.parse(xhr.responseText);
                    const fileUrl = response.url;
                    
                    // Success Toast
                    if (window.Toast) Toast.show(`${file.name} 上传成功`);
                    
                    // Add to done area
                    const successHTML = `
                        <div class="card" style="padding: 16px; margin-bottom: 12px; border-left: 4px solid var(--success-color);">
                            <div style="display: flex; justify-content: space-between; align-items: center;">
                                <div style="overflow: hidden; margin-right: 12px;">
                                    <div style="font-size: 14px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;">${file.name}</div>
                                    <a href="${fileUrl}" target="_blank" style="font-size: 12px; color: var(--primary-color);">${fileUrl}</a>
                                </div>
                                <button class="btn btn-secondary btn-sm" onclick="Utils.copy('${fileUrl}')">复制</button>
                            </div>
                        </div>`;
                    if (doneArea) doneArea.insertAdjacentHTML('afterbegin', successHTML);
                } else {
                    let errorMsg = "上传失败";
                    try {
                        const parsed = JSON.parse(xhr.responseText);
                        const detail = parsed && parsed.detail;
                        if (typeof detail === 'string') {
                            errorMsg = detail;
                        } else if (detail && typeof detail === 'object') {
                            errorMsg = detail.message || errorMsg;
                        } else if (parsed && parsed.message) {
                            errorMsg = parsed.message;
                        }
                    } catch (e) {}
                    
                    if (window.Toast) Toast.show(errorMsg, 'error');
                }
                resolve();
            };

            xhr.onerror = () => {
                const progressRow = document.getElementById(`progress-${fileId}`);
                if (progressRow) progressRow.remove();
                if (window.Toast) Toast.show('网络错误', 'error');
                resolve();
            };

            xhr.send(formData);
        });
    }

    // --- Batch Actions ---
    const selectAllCheckbox = document.getElementById('select-all-checkbox');
    const batchDeleteBtn = document.getElementById('batch-delete-btn');
    const copyLinksBtn = document.getElementById('copy-links-btn');
    const moveFolderBtn = document.getElementById('move-folder-btn');
    const saveLinkSettingsBtn = document.getElementById('save-link-settings-btn');
    const folderPathInput = document.getElementById('folder-path-input');
    const linkVisibilityInput = document.getElementById('link-visibility-input');
    const linkExpiresInput = document.getElementById('link-expires-input');
    const includeSubfoldersToggle = document.getElementById('include-subfolders-toggle');
    const selectionCounter = document.getElementById('selection-counter');
    const batchActionsBar = document.getElementById('batch-actions-bar');
    const formatOptions = document.querySelectorAll('.format-option');

    function updateBatchControls() {
        const visibleCheckboxes = Array.from(document.querySelectorAll('.file-checkbox')).filter(cb => {
            const item = cb.closest('.file-item, .image-card');
            return item && item.style.display !== 'none';
        });
        const checked = Array.from(document.querySelectorAll('.file-checkbox:checked')).filter(cb => {
            const item = cb.closest('.file-item, .image-card');
            return item && item.style.display !== 'none';
        });
        const count = checked.length;

        if (selectionCounter) selectionCounter.textContent = count;

        if (batchActionsBar) {
            if (count > 0) {
                batchActionsBar.classList.remove('hidden');
            } else {
                batchActionsBar.classList.add('hidden');
            }
        }

        if (selectAllCheckbox) {
            selectAllCheckbox.checked = (count > 0 && visibleCheckboxes.length > 0 && count === visibleCheckboxes.length);
        }
    }

    if (selectAllCheckbox) {
        selectAllCheckbox.addEventListener('change', (e) => {
            document.querySelectorAll('.file-checkbox').forEach(cb => {
                const item = cb.closest('.file-item, .image-card');
                if (item && item.style.display !== 'none') {
                    cb.checked = e.target.checked;
                }
            });
            updateBatchControls();
        });
    }

    // Delegation for dynamic checkboxes
    document.addEventListener('change', (e) => {
        if (e.target.classList.contains('file-checkbox')) {
            updateBatchControls();
        }
    });

    // Format selection (Image Hosting)
    if (formatOptions) {
        formatOptions.forEach(opt => {
            opt.addEventListener('click', () => {
                formatOptions.forEach(o => o.classList.remove('active'));
                opt.classList.add('active');
            });
        });
    }

    // Batch Copy
    if (copyLinksBtn) {
        copyLinksBtn.addEventListener('click', () => {
            const checked = document.querySelectorAll('.file-checkbox:checked');
            if (checked.length === 0) return;

            const format = getActiveCopyFormat();

            const links = Array.from(checked).map(cb => {
                const item = cb.closest('.file-item, .image-card');
                return buildCopyText(item, format);
            });

            Utils.copy(links.join('\n'));
        });
    }

    // Batch Delete
    if (batchDeleteBtn) {
        batchDeleteBtn.addEventListener('click', async () => {
            const checked = document.querySelectorAll('.file-checkbox:checked');
            if (checked.length === 0) return;

            const confirmed = await Modal.confirm('批量删除', `确定要删除选中的 ${checked.length} 个文件吗？`);
            if (!confirmed) return;

            const fileIds = Array.from(checked).map(cb => cb.dataset.fileId);

            fetch('/api/batch_delete', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ file_ids: fileIds })
            })
            .then(res => res.json())
            .then(data => {
                if (data.deleted) {
                    data.deleted.forEach(item => {
                         const id = item.details?.file_id || item;
                         removeFileElement(id);
                    });
                    if (window.Toast) Toast.show(`已删除 ${data.deleted.length} 个文件`);
                }
                updateBatchControls();
            });
        });
    }

    function getAllItems() {
        return Array.from(document.querySelectorAll('.file-item, .image-card'));
    }

    function getCheckedItems() {
        return Array.from(document.querySelectorAll('.file-checkbox:checked'))
            .map(cb => cb.closest('.file-item, .image-card'))
            .filter(Boolean);
    }

    function getScopedItems(selectedFolder, includeSubfolders) {
        const normalized = normalizeFolderPathClient(selectedFolder);
        return getAllItems().filter(item => itemMatchesScope(item, normalized, includeSubfolders));
    }

    if (moveFolderBtn) {
        moveFolderBtn.addEventListener('click', async () => {
            const checkedItems = getCheckedItems();
            if (checkedItems.length === 0) {
                if (window.Toast) Toast.show('请先勾选要移动的文件', 'error');
                return;
            }

            const targetFolder = normalizeFolderPathClient((folderPathInput?.value || '').trim());
            const targetIds = checkedItems.map(item => item.dataset.fileId);

            const results = await Promise.all(targetIds.map(fileId =>
                fetch(`/api/files/${encodeURIComponent(fileId)}/move`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ folder_path: targetFolder })
                }).then(async res => ({ ok: res.ok, data: await res.json().catch(() => ({})), fileId }))
            ));

            let movedCount = 0;
            results.forEach(({ ok, data, fileId }) => {
                if (!ok) return;
                movedCount += 1;
                const item = document.getElementById(`file-item-${fileId.replace(/:/g, '-')}`);
                if (item) {
                    item.dataset.folderPath = data.folder_path || '';
                    const folderLabel = item.querySelector('.folder-path-text');
                    if (folderLabel) folderLabel.textContent = data.folder_path || '';
                }
            });

            if (movedCount > 0 && window.Toast) {
                Toast.show(`已移动 ${movedCount} 个文件`);
            }
            renderFolderView();
        });
    }

    if (saveLinkSettingsBtn) {
        saveLinkSettingsBtn.addEventListener('click', async () => {
            const checkedItems = getCheckedItems();
            const visibility = (linkVisibilityInput?.value || 'public').trim() === 'private' ? 'private' : 'public';
            const expiresAt = toRfc3339FromLocalInput(linkExpiresInput?.value || '');
            const includeSubfolders = !!includeSubfoldersToggle?.checked;
            const selectedFolder = normalizeFolderPathClient((folderPathInput?.value || '').trim());

            let targetItems = [];
            if (checkedItems.length > 0) {
                targetItems = checkedItems;
            } else if (selectedFolder) {
                targetItems = getScopedItems(selectedFolder, includeSubfolders);
            }

            if (targetItems.length === 0) {
                if (window.Toast) Toast.show('请先勾选文件，或输入文件夹路径后批量设置', 'error');
                return;
            }

            const results = await Promise.all(targetItems.map(item => {
                const fileId = item.dataset.fileId;
                return fetch(`/api/files/${encodeURIComponent(fileId)}/link-settings`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        link_visibility: visibility,
                        expires_at: expiresAt
                    })
                }).then(async res => ({
                    ok: res.ok,
                    data: await res.json().catch(() => ({})),
                    fileId,
                    item
                }));
            }));

            let updatedCount = 0;
            let failedCount = 0;
            results.forEach(({ ok, data, item }) => {
                if (!ok) {
                    failedCount += 1;
                    return;
                }
                updatedCount += 1;
                updateItemLinkSettings(item, data.link_visibility || visibility, data.expires_at || '');
            });

            if (updatedCount > 0 && window.Toast) {
                Toast.show(`已更新 ${updatedCount} 个文件的短链设置`);
            }
            if (failedCount > 0 && window.Toast) {
                Toast.show(`${failedCount} 个文件更新失败`, 'error');
            }
        });
    }

    // --- SSE & Realtime Updates ---
    const fileListContainer = document.getElementById('file-list-disk');
    if (fileListContainer) {
        let eventSource = null;

        const connectSSE = () => {
            if (eventSource) {
                eventSource.close();
            }
            eventSource = new EventSource('/api/file-updates');

            eventSource.onmessage = (event) => {
                const msg = JSON.parse(event.data);
                const action = msg && msg.action ? msg.action : 'add';
                if (action === 'delete') {
                    removeFileElement(msg.file_id);
                    updateBatchControls();
                    return;
                }
                addNewFileElement(msg);
            };

            eventSource.onerror = () => {
                try { eventSource.close(); } catch (_) {}
                setTimeout(connectSSE, 5000);
            };
        };

        connectSSE();
    }

    function formatDateValue(value) {
        if (!value) return '';
        const d = new Date(value);
        if (!isNaN(d.getTime())) return d.toISOString().split('T')[0];
        const s = String(value);
        return s.split(' ')[0].split('T')[0];
    }

    function addNewFileElement(file) {
        const isGridView = document.querySelector('.image-grid') !== null;
        const container = document.getElementById('file-list-disk');
        
        // Remove empty state if exists
        const emptyState = container.querySelector('div[style*="text-align: center"]');
        if (emptyState) emptyState.remove();

        const formattedSize = (file.filesize / (1024 * 1024)).toFixed(2) + " MB";
        const formattedDate = formatDateValue(file.upload_date);
        const safeId = file.file_id.replace(':', '-');
        
        // URL construction: Always use /d/{file_id} (short_id preferred)
        // 回滚：只使用 /d/{id} 格式，不再拼接文件名或 slug
        let fileUrl = `/d/${file.short_id || file.file_id}`;
        const folderPath = file.folder_path || '';

        let html = '';
        if (isGridView) {
             html = `
                <div class="file-item image-card" style="border: 1px solid var(--border-color); border-radius: var(--radius-md); overflow: hidden; background: var(--bg-body);" id="file-item-${safeId}" data-file-id="${file.file_id}" data-file-url="${fileUrl}" data-filename="${file.filename}" data-short-id="${file.short_id || ''}" data-folder-path="${folderPath}" data-link-visibility="${file.link_visibility || 'public'}" data-expires-at="${file.expires_at || ''}">
                    <div style="position: relative; aspect-ratio: 16/9; background: linear-gradient(135deg, rgba(99,102,241,0.12), rgba(59,130,246,0.08)); border-bottom: 1px solid var(--border-color);">
                        <button type="button" class="preview-image-btn" style="position: absolute; inset: 0; width: 100%; height: 100%; border: 0; background: transparent; color: var(--text-primary); cursor: pointer; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px;">
                            <svg width="34" height="34" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>
                            <span style="font-size: 13px; color: var(--text-secondary);">点击预览</span>
                        </button>
                        <div style="position: absolute; top: 8px; left: 8px; z-index: 1;">
                            <input type="checkbox" class="file-checkbox" data-file-id="${file.file_id}" style="width: 16px; height: 16px; cursor: pointer;">
                        </div>
                    </div>
                    <div style="padding: 12px;">
                        <div class="text-sm font-medium" style="white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-bottom: 4px;" title="${file.filename}">${file.filename}</div>
                        <div class="text-sm text-muted folder-path-text" style="margin-bottom: 4px;">${folderPath}</div>
                        <div class="text-sm text-muted link-settings-text" style="margin-bottom: 4px;">${file.expires_at ? `${file.link_visibility || 'public'} · ${file.expires_at}` : (file.link_visibility || 'public')}</div>
                        <div class="text-sm text-muted" style="margin-bottom: 12px;">${formattedSize}</div>
                        <div style="display: flex; gap: 8px;">
                            <button class="btn btn-secondary btn-sm copy-link-btn" style="flex: 1; height: 32px;">复制</button>
                            <button class="btn btn-secondary btn-sm delete" style="height: 32px; color: var(--danger-color);" onclick="deleteFile('${file.file_id}')">
                                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                            </button>
                        </div>
                    </div>
                </div>`;
        } else {
            html = `
                <tr class="file-item" style="border-bottom: 1px solid var(--border-color);" id="file-item-${safeId}" data-file-id="${file.file_id}" data-file-url="${fileUrl}" data-filename="${file.filename}" data-short-id="${file.short_id || ''}" data-folder-path="${folderPath}" data-link-visibility="${file.link_visibility || 'public'}" data-expires-at="${file.expires_at || ''}">
                    <td style="padding: 12px 16px;"><input type="checkbox" class="file-checkbox" data-file-id="${file.file_id}"></td>
                    <td style="padding: 12px 16px;">
                        <div style="display: flex; align-items: center; gap: 8px;">
                            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="color: var(--primary-color);"><path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"></path><polyline points="13 2 13 9 20 9"></polyline></svg>
                            <div style="display: flex; flex-direction: column; gap: 2px; min-width: 0;">
                                <span class="text-sm font-medium" style="color: var(--text-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;">${file.filename}</span>
                                <span class="text-sm text-muted folder-path-text">${folderPath}</span>
                                <span class="text-sm text-muted link-settings-text">${file.expires_at ? `${file.link_visibility || 'public'} · ${file.expires_at}` : (file.link_visibility || 'public')}</span>
                            </div>
                        </div>
                    </td>
                    <td style="padding: 12px 16px;" class="text-sm text-muted">${formattedSize}</td>
                    <td style="padding: 12px 16px;" class="text-sm text-muted">${formattedDate}</td>
                    <td style="padding: 12px 16px; text-align: right;">
                        <div style="display: flex; justify-content: flex-end; gap: 8px;">
                            <a href="${fileUrl}" class="btn btn-ghost" style="padding: 4px 8px; height: 28px;" title="下载">
                                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>
                            </a>
                            <button class="btn btn-ghost copy-link-btn" style="padding: 4px 8px; height: 28px;" title="复制链接">
                                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                            </button>
                            <button class="btn btn-ghost delete" style="padding: 4px 8px; height: 28px; color: var(--danger-color);" onclick="deleteFile('${file.file_id}')" title="删除">
                                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                            </button>
                        </div>
                    </td>
                </tr>`;
        }

        container.insertAdjacentHTML('afterbegin', html);
        renderFolderView();
    }

    renderFolderView();

    // --- Global Helpers ---
    window.deleteFile = async (fileId) => {
        const confirmed = await Modal.confirm('删除文件', '确定要删除此文件吗？');
        if (!confirmed) return;
        fetch(`/api/files/${fileId}`, { method: 'DELETE' })
            .then(async (res) => {
                let data = null;
                try { data = await res.json(); } catch (e) {}
                return { ok: res.ok, data };
            })
            .then(({ ok, data }) => {
                if (ok && data && data.status === 'ok') {
                    removeFileElement(fileId);
                    if (window.Toast) Toast.show('文件已删除');
                    updateBatchControls();
                } else {
                    const msg = data?.detail?.message || data?.message || '删除失败';
                    if (window.Toast) Toast.show(msg, 'error');
                }
            });
    };

    function removeFileElement(fileId) {
        const el = document.getElementById(`file-item-${fileId.replace(':', '-')}`);
        if (el) el.remove();
        
        // Check if empty
        const container = document.getElementById('file-list-disk');
        if (container && container.children.length === 0) {
            // Re-render empty state logic if needed, or let user refresh
            // Simple text fallback
            const isGridView = document.querySelector('.image-grid') !== null;
            if (isGridView) {
                 container.innerHTML = `
                    <div style="grid-column: 1/-1; padding: 40px; text-align: center; color: var(--text-tertiary);">
                        <p>暂无图片</p>
                    </div>`;
            } else {
                 container.innerHTML = `
                    <tr>
                        <td colspan="5" style="padding: 48px; text-align: center;">
                            <div class="text-muted">暂无文件</div>
                        </td>
                    </tr>`;
            }
        }
    }
});
