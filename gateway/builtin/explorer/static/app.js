// sov explorer — vanilla JS, no deps. Compact IDE UI.
//
// Reads /rpc/_explorer/api.json (the gateway's full IntrospectReport) and
// renders a two-pane browser: a dense router/type sidebar and a detail pane
// with a side-by-side "try it" (request editor | live response). A Cmd/Ctrl-K
// command palette jumps to any method or type. Nothing leaves the page — the
// catalog and every request go straight to this gateway.

let state = {
  catalog: null,
  tab: 'methods',
  selectedMethod: null,
  selectedType: null,
  typeFilter: '',
  methodFilter: '',
  showInternal: false,
};

let paletteIndex = [];
let palette = { open: false, results: [], active: 0 };

const $ = sel => document.querySelector(sel);
const el = (tag, cls) => { const e = document.createElement(tag); if (cls) e.className = cls; return e; };

async function loadCatalog() {
  // The internal variant returns soft-hidden methods (flagged internal); the
  // framework auth/authz hooks are never in either payload.
  const url = state.showInternal
    ? '/rpc/_explorer/api-internal.json'
    : '/rpc/_explorer/api.json';
  const resp = await fetch(url);
  state.catalog = await resp.json();
  paletteIndex = buildPaletteIndex();
  const badge = $('#drift-badge');
  const drift = state.catalog.cross_refs ? Object.keys(state.catalog.cross_refs).length : 0;
  if (drift > 0) {
    badge.hidden = false;
    badge.textContent = `${drift} drift`;
    badge.title = 'jump to drifted types';
    badge.style.cursor = 'pointer';
    badge.onclick = () => {
      const first = Object.keys(state.catalog.cross_refs || {}).sort()[0];
      if (first) { state.tab = 'types'; state.selectedType = first; setTab('types'); route(); }
    };
  } else {
    badge.hidden = true;
  }
  route();
}

function route() {
  if (!state.catalog) { renderSidebar(); renderEmpty(); return; }
  if (state.tab === 'methods') {
    const sel = resolveMethod(state.selectedMethod) || firstMethod();
    state.selectedMethod = sel ? { router: sel.rd.router, method: sel.md.method } : null;
    renderSidebar();
    if (sel) renderMethodDetail(sel.rd, sel.md); else renderEmpty();
  } else {
    const name = (state.selectedType && state.catalog.types && state.catalog.types[state.selectedType])
      ? state.selectedType : firstType();
    state.selectedType = name;
    renderSidebar();
    if (name) renderTypeDetail(name); else renderEmpty();
  }
}

function firstMethod() {
  for (const svc of Object.keys(state.catalog.services || {}).sort())
    for (const rd of state.catalog.services[svc] || [])
      if ((rd.methods || []).length) return { rd, md: rd.methods[0] };
  return null;
}

function resolveMethod(sel) {
  if (!sel) return null;
  for (const svc of Object.keys(state.catalog.services || {}))
    for (const rd of state.catalog.services[svc] || []) {
      if (rd.router !== sel.router) continue;
      for (const md of rd.methods || [])
        if (md.method === sel.method) return { rd, md };
    }
  return null;
}

function firstType() {
  const names = Object.keys(state.catalog.types || {}).sort();
  return names.filter(n => !n.endsWith('Params'))[0] || names[0] || null;
}

function setTab(tab) {
  document.querySelectorAll('header nav button').forEach(b => b.classList.toggle('active', b.dataset.tab === tab));
}

document.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('header nav button').forEach(btn => {
    btn.addEventListener('click', () => { state.tab = btn.dataset.tab; setTab(state.tab); route(); });
  });
  const toggle = $('#show-internal');
  if (toggle) toggle.addEventListener('change', () => { state.showInternal = toggle.checked; loadCatalog(); });
  initTheme();
  bindKeys();
  loadCatalog();
});

