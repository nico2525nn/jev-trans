/* graph.js — SVG renderers for the JEV-Trans console.
 *
 * Three layouts, all hand-rolled because there is no bundler here:
 *   - circuit   : the linear stage chain of plan.md §6, vertical, one box each
 *   - jlir      : events above entities, role edges between them, scopes banded
 *   - forest    : a generic layered DAG for the packed realization forest
 *
 * Every entry point takes server JSON and assumes nothing: a missing array, a
 * null node or a field of the wrong type degrades to "nothing to draw", never
 * to an exception that blanks the panel.
 */
(function (global) {
  'use strict';

  var NS = 'http://www.w3.org/2000/svg';

  /* --------------------------------------------------------- primitives */

  function s(name, attrs, text) {
    var e = document.createElementNS(NS, name);
    if (attrs) {
      for (var k in attrs) {
        if (Object.prototype.hasOwnProperty.call(attrs, k) &&
            attrs[k] !== null && attrs[k] !== undefined) {
          e.setAttribute(k, String(attrs[k]));
        }
      }
    }
    if (text !== null && text !== undefined) e.textContent = String(text);
    return e;
  }

  function arr(v) { return Array.isArray(v) ? v : []; }
  function num(v, d) { return typeof v === 'number' && isFinite(v) ? v : (d === undefined ? 0 : d); }
  function str(v, d) { return typeof v === 'string' && v !== '' ? v : (d === undefined ? '' : d); }
  function clamp(v, lo, hi) { return v < lo ? lo : (v > hi ? hi : v); }

  /* Shorten a label to fit a pixel budget at roughly `pxPerChar` per char. */
  function ellipsize(text, pxPerChar, maxChars) {
    text = String(text == null ? '' : text);
    if (maxChars && text.length > maxChars) return text.slice(0, maxChars - 1) + '…';
    return text;
  }

  /* Longest-path layering with a cycle guard: a malformed forest must still
   * render, it just degrades to insertion order. */
  function layerize(ids, edges) {
    var layer = {}, i;
    for (i = 0; i < ids.length; i++) layer[ids[i]] = 0;
    var succ = {}, indeg = {};
    for (i = 0; i < ids.length; i++) { succ[ids[i]] = []; indeg[ids[i]] = 0; }
    for (i = 0; i < edges.length; i++) {
      var a = edges[i].from, b = edges[i].to;
      if (!(a in succ) || !(b in layer) || a === b) continue;
      succ[a].push(b);
      indeg[b]++;
    }
    var queue = [];
    for (i = 0; i < ids.length; i++) if (indeg[ids[i]] === 0) queue.push(ids[i]);
    var seen = 0;
    while (queue.length) {
      var n = queue.shift();
      seen++;
      var next = succ[n] || [];
      for (var j = 0; j < next.length; j++) {
        if (layer[next[j]] < layer[n] + 1) layer[next[j]] = layer[n] + 1;
        indeg[next[j]]--;
        if (indeg[next[j]] === 0) queue.push(next[j]);
      }
    }
    // Anything left unvisited sits on a cycle: park it under its root.
    if (seen < ids.length) {
      for (i = 0; i < ids.length; i++) {
        if (indeg[ids[i]] > 0 && layer[ids[i]] === 0) layer[ids[i]] = 1;
      }
    }
    return layer;
  }

  /* Order nodes inside each layer by the average position of their neighbours,
   * sweeping down then up. Deterministic: ties fall back to the input order. */
  function orderLayers(ids, layer, edges) {
    var pos = {}, i;
    for (i = 0; i < ids.length; i++) pos[ids[i]] = i;
    var buckets = {};
    for (i = 0; i < ids.length; i++) {
      var k = layer[ids[i]];
      (buckets[k] || (buckets[k] = [])).push(ids[i]);
    }
    var byDepth = Object.keys(buckets).map(Number).sort(function (a, b) { return a - b; });
    var neighbours = {};
    for (i = 0; i < edges.length; i++) {
      (neighbours[edges[i].from] || (neighbours[edges[i].from] = [])).push(edges[i].to);
      (neighbours[edges[i].to] || (neighbours[edges[i].to] = [])).push(edges[i].from);
    }
    var rank = {};
    for (i = 0; i < ids.length; i++) rank[ids[i]] = pos[ids[i]];

    function sweep(depths) {
      for (var d = 0; d < depths.length; d++) {
        var arr = buckets[depths[d]];
        if (!arr) continue;
        var scored = arr.map(function (id, k) {
          var ns = neighbours[id] || [], sum = 0, n = 0;
          for (var t = 0; t < ns.length; t++) {
            if (rank[ns[t]] !== undefined && layer[ns[t]] !== layer[id]) { sum += rank[ns[t]]; n++; }
          }
          return { id: id, key: n ? sum / n : pos[id], tie: pos[id] };
        });
        scored.sort(function (a, b) { return a.key - b.key || a.tie - b.tie; });
        buckets[depths[d]] = scored.map(function (x) { return x.id; });
        for (var q = 0; q < arr.length; q++) rank[arr[q]] = q;
      }
    }
    sweep(byDepth);
    sweep(byDepth.slice().reverse());
    return buckets;
  }

  /* ================================================================ circuit */

  var CIRCUIT_W = 330;

  /* stages: [{key, label, kind}] — kind 'context' marks the discourse store.
   * state:  {stageKey: {status, durationMs, count}} */
  function mountCircuit(svg, stages, state, opts) {
    opts = opts || {};
    while (svg.firstChild) svg.removeChild(svg.firstChild);

    stages = arr(stages);
    var nodeH = 34, gap = 11, top = 24;
    var height = top + stages.length * nodeH + Math.max(0, stages.length - 1) * gap + 12;
    var boxW = CIRCUIT_W - 16, boxX = 8;

    svg.setAttribute('viewBox', '0 0 ' + CIRCUIT_W + ' ' + height);
    svg.setAttribute('width', '100%');
    svg.removeAttribute('height');

    svg.appendChild(s('text', { x: 8, y: 13, class: 'chead' }, 'PIPELINE — plan.md §6'));

    var yOf = {};
    for (var i = 0; i < stages.length; i++) yOf[i] = top + i * (nodeH + gap);

    // Edges first so node boxes paint over the line joins.
    for (var e = 0; e < stages.length - 1; e++) {
      var y0 = yOf[e] + nodeH, y1 = yOf[e + 1];
      var line = s('line', {
        x1: CIRCUIT_W / 2, y1: y0, x2: CIRCUIT_W / 2, y2: y1,
        class: 'cedge', 'data-edge': e
      });
      svg.appendChild(line);
    }

    var groups = [];
    for (var i = 0; i < stages.length; i++) {
      (function (idx, st) {
        var info = (state && state[st.key]) || null;
        var status = info ? str(info.status, 'ok') : 'notrun';
        var g = s('g', {
          class: 'cnode s-' + status + ' st-idle',
          'data-stage': st.key,
          'data-index': idx,
          tabindex: '0',
          role: 'button'
        });
        var y = yOf[idx];
        g.appendChild(s('rect', { class: 'cnode-box', x: boxX, y: y, width: boxW, height: nodeH, rx: 4 }));

        var txt = s('text', { class: 'cnode-label', x: boxX + 8, y: y + 14 }, ellipsize(st.label, 5.6, 34));
        g.appendChild(txt);

        var ms = info ? (num(info.durationMs, 0) < 0.05 ? '<0.05' : num(info.durationMs, 0).toFixed(2)) : 'not run';
        g.appendChild(s('text', { class: 'cnode-ms', x: boxX + boxW - 8, y: y + 14, 'text-anchor': 'end' }, ms + ' ms'));

        var sub = [];
        if (st.kind === 'context') sub.push('discourse store');
        if (info && info.note) sub.push(info.note);
        if (sub.length) {
          g.appendChild(s('text', { class: 'cnode-title', x: boxX + 8, y: y + 27 }, ellipsize(sub.join(' · '), 5, 52)));
        }

        var tip = st.label + ' — ' + (info ? (status + ', ' + ms + ' ms') : 'no span in this trace: not run');
        if (info && info.note) tip += '\n' + info.note;
        g.appendChild(s('title', null, tip));

        function pick() { if (typeof opts.onSelect === 'function') opts.onSelect(st.key, st); }
        g.addEventListener('click', pick);
        g.addEventListener('keydown', function (ev) {
          if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); pick(); }
        });

        svg.appendChild(g);
        groups.push({ el: g, index: idx, key: st.key });
      })(i, stages[i]);
    }

    return { nodes: groups, height: height };
  }

  /* Animate dim → done in stage order, and mark the incoming edge of each
   * node once that node has run. */
  function lightCircuit(mounted, svg, onEach) {
    if (!mounted) return;
    var edges = svg.querySelectorAll('.cedge');
    mounted.nodes.forEach(function (n) {
      n.el.classList.add('is-lit');
      n.el.classList.remove('st-idle');
      if (n.index > 0 && edges[n.index - 1]) edges[n.index - 1].classList.add('is-done');
      if (typeof onEach === 'function') onEach(n.key, n.index);
    });
  }

  function selectCircuit(svg, key) {
    var all = svg.querySelectorAll('.cnode');
    for (var i = 0; i < all.length; i++) {
      all[i].classList.toggle('is-selected', all[i].getAttribute('data-stage') === key);
    }
  }

  /* =================================================================== JLIR */

  function entitySurface(e) {
    var al = arr(e.aliases);
    for (var i = 0; i < al.length; i++) {
      if (al[i] && al[i].surface) return al[i].surface;
    }
    return str(e.identity, str(e.id, '?'));
  }

  /* Top-n of a jlir.Distribution, sorted by probability then option name so
   * the bar chart is stable between identical runs. */
  function topOptions(dist, n) {
    if (!dist || typeof dist !== 'object') return [];
    var opts = arr(dist.options);
    var prob = (dist.prob && typeof dist.prob === 'object') ? dist.prob : {};
    var rows = [];
    var seen = {};
    opts.forEach(function (o) {
      var key = str(o);
      if (!key || seen[key]) return;
      seen[key] = true;
      rows.push({ option: key, p: num(prob[key], 0) });
    });
    for (var k in prob) {
      if (!Object.prototype.hasOwnProperty.call(prob, k)) continue;
      if (seen[k]) continue;
      seen[k] = true;
      rows.push({ option: k, p: num(prob[k], 0) });
    }
    rows.sort(function (a, b) { return b.p - a.p || (a.option < b.option ? -1 : 1); });
    return rows.slice(0, n || 3);
  }

  /* graph: jlir.Graph. onSelectEntity(entity|null). */
  function mountJLIR(svg, graph, opts) {
    opts = opts || {};
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    if (!graph || typeof graph !== 'object') {
      svg.setAttribute('viewBox', '0 0 10 10');
      return { entities: [], events: [], scopes: [] };
    }

    var entities = arr(graph.entities).filter(function (x) { return x && typeof x === 'object'; });
    var events = arr(graph.events).filter(function (x) { return x && typeof x === 'object'; });
    var scopes = arr(graph.scopes).filter(function (x) { return x && typeof x === 'object'; });

    var gapX = 26, gapY = 62, padX = 14, padTop = 22;
    var colW = 168, evH = 40, entPad = 34, barH = 9;

    // Entity boxes grow with the number of referent bars they carry.
    var entH = {};
    entities.forEach(function (e, i) {
      var bars = e.referent && !e.referent.resolved ? topOptions(e.referent, 3).length : 0;
      entH[e.id || ('e' + i)] = entPad + bars * barH;
    });

    var cols = Math.max(entities.length, events.length, 1);
    var width = padX * 2 + cols * colW + Math.max(0, cols - 1) * gapX;
    var eventsBandH = events.length ? padTop + evH : 0;
    var entTop = eventsBandH + gapY;
    var entBottom = 0;
    entities.forEach(function (e, i) { entBottom = Math.max(entBottom, entTop + (entH[e.id || ('e' + i)] || entPad)); });
    if (!entities.length) entBottom = entTop;

    var bandTop = entBottom + 34;
    var scopeH = 34;
    var scopeBandH = scopes.length ? bandTop + 22 + scopeH : 0;
    var height = Math.max(entBottom + 12, scopeBandH + 12, 40);

    svg.setAttribute('viewBox', '0 0 ' + Math.max(width, 200) + ' ' + height);
    svg.setAttribute('width', '100%');
    svg.removeAttribute('height');

    var colX;
    var totalW = cols * colW + Math.max(0, cols - 1) * gapX;
    var originX = Math.max(padX, (Math.max(width, 200) - totalW) / 2);
    colX = function (i) { return originX + i * (colW + gapX); };
    // Event band -----------------------------------------------------------
    var evPos = {};
    events.forEach(function (ev, i) {
      var x = colX(i), y = padTop;
      var g = s('g', { class: 'gnode' });
      evPos[str(ev.id, 'v' + i)] = { cx: x + colW / 2, y: y, h: evH };
      g.appendChild(s('rect', { class: 'cnode-box', x: x, y: y, width: colW, height: evH, rx: 4 }));
      g.appendChild(s('text', { class: 'gnode-label', x: x + 8, y: y + 16 }, str(ev.id, 'v?')));
      g.appendChild(s('text', { class: 'gnode-sub', x: x + 8, y: y + 29 }, ellipsize(str(ev.predicate, '?'), 5, 26)));
      var argKeys = ev.args && typeof ev.args === 'object' ? Object.keys(ev.args).sort() : [];
      g.appendChild(s('title', null, 'event ' + str(ev.id) + '\npredicate ' + str(ev.predicate) +
        '\ntense ' + str(ev.tense, '?') + '  polarity ' + str(ev.polarity, '?') +
        (argKeys.length ? '\nroles: ' + argKeys.join(', ') : '\nno arguments')));
      svg.appendChild(g);
    });

    // Entity band ----------------------------------------------------------
    var pos = {};
    entities.forEach(function (e, i) {
      var id = str(e.id, 'e' + i);
      var x = colX(i), h = entH[id], y = entTop;
      pos[id] = { x: x, y: y, w: colW, h: h, cx: x + colW / 2, cy: y + h / 2 };

      var unresolved = !!(e.referent && !e.referent.resolved);
           var g = s('g', { class: 'gnode entity', 'data-entity': id, tabindex: '0', role: 'button' });
      g.appendChild(s('rect', {
               class: 'gbox', x: x, y: y, width: colW, height: h, rx: 4,
       style: 'fill:' + (unresolved ? '#241d0c' : '#12251b') + ';stroke:' + (unresolved ? '#7a5f1c' : '#2c6b48')
      }));
      g.appendChild(s('text', { class: 'gnode-label', x: x + 8, y: y + 15 }, id + '  ' + ellipsize(entitySurface(e), 6.4, 16)));
      var sub = [str(e.type, 'UNKNOWN'), str(e.number, '?'), str(e.gender, '?')].join(' · ');
      g.appendChild(s('text', { class: 'gnode-sub', x: x + 8, y: y + 27 }, ellipsize(sub, 5, 26)));

      if (unresolved) {
        // Zero-anaphora ambiguity, shown as it is: three bars, no winner.
        var rows = topOptions(e.referent, 3);
        var bw = colW - 74;
        for (var r = 0; r < rows.length; r++) {
          var by = y + entPad + r * barH;
          g.appendChild(s('rect', { x: x + 8, y: by + 2, width: bw, height: 5, fill: '#10141b', rx: 2 }));
          g.appendChild(s('rect', {
            x: x + 8, y: by + 2, width: Math.max(1, bw * clamp(rows[r].p, 0, 1)), height: 5,
            fill: '#e3b341', rx: 2
          }));
          g.appendChild(s('text', { class: 'gedge-label', x: x + 12 + bw, y: by + 7 },
            ellipsize(rows[r].option, 5, 10) + ' ' + rows[r].p.toFixed(2)));
        }
      }

      var tip = id + ' ' + entitySurface(e) + '\ntype ' + str(e.type, '?') +
        '\nnumber ' + str(e.number, '?') + '  gender ' + str(e.gender, '?') +
        '\nmentions ' + num(e.mentionCount, 0) + '  salience ' + num(e.salience, 0).toFixed(2);
      if (unresolved) {
        tip += '\nreferent UNRESOLVED (entropy ' + num(e.referent.entropy, 0).toFixed(2) + ')';
      }
      g.appendChild(s('title', null, tip + '\n\nclick for features and provenance'));

      function pick() { if (typeof opts.onSelectEntity === 'function') opts.onSelectEntity(id, e); }
      g.addEventListener('click', pick);
      g.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); pick(); }
      });
      svg.appendChild(g);
    });

    // Role edges: event → entity, labelled with the role name.
    var byId = {};
    entities.forEach(function (e, i) { byId[str(e.id, 'e' + i)] = e; });

    events.forEach(function (ev, ei) {
      var args = (ev.args && typeof ev.args === 'object') ? ev.args : {};
      var epos = evPos[str(ev.id, 'v' + ei)];
      if (!epos) return;
      var keys = Object.keys(args).sort();
      for (var k = 0; k < keys.length; k++) {
        var arg = args[keys[k]];
        if (!arg || typeof arg !== 'object') continue;
        var target = pos[str(arg.value)];
        if (!target) continue;
        var x1 = epos.cx, y1 = epos.y + epos.h;
        var x2 = target.cx, y2 = target.y;
        var mid = (y1 + y2) / 2;
        svg.appendChild(s('path', {
          class: 'cedge is-done',
          d: 'M' + x1 + ' ' + y1 + ' C ' + x1 + ' ' + mid + ', ' + x2 + ' ' + mid + ', ' + x2 + ' ' + y2
        }));
        var lx = (x1 + x2) / 2, ly = mid;
        var lab = s('text', { class: 'gedge-label', x: lx + 3, y: ly, 'text-anchor': 'start' },
          ellipsize(keys[k], 5, 16));
        var t = s('title', null, 'role ' + keys[k] + ' → ' + str(arg.value) +
          '\nconfidence ' + num(arg.confidence, 0).toFixed(2));
        lab.appendChild(t);
        svg.appendChild(lab);
      }
    });

    // Scope band -----------------------------------------------------------
    if (scopes.length) {
      svg.appendChild(s('line', { x1: padX, y1: bandTop, x2: Math.max(width, 200) - padX, y2: bandTop, stroke: '#262d3b' }));
      svg.appendChild(s('text', { class: 'gband-label', x: padX, y: bandTop + 13 }, 'SCOPE'));
      var sw = Math.min(180, Math.max(90, (Math.max(width, 200) - padX * 2 - gapX * (scopes.length - 1)) / scopes.length));
      scopes.forEach(function (sc, i) {
        var x = padX + i * (sw + gapX), y = bandTop + 22;
               var g = s('g', { class: 'gnode' });
       g.appendChild(s('rect', { class: 'gbox', x: x, y: y, width: sw, height: scopeH, rx: 4,
         style: 'fill:' + (sc.resolved ? '#12251b' : '#241d0c') + ';stroke:' + (sc.resolved ? '#2c6b48' : '#7a5f1c') }));
        g.appendChild(s('text', { class: 'gnode-label', x: x + 8, y: y + 14 }, str(sc.kind, '?')));
        var lbl = str(sc.label, arr(sc.readings).length + ' readings');
        g.appendChild(s('text', { class: 'gnode-sub', x: x + 8, y: y + 27 }, ellipsize(lbl, 5, 24)));
        g.appendChild(s('title', null, 'scope ' + str(sc.kind) + '\noperands ' + arr(sc.operands).join(', ') +
          '\n' + (sc.resolved ? 'resolved' : 'UNRESOLVED: ' + arr(sc.readings).length + ' readings')));
        svg.appendChild(g);
      });
    }

    return { entities: entities, events: events, scopes: scopes };
  }

  /* ================================================================= forest */

  /* f: forest.Forest = {root, nodes:[{id,label,alts:[{lex,children,probability,rule,note}],shared}], shared} */
  function mountForest(svg, f, opts) {
    opts = opts || {};
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    if (!f || typeof f !== 'object') {
      svg.setAttribute('viewBox', '0 0 10 10');
      return { nodes: [], shared: 0 };
    }
    var nodes = arr(f.nodes).filter(function (n) { return n && n.id; });
    if (!nodes.length) {
      svg.setAttribute('viewBox', '0 0 10 10');
      return { nodes: [], shared: 0 };
    }

    var ids = nodes.map(function (n) { return String(n.id); });
    var edges = [], seen = {};
    nodes.forEach(function (n) {
      arr(n.alts).forEach(function (a) {
        if (!a || typeof a !== 'object') return;
        arr(a.children).forEach(function (c) {
          var key = String(n.id) + '>' + String(c);
          if (seen[key] || String(c) === String(n.id)) return;
          seen[key] = true;
          edges.push({ from: String(n.id), to: String(c) });
        });
      });
    });

    var layer = layerize(ids, edges);
    var buckets = orderLayers(ids, layer, edges);

    var nodeW = 176, nodeH = 40, gapX = 30, gapY = 26, padX = 10, padTop = 10;
    var depths = Object.keys(buckets).map(Number).sort(function (a, b) { return a - b; });
    var maxW = 0;
    depths.forEach(function (d) { maxW = Math.max(maxW, buckets[d].length); });
    var width = padX * 2 + maxW * nodeW + Math.max(0, maxW - 1) * gapX;
    var height = padTop + depths.length * nodeH + Math.max(0, depths.length - 1) * gapY + padTop;

    svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
    svg.setAttribute('width', '100%');
    svg.removeAttribute('height');

    var byId = {};
    nodes.forEach(function (n) { byId[String(n.id)] = n; });
    var pos = {};
    depths.forEach(function (d) {
      var row = buckets[d];
      var rowW = row.length * nodeW + Math.max(0, row.length - 1) * gapX;
      var x0 = (width - rowW) / 2;
      row.forEach(function (id, i) {
        pos[id] = { x: x0 + i * (nodeW + gapX), y: padTop + d * (nodeH + gapY), w: nodeW, h: nodeH };
      });
    });

    var rootId = str(f.root);
    edges.forEach(function (e) {
      var a = pos[e.from], b = pos[e.to];
      if (!a || !b) return;
      var x1 = a.x + a.w / 2, y1 = a.y + a.h;
      var x2 = b.x + b.w / 2, y2 = b.y;
      var mid = (y1 + y2) / 2;
      svg.appendChild(s('path', {
        class: 'cedge is-done',
        d: 'M' + x1 + ' ' + y1 + ' C ' + x1 + ' ' + mid + ', ' + x2 + ' ' + mid + ', ' + x2 + ' ' + y2
      }));
    });

    var sharedCount = 0;
    nodes.forEach(function (n) {
      var id = String(n.id), p = pos[id];
      if (!p) return;
      var shared = !!n.shared;
      if (shared) sharedCount++;
      var alts = arr(n.alts);
            var g = s('g', { class: 'gnode fnode' });
      g.appendChild(s('rect', {
               class: 'gbox', x: p.x, y: p.y, width: p.w, height: p.h, rx: 4,
       style: 'fill:' + (shared ? '#101d28' : '#151a24') + ';stroke:' + (shared ? '#2f6d9c' : '#2c3444')
      }));
      if (shared) g.appendChild(s('rect', { x: p.x, y: p.y, width: p.w, height: 3, rx: 1.5, fill: '#57b6ff' }));
      g.appendChild(s('text', { class: 'gnode-label', x: p.x + 8, y: p.y + (shared ? 17 : 15) },
        ellipsize(str(n.label, id), 5.6, 24)));
      var sub = [];
      if (id === rootId) sub.push('root');
      if (shared) sub.push('shared');
      sub.push(alts.length + ' alt' + (alts.length === 1 ? '' : 's'));
      g.appendChild(s('text', { class: 'gnode-sub', x: p.x + 8, y: p.y + 29 }, sub.join(' · ')));

      var tip = id + ' ' + str(n.label, '') + '\n' + alts.length + ' alternative(s)';
      alts.slice(0, 8).forEach(function (a, i) {
        if (!a) return;
        tip += '\n  [' + num(a.probability, 0).toFixed(3) + '] ' + str(a.lex, '⟨' + arr(a.children).join(' ') + '⟩') +
          (a.rule ? '  rule ' + a.rule : '') + (a.note ? '\n      note: ' + a.note : '');
      });
      if (alts.length > 8) tip += '\n  … ' + (alts.length - 8) + ' more';
      g.appendChild(s('title', null, tip));
      svg.appendChild(g);
    });

    return { nodes: nodes, shared: sharedCount };
  }

  global.JEVGraph = {
    mountCircuit: mountCircuit,
    lightCircuit: lightCircuit,
    selectCircuit: selectCircuit,
    mountJLIR: mountJLIR,
    mountForest: mountForest,
    topOptions: topOptions,
    helpers: { s: s, arr: arr, num: num, str: str, clamp: clamp, ellipsize: ellipsize }
  };
})(window);