// sov explorer — vanilla JS, no deps.
// Reads /rpc/_explorer/api.json (the gateway's full IntrospectReport)
// and renders three panes:
//
//   - Methods: grouped by router, click → form built from params,
//     wire-shape toggle (positional [...] vs named {...}), execute
//     against the live RPC, see response + TypeScript preview.
//   - Types: flat catalog of every Go type appearing on the wire,
//     click → fields + UsedBy list.
//   - Drift radar: cross_refs entries — same-name types with diverging
//     ShapeHash across services. Each variant rendered side-by-side.

let state = {
  catalog: null,
  tab: 'methods',
  selectedMethod: null,
  selectedType: null,
  typeFilter: '',
  methodFilter: '',
  showInternal: false,
};

const $ = sel => document.querySelector(sel);

async function loadCatalog() {
  // The internal variant returns soft-hidden methods (flagged internal);
  // the framework auth/authz hooks are never in either payload.
  const url = state.showInternal
    ? '/rpc/_explorer/api-internal.json'
    : '/rpc/_explorer/api.json';
  const resp = await fetch(url);
  state.catalog = await resp.json();
  if (state.catalog.cross_refs && Object.keys(state.catalog.cross_refs).length > 0) {
    const badge = $('#drift-badge');
    badge.hidden = false;
    badge.textContent = `${Object.keys(state.catalog.cross_refs).length} drift`;
  }
  route();
}

// route renders the sidebar + the detail pane for the active tab,
// defaulting the selection to the first item when nothing is selected (or
// the prior selection no longer exists).
function route() {
  if (!state.catalog) {
    renderSidebar();
    renderEmpty();
    return;
  }
  if (state.tab === 'methods') {
    const sel = resolveMethod(state.selectedMethod) || firstMethod();
    state.selectedMethod = sel ? {router: sel.rd.router, method: sel.md.method} : null;
    renderSidebar();
    if (sel) renderMethodDetail(sel.rd, sel.md); else renderEmpty();
  } else {
    let name = (state.selectedType && state.catalog.types && state.catalog.types[state.selectedType])
      ? state.selectedType : firstType();
    state.selectedType = name;
    renderSidebar();
    if (name) renderTypeDetail(name); else renderEmpty();
  }
}

function firstMethod() {
  for (const svc of Object.keys(state.catalog.services || {}).sort()) {
    for (const rd of state.catalog.services[svc] || []) {
      if ((rd.methods || []).length) return {rd, md: rd.methods[0]};
    }
  }
  return null;
}

function resolveMethod(sel) {
  if (!sel) return null;
  for (const svc of Object.keys(state.catalog.services || {})) {
    for (const rd of state.catalog.services[svc] || []) {
      if (rd.router !== sel.router) continue;
      for (const md of rd.methods || []) {
        if (md.method === sel.method) return {rd, md};
      }
    }
  }
  return null;
}

function firstType() {
  const names = Object.keys(state.catalog.types || {}).sort();
  const entities = names.filter(n => !n.endsWith('Params'));
  return entities[0] || names[0] || null;
}

document.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('header nav button').forEach(btn => {
    btn.addEventListener('click', () => {
      state.tab = btn.dataset.tab;
      document.querySelectorAll('header nav button').forEach(b => b.classList.toggle('active', b === btn));
      route();
    });
  });
  const toggle = $('#show-internal');
  if (toggle) {
    toggle.addEventListener('change', () => {
      state.showInternal = toggle.checked;
      loadCatalog();
    });
  }
  loadCatalog();
});