/* ---- theme ------------------------------------------------------------ */
function initTheme() {
  const saved = localStorage.getItem('sov-explorer-theme');
  if (saved) document.documentElement.setAttribute('data-theme', saved);
  const btn = $('#theme-toggle');
  if (!btn) return;
  btn.addEventListener('click', () => {
    const cur = document.documentElement.getAttribute('data-theme')
      || (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
    const next = cur === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    localStorage.setItem('sov-explorer-theme', next);
  });
}

/* ---- sidebar ---------------------------------------------------------- */
function renderSidebar() {
  const sb = $('#sidebar');
  sb.innerHTML = '';
  if (!state.catalog) return;

  const mkSearch = (placeholder, value, onInput) => {
    const search = el('input', 'sb-search');
    search.type = 'text';
    search.placeholder = placeholder;
    search.value = value;
    search.spellcheck = false;
    search.addEventListener('input', () => {
      onInput(search.value);
      const fresh = sb.querySelector('.sb-search');
      if (fresh) { fresh.focus(); fresh.setSelectionRange(fresh.value.length, fresh.value.length); }
    });
    sb.appendChild(search);
  };

  if (state.tab === 'methods') {
    mkSearch('filter methods…', state.methodFilter, v => { state.methodFilter = v; renderMethodList(sb); });
    renderMethodList(sb);
  } else {
    const drift = state.catalog.cross_refs || {};
    mkSearch('filter types…', state.typeFilter, v => { state.typeFilter = v; renderTypeList(sb, drift); });
    renderTypeList(sb, drift);
  }
}

function renderTypeList(sb, drift) {
  sb.querySelectorAll('.type-group').forEach(e => e.remove());
  const q = state.typeFilter.trim().toLowerCase();
  const names = Object.keys(state.catalog.types || {}).filter(n => !q || n.toLowerCase().includes(q)).sort();
  const entities = names.filter(n => !n.endsWith('Params'));
  const params = names.filter(n => n.endsWith('Params'));

  const section = (title, list) => {
    if (!list.length) return;
    const group = el('div', 'type-group');
    const head = el('h3'); head.textContent = title; group.appendChild(head);
    for (const name of list) {
      const a = el('a', 'type-item');
      const label = el('span', 'type-name'); label.textContent = name; a.appendChild(label);
      if (drift[name]) {
        const chip = el('span', 'drift-chip'); chip.textContent = 'drift';
        chip.title = 'shape diverges across services'; a.appendChild(chip);
      }
      a.addEventListener('click', () => { state.selectedType = name; renderSidebar(); renderTypeDetail(name); });
      if (state.selectedType === name) a.classList.add('selected');
      group.appendChild(a);
    }
    sb.appendChild(group);
  };

  section('Entities', entities);
  section('Params', params);
  if (!names.length) {
    const none = el('div', 'type-group type-empty'); none.textContent = 'No types match.'; sb.appendChild(none);
  }
}

function renderMethodList(sb) {
  sb.querySelectorAll('.router-group, .sb-empty').forEach(e => e.remove());
  const q = state.methodFilter.trim().toLowerCase();
  let any = false;
  for (const svc of Object.keys(state.catalog.services || {}).sort()) {
    for (const rd of state.catalog.services[svc] || []) {
      const methods = (rd.methods || []).filter(md => !q || `${rd.router}.${md.method}`.toLowerCase().includes(q));
      if (!methods.length) continue;
      any = true;
      const group = el('div', 'router-group');
      const head = el('h3'); head.textContent = rd.router; group.appendChild(head);
      for (const md of methods) {
        const a = el('a', 'method-item');
        const label = el('span', 'method-name'); label.textContent = md.method; a.appendChild(label);
        if (md.internal) {
          a.classList.add('is-internal');
          const chip = el('span', 'internal-chip'); chip.textContent = 'internal'; a.appendChild(chip);
        } else if (!md.hasParams) {
          const chip = el('span', 'arg-chip'); chip.textContent = 'no args'; a.appendChild(chip);
        }
        if (md.perm) {
          const permChip = el('span', 'perm-chip'); permChip.textContent = md.perm;
          permChip.title = 'declared authz requirement (perm)'; a.appendChild(permChip);
        }
        a.addEventListener('click', () => { state.selectedMethod = { router: rd.router, method: md.method }; renderSidebar(); renderMethodDetail(rd, md); });
        if (state.selectedMethod && state.selectedMethod.router === rd.router && state.selectedMethod.method === md.method) a.classList.add('selected');
        group.appendChild(a);
      }
      sb.appendChild(group);
    }
  }
  if (!any) { const none = el('div', 'sb-empty'); none.textContent = 'No methods match.'; sb.appendChild(none); }
}

function renderEmpty() {
  $('#detail').innerHTML = '<div class="empty">Pick a method or type on the left, or press ⌘K.</div>';
}

/* ---- method detail ---------------------------------------------------- */
function renderMethodDetail(rd, md) {
  const detail = $('#detail');
  detail.innerHTML = '';

  const h = el('h2', 'type-title');
  h.innerHTML = `<span class="type-name">${escapeHTML(rd.router)}.${escapeHTML(md.method)}</span>`;
  if (md.deprecated) h.innerHTML += ` <span class="deprecated">deprecated</span>`;
  detail.appendChild(h);

  const path = el('div', 'path');
  path.innerHTML = `<span class="post-chip">POST</span> ${escapeHTML(md.postPath)}`;
  detail.appendChild(path);

  if (md.perm) {
    const perm = el('div', 'perm-line');
    perm.innerHTML = `<span class="perm-label">requires</span> <span class="perm-chip">${escapeHTML(md.perm)}</span> <span class="perm-note">opaque — your AuthzService decides</span>`;
    detail.appendChild(perm);
  }

  detail.appendChild(sectionHead('Parameters'));
  if (md.hasParams && md.params && md.params.length) {
    const wrap = el('div'); wrap.innerHTML = fieldsTableHTML(md.params, { pos: true, docs: true });
    detail.appendChild(wrap.firstElementChild);
  } else {
    const none = el('div', 'usedby-empty'); none.textContent = 'No parameters.'; detail.appendChild(none);
  }

  detail.appendChild(sectionHead('Try it'));
  const grid = el('div', 'tryit-grid');

  // ---- request pane
  const reqPane = el('div', 'tryit-pane');
  const reqHead = el('div', 'pane-head');
  reqHead.innerHTML = '<span>request</span><span class="spacer"></span>';
  const toggle = el('div', 'shape-toggle');
  toggle.innerHTML = '<button data-shape="named" class="active">named {…}</button><button data-shape="positional">positional […]</button>';
  reqHead.appendChild(toggle);
  reqPane.appendChild(reqHead);

  const textarea = document.createElement('textarea');
  textarea.spellcheck = false;
  reqPane.appendChild(textarea);

  const seedBody = (shape, useExamples) => {
    if (!md.hasParams || !md.params || md.params.length === 0) return '{}';
    const pick = f => (useExamples && f.example !== undefined && f.example !== '')
      ? coerceExample(f.example, f.schemaType) : defaultFor(f.schemaType);
    if (shape === 'positional') {
      return JSON.stringify(md.params.filter(f => f.position >= 0).sort((a, b) => a.position - b.position).map(pick), null, 2);
    }
    const obj = {};
    for (const f of md.params) { if (f.source === 'header') continue; obj[f.jsonName] = pick(f); }
    return JSON.stringify(obj, null, 2);
  };
  textarea.value = seedBody('named', false);
  let activeShape = 'named';
  toggle.querySelectorAll('button').forEach(btn => {
    btn.addEventListener('click', () => {
      toggle.querySelectorAll('button').forEach(b => b.classList.toggle('active', b === btn));
      activeShape = btn.dataset.shape;
      textarea.value = seedBody(activeShape, false);
    });
  });

  // Header-bound params bind from real HTTP headers, not the args body.
  const headerParams = (md.params || []).filter(f => f.source === 'header');
  const headerInputs = {};
  if (headerParams.length) {
    const hhead = el('div', 'header-params-head'); hhead.textContent = 'Headers'; reqPane.appendChild(hhead);
    const hwrap = el('div', 'header-inputs');
    for (const f of headerParams) {
      const label = el('label', 'header-input');
      label.innerHTML = `<span class="header-name">${escapeHTML(f.header)}</span>` + (f.required ? ' <span class="required">required</span>' : '');
      const inp = document.createElement('input');
      inp.type = 'text'; inp.spellcheck = false;
      inp.placeholder = (f.example !== undefined && f.example !== '') ? String(f.example) : (f.schemaType || 'value');
      label.appendChild(inp); hwrap.appendChild(label);
      headerInputs[f.header] = inp;
    }
    reqPane.appendChild(hwrap);
  }

  const row = el('div', 'execute-row');
  const exec = el('button', 'execute'); exec.textContent = 'Execute'; row.appendChild(exec);
  if (md.params && md.params.some(f => f.example !== undefined && f.example !== '')) {
    const fill = el('button', 'execute ghost'); fill.textContent = 'Fill example';
    fill.addEventListener('click', () => { textarea.value = seedBody(activeShape, true); });
    row.appendChild(fill);
  }
  const hint = el('span', 'run-hint'); hint.innerHTML = '<kbd class="kbd-hint">⌘⏎</kbd>'; row.appendChild(hint);
  reqPane.appendChild(row);
  grid.appendChild(reqPane);

  // ---- response pane
  const resPane = el('div', 'tryit-pane');
  const resHead = el('div', 'pane-head');
  resHead.innerHTML = '<span>response</span>';
  const spacer = el('span', 'spacer'); resHead.appendChild(spacer);
  const statusPill = el('span', 'status-pill'); statusPill.hidden = true; resHead.appendChild(statusPill);
  const timing = el('span', 'timing'); resHead.appendChild(timing);
  let lastResponse = '';
  const copyResp = copyButton(() => lastResponse); resHead.appendChild(copyResp);
  resPane.appendChild(resHead);
  const out = el('pre', 'placeholder'); out.textContent = '// response appears here'; resPane.appendChild(out);
  grid.appendChild(resPane);

  detail.appendChild(grid);

  const setStatus = (label, cls, ms) => {
    statusPill.hidden = false; statusPill.textContent = label; statusPill.className = 'status-pill ' + cls;
    timing.textContent = (ms === undefined) ? '' : ms + ' ms';
  };
  const showErr = msg => { lastResponse = msg; out.className = ''; out.textContent = msg; setStatus('ERR', 'err'); };

  const doExecute = async () => {
    let args;
    try { args = JSON.parse(textarea.value); }
    catch (e) { showErr('json parse error: ' + e.message); return; }
    const hdrs = { 'Content-Type': 'application/json' };
    for (const name in headerInputs) { const v = headerInputs[name].value; if (v !== '') hdrs[name] = v; }
    statusPill.hidden = true; timing.textContent = '';
    out.className = 'placeholder'; out.textContent = '// executing…';
    const t0 = performance.now();
    try {
      const resp = await fetch(md.postPath, { method: 'POST', headers: hdrs, body: JSON.stringify({ args }) });
      const txt = await resp.text();
      const ms = Math.round(performance.now() - t0);
      const pretty = prettyJSON(txt);
      lastResponse = pretty;
      out.className = ''; out.innerHTML = highlightJSON(pretty);
      setStatus(String(resp.status), resp.status < 300 ? 'ok' : resp.status < 500 ? 'warn' : 'err', ms);
    } catch (e) { showErr('fetch error: ' + e.message); }
  };
  exec.addEventListener('click', doExecute);
  textarea.addEventListener('keydown', e => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); doExecute(); }
  });

  if (md.requestTypeScript) detail.appendChild(tsBlock('Request TypeScript', md.requestTypeScript));
  if (md.responseTypeScript) detail.appendChild(tsBlock('Response TypeScript', md.responseTypeScript));
}

