(() => {
  'use strict';
  const root = document.documentElement;
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  let theme = 'system';
  let key = '';
  root.classList.add('js');
  try {
    const saved = localStorage.getItem('u1-theme');
    if (['light', 'dark', 'system'].includes(saved)) theme = saved;
  } catch (_) { /* Storage can be unavailable; use the system preference. */ }
  function paint() {
    const dark = theme === 'dark' || (theme === 'system' && system.matches);
    root.classList.toggle('dark', dark);
    root.style.colorScheme = dark ? 'dark' : 'light';
    document.querySelectorAll('[data-theme-choice]').forEach(button => {
      button.setAttribute('aria-pressed', String(button.dataset.themeChoice === theme));
    });
  }
  paint();

  document.addEventListener('DOMContentLoaded', () => {
    const byID = id => document.getElementById(id);
    const tabs = [...document.querySelectorAll('[role="tab"]')];
    const menu = byID('nav-menu');
    const sidebar = byID('sidebar');
    const main = byID('main-content');
    const skip = document.querySelector('.skip-link');
    const wide = window.matchMedia('(min-width: 1024px)');
    const tooltipMedia = window.matchMedia('(min-width: 640px) and (hover: hover) and (pointer: fine) and (any-pointer: fine)');
    const touch = window.matchMedia('(any-pointer: coarse)');
    let selected = tabs[0];
    let checking = false;
    let refreshing = false;
    let archiveRequest = 0, plansRequest = 0, keyEpoch = 0;
    let activeTip = null;
    root.classList.add('js-tabs');

    function fade() {
      if (document.startViewTransition && !window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
        document.startViewTransition(paint);
      } else paint();
    }
    paint();
    system.addEventListener('change', () => { if (theme === 'system') fade(); });
    document.querySelectorAll('[data-theme-choice]').forEach(button => {
      button.addEventListener('click', () => {
        theme = button.dataset.themeChoice;
        try { localStorage.setItem('u1-theme', theme); } catch (_) { /* Keep the choice in memory. */ }
        fade();
      });
    });

    const panelOf = tab => byID(tab.getAttribute('aria-controls'));
    const isOpen = () => document.body.classList.contains('nav-open');
    function nav(open, focus) {
      document.body.classList.toggle('nav-open', open);
      menu.setAttribute('aria-expanded', String(open));
      main.inert = open;
      skip.inert = open;
      if (open) selected.focus();
      else if (focus) focus.focus();
    }
    function select(tab, focus = false) {
      selected = tab;
      tabs.forEach(item => {
        const on = item === tab;
        item.setAttribute('aria-selected', String(on));
        item.tabIndex = on ? 0 : -1;
        const panel = panelOf(item);
        if (panel) panel.hidden = !on;
      });
      if (focus) tab.focus();
    }
    menu.addEventListener('click', () => nav(!isOpen(), menu));
    byID('nav-close').addEventListener('click', () => nav(false, menu));
    byID('nav-backdrop').addEventListener('click', () => nav(false, menu));
    wide.addEventListener('change', () => {
      if (wide.matches && isOpen()) nav(false, selected);
    });
    document.addEventListener('keydown', event => {
      if (!isOpen()) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        nav(false, menu);
      } else if (event.key === 'Tab') {
        const items = [...sidebar.querySelectorAll('a[href],button,input')]
          .filter(item => item.tabIndex !== -1 && !item.disabled && !item.hidden && item.getClientRects().length);
        const first = items[0], last = items[items.length - 1];
        if (!items.includes(document.activeElement)) {
          event.preventDefault();
          first.focus();
        } else if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    });
    tabs.forEach((tab, index) => {
      tab.addEventListener('click', event => {
        event.preventDefault();
        select(tab);
        if (isOpen()) nav(false, panelOf(tab) || menu);
      });
      tab.addEventListener('keydown', event => {
        let next;
        if (['ArrowDown', 'ArrowRight'].includes(event.key)) next = (index + 1) % tabs.length;
        else if (['ArrowUp', 'ArrowLeft'].includes(event.key)) next = (index + tabs.length - 1) % tabs.length;
        else if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = tabs.length - 1;
        if (next !== undefined) {
          event.preventDefault();
          select(tabs[next], true);
        }
      });
    });
    select(selected);

    function takeKey() {
      const input = byID('k');
      if (input.value && input.value !== key) { key = input.value; keyEpoch++; }
      input.value = '';
    }
    function accountsButton() {
      const button = byID('check-accounts');
      if (button) button.disabled = checking;
      const refresh = byID('refresh-all');
      if (refresh) {
        refresh.disabled = refreshing;
        refresh.textContent = refreshing ? 'Refreshing...' : 'Refresh All Credentials';
      }
    }
    async function load(accounts) {
      takeKey();
      const container = byID(accounts ? 'usage-accounts' : 'usage-data');
      if (!container) return;
      const epoch = keyEpoch;
      const request = accounts ? ++plansRequest : ++archiveRequest;
      const current = () => epoch === keyEpoch && request === (accounts ? plansRequest : archiveRequest) && container.isConnected;
      const error = byID('usage-error');
      error.textContent = '';
      if (accounts) { checking = true; accountsButton(); }
      try {
        const query = new URLSearchParams();
        const form = byID('quota-controls');
        if (!accounts && form) {
          for (const [name, value] of new FormData(form)) {
            if (name !== 'qx') query.append(name, value);
          }
          form.querySelectorAll('input[name="qx"]:not(:checked)').forEach(box => query.append('qx', box.value));
        }
        const response = await fetch('/v8/management/observability/usage/' + (accounts ? 'accounts' : 'archive?' + query), {
          method: accounts ? 'POST' : 'GET',
          headers: { Authorization: 'Bearer ' + key },
          cache: 'no-store',
          redirect: 'error'
        });
        if (response.status === 200) {
          const fragment = await response.text();
          if (!current()) return;
          container.innerHTML = fragment;
          activeTip = null;
          select(selected);
          accountsButton();
        } else {
          let message = 'HTTP ' + response.status;
          try {
            const body = await response.json();
            if (typeof body.error === 'string' && body.error) message = body.error;
          } catch (_) { /* Non-JSON responses retain the HTTP status. */ }
          if (!current()) return;
          container.replaceChildren();
          if (response.status === 401 || response.status === 403) { key = ''; keyEpoch++; byID('k').value = ''; }
          error.textContent = message;
        }
      } catch (_) {
        if (!current()) return;
        container.replaceChildren();
        error.textContent = 'Network error';
      } finally {
        if (accounts) { checking = false; accountsButton(); }
      }
    }
    document.addEventListener('submit', event => {
      if (event.target.id === 'quota-controls') event.preventDefault();
    });
    document.addEventListener('change', event => {
      if (event.target.closest('#quota-controls') && event.target.matches('[data-recompute]')) load(false);
    });
    async function refreshAll() {
      if (!window.confirm('Refresh every enabled credential on this proxy, for every provider? Each refresh contacts its provider and rewrites its login file. The provider and account choices on this page do not limit this.')) return;
      takeKey();
      const epoch = keyEpoch;
      const labels = new Map();
      byID('refresh-labels')?.content.querySelectorAll('[data-auth-id]').forEach(label => labels.set(label.dataset.authId, label.cloneNode(true)));
      const result = byID('refresh-result');
      result.replaceChildren();
      refreshing = true;
      accountsButton();
      try {
        const response = await fetch('/v8/management/credentials/refresh?all=true', {
          method: 'POST',
          headers: { Authorization: 'Bearer ' + key },
          cache: 'no-store',
          redirect: 'error'
        });
        let body;
        try { body = await response.json(); } catch (_) { /* A malformed body is an unexpected response. */ }
        if (epoch !== keyEpoch) return;
        if (response.status !== 200) {
          if (response.status === 401 || response.status === 403) { key = ''; keyEpoch++; byID('k').value = ''; }
          result.textContent = typeof body?.error === 'string' && body.error ? body.error : 'HTTP ' + response.status;
          return;
        }
        if (!Array.isArray(body?.results)) { result.textContent = 'Unexpected response'; return; }
        const succeeded = body.results.filter(row => row?.success === true).length;
        const heading = document.createElement('p');
        heading.textContent = 'Refreshed ' + succeeded + ' of ' + body.results.length + ' credentials';
        result.append(heading);
        const failures = document.createElement('ul');
        for (const row of body.results) {
          if (row?.success === true) continue;
          const item = document.createElement('li');
          const label = labels.get(row?.id);
          if (label) item.append(label.cloneNode(true));
          else {
            const other = document.createElement('span');
            other.textContent = 'other credentials';
            item.append(other);
          }
          const reason = document.createElement('span');
          const error = typeof row?.error === 'string' ? row.error.toLowerCase() : '';
          const terminal = ['token_invalidated', 'refresh_token_invalidated', 'refresh_token_reused', 'refresh_token_expired', 'refresh_token_revoked', 'invalid_grant'].some(code => error.includes(code));
          reason.textContent = ': ' + (terminal ? 'Sign in again' : 'Failed');
          item.append(reason);
          failures.append(item);
        }
        result.append(failures);
      } catch (_) {
        if (epoch === keyEpoch) result.textContent = 'Network error; some credentials may have refreshed.';
      } finally {
        refreshing = false;
        accountsButton();
      }
    }
    byID('usage-key-form').addEventListener('submit', event => {
      event.preventDefault();
      load(false);
    });
    document.addEventListener('click', event => {
      const page = event.target.closest('#quota-controls button[data-page]');
      if (page && !page.disabled) {
        const input = byID('quota-controls').querySelector('input[name="qpage"]');
        const next = page.dataset.page === 'today' ? 0 : Number(input.value) + (page.dataset.page === 'prev' ? -1 : 1);
        input.value = String(Math.max(-4, Math.min(0, next)));
        load(false);
      }
      if (event.target.closest('#check-accounts') && !checking) load(true);
      if (event.target.closest('#refresh-all') && !refreshing) refreshAll();
    });

    function place(tip) {
      tip.style.top = '';
      tip.style.bottom = '';
      tip.style.transform = '';
      if (!tooltipMedia.matches || touch.matches) return;
      let rect = tip.getBoundingClientRect();
      if (!rect.width) return;
      const width = root.clientWidth, height = window.innerHeight, margin = 8;
      if (rect.bottom > height - margin) {
        tip.style.top = 'auto';
        tip.style.bottom = '24px';
      }
      rect = tip.getBoundingClientRect();
      let x = 0, y = 0;
      if (rect.right > width - margin) x = width - margin - rect.right;
      if (rect.left + x < margin) x = margin - rect.left;
      if (rect.bottom > height - margin) y = height - margin - rect.bottom;
      if (rect.top + y < margin) y = margin - rect.top;
      if (x || y) tip.style.transform = `translate(${x}px,${y}px)`;
    }
    document.addEventListener('mouseover', event => {
      const column = event.target.closest('.col');
      if (!column || column.contains(event.relatedTarget)) return;
      activeTip = column.querySelector('.tip');
      if (activeTip) place(activeTip);
    });
    document.addEventListener('mouseout', event => {
      const column = event.target.closest('.col');
      if (column && !column.contains(event.relatedTarget)) activeTip = null;
    });
    function again() {
      if (activeTip && activeTip.isConnected) place(activeTip);
      else activeTip = null;
    }
    window.addEventListener('scroll', again, { capture: true, passive: true });
    window.addEventListener('resize', again);
  });
})();