function renderSidebar() {
  const sb = $('#sidebar');
  sb.innerHTML = '';
  if (!state.catalog) return;

  if (state.tab === 'methods') {
    const search = document.createElement('input');
    search.className = 'sb-search';
    search.type = 'text';
    search.placeholder = 'Filter methods…';
    search.value = state.methodFilter;
    search.spellcheck = false;
    search.addEventListener('input', () => {
      state.methodFilter = search.value;
      renderMethodList(sb);
      const fresh = sb.querySelector('.sb-search');
      if (fresh) {
        fresh.focus();
        fresh.setSelectionRange(fresh.value.length, fresh.value.length);
      }
    });
    sb.appendChild(search);
    renderMethodList(sb);
  } else if (state.tab === 'types') {
    const drift = state.catalog.cross_refs || {};

    // Search/filter box pinned at the top of the list.
    const search = document.createElement('input');
    search.className = 'sb-search';
    search.type = 'text';
    search.placeholder = 'Filter types…';
    search.value = state.typeFilter;
    search.spellcheck = false;
    search.addEventListener('input', () => {
      state.typeFilter = search.value;
      renderTypeList(sb, drift);
      // Keep focus + caret after re-render.
      const fresh = sb.querySelector('.sb-search');
      if (fresh) {
        fresh.focus();
        fresh.setSelectionRange(fresh.value.length, fresh.value.length);
      }
    });
    sb.appendChild(search);

    renderTypeList(sb, drift);
  }
}

// Renders the grouped, filtered type list below the search input.
// Re-invoked on every keystroke; rebuilds only the group sections so the
// search input itself keeps focus.
function renderTypeList(sb, drift) {
  sb.querySelectorAll('.type-group').forEach(el => el.remove());

  const q = state.typeFilter.trim().toLowerCase();
  const names = Object.keys(state.catalog.types || {})
    .filter(n => !q || n.toLowerCase().includes(q))
    .sort();

  const entities = names.filter(n => !n.endsWith('Params'));
  const params = names.filter(n => n.endsWith('Params'));

  const section = (title, list) => {
    if (!list.length) return;
    const group = document.createElement('div');
    group.className = 'type-group';
    const head = document.createElement('h3');
    head.textContent = title;
    group.appendChild(head);
    for (const name of list) {
      const a = document.createElement('a');
      a.className = 'type-item';
      const label = document.createElement('span');
      label.className = 'type-name';
      label.textContent = name;
      a.appendChild(label);
      if (drift[name]) {
        const chip = document.createElement('span');
        chip.className = 'drift-chip';
        chip.textContent = 'drift';
        chip.title = 'shape diverges across services';
        a.appendChild(chip);
      }
      a.addEventListener('click', () => {
        state.selectedType = name;
        renderSidebar();
        renderTypeDetail(name);
      });
      if (state.selectedType === name) a.classList.add('selected');
      group.appendChild(a);
    }
    sb.appendChild(group);
  };

  section('Entities', entities);
  section('Params', params);

  if (!names.length) {
    const none = document.createElement('div');
    none.className = 'type-group type-empty';
    none.textContent = 'No types match.';
    sb.appendChild(none);
  }
}

// Renders the filtered, router-grouped method list below the search box.
// Re-invoked on each keystroke; rebuilds only the groups so the search
// input keeps focus.
function renderMethodList(sb) {
  sb.querySelectorAll('.router-group, .sb-empty').forEach(el => el.remove());
  const q = state.methodFilter.trim().toLowerCase();
  let any = false;
  for (const svc of Object.keys(state.catalog.services || {}).sort()) {
    for (const rd of state.catalog.services[svc] || []) {
      const methods = (rd.methods || []).filter(md =>
        !q || `${rd.router}.${md.method}`.toLowerCase().includes(q));
      if (!methods.length) continue;
      any = true;
      const group = document.createElement('div');
      group.className = 'router-group';
      const head = document.createElement('h3');
      head.textContent = rd.router;
      group.appendChild(head);
      for (const md of methods) {
        const a = document.createElement('a');
        a.className = 'method-item';
        const label = document.createElement('span');
        label.className = 'method-name';
        label.textContent = md.method;
        a.appendChild(label);
        if (md.internal) {
          a.classList.add('is-internal');
          const chip = document.createElement('span');
          chip.className = 'internal-chip';
          chip.textContent = 'internal';
          a.appendChild(chip);
        } else if (!md.hasParams) {
          const chip = document.createElement('span');
          chip.className = 'arg-chip';
          chip.textContent = 'no args';
          a.appendChild(chip);
        }
        a.addEventListener('click', () => {
          state.selectedMethod = {router: rd.router, method: md.method};
          renderSidebar();
          renderMethodDetail(rd, md);
        });
        if (state.selectedMethod
            && state.selectedMethod.router === rd.router
            && state.selectedMethod.method === md.method) {
          a.classList.add('selected');
        }
        group.appendChild(a);
      }
      sb.appendChild(group);
    }
  }
  if (!any) {
    const none = document.createElement('div');
    none.className = 'sb-empty';
    none.textContent = 'No methods match.';
    sb.appendChild(none);
  }
}