function tsBlock(title, code) {
  const block = el('div', 'ts-block');
  const head = el('div', 'ts-head');
  const label = el('span'); label.textContent = title; head.appendChild(label);
  const spacer = el('span', 'spacer'); head.appendChild(spacer);
  head.appendChild(copyButton(() => code));
  block.appendChild(head);
  const pre = el('pre'); pre.textContent = code; block.appendChild(pre);
  return block;
}

function copyButton(getText) {
  const b = el('button', 'copy-btn'); b.type = 'button'; b.textContent = 'copy';
  b.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(getText());
      b.textContent = 'copied'; b.classList.add('copied');
      setTimeout(() => { b.textContent = 'copy'; b.classList.remove('copied'); }, 1200);
    } catch { b.textContent = 'n/a'; }
  });
  return b;
}

/* ---- type detail ------------------------------------------------------ */
function renderTypeDetail(name) {
  const detail = $('#detail');
  detail.innerHTML = '';
  const drift = state.catalog.cross_refs && state.catalog.cross_refs[name];
  const td = state.catalog.types[name];

  const h = el('h2', 'type-title');
  h.innerHTML = `<span class="type-name">${escapeHTML(name)}</span>`;
  if (drift) h.innerHTML += ' <span class="drift-chip">drift</span>';
  detail.appendChild(h);

  if (drift) {
    const variants = drift.variants || [];
    const diff = drift.diff || [];
    const diverged = diff.filter(d => d.diverges).map(d => d.field);
    const warn = el('div', 'drift-panel');
    warn.innerHTML = `<strong>Shape drift.</strong> <span class="dn">${escapeHTML(name)}</span> resolves to ${variants.length} divergent shapes across services` +
      (diverged.length
        ? ` — ${diverged.length} field${diverged.length > 1 ? 's' : ''} differ: ${diverged.map(f => `<code>${escapeHTML(f)}</code>`).join(' ')}.`
        : '.') +
      ` Callers may serialize incompatible payloads.`;
    detail.appendChild(warn);

    // Which service(s) each variant column belongs to.
    const legend = el('div', 'variant-legend');
    legend.innerHTML = variants.map((v, i) =>
      `<div class="vlegend"><span class="vtag v${i % 6}">variant ${i + 1}</span>` +
      `<span class="hash">${escapeHTML(String(v.shape_hash))}</span>` +
      `<span class="vservices">${(v.services || []).map(s => `<span class="used-chip">${escapeHTML(s)}</span>`).join('')}</span></div>`
    ).join('');
    detail.appendChild(legend);

    // Field × variant matrix — diverging rows highlighted, so you see exactly
    // which fields moved rather than eyeballing two field tables.
    if (diff.length) {
      const wrap = el('div');
      wrap.innerHTML = driftMatrixHTML(diff, variants.length);
      detail.appendChild(wrap.firstElementChild);
    }
    return;
  }

  if (!td) { detail.innerHTML = '<div class="empty">Type not found.</div>'; return; }

  const path = el('div', 'path'); path.textContent = `shape ${td.shape_hash}`; detail.appendChild(path);

  const owners = (td.owners && td.owners.length) ? td.owners : (td.owner ? [td.owner] : []);
  const consumers = (td.consumers && td.consumers.length) ? td.consumers : [];
  const own = el('div', 'ownership');
  let ownerRow;
  if (owners.length === 0) ownerRow = `<div class="own-row"><span class="own-label">Owner</span><span class="owner-badge unowned">no owner — input-only</span></div>`;
  else if (owners.length === 1) ownerRow = `<div class="own-row"><span class="own-label">Owner</span><span class="owner-badge">${escapeHTML(owners[0])}</span></div>`;
  else if (td.shared) ownerRow = `<div class="own-row"><span class="own-label">Owners</span><span class="consumer-list">${owners.map(o => `<span class="owner-badge shared">${escapeHTML(o)}</span>`).join('')}<span class="shared-tag">shared — identical shape</span></span></div>`;
  else ownerRow = `<div class="own-row"><span class="own-label">Owners</span><span class="consumer-list">${owners.map(o => `<span class="owner-badge ambiguous">${escapeHTML(o)}</span>`).join('')}<span class="ambiguous-tag">ambiguous</span></span></div>`;
  const consumerRow = consumers.length
    ? `<div class="own-row"><span class="own-label">Consumers</span><span class="consumer-list">${consumers.map(c => `<span class="consumer-chip">${escapeHTML(c)}</span>`).join('')}</span></div>` : '';
  own.innerHTML = ownerRow + consumerRow;
  detail.appendChild(own);

  detail.appendChild(sectionHead('Fields'));
  const table = el('div'); table.innerHTML = fieldsTableHTML(td.fields, { pos: true }); detail.appendChild(table.firstElementChild);
  renderUsedBy(detail, td.used_by || []);
}

