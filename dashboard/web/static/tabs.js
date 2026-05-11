// Tab router for the dashboard — parses window.location.hash and swaps the
// matching tab fragment into #tab-body via htmx. The hash is the single
// source of truth: clicks update the hash, hashchange does the swap.
(function () {
  'use strict';

  var TABS = ['overview', 'clients', 'system', 'network', 'events', 'about'];
  var DEFAULT_TAB = 'overview';

  function parseHash() {
    var raw = (window.location.hash || '').replace(/^#/, '');
    var qIdx = raw.indexOf('?');
    var name = qIdx === -1 ? raw : raw.slice(0, qIdx);
    var query = qIdx === -1 ? '' : raw.slice(qIdx + 1);
    if (TABS.indexOf(name) === -1) {
      if (name) console.warn('tabs.js: unknown tab "' + name + '", falling back to ' + DEFAULT_TAB);
      name = DEFAULT_TAB;
    }
    return { tab: name, params: new URLSearchParams(query) };
  }

  function markActive(tab) {
    var pills = document.querySelectorAll('.tab-pill');
    pills.forEach(function (pill) {
      if (pill.dataset.tab === tab) {
        pill.setAttribute('aria-current', 'page');
        pill.classList.add('active');
      } else {
        pill.removeAttribute('aria-current');
        pill.classList.remove('active');
      }
    });
  }

  function route() {
    var parsed = parseHash();
    markActive(parsed.tab);
    var range = parsed.params.get('range');
    var url = '/partial/' + parsed.tab + (range ? '?range=' + encodeURIComponent(range) : '');
    htmx.ajax('GET', url, { target: '#tab-body', swap: 'innerHTML' });
  }

  document.addEventListener('click', function (event) {
    var pill = event.target.closest('.tab-pill');
    if (!pill || !pill.dataset.tab) return;
    event.preventDefault();
    window.location.hash = '#' + pill.dataset.tab;
  });

  window.addEventListener('hashchange', route);
  document.addEventListener('DOMContentLoaded', route);
})();