function renderEmpty() {
  $('#detail').innerHTML = '<div class="empty">Pick a method or type on the left.</div>';
}

function renderMethodDetail(rd, md) {
  const detail = $('#detail');
  detail.innerHTML = '';

  const h = document.createElement('h2');
  h.className = 'type-title';
  h.innerHTML = `<span class="type-name">${escapeHTML(rd.router)}.${escapeHTML(md.method)}</span>`;
  detail.appendChild(h);

  const path = document.createElement('div');
  path.className = 'path';
  path.innerHTML = `<span class="post-chip">POST</span> ${escapeHTML(md.postPath)}`;
  detail.appendChild(path);

  detail.appendChild(sectionHead('Parameters'));
  if (md.hasParams && md.params && md.params.length) {
    const wrap = document.createElement('div');
    wrap.innerHTML = fieldsTableHTML(md.params, {pos: true, docs: true});
    detail.appendChild(wrap.firstElementChild);
  } else {
    const none = document.createElement('div');
    none.className = 'usedby-empty';
    none.textContent = 'No parameters.';
    detail.appendChild(none);
  }

  detail.appendChild(sectionHead('Try it'));

  // wire-shape toggle
  const toggle = document.createElement('div');
  toggle.className = 'shape-toggle';
  toggle.innerHTML = `
    <button data-shape="named" class="active">Named {...}</button>
    <button data-shape="positional">Positional [...]</button>
  `;
  detail.appendChild(toggle);

  const textarea = document.createElement('textarea');
  textarea.spellcheck = false;
  detail.appendChild(textarea);

  const seedBody = (shape, useExamples) => {
    if (!md.hasParams || !md.params || md.params.length === 0) return '{}';
    const pick = f => (useExamples && f.example !== undefined && f.example !== '')
      ? coerceExample(f.example, f.schemaType)
      : defaultFor(f.schemaType);
    if (shape === 'positional') {
      return JSON.stringify(md.params.filter(f => f.position >= 0)
                                     .sort((a, b) => a.position - b.position)
                                     .map(pick), null, 2);
    }
    const obj = {};
    for (const f of md.params) obj[f.jsonName] = pick(f);
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

  const row = document.createElement('div');
  row.className = 'execute-row';
  const exec = document.createElement('button');
  exec.className = 'execute';
  exec.textContent = 'Execute';
  row.appendChild(exec);

  if (md.params && md.params.some(f => f.example !== undefined && f.example !== '')) {
    const fill = document.createElement('button');
    fill.className = 'execute ghost';
    fill.textContent = 'Fill example';
    fill.addEventListener('click', () => { textarea.value = seedBody(activeShape, true); });
    row.appendChild(fill);
  }

  detail.appendChild(row);

  const out = document.createElement('pre');
  out.textContent = '// response will appear here';
  detail.appendChild(out);

  exec.addEventListener('click', async () => {
    let args;
    try { args = JSON.parse(textarea.value); }
    catch (e) { out.textContent = 'json parse error: ' + e.message; return; }
    const body = JSON.stringify({args});
    out.textContent = '// executing...';
    try {
      const resp = await fetch(md.postPath, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body,
      });
      const txt = await resp.text();
      out.textContent = `// HTTP ${resp.status}\n${prettyJSON(txt)}`;
    } catch (e) {
      out.textContent = 'fetch error: ' + e.message;
    }
  });

  if (md.requestTypeScript) {
    const ts = document.createElement('div');
    ts.className = 'ts-block';
    ts.innerHTML = `<h4>Request TypeScript</h4><pre>${escapeHTML(md.requestTypeScript)}</pre>`;
    detail.appendChild(ts);
  }
  if (md.responseTypeScript) {
    const ts = document.createElement('div');
    ts.className = 'ts-block';
    ts.innerHTML = `<h4>Response TypeScript</h4><pre>${escapeHTML(md.responseTypeScript)}</pre>`;
    detail.appendChild(ts);
  }
}