function sectionHead(text) {
  const h = el('h4', 'section-head'); h.textContent = text; return h;
}

function fieldsTableHTML(fields, opts) {
  opts = opts || {}; fields = fields || [];
  const posHead = opts.pos ? '<th>Pos</th>' : '';
  return `
    <table class="fields-table">
      <thead><tr><th>Name</th><th>Type</th>${posHead}<th>Flags</th></tr></thead>
      <tbody>${fields.map(f => {
        const posCell = opts.pos ? `<td>${f.position >= 0 ? f.position : ''}</td>` : '';
        const flags = [
          f.required ? '<span class="required">required</span>' : '',
          f.omitempty ? '<span class="flag-muted">omitempty</span>' : '',
          f.deprecated ? '<span class="deprecated">deprecated</span>' : '',
          (opts.docs && f.example !== undefined && f.example !== '') ? `<span class="flag-muted">e.g. ${escapeHTML(String(f.example))}</span>` : '',
        ].filter(Boolean).join(' ');
        const typeRef = f.typeName ? ` <span class="type-ref">(${escapeHTML(f.typeName)})</span>` : '';
        const isHeader = f.source === 'header';
        const primaryName = isHeader ? f.header : f.jsonName;
        const srcBadge = isHeader ? ' <span class="src-badge" title="bound from this request header">header</span>' : '';
        let nameCell;
        if (opts.docs && (f.title || f.desc)) {
          nameCell =
            `<div class="field-name">${f.title ? escapeHTML(f.title) : escapeHTML(primaryName)}${srcBadge}</div>` +
            (f.title ? `<div class="field-sub">${escapeHTML(primaryName)}</div>` : '') +
            (f.desc ? `<div class="field-desc">${escapeHTML(f.desc)}</div>` : '');
        } else {
          nameCell = `<span class="field-name">${escapeHTML(primaryName)}</span>${srcBadge}`;
        }
        return `<tr${f.deprecated ? ' class="row-deprecated"' : ''}><td>${nameCell}</td><td>${escapeHTML(f.designerHint || f.schemaType || '')}${typeRef}</td>${posCell}<td>${flags}</td></tr>`;
      }).join('')}</tbody>
    </table>`;
}

