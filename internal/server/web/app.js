/* app.js — state, fetch, event wiring, re-render orchestration.
 *
 * One rule runs through this file: the panel is the product. Raw JSON is kept
 * behind a toggle so it can be checked, never so it can be avoided.
 */
(function (global) {
  'use strict';

  var P = global.JEVPanels;
  var G = global.JEVGraph;

  var dom = {
    form: document.getElementById('controls'),
    srcLang: document.getElementById('srcLang'),
    tgtLang: document.getElementById('tgtLang'),
    text: document.getElementById('srcText'),
    politeness: document.getElementById('politeness'),
    politenessOut: document.getElementById('politenessOut'),
    pronoun: document.getElementById('pronoun'),
    pronounOut: document.getElementById('pronounOut'),
    register: document.getElementById('register'),
    mode: document.getElementById('mode'),
    translate: document.getElementById('btnTranslate'),
    reset: document.getElementById('btnReset'),
    health: document.getElementById('health'),
    banner: document.getElementById('banner'),
    circuit: document.getElementById('circuit'),
    circuitMeta: document.getElementById('circuitMeta'),
    stageDetail: document.getElementById('stageDetail'),
    tabs: document.getElementById('tabs'),
    panel: document.getElementById('panel'),
    panelTitle: document.getElementById('panelTitle'),
    inspect: document.getElementById('btnInspect')
  };

  var state = {
    resp: null,
    health: null,
    tab: 'output',
    selectedStage: null,
    entitySelection: null,      // {side, id}
    jlirSide: 'source',
    inspectOpen: false,
    busy: false,
    documentId: null
  };

  var afterRenderQueue = [];

  /* ------------------------------------------------------------ utilities */

  function sessionKey() { return 'jevtrans.documentId'; }

  function documentId() {
    if (state.documentId) return state.documentId;
    var id = null;
    try { id = sessionStorage.getItem(sessionKey()); } catch (e) { id = null; }
    if (!id) {
      id = 'ui-' + Math.random().toString(36).slice(2, 10);
      try { sessionStorage.setItem(sessionKey(), id); } catch (e) { /* private mode */ }
    }
    state.documentId = id;
    return id;
  }

  function banner(msg) {
    if (!msg) { dom.banner.hidden = true; dom.banner.textContent = ''; return; }
    dom.banner.hidden = false;
    dom.banner.textContent = msg;
  }

  function setBusy(on) {
    state.busy = !!on;
    dom.translate.disabled = on;
    dom.reset.disabled = on;
    dom.translate.textContent = on ? 'Translating…' : 'Translate';
  }

  /* The server answers 4xx with {error, code}; a proxy in between may answer
   * with HTML. Either way the user gets a sentence, not a stack trace. */
  function postJSON(path, body) {
    return fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    }).then(function (res) {
      return res.text().then(function (txt) {
        var data = null;
        try { data = txt ? JSON.parse(txt) : null; } catch (e) { data = null; }
        if (!res.ok || data === null || typeof data !== 'object') {
          var msg = (data && data.error) ? String(data.error)
            : ('HTTP ' + res.status + ' ' + (res.statusText || '') + (txt ? ': ' + txt.slice(0, 400) : ''));
          throw new Error(msg);
        }
        return data;
      });
    });
  }

  function getJSON(path) {
    return fetch(path).then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.json();
    });
  }

  /* --------------------------------------------------------------- health */

  function renderHealth(h) {
    var dot = '<span class="health-dot"></span>';
    var text, meta, cls;
    if (!h) {
      cls = 'health-down'; text = 'SERVER UNREACHABLE'; meta = 'no answer from /api/health';
    } else if (h.jev && h.jev.configured) {
      cls = 'health-online';
      text = 'ORACLE ONLINE — ' + String(h.jev.model || 'jev');
      meta = 'v' + String(h.version || '?') + ' · ' + String(h.jev.endpoint || '');
    } else {
      cls = 'health-offline';
      text = 'ORACLE OFFLINE — analysis priors only';
      meta = 'v' + String(h.version || '?') + ' · no OPENCODE_API_KEY · every decision falls back to a prior';
    }
    dom.health.className = 'health ' + cls;
    dom.health.title = h
      ? 'jev.configured = ' + String(!!(h.jev && h.jev.configured)) +
        '\nmodel = ' + String((h.jev || {}).model || '—') +
        '\nendpoint = ' + String((h.jev || {}).endpoint || '—')
      : 'the server did not answer /api/health';
    dom.health.innerHTML = dot +
      '<span class="health-text">' + escapeHTML(text) + '</span>' +
      '<span class="health-meta">' + escapeHTML(meta) + '</span>';
  }

  function escapeHTML(s) {
    return String(s === null || s === undefined ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  function refreshHealth() {
    var prev = state.health;
    var before = prev && prev.jev ? !!prev.jev.configured : null;
    return getJSON('/api/health').then(function (h) {
      state.health = h;
      renderHealth(h);
      // Re-render only when the oracle actually changed state; nothing else in
      // the panel depends on it, and a needless re-render loses the scroll.
      var now = !!(h && h.jev && h.jev.configured);
      if (before !== now) renderPanel();
      return h;
    }).catch(function (err) {
      state.health = null;
      renderHealth(null);
      banner('Cannot reach /api/health: ' + err.message +
        '. Is the server running on this origin?');
    });
  }

  /* -------------------------------------------------------------- circuit */

  function renderCircuit(animate) {
    var idx = state.resp ? P.indexTrace(state.resp) : {};
    var summary = (state.resp && state.resp.summary) || {};
    var circuitState = {};
    P.STAGES.forEach(function (st) {
      var e = idx[st.key];
      if (!e) return;
      var note = firstNote(e.events && e.events[0]);
      circuitState[st.key] = {
        status: e.status,
        durationMs: e.durationMs,
        note: note
      };
    });

    var mounted = G.mountCircuit(dom.circuit, P.STAGES, circuitState, {
      onSelect: function (key) { selectStage(key); }
    });

    if (state.resp) {
      var ran = 0, worst = 'ok';
      P.STAGES.forEach(function (st) {
        var e = idx[st.key];
        if (!e) return;
        ran++;
        if (rank(e.status) > rank(worst)) worst = e.status;
      });
      dom.circuitMeta.textContent = ran + '/' + P.STAGES.length + ' stages · ' +
        (summary.events || 0) + ' events · ' + Number(summary.totalMs || 0).toFixed(2) + ' ms · worst ' + worst;
    } else {
      dom.circuitMeta.textContent = 'no run yet';
    }

    if (animate) {
      var nodes = mounted.nodes;
      var step = Math.max(16, Math.min(40, 520 / Math.max(1, nodes.length)));
      nodes.forEach(function (n) {
        setTimeout(function () {
          n.el.classList.add('is-lit');
          n.el.classList.remove('st-idle');
          var edges = dom.circuit.querySelectorAll('.cedge');
          if (n.index > 0 && edges[n.index - 1]) edges[n.index - 1].classList.add('is-done');
        }, n.index * step);
      });
    } else {
      G.lightCircuit(mounted, dom.circuit);
    }

    G.selectCircuit(dom.circuit, state.selectedStage);
  }

  function firstNote(ev) {
    var ns = P.notes ? (ev && Array.isArray(ev.notes) ? ev.notes : []) : [];
    return ns.length ? String(ns[0]) : '';
  }

  function rank(s) {
    return ({ ok: 0, skip: 1, warn: 2, error: 3 })[s] === undefined ? 0 : ({ ok: 0, skip: 1, warn: 2, error: 3 })[s];
  }

  /* ---------------------------------------------------------- stage detail */

  function selectStage(key) {
    state.selectedStage = key;
    G.selectCircuit(dom.circuit, key);
    renderStageDetail();
  }

  function renderStageDetail() {
    dom.stageDetail.textContent = '';
    var key = state.selectedStage;
    if (!key) {
      dom.stageDetail.appendChild(P.empty('no stage selected',
        'Click any node of the circuit to read that stage\'s span: its title, status, duration, labels, notes and the raw event payload it attached. A node with no span is a stage that did not run in this translation.'));
      return;
    }

    var def = null;
    P.STAGES.forEach(function (s) { if (s.key === key) def = s; });
    var idx = state.resp ? P.indexTrace(state.resp) : {};
    var e = idx[key];

    var head = P.h('div', { class: 'dec-head' }, [
      P.h('span', { class: 'dec-stage', text: def ? def.label : key }),
      e ? P.badge(String(e.status), String(e.status)) : P.badge('NOT RUN', 'skip'),
      e ? P.badge(numText(e.durationMs) + ' ms') : null,
      e ? P.badge((e.events || []).length + ' event(s)') : null
    ]);
    dom.stageDetail.appendChild(head);

    if (!e) {
      dom.stageDetail.appendChild(P.empty('this stage did not run',
        'No span with stage ' + key + ' exists in the last response. Nothing was skipped silently: the trace is the proof, and there is no proof here. Switch to Output for what the pipeline reported instead.'));
      return;
    }

    var events = (e.events || []).slice();
    var root = null;
    if (events.length) root = events.shift();

    if (root) {
      dom.stageDetail.appendChild(P.kv([
        ['event id', String(root.id || '—')],
        ['title', String(root.title || e.title || '—')],
        ['started', String(root.started || '—')],
        ['duration', numText(root.durationMs) + ' ms'],
        ['detail', String(root.detail || '—')]
      ]));
      var counts = root.counts && typeof root.counts === 'object' ? root.counts : null;
      if (counts) {
        dom.stageDetail.appendChild(P.kv(Object.keys(counts).sort().map(function (k) {
          return [k, String(counts[k])];
        })));
      }
      var labels = root.labels && typeof root.labels === 'object' ? root.labels : null;
      if (labels) {
        dom.stageDetail.appendChild(P.kv(Object.keys(labels).sort().map(function (k) {
          return [k, String(labels[k])];
        })));
      }
      var nl = P.notes(root.notes);
      if (nl) dom.stageDetail.appendChild(P.h('div', { class: 'sec' }, [
        P.h('div', { class: 'sec-head' }, P.h('h3', { text: 'notes' })), nl
      ]));
    }

    if (events.length) {
      dom.stageDetail.appendChild(P.h('div', { class: 'sec' }, [
        P.h('div', { class: 'sec-head' }, P.h('h3', { text: 'child events — ' + events.length })),
        P.h('div', { class: 'list' }, events.map(function (ev) {
          return P.h('details', { class: 'json-inspect' }, [
            P.h('summary', { text: String(ev.title || ev.stage || 'event') + ' · ' + String(ev.status || '?') +
              ' · ' + numText(ev.durationMs) + ' ms' }),
            P.h('pre', { text: safeJSON(ev) })
          ]);
        }))
      ]));
    }

    dom.stageDetail.appendChild(P.h('details', { class: 'json-inspect' }, [
      P.h('summary', { text: 'inspect JSON — every span recorded for ' + key }),
      P.h('pre', { text: safeJSON(e) })
    ]));
  }

  function numText(v) {
    var n = typeof v === 'number' && isFinite(v) ? v : 0;
    return n < 0.05 ? '<0.05' : n.toFixed(3);
  }

  function safeJSON(v) {
    try { return JSON.stringify(v, null, 2); }
    catch (err) { return '/* this value cannot be serialized: ' + err.message + ' */'; }
  }

  /* ---------------------------------------------------------------- panel */

  function panelContext() {
    return {
      resp: state.resp,
      jlirSide: state.jlirSide,
      entitySelection: state.entitySelection,
      setJlirSide: function (side) { state.jlirSide = side; renderPanel(); },
      selectEntity: function (side, id) { state.entitySelection = { side: side, id: id }; renderPanel(); },
      clearEntity: function () { state.entitySelection = null; renderPanel(); },
      disambiguate: disambiguate,
      afterRender: function (fn) { afterRenderQueue.push(fn); }
    };
  }

  function renderPanel() {
    var name = state.tab;
    var spec = P.PANELS[name];
    dom.panelTitle.textContent = spec ? spec.title : name;
    dom.panel.textContent = '';
    afterRenderQueue = [];

    if (!spec) {
      dom.panel.appendChild(P.empty('unknown tab', 'No renderer is registered for "' + name + '".'));
      return;
    }

    var out;
    try {
      out = spec.render(panelContext());
    } catch (err) {
      dom.panel.appendChild(P.h('div', { class: 'why' }, [
        P.h('h4', { text: 'this panel failed to render' }),
        P.h('p', { text: String(err && err.message ? err.message : err) }),
        P.h('p', { class: 'faint', text: 'The response itself is still available: the circuit on the left and the JSON toggle below are unaffected.' })
      ]));
      out = { node: null, inspect: null };
    }

    if (out && out.node) dom.panel.appendChild(out.node);

    if (state.inspectOpen) {
      dom.panel.appendChild(P.h('details', { class: 'json-inspect', open: 'open' }, [
        P.h('summary', { text: 'inspect JSON — ' + (spec ? spec.title : name) + ' payload' }),
        P.h('pre', { text: out && out.inspect !== undefined && out.inspect !== null
          ? safeJSON(out.inspect)
          : '/* nothing to inspect for this panel yet */' })
      ]));
    }

    // Graph renderers need their <svg> in the document to measure.
    var queue = afterRenderQueue;
    afterRenderQueue = [];
    queue.forEach(function (fn) {
      try { fn(); } catch (err) {
        console.error('graph render failed', err);
      }
    });
  }

  function renderTabs() {
    var buttons = dom.tabs.querySelectorAll('.tab');
    for (var i = 0; i < buttons.length; i++) {
      var b = buttons[i];
      b.classList.toggle('is-active', b.getAttribute('data-tab') === state.tab);
      b.setAttribute('aria-selected', b.getAttribute('data-tab') === state.tab ? 'true' : 'false');
    }
  }

  function renderAll(animate) {
    renderCircuit(animate);
    renderStageDetail();
    renderTabs();
    renderPanel();
  }

  /* -------------------------------------------------------------- actions */

  function styleObject() {
    return {
      politeness: parseFloat(dom.politeness.value),
      register: dom.register.value,
      pronounExplicitness: parseFloat(dom.pronoun.value),
      literaryness: 0
    };
  }

  function baseRequest() {
    return {
      text: dom.text.value,
      sourceLang: dom.srcLang.value,
      targetLang: dom.tgtLang.value,
      style: styleObject(),
      documentId: documentId(),
      mode: dom.mode.value
    };
  }

  function translate() {
    if (state.busy) return;
    if (!dom.text.value.trim()) {
      banner('Nothing to translate: the source text is empty.');
      return;
    }
    banner('');
    setBusy(true);
    state.entitySelection = null;
    state.selectedStage = null;
    postJSON('/api/translate', baseRequest())
      .then(function (resp) {
        state.resp = resp;
        setBusy(false);
        renderAll(true);
        refreshHealth();
      })
      .catch(function (err) {
        setBusy(false);
        state.resp = null;
        renderAll(false);
        banner('Translation failed: ' + err.message);
      });
  }

  function disambiguate(questionId, option) {
    if (state.busy) return;
    if (!questionId) { banner('That question carried no id, so the answer cannot be attached.'); return; }
    banner('');
    setBusy(true);
    var body = baseRequest();
    body.answer = { questionId: questionId, option: option };
    // The documented shape also carries questionId/option at the top level;
    // the server reads the nested `answer`, the flat fields keep the request
    // self-describing for anything that reads the contract literally.
    body.questionId = questionId;
    body.option = option;

    postJSON('/api/disambiguate', body)
      .then(function (resp) {
        state.resp = resp;
        setBusy(false);
        state.entitySelection = null;
        renderAll(true);
        refreshHealth();
      })
      .catch(function (err) {
        setBusy(false);
        banner('Disambiguation failed: ' + err.message);
      });
  }

  function resetDocument() {
    if (state.busy) return;
    setBusy(true);
    banner('');
    postJSON('/api/document/reset', { documentId: documentId() })
      .then(function () {
        state.resp = null;
        state.entitySelection = null;
        state.selectedStage = null;
        setBusy(false);
        renderAll(false);
        banner('Document state reset. The next translation starts from an empty discourse store.');
      })
      .catch(function (err) {
        setBusy(false);
        banner('Reset failed: ' + err.message);
      });
  }

  /* ----------------------------------------------------------------- wire */

  function wire() {
    dom.form.addEventListener('submit', function (ev) { ev.preventDefault(); translate(); });
    dom.reset.addEventListener('click', resetDocument);

    dom.text.addEventListener('keydown', function (ev) {
      if ((ev.ctrlKey || ev.metaKey) && ev.key === 'Enter') { ev.preventDefault(); translate(); }
    });

    function slider(input, out) {
      input.addEventListener('input', function () { out.textContent = parseFloat(input.value).toFixed(2); });
    }
    slider(dom.politeness, dom.politenessOut);
    slider(dom.pronoun, dom.pronounOut);

    dom.srcLang.addEventListener('change', function () {
      // English source, Japanese target and back; never the same language.
      var other = dom.srcLang.value === 'ja' ? 'en' : 'ja';
      if (dom.tgtLang.value === dom.srcLang.value) dom.tgtLang.value = other;
    });

    dom.tabs.addEventListener('click', function (ev) {
      var btn = ev.target.closest ? ev.target.closest('.tab') : null;
      if (!btn) return;
      state.tab = btn.getAttribute('data-tab');
      renderTabs();
      renderPanel();
    });

    dom.inspect.addEventListener('click', function () {
      state.inspectOpen = !state.inspectOpen;
      dom.inspect.setAttribute('aria-pressed', state.inspectOpen ? 'true' : 'false');
      renderPanel();
    });

    global.addEventListener('error', function (ev) {
      console.error('unhandled error', ev.error || ev.message);
    });
  }

  /* ----------------------------------------------------------------- boot */

  function boot() {
    wire();
    renderAll(false);
    refreshHealth();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }

  // Exposed so the page can be driven from a console during review.
  global.JEVApp = {
    state: state,
    translate: translate,
    reset: resetDocument,
    disambiguate: disambiguate,
    renderAll: renderAll,
    renderPanel: renderPanel,
    selectStage: selectStage,
    setResp: function (r) { state.resp = r; renderAll(false); }
  };
})(window);