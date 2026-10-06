/* panels.js — one render function per inspector tab.
 *
 * Each render returns {node, inspect}: `node` is the DOM the panel shows, and
 * `inspect` is the raw slice the "inspect JSON" toggle dumps. JSON is never the
 * primary display — it is the receipt, not the report.
 *
 * The server's response is treated as untrusted input. Every accessor below
 * tolerates a missing key, a null and a wrong type; the worst outcome is an
 * empty state that says what would have appeared there.
 */
(function (global) {
  'use strict';

  var H = (global.JEVGraph && global.JEVGraph.helpers) || {};
  var arr = H.arr || function (v) { return Array.isArray(v) ? v : []; };
  var num = H.num || function (v, d) { return typeof v === 'number' && isFinite(v) ? v : (d || 0); };
  var str = H.str || function (v, d) { return typeof v === 'string' && v !== '' ? v : (d === undefined ? '' : d); };

  /* ------------------------------------------------------------- DOM kit */

  function h(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (!Object.prototype.hasOwnProperty.call(attrs, k)) continue;
        var v = attrs[k];
        if (v === null || v === undefined || v === false) continue;
        if (k === 'class') e.className = v;
        else if (k === 'text') e.textContent = String(v);
        else if (k.slice(0, 2) === 'on' && typeof v === 'function') e.addEventListener(k.slice(2), v);
        else if (k === 'dataset') { for (var d in v) e.dataset[d] = v[d]; }
        else e.setAttribute(k, v === true ? '' : String(v));
      }
    }
    append(e, kids);
    return e;
  }

  function append(parent, kids) {
    if (kids === null || kids === undefined) return parent;
    if (Array.isArray(kids)) { kids.forEach(function (k) { append(parent, k); }); return parent; }
    parent.appendChild(kids instanceof Node ? kids : document.createTextNode(String(kids)));
    return parent;
  }

  function badge(text, kind) { return h('span', { class: 'badge' + (kind ? ' badge-' + kind : ''), text: text }); }

  function empty(title, body) {
    return h('div', { class: 'empty' }, [
      h('p', { class: 'empty-title', text: title }),
      h('p', { class: 'empty-body', text: body })
    ]);
  }

  function section(title, note, kids) {
    return h('section', { class: 'sec' }, [
      h('div', { class: 'sec-head' }, [
        h('h3', { text: title }),
        note ? h('span', { class: 'sec-note', text: note }) : null
      ]),
      h('div', { class: 'sec-body' }, kids)
    ]);
  }

  function kv(pairs) {
    var dl = h('dl', { class: 'kv' });
    pairs.forEach(function (p) {
      if (!p) return;
      dl.appendChild(h('dt', { text: p[0] }));
      var v = p[1];
      if (v instanceof Node) dl.appendChild(h('dd', null, v));
      else dl.appendChild(h('dd', { text: (v === null || v === undefined || v === '') ? '—' : String(v) }));
    });
    return dl;
  }

  function notes(list) {
    var items = arr(list);
    if (!items.length) return null;
    return h('ul', { class: 'note-list' }, items.map(function (n) {
      return h('li', { text: String(n) });
    }));
  }

  function pct(v) { return (num(v, 0) * 100).toFixed(1) + '%'; }

  function bar(label, value, max, cls) {
    var v = num(value, 0), m = num(max, 1) || 1;
    var f = Math.max(0, Math.min(1, m ? v / m : 0));
    return h('div', { class: 'bar-row' + (v === 0 ? ' is-zero' : '') }, [
      h('span', { class: 'bar-label', text: label, title: label }),
      h('span', { class: 'bar-track' }, h('span', { class: 'bar-fill' + (cls ? ' is-' + cls : ''), style: 'width:' + (f * 100).toFixed(2) + '%' })),
      h('span', { class: 'bar-val', text: num(v, 0).toFixed(3) })
    ]);
  }

  function tags(list, kind) {
    var items = arr(list);
    if (!items.length) return null;
    return h('div', { class: 'tag-row' }, items.map(function (t) { return badge(String(t), kind); }));
  }

  function details(summary, kids, open) {
    return h('details', { class: 'json-inspect', open: open ? true : null }, [
      h('summary', { text: summary }),
      kids
    ]);
  }

  /* ----------------------------------------------------------- status map */

  var STATUS_KIND = {
    EXACT: 'exact', GOOD: 'good', LOSSY: 'lossy', AMBIGUOUS: 'ambig',
    UNDERDETERMINED: 'under', UNSUPPORTED: 'unsup', UNPARSABLE: 'unpars'
  };

  var LOSS_DIMS = [
    ['propositional', 'propositional'],
    ['referential', 'referential'],
    ['temporal', 'temporal'],
    ['pragmatic', 'pragmatic'],
    ['stylistic', 'stylistic'],
    ['implicature', 'implicature']
  ];

  var SOURCE_KIND = { jev: 'jev', cache: 'cache', prior: 'prior', skipped: 'skipped' };

  function statusBadge(s) {
    s = str(s);
    return s ? badge(s, STATUS_KIND[s] || null) : badge('NO STATUS', 'skip');
  }

  function lossTotal(loss) {
    if (!loss || typeof loss !== 'object') return 0;
    var t = 0;
    LOSS_DIMS.forEach(function (d) { t += num(loss[d[0]], 0); });
    return t;
  }

  /* The 18 boxes of plan.md §6, in stage order. `key` is the trace.Stage value
   * the server emits; `label` is what a reader sees. They are not always the
   * same string, and the UI must match on `key` or the circuit goes dark. */
  var STAGES = [
    { key: 'DOCUMENT_STATE_UPDATE', label: 'DOCUMENT STATE', kind: 'context' },
    { key: 'INPUT_NORMALIZATION', label: 'INPUT NORMALIZATION' },
    { key: 'MORPHOLOGICAL_LATTICE', label: 'MORPHOLOGICAL LATTICE' },
    { key: 'PACKED_SYNTACTIC_FOREST', label: 'PACKED SYNTACTIC FOREST' },
    { key: 'SOURCE_SEMANTIC_FOREST', label: 'SOURCE SEMANTIC FOREST' },
    { key: 'JLIR_CORE', label: 'JLIR CORE' },
    { key: 'JEV_DECISION_GRAPH', label: 'JEV DECISION GRAPH' },
    { key: 'CONSTRAINED_JLIR_STATE', label: 'CONSTRAINED JLIR STATE' },
    { key: 'TARGET_PROJECTION_ENGINE', label: 'TARGET PROJECTION ENGINE' },
    { key: 'TARGET_MESSAGE_PLANNER', label: 'TARGET MESSAGE PLANNER' },
    { key: 'CONSTRUCTION_SELECTION', label: 'CONSTRUCTION SELECTION' },
    { key: 'PACKED_REALIZATION_FOREST', label: 'PACKED REALIZATION FOREST' },
    { key: 'GRAMMATICAL_REALIZER', label: 'GRAMMATICAL REALIZER' },
    { key: 'TARGET_REPARSER', label: 'TARGET RE-PARSER' },
    { key: 'TARGET_JLIR', label: 'TARGET JLIR' },
    { key: 'SEMANTIC_EQUIVALENCE_VERIFIER', label: 'SEMANTIC EQUIVALENCE VERIFIER' },
    { key: 'JEV_RERANKER', label: 'JEV RERANKER' },
    { key: 'FINAL_OUTPUT', label: 'FINAL OUTPUT' }
  ];

  /* ------------------------------------------------------------ trace help */

  /* The trace arrives as a tree whose root is the whole run. Index every event
   * by stage so a circuit box can find its span without assuming the tree is
   * flat. A stage with no event stays absent — "not run" is a real signal. */
  function indexTrace(resp) {
    var out = {};
    var summary = (resp && resp.summary) || {};
    var bySummary = {};
    arr(summary.stages).forEach(function (s) {
      if (s && s.stage) bySummary[s.stage] = s;
    });

    function visit(node) {
      if (!node || typeof node !== 'object') return;
      var stage = str(node.stage);
      if (stage && !(stage in out)) {
        out[stage] = {
          stage: stage, status: str(node.status, 'ok'),
          durationMs: num(node.durationMs, 0),
          events: [], id: node.id, title: node.title
        };
      }
      var bucket = stage ? out[stage] : null;
      arr(node.children).forEach(function (c) {
        if (bucket) bucket.events.push(c);
        visit(c);
      });
    }
    visit(resp && resp.trace);

    for (var k in bySummary) {
      if (k in out) continue;
      out[k] = {
        stage: k, status: str(bySummary[k].status, 'ok'),
        durationMs: num(bySummary[k].durationMs, 0),
        events: [], title: 'summary only'
      };
    }
    return out;
  }

  /* Notes attached to the stages that explain a failure, for the "why there is
   * no output" block. Ordered so the earliest structural problem reads first. */
  var WHY_STAGES = [
    'INPUT_NORMALIZATION', 'MORPHOLOGICAL_LATTICE', 'PACKED_SYNTACTIC_FOREST',
    'SOURCE_SEMANTIC_FOREST', 'JLIR_CORE', 'JEV_DECISION_GRAPH',
    'CONSTRAINED_JLIR_STATE', 'PACKED_REALIZATION_FOREST',
    'GRAMMATICAL_REALIZER', 'SEMANTIC_EQUIVALENCE_VERIFIER', 'JEV_RERANKER',
    'FINAL_OUTPUT'
  ];

  function collectTraceNotes(resp, limit) {
    var idx = indexTrace(resp);
    var out = [];
    WHY_STAGES.forEach(function (stage) {
      var e = idx[stage];
      if (!e) return;
      var all = [e].concat(e.events || []);
      all.forEach(function (ev) {
        var d = str(ev.detail);
        if (d) out.push(stage.toLowerCase().replace(/_/g, ' ') + ': ' + d);
        arr(ev.notes).forEach(function (n) { out.push(stage.toLowerCase().replace(/_/g, ' ') + ': ' + String(n)); });
      });
    });
    return out.slice(0, limit || 14);
  }

  /* ================================================================ OUTPUT */

  function renderOutput(ctx) {
    var resp = ctx.resp;
    if (!resp) {
      return {
        node: empty('no translation yet',
          'Paste a sentence and press Translate. This panel will show the selected target string, its status, the six loss dimensions, the per-feature confidence breakdown and every alternative candidate that survived verification.'),
        inspect: null
      };
    }

    var result = resp.result || {};
    var selected = result.selected || null;
    var node = h('div');

    node.appendChild(h('div', { class: 'headline' }, [
      h('div', { class: 'headline-src' }, [
        badge(str((resp.source || {}).lang, '?'), null),
        ' ',
        str((resp.source || {}).text, '(empty source)')
      ]),
      selected && str(selected.text)
        ? h('div', { class: 'headline-tgt', text: str(selected.text) })
        : h('div', { class: 'headline-tgt is-empty' },
            'no target string — the pipeline reported ' + str(result.status, 'no status')),
      h('div', { class: 'tag-row' }, [
        statusBadge(str(result.status, '')),
        selected ? badge('naturalness ' + num(selected.naturalness, 0).toFixed(3)) : null,
        selected ? badge('loss total ' + lossTotal(selected.loss).toFixed(3), 'lossy') : null,
        selected && num((selected.confidence || {}).overall, 0) > 0
          ? badge('confidence ' + pct((selected.confidence || {}).overall), 'good') : null
      ])
    ]));

    if (!selected) node.appendChild(whyBlock(resp));

    // Interactive questions -------------------------------------------------
    var qs = arr(result.questions);
    if (qs.length) node.appendChild(section('open questions', qs.length + ' blocking', qs.map(function (q) {
      return questionCard(q, ctx);
    })));

    if (selected) {
      node.appendChild(section('loss', 'six dimensions, 0 = nothing lost', LOSS_DIMS.map(function (d) {
        return bar(d[1], (selected.loss || {})[d[0]], 1, 'loss');
      })));

      var conf = selected.confidence || {};
      var feats = (conf.byFeature && typeof conf.byFeature === 'object') ? conf.byFeature : {};
      var featRows = Object.keys(feats).sort(function (a, b) {
        return num(feats[a], 0) - num(feats[b], 0) || (a < b ? -1 : 1);
      }).map(function (k) { return bar(k, feats[k], 1, 'conf'); });
      node.appendChild(section('confidence',
        'weakest feature first' + (featRows.length ? '' : ' — none reported'),
        [bar('OVERALL', conf.overall, 1, 'conf')].concat(featRows.length ? featRows : [
          h('p', { class: 'muted-none', text: 'the verifier reported no per-feature confidences' })
        ])));

      var diffs = arr(selected.diffs);
      node.appendChild(section('differences vs source', diffs.length + ' diff(s)', diffs.length ? h('div', { class: 'list' },
        diffs.map(function (d) {
          return h('div', { class: 'rejected' }, [
            h('div', {}, [
              badge(str(d.severity, 'soft'), d.severity === 'hard' ? 'error' : 'warn'),
              ' ',
              h('span', { class: 'mono', text: str(d.dimension, '?') + ' · ' + str(d.item, '?') })
            ]),
            h('div', { class: 'r-reason', text: str(d.detail) }),
            kv([['source', str(d.source, '—')], ['target', str(d.target, '—')]])
          ]);
        })) : h('p', { class: 'muted-none', text: 'no differences: source and target agreed feature by feature.' })));

      var unsup = arr(selected.unsupported);
      if (unsup.length) {
        node.appendChild(section('unsupported source features', unsup.length + ' item(s)',
          h('div', { class: 'list' }, unsup.map(function (u) {
            return h('div', { class: 'card' }, [
              h('div', { class: 'mono', text: str(u.feature || u.kind || '?') }),
              h('div', { class: 'faint', text: str(u.detail || u.reason || u.note, '') })
            ]);
          }))));
      }

      var cnote = notes(selected.notes);
      if (cnote) node.appendChild(section('notes', null, cnote));

      var ct = selected.trace && typeof selected.trace === 'object' ? selected.trace : null;
      if (ct) {
        node.appendChild(section('candidate trace', null, kv(Object.keys(ct).sort().map(function (k) {
          return [k, str(ct[k])];
        }))));
      }

      if (arr(selected.constructions).length) {
        node.appendChild(section('constructions', null, tags(selected.constructions)));
      }
    }

    // Interpretations: the readings the source stage deliberately kept open.
    var interps = arr(result.interpretations);
    if (interps.length) {
      node.appendChild(section('source readings kept open', interps.length + ' reading(s)',
        h('div', { class: 'list' }, interps.slice().sort(function (a, b) { return num(b.weight, 0) - num(a.weight, 0); })
          .map(function (r) {
            return h('div', { class: 'row' }, [
              h('div', { class: 'row-main' }, [
                h('div', { class: 'row-main-text', text: str(r.label, str(r.origin, 'reading')) }),
                h('div', { class: 'row-meta', text: str(r.origin, '') })
              ]),
              h('div', { class: 'row-side' }, badge('w ' + num(r.weight, 0).toFixed(3)))
            ]);
          }))));
    }

    // Candidates other than the selected one.
    var cands = arr(result.candidates);
    var others = cands.filter(function (c) { return c && (!selected || c !== selected); });
    node.appendChild(section('other candidates', others.length + ' of ' + cands.length,
      others.length ? h('div', { class: 'list' }, others.map(function (c) {
        return h('div', { class: 'row' }, [
          h('div', { class: 'row-main' }, [
            h('div', { class: 'row-main-text', text: str(c.text, '(empty)') }),
            h('div', { class: 'row-meta', text: 'loss ' + lossTotal(c.loss).toFixed(3) +
              ' · confidence ' + pct((c.confidence || {}).overall) })
          ]),
          h('div', { class: 'row-side' }, [statusBadge(str(c.status, ''))])
        ]);
      })) : h('p', { class: 'muted-none', text: selected ? 'no other candidate survived verification.' : 'no candidate was verified.' })));

    // Stage metrics. The funnel is cumulative — a stage only counts when every
    // earlier stage counted for the same sentence — so the numbers are read
    // together, and the four candidate counts are separate claims: raw is what
    // the realizer produced, eligible is what survived the hard gate,
    // certified is what the equivalence verifier actually proved, and selected
    // is what came out. Collapsing eligible and certified is how a candidate
    // the system merely declined to reject ends up reported as verified.
    node.appendChild(stageMetricsPanel(resp.stageMetrics));

    var warn = arr(resp.warnings);
    node.appendChild(section('warnings', warn.length ? warn.length + ' item(s)' : null,
      warn.length ? notes(warn) : h('p', { class: 'muted-none', text: 'none.' })));

    return { node: node, inspect: { result: resp.result, warnings: resp.warnings } };
  }

  // stageMetricsPanel renders the cumulative funnel and the two diagnostics
  // that say where a sentence stopped.
  //
  // The unresolved-clause-head table is here rather than buried in the JSON
  // because it is the thing that decides what to work on next, and it is only
  // useful if it separates the causes: a missing dictionary entry, a parser
  // that attached the wrong word, and a clause with no predicate at all send
  // three people to three different tables. A single count of "unknown
  // predicates" sends all of them to the dictionary.
  function stageMetricsPanel(m) {
    m = m || {};
    function frac(a, b) {
      a = num(a, 0); b = num(b, 0);
      return b > 0 ? a + '/' + b : String(a);
    }
    var rows = [
      ['morphology', frac(m.tokens - num(m.opaqueTokens, 0), m.tokens),
        num(m.opaqueTokens, 0) + ' opaque token(s)'],
      ['predicate', frac(m.predicatesResolved, m.predicatesTotal),
        num(m.openPositions, 0) + ' open position(s)'],
      ['semantic frame', frac(m.framesResolved, m.framesTotal), null],
      ['construction', frac(m.constructionsSelected, m.constructionsTotal), null]
    ];
    var body = h('div', { class: 'list' }, rows.map(function (r) {
      return h('div', { class: 'row' }, [
        h('div', { class: 'row-main' }, [
          h('div', { class: 'row-main-text', text: r[0] }),
          r[2] ? h('div', { class: 'row-meta', text: r[2] }) : null
        ]),
        h('div', { class: 'row-side' }, [badge('w ' + r[1])])
      ]);
    }));

    body.appendChild(h('p', { class: 'faint', text: 'funnel is cumulative: a stage counts only when every earlier stage counted for this sentence' }));

    var funnel = h('div', { class: 'list' }, [
      ['raw candidates', num(m.rawCandidates, 0)],
      ['eligible (passed the hard gate)', num(m.eligibleCandidates, 0)],
      ['certified (equivalence proved)', num(m.certifiedCandidates, 0)],
      ['shortlisted', num(m.shortlistedCandidates, num(m.eligibleCandidates, 0))],
      ['selected', num(m.selected, 0) + ' of 1']
    ].map(function (r) {
      return h('div', { class: 'row' }, [
        h('div', { class: 'row-main' }, h('div', { class: 'row-main-text', text: r[0] })),
        h('div', { class: 'row-side' }, [badge('w ' + r[1])])
      ]);
    }));
    funnel.appendChild(h('p', {
      class: 'faint',
      text: 'passing the gate and proving equivalence are different claims; a candidate the ' +
        'verifier merely declined to reject is eligible, not certified. The response returns ' +
        'one candidate, so selected is at most 1 — shortlisted is how many it chose from.'
    }));
    body.appendChild(h('p', { class: 'faint', text: 'candidate funnel' }));
    body.appendChild(funnel);

    var gaps = arr(m.predicateGaps);
    if (gaps.length) {
      var byCause = {};
      gaps.forEach(function (g) {
        var k = str(g.cause, 'unknown');
        byCause[k] = (byCause[k] || 0) + 1;
      });
      var head = 'unresolved clause heads — ' + gaps.length + ', by cause: ' +
        Object.keys(byCause).sort(function (a, b) { return byCause[b] - byCause[a]; })
          .map(function (k) { return k + ' ' + byCause[k]; }).join(', ');
      body.appendChild(h('p', { class: 'faint', text: head }));
      body.appendChild(h('div', { class: 'list' }, gaps.slice(0, 12).map(function (g) {
        return h('div', { class: 'row' }, [
          h('div', { class: 'row-main' }, [
            h('div', { class: 'row-main-text', text: str(g.surface, '(no surface)') }),
            h('div', { class: 'row-meta', text: [str(g.lemma, ''), str(g.pos, ''),
              str(g.tier, ''), str(g.backend, '')].filter(Boolean).join(' · ') }),
            g.note ? h('div', { class: 'row-meta', text: g.note }) : null
          ]),
          h('div', { class: 'row-side' }, [badge('w ' + str(g.cause, ''))])
        ]);
      })));
      if (gaps.length > 12) {
        body.appendChild(h('p', { class: 'faint', text: '… and ' + (gaps.length - 12) + ' more; the JSON has all of them' }));
      }
    }

    // Why nothing was certified. A single "certified: 0" is indistinguishable
    // between a verifier that is strict and one that has no way to check what
    // it produced; the slugs say which, and the first one is the cheapest
    // capability to build.
    var blockers = arr(m.certificationBlockers);
    if (blockers.length) {
      var counts = {};
      blockers.forEach(function (b) { counts[b] = (counts[b] || 0) + 1; });
      body.appendChild(h('p', {
        class: 'faint',
        text: 'why nothing was certified: ' + Object.keys(counts)
          .sort(function (a, b) { return counts[b] - counts[a]; })
          .map(function (k) { return k + ' ×' + counts[k]; }).join(', ')
      }));
      body.appendChild(h('p', {
        class: 'faint',
        text: 'a blocker is a reason equivalence could not be PROVED, not a mistranslation'
      }));
    }

    var losses = arr(m.frameLosses);
    if (losses.length) {
      var counts = {};
      losses.forEach(function (l) { counts[l] = (counts[l] || 0) + 1; });
      body.appendChild(h('p', {
        class: 'faint',
        text: 'arguments lost: ' + Object.keys(counts).sort(function (a, b) { return counts[b] - counts[a]; })
          .map(function (k) { return k + ' ×' + counts[k]; }).join(', ')
      }));
    }

    return section('stage metrics', null, body);
  }

  function questionCard(q, ctx) {
    var opts = arr(q.options).slice().sort(function (a, b) {
      return num(b.probability, 0) - num(a.probability, 0) || (str(a.key) < str(b.key) ? -1 : 1);
    });
    var box = h('div', { class: 'q' }, [
      h('div', { class: 'q-head' }, [
        h('span', { class: 'q-title', text: str(q.question, '(question)') }),
        badge(str(q.kind, 'question')),
        q.blocking ? badge('blocking', 'warn') : null,
        str(q.default) ? badge('default ' + str(q.default), 'skip') : null
      ]),
      str(q.why) ? h('p', { class: 'q-why', text: str(q.why) }) : null,
      h('div', { class: 'opts' }, opts.length ? opts.map(function (o) {
        var key = str(o.key, str(o.label));
        var btn = h('button', {
          type: 'button', class: 'opt' + (str(q.default) === key ? ' is-default' : ''),
          title: str(o.description, 'answer with ' + key),
          onclick: function () {
            if (typeof ctx.disambiguate === 'function') ctx.disambiguate(str(q.id, ''), key);
          }
        }, [
          h('span', { class: 'opt-label', text: str(o.label, key) }),
          h('span', { class: 'bar-track' }, h('span', { class: 'bar-fill is-prob', style: 'width:' +
            (Math.max(0, Math.min(1, num(o.probability, 0))) * 100).toFixed(1) + '%' })),
          h('span', { class: 'bar-val', text: pct(o.probability) }),
          str(o.description) ? h('span', { class: 'opt-desc', text: str(o.description) }) : null
        ]);
        return btn;
      }) : [h('p', { class: 'muted-none', text: 'this question carries no options' })])
    ]);
    return box;
  }

  function whyBlock(resp) {
    var status = str((resp.result || {}).status, 'UNKNOWN');
    var result = resp.result || {};
    var why = h('div', { class: 'why' }, [
      h('h4', { text: 'no target string was produced — status ' + status }),
      h('p', {
        text: status === 'UNPARSABLE'
          ? 'The pipeline analysed the source but every construction it generated failed re-parsing or the semantic-equivalence check. That is a refusal, not a translation: an unverified string would be a hallucination, so nothing was returned.'
          : 'No candidate was selected. The evidence the pipeline recorded about the failure is below, stage by stage, so the refusal can be read rather than guessed at.'
      }),
      h('p', { class: 'faint', text: 'target side reported: ' + JSON.stringify(str((resp.target || {}).text, '')) })
    ]);

    var warn = arr(resp.warnings);
    if (warn.length) why.appendChild(h('p', { class: 'faint', text: 'warnings: ' + warn.join(' · ') }));

    var tl = collectTraceNotes(resp, 12);
    if (tl.length) why.appendChild(h('ul', null, tl.map(function (t) { return h('li', { text: t }); })));

    var cands = arr(result.candidates);
    why.appendChild(h('p', { class: 'faint', text: 'candidates reaching verification: ' + cands.length }));

    return why;
  }

  /* ================================================================== JLIR */

  function renderJLIR(ctx) {
    var resp = ctx.resp;
    if (!resp) {
      return {
        node: empty('no graph yet',
          'Translate a sentence and the 9-layer intermediate representation appears here: entity and event nodes joined by labelled role edges, any unresolved referent distribution drawn as bars, and the scope operators in their own band. Click an entity to read its features and their provenance.'),
        inspect: null
      };
    }
    var pair = resp.jlir || {};
    var side = ctx.jlirSide === 'target' ? 'target' : 'source';
    var graph = pair[side];

    var node = h('div');

    var toggle = h('div', { class: 'tag-row' }, ['source', 'target'].map(function (s) {
      var present = !!(pair[s] && (arr(pair[s].entities).length || arr(pair[s].events).length));
      return h('button', {
        type: 'button', class: 'btn btn-ghost',
        'aria-pressed': side === s ? 'true' : 'false',
        style: side === s ? 'background:#1d3f5c;border-color:#2f6d9c;color:#d7ecff' : '',
        text: s + (present ? '' : ' (absent)'),
        onclick: function () { ctx.setJlirSide(s); }
      });
    }));

    node.appendChild(h('div', { class: 'headline' }, [
      h('div', { class: 'tag-row' }, [
        badge(side + ' graph'),
        graph ? badge(str(graph.lang, '?')) : null,
        graph ? badge(arr(graph.entities).length + ' entities') : null,
        graph ? badge(arr(graph.events).length + ' events') : null,
        graph ? badge(arr(graph.scopes).length + ' scopes') : null
      ]),
      toggle
    ]));

    if (!graph) {
      node.appendChild(empty('no ' + side + ' graph in this response',
        side === 'target'
          ? 'The target side was never materialized: the pipeline stopped before a target graph existed, or the re-parser produced nothing that could be lifted into JLIR. The source graph is still worth reading — switch to source.'
          : 'The response carried no source graph. The Morph and Syntax tabs will show what analysis did run.'));
      if (side === 'target' && pair.source) {
        node.appendChild(h('button', {
          type: 'button', class: 'btn', text: 'show the source graph instead',
          onclick: function () { ctx.setJlirSide('source'); }
        }));
      }
      return { node: node, inspect: pair[side] || null };
    }

    var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'graph');
    node.appendChild(h('div', { class: 'graph-wrap' }, svg));

    var draw = function () {
      try {
        global.JEVGraph.mountJLIR(svg, graph, {
          onSelectEntity: function (id, ent) { ctx.selectEntity(side, id, ent); }
        });
      } catch (err) {
        svg.replaceWith(h('div', { class: 'empty' }, [
          h('p', { class: 'empty-title', text: 'the graph could not be laid out' }),
          h('p', { class: 'empty-body', text: String(err && err.message ? err.message : err) })
        ]));
      }
    };
    ctx.afterRender(draw);

    // Entity drill-down: plan.md §60, features with their provenance.
    var sel = ctx.entitySelection;
    if (sel && sel.side === side) {
      var ent = findEntity(graph, sel.id);
      node.appendChild(ent ? entityDrawer(ent, sel.id, ctx) : empty('entity ' + sel.id + ' is gone',
        'It was present in an earlier render of this response. Translate again to rebuild the graph.'));
    }

    var gn = notes(graph.notes);
    var metas = kv([
      ['language', str(graph.lang, '?')],
      ['source text', str(graph.source, '—')],
      ['clause order', arr(graph.clauseOrder).join(' → ') || '—'],
      ['weight', num(graph.weight, 0).toFixed(4)],
      ['structural', graph.structural ? 'yes (intermediate artifact)' : 'no (final interpretation)']
    ]);
    node.appendChild(section('graph metadata', null, [metas, gn]));

    var info = graph.info || {};
    var topicIds = arr(info.topic), focusIds = arr(info.focus);
    if (topicIds.length || focusIds.length) {
      node.appendChild(section('information structure', null, kv([
        ['topic', topicIds.join(', ') || '—'],
        ['focus', focusIds.join(', ') || '—'],
        ['marker', str(info.marker, '—')]
      ])));
    }

    var preds = arr(graph.predicates);
    if (preds.length) {
      node.appendChild(section('predicate frames', preds.length + ' sense(s)', h('div', { class: 'list' },
        preds.map(function (p) {
          var args = arr(p.args).map(function (a) {
            return str(a.role, '?') + (a.required ? '*' : '');
          }).sort();
          return h('div', { class: 'card' }, [
            h('div', { class: 'mono' }, [h('strong', { text: str(p.id, '?') }), ' ', h('span', { class: 'faint', text: str(p.gloss, '') })]),
            h('div', { class: 'faint', text: 'arguments: ' + (args.join(', ') || 'none') }),
            kv(Object.keys(p.features || {}).sort().map(function (k) { return [k, str(p.features[k])]; }))
          ]);
        }))));
    }

    return { node: node, inspect: graph };
  }

  function findEntity(graph, id) {
    var found = null;
    arr(graph && graph.entities).forEach(function (e) {
      if (!found && e && str(e.id) === id) found = e;
    });
    return found;
  }

  function entityDrawer(e, id, ctx) {
    var drawer = h('div', { class: 'drawer' }, [
      h('div', { class: 'drawer-head' }, [
        h('span', { class: 'drawer-title', text: id + ' — ' + firstSurface(e) }),
        h('button', { type: 'button', class: 'drawer-close', text: '×', title: 'close', onclick: function () { ctx.clearEntity(); } })
      ])
    ]);

    drawer.appendChild(kv([
      ['identity', str(e.identity, '—')],
      ['type', str(e.type, 'UNKNOWN')],
      ['proper', e.proper ? 'yes' : 'no'],
      ['number', str(e.number, 'UNKNOWN')],
      ['gender', str(e.gender, 'UNKNOWN')],
      ['animacy', str(e.animacy, 'UNKNOWN')],
      ['person', e.person === undefined ? '—' : String(e.person)],
      ['plural', e.plural ? 'yes' : 'no'],
      ['zero (unrealized mention)', e.zero ? 'yes' : 'no'],
      ['mentions', String(num(e.mentionCount, 0))],
      ['salience', num(e.salience, 0).toFixed(3)]
    ]));

    var ref = e.referent;
    if (ref && typeof ref === 'object') {
      var opts = (global.JEVGraph ? global.JEVGraph.topOptions(ref, 8) : []);
      drawer.appendChild(h('div', { class: 'sec' }, [
        h('div', { class: 'sec-head' }, h('h3', {
          text: 'referent distribution — ' + (ref.resolved ? 'RESOLVED to ' + str(ref.winner, '?') : 'UNRESOLVED')
        })),
        h('div', { class: 'sec-body' }, [
          kv([['entropy', num(ref.entropy, 0).toFixed(3)], ['provenance', str(ref.provenance, '—')]]),
          h('div', { class: 'list', style: 'margin-top:6px' }, opts.map(function (o) {
            return bar(o.option, o.p, 1, 'prob');
          }))
        ])
      ]));
    }

    var feats = arr(e.features);
    drawer.appendChild(h('div', { class: 'sec' }, [
      h('div', { class: 'sec-head' }, h('h3', { text: 'features and their provenance — ' + feats.length })),
      h('div', { class: 'sec-body' }, feats.length
        ? h('div', null, feats.map(function (f) {
            if (!f || typeof f !== 'object') return null;
            return h('div', { class: 'prov' }, [
              h('div', { class: 'prov-head' }, [
                badge(str(f.key, '?'), 'good'),
                h('span', { class: 'mono', text: JSON.stringify(f.value === undefined ? null : f.value) }),
                badge('confidence ' + num(f.confidence, 0).toFixed(2), 'skip')
              ]),
              provenanceList(f.provenance || f.prov)
            ]);
          }))
        : h('p', { class: 'muted-none', text: 'no per-feature record was attached to this entity; its provenance list below is all the evidence there is.' }))
    ]));

    var provs = arr(e.provenance || e.prov);
    drawer.appendChild(h('div', { class: 'sec' }, [
      h('div', { class: 'sec-head' }, h('h3', { text: 'entity provenance — ' + provs.length })),
      h('div', { class: 'sec-body' }, provs.length
        ? provenanceList(provs)
        : h('p', { class: 'muted-none', text: 'none recorded' }))
    ]));

    return drawer;
  }

  function firstSurface(e) {
    var al = arr(e && e.aliases);
    for (var i = 0; i < al.length; i++) if (al[i] && al[i].surface) return al[i].surface;
    return str(e && e.identity, '?');
  }

  function provenanceList(list) {
    var items = arr(list).filter(function (p) { return p && typeof p === 'object'; });
    if (!items.length) return h('p', { class: 'muted-none', text: 'no provenance recorded for this item' });
    return h('div', null, items.map(function (p) {
      var span = (p.span && typeof p.span === 'object') ? (num(p.span.start, 0) + '–' + num(p.span.end, 0)) : '';
      return h('div', { class: 'prov' }, [
        h('div', { class: 'prov-head' }, [
          badge(str(p.origin, 'unknown'), p.origin === 'lexical' ? 'good' : (p.origin === 'user' ? 'cache' : 'skip')),
          str(p.token) ? h('span', { class: 'mono', text: str(p.token) }) : null,
          span ? badge('bytes ' + span, 'skip') : null,
          badge('conf ' + num(p.confidence, 0).toFixed(2), 'skip'),
          str(p.decision) ? badge('decision ' + str(p.decision), 'jev') : null
        ]),
        str(p.note) ? h('p', { class: 'prov-note', text: str(p.note) }) : null
      ]);
    }));
  }

  /* ================================================================= MORPH */

  function renderMorph(ctx) {
    var resp = ctx.resp;
    if (!resp) {
      return {
        node: empty('no lattice yet',
          'The morphological lattice is the first analysis the pipeline runs: every competing segmentation of the source, kept rather than collapsed. It would appear here as rows of token cells coloured by part of speech, with any morpheme that had to be reconstructed marked as such.'),
        inspect: null
      };
    }
    var mf = (resp.artifacts || {}).morph;
    if (!mf || typeof mf !== 'object') {
      return {
        node: empty('this response carried no morphological artifact',
          'The stage either did not run or attached nothing. The circuit on the left says which: a node with no span is a stage that did not run.'),
        inspect: null
      };
    }

    var paths = arr(mf.paths).filter(function (p) { return p && typeof p === 'object'; })
      .slice().sort(function (a, b) { return num(b.weight, 0) - num(a.weight, 0); });
    var node = h('div');

    node.appendChild(section('lattice', paths.length + ' competing segmentation(s)', [
      kv([
        ['language', str(mf.lang, '?')],
        ['source', str(mf.source, '—')],
        ['dictionary size', String(num(mf.dictionarySize, 0))],
        ['unknown rate', pct(mf.unknownRate)]
      ]),
      bar('unknown rate', mf.unknownRate, 1, 'loss'),
      notes(mf.notes)
    ]));

    if (!paths.length) {
      node.appendChild(empty('the lattice is empty', 'No segmentation survived to be recorded. ' + (str(mf.source) ? 'source was ' + JSON.stringify(str(mf.source)) + '.' : '')));
      return { node: node, inspect: mf };
    }

    paths.forEach(function (p, i) {
      var morphs = arr(p.morphs);
      var kids = [h('div', { class: 'lattice' }, morphs.length ? morphs.map(tokenCell) :
        [h('p', { class: 'muted-none', text: 'this path contains no morphemes' })])];
      kids.push(h('div', { class: 'row-meta', style: 'margin-top:6px' },
        'rule: ' + str(p.rule, 'unspecified') + ' · weight ' + num(p.weight, 0).toFixed(4) +
        ' · ' + morphs.length + ' morphemes' +
        ' · reconstructed: ' + morphs.filter(isReconstructed).length));

      if (i === 0) {
        node.appendChild(section('winning segmentation', null, kids));
      } else {
        node.appendChild(details('alternative segmentation — weight ' + num(p.weight, 0).toFixed(4) +
          ', rule ' + str(p.rule, '?'), h('div', null, kids)));
      }
    });

    var unknowns = arr((resp.artifacts || {}).syntax && (resp.artifacts || {}).syntax.unknowns);
    if (unknowns.length) {
      node.appendChild(section('reconstructed morphemes', unknowns.length, tags(unknowns, 'warn')));
    }

    return { node: node, inspect: mf };
  }

  function isReconstructed(m) {
    return !!(m && (m.unknown === true || m.dict !== true));
  }

  function whyReconstructed(m) {
    return m.dict === true
      ? 'RECONSTRUCTED: the surface form was inflected away from a dictionary head'
      : 'RECONSTRUCTED: no dictionary entry, carried as UNKNOWN rather than guessed';
  }

  function posClass(pos) {
    var p = str(pos, 'DEFAULT').toUpperCase();
    return 'pos-' + (/^[A-Z]+$/.test(p) ? p : 'DEFAULT');
  }

  function tokenCell(m) {
    if (!m || typeof m !== 'object') return h('div', { class: 'tok pos-DEFAULT' }, h('div', { class: 'tok-surface', text: '(malformed)' }));
    var surface = str(m.surface, '?');
    var base = str(m.base, ''), lem = str(m.lemma, '');
    var sub = [];
    if (base && base !== surface) sub.push('base ' + base);
    if (lem && lem !== base && lem !== surface) sub.push('lemma ' + lem);
    if (!sub.length) sub.push(str(m.lemma, str(m.base, surface)));
    var feats = (m.features && typeof m.features === 'object') ? m.features : {};
    var featStr = Object.keys(feats).sort().filter(function (k) { return k !== 'rule'; })
      .map(function (k) { return k + '=' + String(feats[k]); }).join(' ');
    var recon = isReconstructed(m);
    var cell = h('div', {
      class: 'tok ' + posClass(m.pos) + (recon ? ' is-reconstructed' : ''),
      title: str(m.id, '') + ' ' + surface + '\npos ' + str(m.pos, '?') +
        '\nbytes ' + num(m.start, 0) + '–' + num(m.end, 0) +
        (recon ? '\n' + whyReconstructed(m) : '\ndictionary entry')
    }, [
      h('div', { class: 'tok-surface', text: surface }),
      h('div', { class: 'tok-sub', text: sub.join(' · ') }),
      h('div', { class: 'tok-pos', text: (recon ? '⚠ recon · ' : '') + str(m.pos, '?') }),
      featStr ? h('div', { class: 'tok-feats', text: featStr }) : null
    ]);
    return cell;
  }

  /* ================================================================ SYNTAX */

  function renderSyntax(ctx) {
    var resp = ctx.resp;
    if (!resp) {
      return {
        node: empty('no parse yet',
          'The winning clause parse would appear here: the matrix predicate with its tense, politeness and mood, every case-marked argument, topic shown distinctly from subject, and a zero argument written literally as (zero).'),
        inspect: null
      };
    }
    var bundle = (resp.artifacts || {}).syntax;
    if (!bundle || typeof bundle !== 'object') {
      return { node: empty('this response carried no syntax artifact', 'The parse stage attached nothing to the trace.'), inspect: null };
    }

    var morphs = (bundle.morphs && typeof bundle.morphs === 'object') ? bundle.morphs : {};
    function surfaceOf(id) {
      var m = morphs[id];
      return m && typeof m === 'object' ? str(m.surface, str(id, '')) : str(id, '');
    }

    var clauses = arr(bundle.clauses).filter(function (c) { return c && typeof c === 'object'; });
    var node = h('div');

    node.appendChild(section('bundle', null, [
      kv([
        ['language', str(bundle.lang, '?')],
        ['coverage', pct(bundle.coverage)],
        ['grammar tier', 'tier ' + (typeof bundle.tier === 'number' ? String(bundle.tier) : str(bundle.tier, '?')) +
          (num(bundle.tier, 0) >= 2 ? ' (robust fallback: some structure is underdetermined)' : ' (precision grammar)')],
        ['clauses', String(clauses.length)],
        ['reconstructed morphemes', arr(bundle.unknowns).join(', ') || 'none']
      ]),
      bar('coverage', bundle.coverage, 1, 'conf'),
      notes(bundle.notes)
    ]));

    if (!clauses.length) {
      node.appendChild(empty('no clause was produced', 'The parse stage recorded a bundle with an empty clause list. That is a real outcome, not a display problem: the circuit shows whether the stage ran.'));
      return { node: node, inspect: bundle };
    }

    clauses.slice().sort(function (a, b) { return num(a.index, 0) - num(b.index, 0); })
      .forEach(function (c, i) {
        node.appendChild(section('clause ' + str(c.id, '#' + i) + (i === 0 ? ' (matrix)' : ''),
          str(c.conjoin) ? 'conjoined by ' + str(c.conjoin) : null,
          clauseBlock(c, surfaceOf)));
      });

    var forest = bundle.forest;
    if (forest && typeof forest === 'object' && arr(forest.nodes).length) {
      var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.setAttribute('class', 'graph');
      node.appendChild(section('packed syntactic forest',
        arr(forest.nodes).length + ' node(s), ' + num(forest.shared, 0) + ' shared',
        h('div', { class: 'graph-wrap' }, svg)));
      ctx.afterRender(function () {
        try { global.JEVGraph.mountForest(svg, forest, {}); }
        catch (err) { /* the clause tree above already carries the parse */ }
      });
    }

    return { node: node, inspect: bundle };
  }

  function clauseBlock(c, surfaceOf) {
    var args = h('div', { class: 'args' });

    function argRow(role, ph, cls, extra) {
      if (!ph || typeof ph !== 'object') return;
      if (ph.zero) {
        args.appendChild(h('div', { class: 'arg is-zero' }, [
          h('span', { class: 'arg-role', text: role }),
          h('span', { class: 'arg-val', text: '(zero)' })
        ]));
        return;
      }
      var head = ph.head ? surfaceOf(ph.head) : '';
      var mods = arr(ph.modifiers).map(surfaceOf).filter(Boolean);
      var bits = [];
      if (ph.case) bits.push(str(ph.case));
      if (ph.pronoun) bits.push('pronoun');
      if (ph.demonstrative) bits.push(str(ph.demonstrative));
      if (ph.numeral) bits.push(str(ph.numeral));
      if (ph.honorific) bits.push(str(ph.honorific));
      if (!ph.marked) bits.push('unmarked');
      var txt = (head || '(empty)');
      if (mods.length) txt = mods.join(' ') + ' ' + txt;
      args.appendChild(h('div', { class: 'arg ' + (cls || '') }, [
        h('span', { class: 'arg-role', text: role }),
        h('span', { class: 'arg-val', title: extra || '' }, [
          txt,
          bits.length ? h('span', { class: 'faint', text: '  [' + bits.join(', ') + ']' }) : null,
          ph.probability !== undefined && num(ph.probability, 1) < 1
            ? h('span', { class: 'faint', text: '  p=' + num(ph.probability, 0).toFixed(2) }) : null
        ])
      ]));
    }

    argRow('topic', c.topic, 'is-topic', 'は marks a topic, not a subject');
    argRow('subject', c.subject, 'is-subject');
    argRow('object', c.object, '');
    var indirect = (c.indirect && typeof c.indirect === 'object') ? c.indirect : {};
    Object.keys(indirect).sort().forEach(function (k) { argRow(k, indirect[k], ''); });

    arr(c.adnominals).forEach(function (a) {
      args.appendChild(h('div', { class: 'arg' }, [
        h('span', { class: 'arg-role', text: 'adnominal' }),
        h('span', { class: 'arg-val', text: surfaceOf(a) + '  [の]' })
      ]));
    });
    arr(c.adverbs).forEach(function (a) {
      args.appendChild(h('div', { class: 'arg' }, [
        h('span', { class: 'arg-role', text: 'adverb' }),
        h('span', { class: 'arg-val', text: str(a) })
      ]));
    });
    arr(c.modifiers).forEach(function (a) {
      args.appendChild(h('div', { class: 'arg' }, [
        h('span', { class: 'arg-role', text: 'modifier' }),
        h('span', { class: 'arg-val', text: str(a) })
      ]));
    });

    var morph = kv([
      ['tense', str(c.tense, '—')],
      ['aspect', str(c.aspect, '—')],
      ['completion', str(c.completion, '—')],
      ['polarity', str(c.polarity, '—')],
      ['politeness', str(c.politeness, '—')],
      ['mood', str(c.mood, '—')],
      ['modality', str(c.modality, '—')],
      ['honorific', c.honorific ? 'yes' : 'no'],
      ['negation', c.negation ? surfaceOf(c.negation) : 'none'],
      ['auxiliaries', arr(c.auxiliaries).map(surfaceOf).join(' ') || 'none'],
      ['probability', num(c.probability, 0).toFixed(3)]
    ]);

    return h('div', { class: 'clause' + (num(c.index, 0) === 0 ? ' is-matrix' : '') }, [
      h('div', { class: 'clause-head' }, [
        h('span', { class: 'clause-pred', text: str(c.matrix) ? surfaceOf(c.matrix) : '(no matrix predicate)' }),
        c.index !== undefined && c.index !== null ? badge('clause index ' + num(c.index, 0)) : null,
        str(c.conjoin) ? badge(str(c.conjoin), 'warn') : null
      ]),
      args,
      h('div', { style: 'margin-top:8px' }, morph),
      notes(c.notes)
    ]);
  }

  /* ================================================================ FOREST */

  function renderForest(ctx) {
    var resp = ctx.resp;
    if (!resp) {
      return {
        node: empty('no forest yet',
          'After the target projection, every admissible realization is kept as a packed forest rather than generated directly. That forest, and the hard constraints that deleted branches from it, would appear here.'),
        inspect: null
      };
    }
    var art = resp.artifacts || {};
    var node = h('div');

    var real = art.realization;
    if (!real || typeof real !== 'object') {
      node.appendChild(empty('no realization forest in this response',
        'The realization stage attached nothing. If it did run, the circuit node for it will be lit; if not, the forest never existed.'));
    } else {
      var forest = real.forest || null;
      var nodes = arr(forest && forest.nodes);
      var meta = h('div', { class: 'kv-wrap' }, kv([
        ['nodes', String(nodes.length)],
        ['shared nodes', String(num(forest && forest.shared, 0))],
        ['root', str(forest && forest.root, '—')],
        ['slots', (real.slots && typeof real.slots === 'object')
          ? Object.keys(real.slots).sort().map(function (k) { return k + '=' + String(real.slots[k]); }).join(', ')
          : '—']
      ]));

      var kids = [meta];
      if (nodes.length) {
        var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('class', 'graph');
        kids.push(h('div', { class: 'graph-wrap', style: 'margin-top:8px' }, svg));
        ctx.afterRender(function () {
          try { global.JEVGraph.mountForest(svg, forest, {}); }
          catch (err) { /* the rejected list below is still readable */ }
        });
      } else {
        kids.push(h('p', { class: 'muted-none', text: 'the forest has no nodes: every branch was rejected before realization.' }));
      }
      var rnotes = notes(real.notes);
      if (rnotes) kids.push(rnotes);

      node.appendChild(section('packed realization forest', nodes.length + ' node(s)', kids));

      var rej = arr(real.rejected);
      node.appendChild(section('branches that never existed', rej.length + ' rejected by hard or soft constraint',
        rej.length ? h('div', { class: 'list' }, rej.map(function (r) {
          return h('div', { class: 'rejected' }, [
            h('div', {}, [
              h('span', { class: 'r-rule', text: str(r.rule, 'UNKNOWN RULE') }),
              ' ',
              h('span', { class: 'faint', text: str(r.lex, '(no lexeme)') })
            ]),
            h('div', { class: 'r-reason', text: str(r.reason, 'no reason recorded') }),
            kv([
              ['slot', str(r.slot, '—')],
              ['stage', str(r.stage, '—')],
              ['kind', /^HARD\./.test(str(r.rule)) ? 'hard constraint — the branch was illegal'
                : (/^SOFT\./.test(str(r.rule)) ? 'soft constraint — the branch ranked lower'
                : 'rule class not declared')]
            ])
          ]);
        })) : h('p', { class: 'muted-none', text: 'nothing was rejected: the candidate set survived intact.' })));
    }

    var sf = art.semanticForest;
    if (!sf || typeof sf !== 'object') {
      node.appendChild(empty('no source semantic forest', 'The source reading stage attached nothing.'));
    } else {
      var readings = arr(sf.readings);
      node.appendChild(section('source readings', readings.length + ' reading(s) kept open',
        readings.length ? readings.slice().sort(function (a, b) { return num(b.weight, 0) - num(a.weight, 0); })
          .map(function (r, i) {
            var g = (r && r.graph) || null;
            var ents = arr(g && g.entities), evs = arr(g && g.events);
            var body = h('div', null, [
              kv([
                ['weight', num(r.weight, 0).toFixed(4)],
                ['origin', str(r.origin, '—')],
                ['entities', String(ents.length)],
                ['events', evs.map(function (e) { return str(e && e.predicate, '?'); }).join(', ') || 'none']
              ]),
              bar('reading weight', r.weight, 1, 'prob')
            ]);
            if (g) {
              var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
              svg.setAttribute('class', 'graph');
              body.appendChild(h('div', { class: 'graph-wrap', style: 'margin-top:8px' }, svg));
              ctx.afterRender(function () {
                try { global.JEVGraph.mountJLIR(svg, g, {}); } catch (err) { /* summary above suffices */ }
              });
            }
            return h('details', { class: 'json-inspect', open: i === 0 ? true : null }, [
              h('summary', { text: 'reading ' + num(r.weight, 0).toFixed(4) + ' — ' + str(r.origin, '?') +
                ' — ' + str(evs[0] && evs[0].predicate, 'no predicate') }),
              body
            ]);
          }) : h('p', { class: 'muted-none', text: 'the forest is empty: the source was read a single way.' })));
    }

    return { node: node, inspect: { realization: art.realization || null, semanticForest: art.semanticForest || null } };
  }

  /* ============================================================= DECISIONS */

  function renderDecisions(ctx) {
    var resp = ctx.resp;
    if (!resp) {
      return {
        node: empty('no decisions yet',
          'Every ambiguity the pipeline meets is turned into a decision question with a bounded option set. This tab is the audit trail: the question, the options, their probabilities, the winner, the confidence and whether the answer came from the oracle, the cache or a prior.'),
        inspect: null
      };
    }
    var list = arr((resp.artifacts || {}).decisions);
    if (!list.length) {
      return {
        node: empty('this translation raised no decision',
          'The audit trail is empty: either every ambiguity was resolved deterministically upstream, or the stage did not run. The circuit node for JEV DECISION GRAPH says which.'),
        inspect: null
      };
    }

    var node = h('div');
    var sorted = list.slice().sort(function (a, b) {
      return str(a.stage).localeCompare(str(b.stage)) || str(a.id).localeCompare(str(b.id)) ||
        str(a.question).localeCompare(str(b.question));
    });

    var counts = {};
    sorted.forEach(function (d) { counts[str(d.source, 'unknown')] = (counts[str(d.source, 'unknown')] || 0) + 1; });

    node.appendChild(section('audit trail', sorted.length + ' decision(s)', [
      h('div', { class: 'tag-row' }, Object.keys(counts).sort().map(function (k) {
        return badge(k + ' × ' + counts[k], SOURCE_KIND[k] || null);
      })),
      h('p', { class: 'faint', style: 'margin:6px 0 0' },
        'D0–D9 run in stage order. A decision is a bounded question, never a generation request.')
    ]));

    sorted.forEach(function (d, i) {
      node.appendChild(decisionCard(d, i));
    });

    return { node: node, inspect: (resp.artifacts || {}).decisions };
  }

  function decisionCard(d, i) {
    if (!d || typeof d !== 'object') return h('div', { class: 'dec' }, h('p', { class: 'muted-none', text: 'malformed decision record #' + i }));
    var probs = (d.probabilities && typeof d.probabilities === 'object') ? d.probabilities : {};
    var keys = Object.keys(probs);
    keys.sort(function (a, b) {
      return num(probs[b], 0) - num(probs[a], 0) || (a < b ? -1 : 1);
    });
    if (!keys.length) keys = arr(d.options).slice().sort();

    var src = str(d.source, 'unknown');

    return h('div', { class: 'dec' }, [
      h('div', { class: 'dec-head' }, [
        h('span', { class: 'dec-stage', text: str(d.stage, 'D?') }),
        badge(str(d.kind, 'kind')),
        badge(src.toUpperCase(), SOURCE_KIND[src] || null),
        d.cacheHit ? badge('cache hit', 'cache') : null,
        d.skipped ? badge('skipped', 'skipped') : null,
        h('span', { class: 'faint', text: 'id ' + str(d.id, '—') })
      ]),
      h('p', { class: 'dec-q', text: str(d.question, '(no question recorded)') }),
      h('div', { class: 'dec-opts' }, keys.length ? keys.map(function (k) {
        var isWinner = str(d.winner) === k;
        var row = bar(k, num(probs[k], 0), 1, 'prob');
        if (isWinner) {
          row.querySelector('.bar-label').textContent = '★ ' + row.querySelector('.bar-label').textContent;
          row.querySelector('.bar-fill').style.background = 'linear-gradient(90deg,#1f4d33,#4ec98a)';
        }
        return row;
      }) : [h('p', { class: 'muted-none', text: 'no option probabilities were recorded' })]),
      h('div', { class: 'dec-foot' }, [
        h('span', { text: 'winner: ' + (str(d.winner) === '' ? '(none)' : str(d.winner)) }),
        h('span', { text: 'confidence: ' + num(d.confidence, 0).toFixed(3) }),
        h('span', { text: 'latency: ' + num(d.latencyMs, 0).toFixed(1) + ' ms' }),
        d.noul !== null && d.noul !== undefined ? h('span', { text: 'NOUL: ' + num(d.noul, 0).toFixed(3) }) : null,
        d.score !== null && d.score !== undefined ? h('span', { text: 'score: ' + num(d.score, 0).toFixed(3) }) : null,
        h('span', { text: 'options offered: ' + arr(d.options).length })
      ]),
      (d.skipped && str(d.skipReason)) ? h('p', { class: 'dec-skip', text: 'not asked: ' + str(d.skipReason) }) : null
    ]);
  }

  /* =============================================================== DOCUMENT */

  function renderDocument(ctx) {
    var resp = ctx.resp;
    if (!resp) {
      return {
        node: empty('no document state yet',
          'The discourse store is carried across sentences: which entities exist, how salient they are, what they are called in each language, what the current topic and focus are, and which events have happened when. It appears here after a translation.'),
        inspect: null
      };
    }
    var ds = resp.documentState;
    if (!ds || typeof ds !== 'object') {
      return { node: empty('this response carried no document state', 'The discourse stage did not attach a snapshot.'), inspect: null };
    }

    var ents = arr(ds.entities).filter(function (e) { return e && typeof e === 'object'; });
    var node = h('div');

    node.appendChild(section('document', null, kv([
      ['version', String(num(ds.version, 0))],
      ['sentences folded in', String(num(ds.sentences, 0))],
      ['entities', String(ents.length)],
      ['topics', arr(ds.topicStack).join(' → ') || '—'],
      ['focus', arr(ds.focusStack).join(' → ') || '—']
    ])));

    var style = ds.style || {};
    node.appendChild(section('discourse style', 'observed from the source so far', [
      kv(Object.keys(style).sort().map(function (k) {
        var v = style[k];
        return [k, typeof v === 'number' ? num(v, 0).toFixed(2) : str(v, '—')];
      }))
    ]));

    node.appendChild(section('entities', ents.length, ents.length ? h('div', { class: 'list' },
      ents.map(function (e) {
        var aliases = arr(e.aliases);
        var chain = aliases.map(function (a) {
          if (!a) return null;
          return str(a.surface, '?') + ' [' + str(a.kind, 'alias') + '×' + num(a.mention, 1) + ', ' + str(a.lang, '?') + ']';
        }).filter(Boolean);
        var forms = (e.forms && typeof e.forms === 'object')
          ? Object.keys(e.forms).sort().map(function (k) { return k + ': ' + str(e.forms[k]); }).join(' · ')
          : '';
        return h('div', { class: 'card' }, [
          h('div', { class: 'dec-head' }, [
            h('span', { class: 'dec-stage', text: str(e.id, '?') }),
            badge(str(e.canonical, str(e.identity, '(no canonical form)'))),
            e.proper ? badge('proper', 'good') : null,
            badge('type ' + str(e.type, 'UNKNOWN'), 'skip'),
            badge('gender ' + str(e.gender, 'UNKNOWN'), 'skip'),
            badge('number ' + str(e.number, 'UNKNOWN'), 'skip')
          ]),
          bar('salience', e.salience, 1, 'sal'),
          kv([
            ['salience', num(e.salience, 0).toFixed(4)],
            ['mentions', String(num(e.mentionCount, 0))],
            ['first / last mention', String(num(e.firstMention, 0)) + ' / ' + String(num(e.lastMention, 0))],
            ['forms', forms || '—'],
            ['alias chain', chain.join('  →  ') || 'none']
          ])
        ]);
      })) : h('p', { class: 'muted-none', text: 'no entities have been introduced yet.' })));

    var timeline = arr(ds.timeline);
    node.appendChild(section('timeline', timeline.length + ' event(s)', timeline.length
      ? h('div', { class: 'list' }, timeline.map(function (t) {
          return h('div', { class: 'row' }, [
            h('div', { class: 'row-main' }, [
              h('div', { class: 'row-main-text', text: str(t.predicate, '?') }),
              h('div', { class: 'row-meta', text: 'sentence ' + num(t.sentence, 0) + ' clause ' + num(t.clause, 0) +
                ' · subject ' + str(t.subjectText, str(t.subject, '—')) +
                ' · ' + str(t.tense, '?') + ' / ' + str(t.polarity, '?') })
            ]),
            h('div', { class: 'row-side' }, badge('w ' + num(t.weight, 0).toFixed(2)))
          ]);
        }))
      : h('p', { class: 'muted-none', text: 'the timeline is empty: no event has been placed yet.' })));

    var terms = arr(ds.terms);
    if (terms.length) {
      node.appendChild(section('terminology', terms.length + ' term(s)', h('div', { class: 'list' },
        terms.map(function (t) {
          return h('div', { class: 'card' }, [h('div', { class: 'mono', text: str(t.source, '?') }),
            h('div', { class: 'faint', text: str(t.target, str(t.note, '')) })]);
        }))));
    }

    return { node: node, inspect: ds };
  }

  /* ================================================================ exports */

  var PANELS = {
    output: { title: 'Output', render: renderOutput },
    jlir: { title: 'JLIR', render: renderJLIR },
    morph: { title: 'Morph', render: renderMorph },
    syntax: { title: 'Syntax', render: renderSyntax },
    forest: { title: 'Forest', render: renderForest },
    decisions: { title: 'Decisions', render: renderDecisions },
    document: { title: 'Document', render: renderDocument }
  };

  global.JEVPanels = {
    STAGES: STAGES,
    PANELS: PANELS,
    indexTrace: indexTrace,
    collectTraceNotes: collectTraceNotes,
    statusBadge: statusBadge,
    lossTotal: lossTotal,
    badge: badge,
    empty: empty,
    section: section,
    kv: kv,
    notes: notes,
    bar: bar,
    h: h
  };
})(window);