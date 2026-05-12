// Theme shim — resolves the active theme on parse (localStorage > prefers-color-scheme),
// sets <html data-theme>, and toggles + persists on #theme-toggle clicks. Loaded with
// `defer` like the rest of the static JS; this is a small FOWT trade-off, since a strict
// no-flash contract would require a render-blocking inline <script> in <head>.
(function () {
  'use strict';

  var STORAGE_KEY = 'theme';
  var VALID = { light: true, dark: true };

  function readStored() {
    try {
      var v = window.localStorage.getItem(STORAGE_KEY);
      return VALID[v] ? v : null;
    } catch (e) {
      return null;
    }
  }

  function resolveInitial() {
    var stored = readStored();
    if (stored) return stored;
    var prefersDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
    return prefersDark ? 'dark' : 'light';
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
  }

  applyTheme(resolveInitial());

  document.addEventListener('click', function (event) {
    var btn = event.target.closest('#theme-toggle');
    if (!btn) return;
    event.preventDefault();
    var current = document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
    var next = current === 'dark' ? 'light' : 'dark';
    applyTheme(next);
    try {
      window.localStorage.setItem(STORAGE_KEY, next);
    } catch (e) {
      console.warn('theme.js: failed to persist theme: ' + e);
    }
    window.dispatchEvent(new CustomEvent('__themeChanged', { detail: { theme: next } }));
  });
})();