function renderTypeDetail(name) {
  const detail = $('#detail');
  detail.innerHTML = '';
  const drift = state.catalog.cross_refs && state.catalog.cross_refs[name];
  const td = state.catalog.types[name];

  const h = document.createElement('h2');
  h.className = 'type-title';
  h.innerHTML = `<span class="type-name">${escapeHTML(name)}</span>`;
  if (drift) h.innerHTML += ` <span class="drift-chip">drift</span>`;
  detail.appendChild(h);

  // Drift types: same name, divergent shapes across services. Warn loudly.
  if (drift) {
    const warn = document.createElement('div');
    warn.className = 'drift-panel';
    warn.innerHTML = `<strong>Shape drift.</strong> This type name resolves to
      ${drift.variants.length} divergent shapes across services. Callers may
      serialize incompatible payloads.`;
    detail.appendChild(warn);

    drift.variants.forEach((v, i) => {
      const box = document.createElement('div');
      box.className = 'variant ' + (i === 0 ? 'first' : 'diff');
      box.innerHTML = `
        <h4>variant ${i + 1} <span class="hash">hash ${escapeHTML(String(v.shape_hash))}</span></h4>
        <div class="services">${(v.services || []).map(s =>
          `<span class="used-chip">${escapeHTML(s)}</span>`).join('')}</div>
        ${fieldsTableHTML(v.fields, {})}
      `;
      detail.appendChild(box);
    });
    return;
  }

  if (!td) {
    detail.innerHTML = '<div class="empty">Type not found.</div>';
    return;
  }

  const path = document.createElement('div');
  path.className = 'path';
  path.textContent = `shape ${td.shape_hash}`;
  detail.appendChild(path);

  // Data-ownership: who produces this type vs who consumes it.
  const owners = (td.owners && td.owners.length) ? td.owners
    : (td.owner ? [td.owner] : []);
  const consumers = (td.consumers && td.consumers.length) ? td.consumers : [];
  const own = document.createElement('div');
  own.className = 'ownership';
  let ownerRow;
  if (owners.length === 0) {
    ownerRow = `<div class="own-row"><span class="own-label">Owner</span>
         <span class="owner-badge unowned">no owner — input-only</span></div>`;
  } else if (owners.length === 1) {
    ownerRow = `<div class="own-row"><span class="own-label">Owner</span>
         <span class="owner-badge">${escapeHTML(owners[0])}</span></div>`;
  } else {
    // Ambiguous: more than one service returns this type.
    ownerRow = `<div class="own-row"><span class="own-label">Owners</span>
         <span class="consumer-list">${owners.map(o =>
           `<span class="owner-badge ambiguous">${escapeHTML(o)}</span>`).join('')}
         <span class="ambiguous-tag">ambiguous</span></span></div>`;
  }
  const consumerRow = consumers.length
    ? `<div class="own-row"><span class="own-label">Consumers</span>
         <span class="consumer-list">${consumers.map(c =>
           `<span class="consumer-chip">${escapeHTML(c)}</span>`).join('')}</span></div>`
    : '';
  own.innerHTML = ownerRow + consumerRow;
  detail.appendChild(own);

  detail.appendChild(sectionHead('Fields'));
  const table = document.createElement('div');
  table.innerHTML = fieldsTableHTML(td.fields, {pos: true});
  detail.appendChild(table.firstElementChild);

  // Used by, grouped by role rather than a raw <pre> dump.
  renderUsedBy(detail, td.used_by || []);
}

function sectionHead(text) {
  const h = document.createElement('h4');
  h.className = 'section-head';
  h.textContent = text;
  return h;
}

