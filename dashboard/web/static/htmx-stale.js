// Stale-data indicator for the dashboard.
//
// Per the functional spec §2.8, "stale" means the most recent htmx swap
// failed (network error or non-2xx response), not "200 with degraded cards".
// On any failed swap we reveal #stale-pill; on the next successful swap we
// hide it again. Listening on document.body works because all htmx events
// bubble up there, and #dashboard-content (the swap target) gets its
// innerHTML replaced — listeners attached to it would survive, but the body
// is the conventional bus for these events.
(function () {
  'use strict';

  function setStale(stale) {
    var pill = document.getElementById('stale-pill');
    if (!pill) return;
    if (stale) {
      pill.removeAttribute('hidden');
    } else {
      pill.setAttribute('hidden', '');
    }
  }

  // htmx:responseError fires for any non-2xx response from the server.
  // htmx:sendError fires when the request never gets a response (network
  // down, timeout, etc.). Both qualify as "swap failed".
  document.body.addEventListener('htmx:responseError', function () { setStale(true); });
  document.body.addEventListener('htmx:sendError', function () { setStale(true); });

  // htmx:afterSwap fires only on a successful swap (no error events emitted
  // beforehand). Hide the pill on the next success.
  document.body.addEventListener('htmx:afterSwap', function () { setStale(false); });
})();