// driftMatrixHTML renders the field × variant diff: one row per field, one
// column per variant, cell = the field's type in that variant (or "absent").
// Diverging rows are highlighted so the actual drift is obvious at a glance.
function driftMatrixHTML(diff, nVariants) {
  const heads = Array.from({ length: nVariants }, (_, i) => `<th class="v${i % 6}">variant ${i + 1}</th>`).join('');
  return `<table class="fields-table drift-matrix">
    <thead><tr><th>Field</th>${heads}</tr></thead>
    <tbody>${diff.map(d => {
      const cells = (d.types || []).map(t => t
        ? `<td>${escapeHTML(t)}</td>`
        : `<td class="absent">absent</td>`).join('');
      return `<tr class="${d.diverges ? 'drow-diff' : 'drow-common'}"><td class="field-name">${escapeHTML(d.field)}</td>${cells}</tr>`;
    }).join('')}</tbody>
  </table>`;
}

function renderUsedBy(detail, usedBy) {
  const block = el('div', 'usedby-block');
  const head = el('h4'); head.textContent = 'Used by'; block.appendChild(head);
  if (!usedBy.length) {
    const none = el('div', 'usedby-empty'); none.textContent = 'Not referenced by any method.'; block.appendChild(none); detail.appendChild(block); return;
  }
  const groups = [
    { role: 'response', label: 'Returned by' },
    { role: 'request', label: 'Accepted by' },
    { role: 'nested', label: 'Referenced by' },
  ];
  const seen = new Set();
  for (const g of groups) {
    const rows = usedBy.filter(u => u.role === g.role);
    if (!rows.length) continue;
    rows.forEach(r => seen.add(r.role));
    const grp = el('div', 'usedby-group');
    grp.innerHTML = `<div class="usedby-label">${g.label}</div><div class="usedby-chips">${rows.map(u => `<span class="used-chip">${escapeHTML(u.service)}<span class="dot">.</span>${escapeHTML(u.method)}</span>`).join('')}</div>`;
    block.appendChild(grp);
  }
  const rest = usedBy.filter(u => !seen.has(u.role));
  if (rest.length) {
    const grp = el('div', 'usedby-group');
    grp.innerHTML = `<div class="usedby-label">Other</div><div class="usedby-chips">${rest.map(u => `<span class="used-chip">${escapeHTML(u.service)}<span class="dot">.</span>${escapeHTML(u.method)} <span class="role-tag">${escapeHTML(u.role || '')}</span></span>`).join('')}</div>`;
    block.appendChild(grp);
  }
  detail.appendChild(block);
}