// Builds a fields table as an HTML string. opts.pos adds the positional
// column; opts.docs renders title/description sublines + example hints
// (used for method params, which carry richer metadata than plain types).
function fieldsTableHTML(fields, opts) {
  opts = opts || {};
  fields = fields || [];
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
          (opts.docs && f.example !== undefined && f.example !== '')
            ? `<span class="flag-muted">e.g. ${escapeHTML(String(f.example))}</span>` : '',
        ].filter(Boolean).join(' ');
        const typeRef = f.typeName
          ? ` <span class="type-ref">(${escapeHTML(f.typeName)})</span>` : '';
        let nameCell;
        if (opts.docs && (f.title || f.desc)) {
          nameCell =
            `<div class="field-name">${f.title ? escapeHTML(f.title) : escapeHTML(f.jsonName)}</div>` +
            (f.title ? `<div class="field-sub">${escapeHTML(f.jsonName)}</div>` : '') +
            (f.desc ? `<div class="field-desc">${escapeHTML(f.desc)}</div>` : '');
        } else {
          nameCell = `<span class="field-name">${escapeHTML(f.jsonName)}</span>`;
        }
        return `
          <tr${f.deprecated ? ' class="row-deprecated"' : ''}>
            <td>${nameCell}</td>
            <td>${escapeHTML(f.designerHint || f.schemaType || '')}${typeRef}</td>
            ${posCell}
            <td>${flags}</td>
          </tr>`;
      }).join('')}</tbody>
    </table>`;
}

// Renders the "Used by" section grouped by role with small chips.
function renderUsedBy(detail, usedBy) {
  const block = document.createElement('div');
  block.className = 'usedby-block';
  const head = document.createElement('h4');
  head.textContent = 'Used by';
  block.appendChild(head);

  if (!usedBy.length) {
    const none = document.createElement('div');
    none.className = 'usedby-empty';
    none.textContent = 'Not referenced by any method.';
    block.appendChild(none);
    detail.appendChild(block);
    return;
  }

  const groups = [
    {role: 'response', label: 'Returned by'},
    {role: 'request', label: 'Accepted by'},
    {role: 'nested', label: 'Referenced by'},
  ];
  const seen = new Set();
  for (const g of groups) {
    const rows = usedBy.filter(u => u.role === g.role);
    if (!rows.length) continue;
    rows.forEach(r => seen.add(r.role));
    const grp = document.createElement('div');
    grp.className = 'usedby-group';
    grp.innerHTML = `<div class="usedby-label">${g.label}</div>
      <div class="usedby-chips">${rows.map(u =>
        `<span class="used-chip">${escapeHTML(u.service)}<span class="dot">.</span>${escapeHTML(u.method)}</span>`
      ).join('')}</div>`;
    block.appendChild(grp);
  }
  // Any unexpected roles fall through to a generic group.
  const rest = usedBy.filter(u => !seen.has(u.role));
  if (rest.length) {
    const grp = document.createElement('div');
    grp.className = 'usedby-group';
    grp.innerHTML = `<div class="usedby-label">Other</div>
      <div class="usedby-chips">${rest.map(u =>
        `<span class="used-chip">${escapeHTML(u.service)}<span class="dot">.</span>${escapeHTML(u.method)} <span class="role-tag">${escapeHTML(u.role || '')}</span></span>`
      ).join('')}</div>`;
    block.appendChild(grp);
  }
  detail.appendChild(block);
}

function defaultFor(t) {
  switch (t) {
    case 'string':  return '';
    case 'number':  return 0;
    case 'boolean': return false;
    case 'array':   return [];
    case 'object':  return {};
    default:        return null;
  }
}

function coerceExample(raw, t) {
  switch (t) {
    case 'number':
      return Number.isNaN(Number(raw)) ? 0 : Number(raw);
    case 'boolean':
      return raw === 'true' || raw === '1';
    case 'array':
    case 'object':
      try { return JSON.parse(String(raw)); } catch { return raw; }
    default:
      return String(raw);
  }
}

function escapeAttr(s) {
  return String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
}

function prettyJSON(s) {
  try { return JSON.stringify(JSON.parse(s), null, 2); }
  catch { return s; }
}

function escapeHTML(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
