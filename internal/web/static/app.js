/**
 * mihon-sync Web Dashboard Client
 */
(function () {
  'use strict';

  // Constants & State
  const STORAGE_KEY = 'mihon_sync_api_key';
  const THEME_KEY = 'mihon_sync_theme';

  let currentApiKey = localStorage.getItem(STORAGE_KEY) || '';
  let currentTheme = localStorage.getItem(THEME_KEY) || 'system';
  let serverInfo = { allow_registration: true, version: '0.1.0' };
  let isCheckingHealth = false;

  // DOM Elements
  const el = {
    // Navigation
    themeToggleBtn: document.getElementById('themeToggleBtn'),
    navLogoutBtn: document.getElementById('navLogoutBtn'),
    serverStatusPill: document.getElementById('serverStatusPill'),
    serverStatusLabel: document.getElementById('serverStatusLabel'),

    // Views
    viewLoading: document.getElementById('viewLoading'),
    viewAuth: document.getElementById('viewAuth'),
    viewDashboard: document.getElementById('viewDashboard'),

    // Auth Form Elements
    authTabSwitcher: document.getElementById('authTabSwitcher'),
    tabLoginBtn: document.getElementById('tabLoginBtn'),
    tabRegisterBtn: document.getElementById('tabRegisterBtn'),
    loginForm: document.getElementById('loginForm'),
    registerForm: document.getElementById('registerForm'),
    loginApiKey: document.getElementById('loginApiKey'),
    loginSubmitBtn: document.getElementById('loginSubmitBtn'),
    loginError: document.getElementById('loginError'),
    regLabel: document.getElementById('regLabel'),
    registerSubmitBtn: document.getElementById('registerSubmitBtn'),
    registerError: document.getElementById('registerError'),
    regDisabledNotice: document.getElementById('regDisabledNotice'),

    // Key Created Box
    keyCreatedCard: document.getElementById('keyCreatedCard'),
    newGeneratedKeyDisplay: document.getElementById('newGeneratedKeyDisplay'),
    copyNewKeyBtn: document.getElementById('copyNewKeyBtn'),
    continueWithNewKeyBtn: document.getElementById('continueWithNewKeyBtn'),

    // Dashboard Elements
    dashMaskedKey: document.getElementById('dashMaskedKey'),
    dashCopyKeyBtn: document.getElementById('dashCopyKeyBtn'),
    dashAccountDate: document.getElementById('dashAccountDate'),
    dashRevValue: document.getElementById('dashRevValue'),
    dashRefreshBtn: document.getElementById('dashRefreshBtn'),
    refreshIcon: document.getElementById('refreshIcon'),

    // Metric Values
    statManga: document.getElementById('statManga'),
    statChapters: document.getElementById('statChapters'),
    statCategories: document.getElementById('statCategories'),
    statHistory: document.getElementById('statHistory'),
    statPreferences: document.getElementById('statPreferences'),
    statDevices: document.getElementById('statDevices'),

    // Setup Helpers
    setupServerUrl: document.getElementById('setupServerUrl'),
    setupApiKey: document.getElementById('setupApiKey'),
    copyServerUrlBtn: document.getElementById('copyServerUrlBtn'),
    copyApiKeyBtn: document.getElementById('copyApiKeyBtn'),
    qrCanvas: document.getElementById('qrCanvas'),

    // Delete Modal
    dangerZoneSection: document.querySelector('.danger-zone-section'),
    openDeleteAccountModalBtn: document.getElementById('openDeleteAccountModalBtn'),
    deleteModal: document.getElementById('deleteModal'),
    deleteConfirmInput: document.getElementById('deleteConfirmInput'),
    confirmDeleteBtn: document.getElementById('confirmDeleteBtn'),
    cancelDeleteBtn: document.getElementById('cancelDeleteBtn'),
    deleteModalError: document.getElementById('deleteModalError'),

    // Toast Container
    toastContainer: document.getElementById('toastContainer'),
  };

  // Initialize App
  async function init() {
    initTheme();
    bindEvents();

    // Check server health and capabilities
    await checkServer();

    if (currentApiKey) {
      const valid = await validateAndLoad(currentApiKey);
      if (!valid) {
        showAuthView();
      }
    } else {
      showAuthView();
    }
  }

  // Theme Management
  function initTheme() {
    applyTheme(currentTheme);
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (!localStorage.getItem(THEME_KEY) || localStorage.getItem(THEME_KEY) === 'system') {
        updateThemeLabel();
      }
    });
  }

  function updateThemeLabel() {
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (!document.documentElement.getAttribute('data-theme') && window.matchMedia('(prefers-color-scheme: dark)').matches);
    const label = document.getElementById('themeToggleText');
    if (label) {
      label.textContent = isDark ? 'light' : 'dark';
    }
  }

  function applyTheme(theme) {
    currentTheme = theme;
    localStorage.setItem(THEME_KEY, theme);
    if (theme === 'system') {
      document.documentElement.removeAttribute('data-theme');
    } else {
      document.documentElement.setAttribute('data-theme', theme);
    }
    updateThemeLabel();
  }

  function toggleTheme() {
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (!document.documentElement.getAttribute('data-theme') && window.matchMedia('(prefers-color-scheme: dark)').matches);
    applyTheme(isDark ? 'light' : 'dark');
  }

  // Server Communication
  async function checkServer() {
    if (isCheckingHealth) return;
    isCheckingHealth = true;

    try {
      const res = await fetch('/healthz');
      if (res.ok) {
        el.serverStatusPill.className = 'server-status-pill online';
        el.serverStatusLabel.textContent = 'Online';
      } else {
        el.serverStatusPill.className = 'server-status-pill error';
        el.serverStatusLabel.textContent = 'Error';
      }
    } catch {
      el.serverStatusPill.className = 'server-status-pill error';
      el.serverStatusLabel.textContent = 'Offline';
    }

    try {
      const infoRes = await fetch('/api/v1/info');
      if (infoRes.ok) {
        serverInfo = await infoRes.json();
        if (!serverInfo.allow_registration) {
          el.tabRegisterBtn.classList.add('hidden');
          el.registerSubmitBtn.disabled = true;
          el.regDisabledNotice.classList.remove('hidden');
        }
      }
    } catch (e) {
      console.warn('Could not fetch server info:', e);
    } finally {
      isCheckingHealth = false;
    }
  }

  async function validateAndLoad(key) {
    showLoading();
    try {
      const checkRes = await fetch('/api/v1/auth/check', {
        headers: { 'Authorization': `Bearer ${key}` }
      });

      if (!checkRes.ok) {
        return false;
      }

      currentApiKey = key;
      localStorage.setItem(STORAGE_KEY, key);
      await loadDashboardData();
      showDashboardView();
      return true;
    } catch (e) {
      showToast('Failed to connect to sync server', 'error');
      return false;
    }
  }

  async function loadDashboardData() {
    try {
      if (el.refreshIcon) el.refreshIcon.classList.add('spinning');

      const res = await fetch('/api/v1/sync/status', {
        headers: { 'Authorization': `Bearer ${currentApiKey}` }
      });

      if (!res.ok) {
        if (res.status === 401) {
          showToast('Session expired or key revoked', 'error');
          logout();
          return;
        }
        throw new Error(`HTTP ${res.status}`);
      }

      const data = await res.json();
      renderDashboard(data);
    } catch (err) {
      showToast('Error refreshing stats: ' + err.message, 'error');
    } finally {
      if (el.refreshIcon) {
        setTimeout(() => el.refreshIcon.classList.remove('spinning'), 400);
      }
    }
  }

  function renderDashboard(data) {
    // Header & Meta
    el.dashRevValue.textContent = data.rev ?? 0;

    const masked = currentApiKey.length > 12
      ? currentApiKey.slice(0, 6) + '••••••••' + currentApiKey.slice(-4)
      : '••••••••';
    el.dashMaskedKey.textContent = masked;

    if (data.account_created_at > 0) {
      const date = new Date(data.account_created_at * 1000);
      el.dashAccountDate.textContent = `Created: ${date.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })}`;
    } else {
      el.dashAccountDate.textContent = 'Account active';
    }

    // Metric counters
    animateValue(el.statManga, data.manga_count ?? 0);
    animateValue(el.statChapters, data.chapter_count ?? 0);
    animateValue(el.statCategories, data.category_count ?? 0);
    animateValue(el.statHistory, data.history_count ?? 0);
    animateValue(el.statPreferences, data.preference_count ?? 0);
    animateValue(el.statDevices, data.device_count ?? 0);

    // Setup helper fields
    const serverUrl = window.location.origin;
    el.setupServerUrl.value = serverUrl;
    el.setupApiKey.value = currentApiKey;

    // Render Setup QR
    renderQR(`${serverUrl}|${currentApiKey}`);
  }

  function animateValue(elem, endValue) {
    if (!elem) return;
    const startValue = parseInt(elem.textContent.replace(/,/g, ''), 10) || 0;
    if (startValue === endValue) {
      elem.textContent = Number(endValue).toLocaleString();
      return;
    }

    const duration = 400;
    const startTime = performance.now();

    function update(currentTime) {
      const elapsed = currentTime - startTime;
      const progress = Math.min(elapsed / duration, 1);
      const current = Math.floor(startValue + (endValue - startValue) * progress);
      elem.textContent = current.toLocaleString();

      if (progress < 1) {
        requestAnimationFrame(update);
      } else {
        elem.textContent = Number(endValue).toLocaleString();
      }
    }

    requestAnimationFrame(update);
  }

  // View Transitions
  function showLoading() {
    el.viewLoading.classList.remove('hidden');
    el.viewAuth.classList.add('hidden');
    el.viewDashboard.classList.add('hidden');
    el.navLogoutBtn.classList.add('hidden');
  }

  function showAuthView() {
    el.viewLoading.classList.add('hidden');
    el.viewAuth.classList.remove('hidden');
    el.viewDashboard.classList.add('hidden');
    el.navLogoutBtn.classList.add('hidden');
    el.keyCreatedCard.classList.add('hidden');
    switchTab('login');
  }

  function showDashboardView() {
    el.viewLoading.classList.add('hidden');
    el.viewAuth.classList.add('hidden');
    el.viewDashboard.classList.remove('hidden');
    el.navLogoutBtn.classList.remove('hidden');
  }

  function switchTab(tab) {
    if (tab === 'login') {
      el.tabLoginBtn.classList.add('active');
      el.tabRegisterBtn.classList.remove('active');
      el.loginForm.classList.remove('hidden');
      el.registerForm.classList.add('hidden');
      el.keyCreatedCard.classList.add('hidden');
      el.loginApiKey.focus();
    } else if (tab === 'register') {
      el.tabRegisterBtn.classList.add('active');
      el.tabLoginBtn.classList.remove('active');
      el.registerForm.classList.remove('hidden');
      el.loginForm.classList.add('hidden');
      el.keyCreatedCard.classList.add('hidden');
      el.regLabel.focus();
    }
  }

  function logout() {
    currentApiKey = '';
    localStorage.removeItem(STORAGE_KEY);
    showAuthView();
    showToast('Signed out', 'info');
  }

  // Event Handlers
  function bindEvents() {
    // Theme toggle
    el.themeToggleBtn.addEventListener('click', toggleTheme);

    // Logout
    el.navLogoutBtn.addEventListener('click', logout);

    // Tab switcher
    el.tabLoginBtn.addEventListener('click', () => switchTab('login'));
    el.tabRegisterBtn.addEventListener('click', () => switchTab('register'));

    // Toggle password show/hide
    document.querySelectorAll('.toggle-password-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const targetId = btn.getAttribute('data-target');
        const input = document.getElementById(targetId);
        if (!input) return;
        const isPassword = input.type === 'password';
        input.type = isPassword ? 'text' : 'password';

        const openIcon = btn.querySelector('.eye-open');
        const closedIcon = btn.querySelector('.eye-closed');
        if (openIcon && closedIcon) {
          openIcon.classList.toggle('hidden', !isPassword);
          closedIcon.classList.toggle('hidden', isPassword);
        }
      });
    });

    // Login Form Submit
    el.loginForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const key = el.loginApiKey.value.trim();
      if (!key) return;

      setBtnLoading(el.loginSubmitBtn, true);
      el.loginError.classList.add('hidden');

      try {
        const checkRes = await fetch('/api/v1/auth/check', {
          headers: { 'Authorization': `Bearer ${key}` }
        });

        if (checkRes.ok) {
          currentApiKey = key;
          localStorage.setItem(STORAGE_KEY, key);
          await loadDashboardData();
          showDashboardView();
          showToast('Logged in successfully', 'success');
          el.loginForm.reset();
        } else {
          el.loginError.textContent = 'Invalid API key. Please verify and try again.';
          el.loginError.classList.remove('hidden');
        }
      } catch (err) {
        el.loginError.textContent = 'Failed to connect to server: ' + err.message;
        el.loginError.classList.remove('hidden');
      } finally {
        setBtnLoading(el.loginSubmitBtn, false);
      }
    });

    // Register Form Submit
    el.registerForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const label = el.regLabel.value.trim();

      setBtnLoading(el.registerSubmitBtn, true);
      el.registerError.classList.add('hidden');

      try {
        const res = await fetch('/api/v1/auth/register', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ label })
        });

        const data = await res.json();
        if (res.ok && data.api_key) {
          currentApiKey = data.api_key;
          el.newGeneratedKeyDisplay.textContent = data.api_key;
          el.registerForm.classList.add('hidden');
          el.keyCreatedCard.classList.remove('hidden');
        } else {
          el.registerError.textContent = data.error || 'Failed to generate API key';
          el.registerError.classList.remove('hidden');
        }
      } catch (err) {
        el.registerError.textContent = 'Error connecting to server: ' + err.message;
        el.registerError.classList.remove('hidden');
      } finally {
        setBtnLoading(el.registerSubmitBtn, false);
      }
    });

    // Copy new generated key
    el.copyNewKeyBtn.addEventListener('click', () => {
      copyToClipboard(currentApiKey, 'API Key copied to clipboard');
    });

    // Continue with new key
    el.continueWithNewKeyBtn.addEventListener('click', async () => {
      localStorage.setItem(STORAGE_KEY, currentApiKey);
      await loadDashboardData();
      showDashboardView();
      showToast('Welcome to your new sync dashboard!', 'success');
    });

    // Refresh Dashboard
    el.dashRefreshBtn.addEventListener('click', () => {
      loadDashboardData();
    });

    // Copy helper buttons
    el.dashCopyKeyBtn.addEventListener('click', () => {
      copyToClipboard(currentApiKey, 'API Key copied');
    });
    el.copyServerUrlBtn.addEventListener('click', () => {
      copyToClipboard(el.setupServerUrl.value, 'Server URL copied');
    });
    el.copyApiKeyBtn.addEventListener('click', () => {
      copyToClipboard(currentApiKey, 'API Key copied');
    });

    // Delete Modal
    el.openDeleteAccountModalBtn.addEventListener('click', () => {
      el.deleteConfirmInput.value = '';
      el.confirmDeleteBtn.disabled = true;
      el.deleteModalError.classList.add('hidden');
      el.deleteModal.classList.remove('hidden');
      el.deleteConfirmInput.focus();
    });

    el.cancelDeleteBtn.addEventListener('click', () => {
      el.deleteModal.classList.add('hidden');
    });

    el.deleteConfirmInput.addEventListener('input', () => {
      el.confirmDeleteBtn.disabled = el.deleteConfirmInput.value.trim() !== 'DELETE';
    });

    el.confirmDeleteBtn.addEventListener('click', async () => {
      if (el.deleteConfirmInput.value.trim() !== 'DELETE') return;

      setBtnLoading(el.confirmDeleteBtn, true);
      el.deleteModalError.classList.add('hidden');

      try {
        const res = await fetch('/api/v1/auth/account', {
          method: 'DELETE',
          headers: { 'Authorization': `Bearer ${currentApiKey}` }
        });

        if (res.ok) {
          el.deleteModal.classList.add('hidden');
          logout();
          showToast('Account and all synced data permanently deleted', 'info');
        } else {
          const err = await res.json();
          el.deleteModalError.textContent = err.error || 'Failed to delete account';
          el.deleteModalError.classList.remove('hidden');
        }
      } catch (err) {
        el.deleteModalError.textContent = 'Connection error: ' + err.message;
        el.deleteModalError.classList.remove('hidden');
      } finally {
        setBtnLoading(el.confirmDeleteBtn, false);
      }
    });
  }

  function setBtnLoading(btn, isLoading) {
    if (!btn) return;
    btn.disabled = isLoading;
    const text = btn.querySelector('.btn-text');
    const spinner = btn.querySelector('.btn-spinner');
    if (text) text.classList.toggle('hidden', isLoading);
    if (spinner) spinner.classList.toggle('hidden', !isLoading);
  }

  function copyToClipboard(text, msg = 'Copied to clipboard') {
    if (!navigator.clipboard) {
      fallbackCopy(text, msg);
      return;
    }
    navigator.clipboard.writeText(text).then(
      () => showToast(msg, 'success'),
      () => fallbackCopy(text, msg)
    );
  }

  function fallbackCopy(text, msg) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    try {
      document.execCommand('copy');
      showToast(msg, 'success');
    } catch (e) {
      showToast('Could not copy to clipboard', 'error');
    }
    document.body.removeChild(ta);
  }

  function showToast(message, type = 'info') {
    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;

    let iconSvg = '';
    if (type === 'success') {
      iconSvg = `<span class="toast-icon toast-icon-success"><svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg></span>`;
    } else if (type === 'error') {
      iconSvg = `<span class="toast-icon toast-icon-error"><svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="8" x2="12" y2="12"></line><line x1="12" y1="16" x2="12.01" y2="16"></line></svg></span>`;
    } else {
      iconSvg = `<span class="toast-icon toast-icon-info"><svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="16" x2="12" y2="12"></line><line x1="12" y1="8" x2="12.01" y2="8"></line></svg></span>`;
    }

    const textSpan = document.createElement('span');
    textSpan.className = 'toast-text';
    textSpan.textContent = message;

    toast.innerHTML = iconSvg;
    toast.appendChild(textSpan);

    el.toastContainer.appendChild(toast);
    setTimeout(() => {
      toast.classList.add('toast-exit');
      setTimeout(() => {
        if (toast.parentNode) toast.parentNode.removeChild(toast);
      }, 250);
    }, 2800);
  }

  // Standard QR Code Generator on Canvas
  function renderQR(text) {
    const canvas = el.qrCanvas;
    if (!canvas || typeof qrcode === 'undefined') return;
    const ctx = canvas.getContext('2d');
    const size = canvas.width;

    // Clear canvas with crisp white background
    ctx.fillStyle = '#ffffff';
    ctx.fillRect(0, 0, size, size);

    try {
      const qr = qrcode(0, 'M');
      qr.addData(text);
      qr.make();

      const count = qr.getModuleCount();
      const quietZone = 2;
      const totalCells = count + quietZone * 2;
      const cellSize = Math.floor(size / totalCells);
      const offset = Math.floor((size - cellSize * count) / 2);

      ctx.fillStyle = '#0f0d13';
      for (let r = 0; r < count; r++) {
        for (let c = 0; c < count; c++) {
          if (qr.isDark(r, c)) {
            ctx.fillRect(offset + c * cellSize, offset + r * cellSize, cellSize, cellSize);
          }
        }
      }
    } catch (e) {
      console.warn('QR render error:', e);
    }
  }

  // Start application
  document.addEventListener('DOMContentLoaded', init);
})();