/* ---- command palette -------------------------------------------------- */
function buildPaletteIndex() {
  const items = [];
  for (const svc of Object.keys(state.catalog.services || {}).sort())
    for (const rd of state.catalog.services[svc] || [])
      for (const md of rd.methods || [])
        items.push({ kind: 'method', label: `${rd.router}.${md.method}`, sub: md.postPath, router: rd.router, method: md.method });
  for (const name of Object.keys(state.catalog.types || {}).sort())
    items.push({ kind: 'type', label: name, sub: '', name });
  return items;
}

function openPalette() {
  if (!state.catalog) return;
  $('#palette').hidden = false; palette.open = true;
  const inp = $('#palette-input'); inp.value = ''; palette.active = 0;
  updatePalette(''); inp.focus();
}
function closePalette() { $('#palette').hidden = true; palette.open = false; }

function updatePalette(q) {
  q = q.trim();
  let items;
  if (!q) items = paletteIndex.slice(0, 40);
  else items = paletteIndex.map(it => ({ it, s: fuzzyScore(it.label, q) }))
    .filter(x => x.s >= 0).sort((a, b) => b.s - a.s).slice(0, 40).map(x => x.it);
  palette.results = items; palette.active = 0;
  renderPaletteResults();
}

function renderPaletteResults() {
  const ul = $('#palette-results'); ul.innerHTML = '';
  if (!palette.results.length) { ul.innerHTML = '<li class="palette-empty">No matches.</li>'; return; }
  palette.results.forEach((it, i) => {
    const li = el('li', 'palette-item' + (i === palette.active ? ' active' : ''));
    li.innerHTML = `<span class="palette-kind">${it.kind}</span><span class="palette-label">${escapeHTML(it.label)}</span>${it.sub ? `<span class="palette-sub">${escapeHTML(it.sub)}</span>` : ''}`;
    li.addEventListener('click', () => selectPalette(i));
    li.addEventListener('mousemove', () => { if (palette.active !== i) { palette.active = i; renderPaletteResults(); } });
    ul.appendChild(li);
  });
  const act = ul.querySelector('.palette-item.active');
  if (act) act.scrollIntoView({ block: 'nearest' });
}

