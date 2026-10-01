const state = {
  token: '',
  answer: '',
  text: '',
  base64: '',
  busy: false,
};

const elements = {
  form: document.querySelector('#converter-form'),
  input: document.querySelector('#converter-input'),
  target: document.querySelector('#converter-target'),
  captchaQuestion: document.querySelector('#captcha-question'),
  captchaAnswer: document.querySelector('#captcha-answer'),
  captchaRefresh: document.querySelector('#captcha-refresh'),
  submit: document.querySelector('#convert-submit'),
  status: document.querySelector('#converter-status'),
  resultPanel: document.querySelector('#result-panel'),
  resultSummary: document.querySelector('#result-summary'),
  resultOutput: document.querySelector('#result-output'),
  copyButtons: Array.from(document.querySelectorAll('[data-copy]')),
};

function setStatus(message, type = '') {
  elements.status.textContent = message || '';
  elements.status.className = `status${type ? ` ${type}` : ''}`;
}

function setBusy(busy) {
  state.busy = busy;
  elements.submit.disabled = busy;
  elements.captchaRefresh.disabled = busy;
  elements.copyButtons.forEach((button) => {
    button.disabled = busy || !state.text;
  });
  elements.submit.textContent = busy ? '转换中...' : '开始转换';
}

async function requestJSON(path, body) {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const contentType = response.headers.get('content-type') || '';
  const payload = contentType.includes('application/json') ? await response.json() : await response.text();
  if (!response.ok) {
    const message = payload && payload.error ? payload.error : (payload || response.statusText);
    throw new Error(message || '请求失败');
  }
  return payload;
}

async function loadChallenge() {
  elements.captchaRefresh.disabled = true;
  elements.captchaQuestion.textContent = '加载中...';
  elements.captchaAnswer.value = '';
  state.answer = '';
  try {
    const challenge = await requestJSON('/api/public/challenge', {});
    state.token = challenge.token || '';
    elements.captchaQuestion.textContent = challenge.question || '验证题不可用';
    elements.captchaAnswer.focus();
  } catch (error) {
    state.token = '';
    elements.captchaQuestion.textContent = '加载失败';
    setStatus(error.message, 'error');
  } finally {
    elements.captchaRefresh.disabled = state.busy;
  }
}

function renderResult(result) {
  state.text = result.text || '';
  state.base64 = result.base64 || '';
  elements.resultOutput.value = elements.target.value === 'base64' ? state.base64 : state.text;
  elements.resultSummary.textContent = `${result.count || 0} 个节点 · 已识别 ${result.format_label || result.format || '节点格式'}`;
  elements.resultPanel.classList.remove('hidden');
  elements.copyButtons.forEach((button) => {
    button.disabled = !state.text;
  });
}

async function copyValue(value, label) {
  if (!value) return;
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(value);
    } else {
      const helper = document.createElement('textarea');
      helper.value = value;
      helper.setAttribute('readonly', '');
      helper.style.position = 'fixed';
      helper.style.opacity = '0';
      document.body.appendChild(helper);
      helper.select();
      document.execCommand('copy');
      helper.remove();
    }
    setStatus(`${label}已复制`, 'success');
  } catch {
    setStatus('复制失败，请手动选择文本', 'error');
  }
}

elements.form.addEventListener('submit', async (event) => {
  event.preventDefault();
  if (state.busy) return;
  const input = elements.input.value.trim();
  if (!input) {
    setStatus('请先粘贴节点内容', 'error');
    elements.input.focus();
    return;
  }
  if (!state.token) {
    await loadChallenge();
    if (!state.token) return;
  }
  if (!elements.captchaAnswer.value.trim()) {
    setStatus('请填写验证答案', 'error');
    elements.captchaAnswer.focus();
    return;
  }

  setBusy(true);
  setStatus('正在转换，请稍候...');
  try {
    const result = await requestJSON('/api/public/convert', {
      token: state.token,
      answer: elements.captchaAnswer.value.trim(),
      input,
      target: elements.target.value,
    });
    renderResult(result);
    setStatus(`转换完成，共 ${result.count || 0} 个节点`, 'success');
  } catch (error) {
    setStatus(error.message, 'error');
  } finally {
    setBusy(false);
    await loadChallenge();
  }
});

elements.captchaRefresh.addEventListener('click', loadChallenge);

elements.copyButtons.forEach((button) => {
  button.addEventListener('click', () => {
    const kind = button.dataset.copy;
    copyValue(kind === 'base64' ? state.base64 : state.text, kind === 'base64' ? 'Base64' : '转换文本');
  });
});

elements.target.addEventListener('change', () => {
  if (!state.text) return;
  elements.resultOutput.value = elements.target.value === 'base64' ? state.base64 : state.text;
});

setBusy(false);
loadChallenge();
