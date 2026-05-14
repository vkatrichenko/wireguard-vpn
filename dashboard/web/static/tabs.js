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

  function triggerExpand(pubkey) {
    var placeholder = document.getElementById('detail-' + pubkey);
    if (!placeholder) {
      console.info('tabs.js: expand target not found', pubkey);
      return;
    }
    var row = placeholder.previousElementSibling;
    if (!row || !row.classList.contains('client-row')) {
      console.info('tabs.js: expand row sibling missing', pubkey);
      return;
    }
    row.click();
  }

  function route() {
    var parsed = parseHash();
    markActive(parsed.tab);
    var range = parsed.params.get('range');
    var expand = parsed.params.get('expand');
    var url = '/partial/' + parsed.tab + (range ? '?range=' + encodeURIComponent(range) : '');
    var swap = htmx.ajax('GET', url, { target: '#tab-body', swap: 'innerHTML' });
    if (parsed.tab === 'clients' && expand && swap && typeof swap.then === 'function') {
      swap.then(function () { triggerExpand(expand); });
    }
  }

  document.addEventListener('click', function (event) {
    var pill = event.target.closest('.tab-pill');
    if (!pill || !pill.dataset.tab) return;
    event.preventDefault();
    window.location.hash = '#' + pill.dataset.tab;
  });

  // Range selector change: mirror the new range into the URL hash so a reload
  // preserves it. Delegated on document.body because range-selector.html is
  // re-rendered into the tab body on every htmx swap, so a direct listener
  // wouldn't survive. pushState (not `location.hash = ...`) is deliberate:
  // hash= fires hashchange, which would call route() and trigger a redundant
  // htmx swap on top of the one htmx already fired from the form.
  document.body.addEventListener('change', function (event) {
    var select = event.target.closest('.range-selector select');
    if (!select) return;
    var parsed = parseHash();
    if (select.value === parsed.params.get('range')) return;
    parsed.params.set('range', select.value);
    var qs = parsed.params.toString();
    var newHash = '#' + parsed.tab + (qs ? '?' + qs : '');
    history.pushState({}, '', newHash);
  });

  window.addEventListener('hashchange', route);
  document.addEventListener('DOMContentLoaded', route);

  // Client-detail toggle: same-row click collapses without re-fetching, and
  // a click on a different row collapses any other open detail first so only
  // one detail is ever expanded.
  document.body.addEventListener('htmx:beforeRequest', function (event) {
    var path = (event.detail && event.detail.requestConfig && event.detail.requestConfig.path) || '';
    var m = path.match(/^\/partial\/clients\/([^/]+)\/detail$/);
    if (!m) return;

    var pubkey = decodeURIComponent(m[1]);
    var target = document.getElementById('detail-' + pubkey);
    if (!target) return;

    if (!target.classList.contains('hidden')) {
      target.classList.add('hidden');
      // Leaks at most one stale Chart.js instance per pubkey in charts.js's
      // clientCharts Map; initClientChart destroys the prior one on reopen.
      target.innerHTML = '';
      event.preventDefault();
      return;
    }

    document.querySelectorAll('.detail-row:not(.hidden)').forEach(function (row) {
      if (row.id !== 'detail-' + pubkey) {
        row.classList.add('hidden');
        // Same bounded-leak note as above applies on cross-row collapse.
        row.innerHTML = '';
      }
    });
  });

  // Client-detail reveal: drop the .hidden class once htmx swaps the fragment
  // in so the detail row becomes visible.
  document.body.addEventListener('htmx:afterSwap', function (event) {
    var target = event.detail && event.detail.target;
    if (!target || !target.id || target.id.indexOf('detail-') !== 0) return;
    target.classList.remove('hidden');
  });
})();