function selectPalette(i) {
  const it = palette.results[i];
  if (!it) return;
  closePalette();
  if (it.kind === 'method') { state.tab = 'methods'; state.selectedMethod = { router: it.router, method: it.method }; }
  else { state.tab = 'types'; state.selectedType = it.name; }
  setTab(state.tab); route();
}

// fuzzyScore: subsequence match with a contiguity + prefix bonus. -1 = no match.
function fuzzyScore(text, q) {
  text = text.toLowerCase(); q = q.toLowerCase();
  if (!q) return 0;
  let ti = 0, score = 0, streak = 0;
  for (let qi = 0; qi < q.length; qi++) {
    const found = text.indexOf(q[qi], ti);
    if (found < 0) return -1;
    streak = (found === ti) ? streak + 1 : 0;
    score += 1 + streak * 2 - (found - ti) * 0.05;
    ti = found + 1;
  }
  if (text.startsWith(q)) score += 20;
  return score;
}

/* ---- keyboard --------------------------------------------------------- */
function bindKeys() {
  document.addEventListener('keydown', e => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault(); palette.open ? closePalette() : openPalette(); return;
    }
    if (palette.open) {
      if (e.key === 'Escape') { e.preventDefault(); closePalette(); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); palette.active = Math.min(palette.active + 1, palette.results.length - 1); renderPaletteResults(); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); palette.active = Math.max(palette.active - 1, 0); renderPaletteResults(); }
      else if (e.key === 'Enter') { e.preventDefault(); selectPalette(palette.active); }
      return;
    }
    if (e.key === '/' && !/^(INPUT|TEXTAREA)$/.test(document.activeElement.tagName)) {
      const s = $('.sb-search'); if (s) { e.preventDefault(); s.focus(); }
    }
  });
  const inp = $('#palette-input');
  if (inp) inp.addEventListener('input', ev => updatePalette(ev.target.value));
  const overlay = $('#palette');
  if (overlay) overlay.addEventListener('click', ev => { if (ev.target.id === 'palette') closePalette(); });
  const opener = $('#palette-open');
  if (opener) opener.addEventListener('click', openPalette);
}

/* ---- helpers ---------------------------------------------------------- */
function defaultFor(t) {
  switch (t) {
    case 'string': return '';
    case 'number': return 0;
    case 'boolean': return false;
    case 'array': return [];
    case 'object': return {};
    default: return null;
  }
}

function coerceExample(raw, t) {
  switch (t) {
    case 'number': return Number.isNaN(Number(raw)) ? 0 : Number(raw);
    case 'boolean': return raw === 'true' || raw === '1';
    case 'array':
    case 'object': try { return JSON.parse(String(raw)); } catch { return raw; }
    default: return String(raw);
  }
}

function prettyJSON(s) {
  try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return s; }
}

// highlightJSON tokenizes an already-pretty JSON string into colored spans.
// Operates on the HTML-escaped text (quotes survive escaping), so it is safe
// against injection from response bodies.
function highlightJSON(str) {
  const esc = escapeHTML(str);
  return esc.replace(
    /("(?:\\u[0-9a-fA-F]{4}|\\[^u]|[^\\"])*"(?:\s*:)?)|(\b(?:true|false)\b)|(\bnull\b)|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g,
    (m, strm, boolm, nullm, numm) => {
      if (strm) return `<span class="${/:\s*$/.test(strm) ? 'j-key' : 'j-str'}">${strm}</span>`;
      if (boolm) return `<span class="j-bool">${boolm}</span>`;
      if (nullm) return `<span class="j-null">${nullm}</span>`;
      return `<span class="j-num">${numm}</span>`;
    }
  );
}

function escapeHTML(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
