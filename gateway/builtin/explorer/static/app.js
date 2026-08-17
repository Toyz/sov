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
let extensionsLoaded = false;

const $ = sel => document.querySelector(sel);
const el = (tag, cls) => { const e = document.createElement(tag); if (cls) e.className = cls; return e; };

// highlightCode tokenizes `code` with a rule list ([{cls, re}]) and returns SAFE
// html: every character is escaped, matched runs wrapped in <span class="tok-CLS">.
// Sticky scan, left-to-right, first-rule-that-matches-here wins — no overlap
// ambiguity (a keyword inside a string never re-matches). cls is sanitized to
// [a-z0-9-] so an extension can never inject a class/attr. This is the engine
// behind sovx.highlighter(lang, rules): the dashboard ships colors for
// kw/str/num/comment/fn/type/punct; any other cls just needs a color rule in the
// extension's own ExplorerAssets CSS.
function highlightCode(code, rules) {
  code = String(code == null ? '' : code);
  if (!rules || !rules.length) return escapeHTML(code);
  const cx = rules.map(r => ({
    cls: String(r.cls || 'punct').replace(/[^a-z0-9-]/gi, '') || 'punct',
    re: new RegExp(r.re.source, 'y' + (r.re.flags.includes('i') ? 'i' : '')),
  }));
  let out = '', i = 0, guard = 0;
  while (i < code.length && guard++ < 200000) {
    let hit = null;
    for (const r of cx) {
      r.re.lastIndex = i;
      const m = r.re.exec(code);
      if (m && m.index === i && m[0].length) { hit = { cls: r.cls, text: m[0] }; break; }
    }
    if (hit) { out += '<span class="tok-' + hit.cls + '">' + escapeHTML(hit.text) + '</span>'; i += hit.text.length; }
    else { out += escapeHTML(code[i]); i++; }
  }
  return out;
}

// fmtDur / fmtBytes render a round-trip time and a payload size compactly.
function fmtDur(ms) { return ms >= 1000 ? (ms / 1000).toFixed(2) + ' s' : ms + ' ms'; }
function fmtBytes(n) {
  if (n < 1024) return n + ' B';
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1048576).toFixed(1) + ' MB';
}

// buildCurl renders a reqObj ({path,headers,body}) as a runnable curl command,
// including whatever an extension's requestHook injected. Single-quoted + escaped.
function buildCurl(reqObj) {
  const q = s => "'" + String(s).replace(/'/g, "'\\''") + "'";
  let c = 'curl -X POST ' + q(location.origin + reqObj.path);
  const h = reqObj.headers || {};
  for (const k of Object.keys(h)) c += ' \\\n  -H ' + q(k + ': ' + h[k]);
  if (reqObj.body) c += ' \\\n  -d ' + q(reqObj.body);
  return c;
}

// ---- extension SDK (window.sovx) --------------------------------------------
// Plugin-provided ES modules (listed in /rpc/_explorer/extensions.json) register
// against this. It is the ENTIRE surface an extension touches — actions on
// methods/types, hooks on outgoing requests, persisted settings, panels, plus
// the dashboard's own theme tokens + dom helpers so extensions look native.
const sovx = {
  _actions: { method: [], type: [] },
  _requestHooks: [],
  _settings: [],       // [{id,label,placeholder,secret}]
  _settingVals: {},    // id -> value (persisted in this browser)
  _highlighters: {},   // lang -> [{cls, re}]
  _codegens: [],       // [{id,label,lang,render}]

  // action('method'|'type', {id,label,run(ctx)}) — a button on the detail pane.
  action(scope, spec) {
    if ((scope === 'method' || scope === 'type') && spec && typeof spec.run === 'function') {
      sovx._actions[scope].push({ label: spec.label || spec.id || 'action', run: spec.run });
    }
  },
  // requestHook(fn) — fn({path,headers,body}) mutates a "try it" request in place
  // (this is how a user extension injects a bearer/identity header).
  requestHook(fn) { if (typeof fn === 'function') sovx._requestHooks.push(fn); },
  // setting({id,label,placeholder,secret}) registers a persisted global input;
  // setting('id') reads its current value.
  setting(arg) {
    if (typeof arg === 'string') return sovx._settingVals[arg] || '';
    if (arg && arg.id && !sovx._settings.some(s => s.id === arg.id)) {
      sovx._settings.push({ id: arg.id, label: arg.label || arg.id, placeholder: arg.placeholder || '', secret: !!arg.secret });
    }
    return undefined;
  },
  // theme('--accent') -> the resolved CSS custom-property value.
  theme(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); },
  // highlighter(lang, rules) registers syntax colors for a language an extension
  // emits (python, csharp, curl, whatever it picks). rules is [{cls, re}]: at each
  // position the FIRST rule whose RegExp matches wins, and its run becomes a
  // <span class="tok-CLS">. The dashboard already colors kw/str/num/comment/fn/
  // type/punct; use those and it just works, or invent your own cls and color it
  // from your ExplorerAssets CSS. Ordering matters — put strings/comments first.
  highlighter(lang, rules) {
    if (lang && Array.isArray(rules)) sovx._highlighters[String(lang).toLowerCase()] = rules;
  },
  // highlight(lang, code) -> safe html (escaped + tokens wrapped). The dashboard
  // uses it for panel({code, lang}); extensions can call it directly too.
  highlight(lang, code) { return highlightCode(code, sovx._highlighters[String(lang || '').toLowerCase()]); },
  // ident(name) reduces a Go type name — including a generic instantiation like
  // "Page[main.Charge]" — to a legal PascalCase identifier ("PageCharge"), dropping
  // lowercased package qualifiers ("main."). Every codegen should route type names
  // through it so generics/packages don't emit broken identifiers.
  ident(name) {
    return String(name || '')
      // drop pkg qualifiers (main. time. etc.) — only a WHOLE lowercased segment at
      // a boundary, so "Accounts.GetParams" keeps "Accounts" (not "A").
      .replace(/(^|[^A-Za-z0-9])[a-z][A-Za-z0-9]*\./g, '$1')
      .split(/[^A-Za-z0-9]+/).filter(Boolean)        // split on [ ] . and any non-ident char
      .map(s => s.charAt(0).toUpperCase() + s.slice(1))
      .join('') || 'T';
  },
  // codegen({id,label,lang,render}) registers a language emitter. The dashboard
  // calls render(shape) and drops a colorized, copyable block INLINE — on every
  // method (a "Request <label>" + "Response <label>" from its bodies) and on every
  // type page ("<label>"). No buttons. shape = {name, fields, nested}: name is the
  // type name, fields is its field list, nested maps typeName -> fields so the
  // emitter can walk referenced types. Pair with highlighter(lang, …) for color.
  codegen(spec) {
    if (spec && typeof spec.render === 'function') {
      sovx._codegens.push({ id: spec.id, label: spec.label || spec.id || 'code', lang: spec.lang || '', render: spec.render });
    }
  },
  el, escapeHTML,
  copy(text) { try { return navigator.clipboard.writeText(String(text)); } catch (e) { return Promise.reject(e); } },
};
window.sovx = sovx;

// Dashboard-shipped highlighter for the built-in curl output. Extensions register
// their own languages (python, csharp, …) via sovx.highlighter — this is just the
// same API, dogfooded, so curl looks native out of the box.
sovx.highlighter('bash', [
  { cls: 'comment', re: /#.*/ },
  { cls: 'str', re: /'(?:[^'\\]|\\.)*'/ },
  { cls: 'str', re: /"(?:[^"\\]|\\.)*"/ },
  { cls: 'fn', re: /\b(?:curl|http|https)\b/ },
  { cls: 'kw', re: /(?:^|\s)-[A-Za-z-]+/ },
  { cls: 'num', re: /\b\d+(?:\.\d+)?\b/ },
]);
// TypeScript for the request/response schema blocks (dashboard-shipped).
sovx.highlighter('typescript', [
  { cls: 'str', re: /'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"/ },
  { cls: 'kw', re: /\b(?:interface|type|extends|readonly|keyof|typeof)\b/ },
  { cls: 'type', re: /\b(?:string|number|boolean|null|undefined|any|unknown|void|Date|bigint|symbol|Record)\b/ },
  { cls: 'num', re: /\b\d+(?:\.\d+)?\b/ },
]);

// TypeScript codegen is a dashboard built-in (every explorer gets it, no extension
// needed) — named `interface`s, nested types by reference, walked so a response
// carries its whole shape. It dogfoods sovx.codegen, so it rides the same tabbed
// schema card as extension languages, and is registered FIRST so it's the default
// tab. Replaces the server's flat inline `{ … }` string.
sovx.codegen({
  id: 'typescript', label: 'TypeScript', lang: 'typescript',
  render(shape) {
    const TS = { string: 'string', number: 'number', integer: 'number', boolean: 'boolean', object: 'Record<string, unknown>' };
    // An array field carries its element in typeName (named) or elemType (scalar);
    // resolve the element, then wrap it as `elem[]`.
    const tsType = f => {
      if (f.schemaType === 'array') return (f.typeName ? sovx.ident(f.typeName) : (TS[f.elemType] || 'unknown')) + '[]';
      return f.typeName ? sovx.ident(f.typeName) : (TS[f.schemaType] || 'unknown');
    };
    const iface = (name, fields) => {
      let out = 'interface ' + sovx.ident(name) + ' {\n';
      for (const f of fields || []) out += '  ' + f.jsonName + ': ' + tsType(f) + ';\n';
      return out + '}\n';
    };
    const seen = new Set(), chunks = [];
    const visit = (name, fields) => {
      if (seen.has(name)) return;
      seen.add(name);
      chunks.push(iface(name, fields));
      for (const f of fields || []) if (f.typeName && shape.nested && shape.nested[f.typeName]) visit(f.typeName, shape.nested[f.typeName]);
    };
    visit(shape.name, shape.fields || []);
    return chunks.join('\n');
  },
});

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

document.addEventListener('DOMContentLoaded', async () => {
  document.querySelectorAll('header nav button').forEach(btn => {
    btn.addEventListener('click', () => { state.tab = btn.dataset.tab; setTab(state.tab); route(); });
  });
  const toggle = $('#show-internal');
  if (toggle) toggle.addEventListener('change', () => { state.showInternal = toggle.checked; loadCatalog(); });
  initTheme();
  bindKeys();
  loadSettingVals();
  await loadExtensions(); // register plugin actions/hooks/settings before first render
  renderSettingsButton();
  loadCatalog();
});

// ---- extension loading ------------------------------------------------------
// Fetch the plugin asset manifest, <link> its CSS, and dynamic-import its ES
// modules; each module's default export receives the sovx SDK. Best-effort — a
// failing extension logs and is skipped, never blocking the explorer.
async function loadExtensions() {
  if (extensionsLoaded) return;
  extensionsLoaded = true;
  let manifest;
  try {
    const r = await fetch('/rpc/_explorer/extensions.json');
    if (!r.ok) return;
    manifest = await r.json();
  } catch { return; }
  for (const href of (manifest.css || [])) {
    if (document.querySelector(`link[data-sovx="${href}"]`)) continue;
    const link = el('link'); link.rel = 'stylesheet'; link.href = href; link.setAttribute('data-sovx', href);
    document.head.appendChild(link);
  }
  for (const src of (manifest.js || [])) {
    try {
      const mod = await import(src);
      const reg = mod.default || mod.register;
      if (typeof reg === 'function') reg(sovx);
    } catch (e) { console.error('sov: extension failed to load', src, e); }
  }
}

function settingsKey() { return 'sov-explorer-settings:' + location.pathname; }
function loadSettingVals() {
  try { sovx._settingVals = JSON.parse(localStorage.getItem(settingsKey()) || '{}') || {}; }
  catch { sovx._settingVals = {}; }
}
function saveSettingVals() { try { localStorage.setItem(settingsKey(), JSON.stringify(sovx._settingVals)); } catch {} }

function renderSettingsButton() {
  const btn = $('#settings-btn');
  if (!btn) return;
  btn.hidden = sovx._settings.length === 0;
}
function openSettings() {
  const fields = $('#settings-fields');
  fields.innerHTML = '';
  for (const s of sovx._settings) {
    const wrap = el('label', 'settings-field');
    wrap.innerHTML = `<span class="settings-name">${escapeHTML(s.label)}</span>`;
    const inp = el('input');
    inp.type = s.secret ? 'password' : 'text';
    inp.spellcheck = false; inp.autocomplete = 'off';
    inp.placeholder = s.placeholder || '';
    inp.value = sovx._settingVals[s.id] || '';
    inp.addEventListener('input', () => {
      if (inp.value) sovx._settingVals[s.id] = inp.value; else delete sovx._settingVals[s.id];
      saveSettingVals();
    });
    wrap.appendChild(inp);
    fields.appendChild(wrap);
  }
  $('#settings-modal').hidden = false;
  const first = fields.querySelector('input'); if (first) first.focus();
}
function closeSettings() { const m = $('#settings-modal'); if (m) m.hidden = true; }

// renderActions puts an extension's method/type buttons on the detail pane; each
// click runs the extension with a context bound to a fresh panel host.
function renderActions(detail, scope, base) {
  const acts = sovx._actions[scope];
  if (!acts.length) return;
  detail.appendChild(sectionHead('Actions'));
  const row = el('div', 'ext-actions');
  const host = el('div', 'ext-panels');
  for (const a of acts) {
    const b = el('button', 'ext-btn'); b.type = 'button'; b.textContent = a.label;
    b.addEventListener('click', () => {
      host.innerHTML = '';
      const ctx = Object.assign({ scope }, base, {
        types: (state.catalog && state.catalog.types) || {},
        panel: (opts) => extPanel(host, opts),
        copy: sovx.copy, theme: sovx.theme, el, escapeHTML,
      });
      try { a.run(ctx); }
      catch (e) { extPanel(host, { title: 'Extension error', body: `<pre>${escapeHTML(String((e && e.stack) || e))}</pre>` }); }
    });
    row.appendChild(b);
  }
  detail.appendChild(row);
  detail.appendChild(host);
}

// extPanel renders an extension output panel using the dashboard's own styling.
function extPanel(host, opts) {
  opts = opts || {};
  const p = el('div', 'ext-panel');
  const head = el('div', 'ext-panel-head');
  const t = el('span'); t.textContent = opts.title || 'output'; head.appendChild(t);
  head.appendChild(el('span', 'spacer'));
  // panel({code, lang}) is the colorized shortcut; copy defaults to that code.
  const copyText = opts.copyText !== undefined ? opts.copyText : opts.code;
  if (copyText !== undefined) head.appendChild(copyButton(() => copyText));
  p.appendChild(head);
  const body = el('div', 'ext-panel-body');
  if (opts.code !== undefined) {
    const pre = el('pre', 'code-block'); if (opts.lang) pre.dataset.lang = String(opts.lang);
    pre.innerHTML = sovx.highlight(opts.lang, opts.code);
    body.appendChild(pre);
  } else if (opts.body instanceof Node) body.appendChild(opts.body);
  else body.innerHTML = String(opts.body != null ? opts.body : '');
  p.appendChild(body);
  host.appendChild(p);
  return p;
}

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
        } else {
          // Header-bound params aren't body args, so a method with only headers
          // (e.g. whoami) still takes "no args" on the wire — denote it, and note
          // when it's headers-only rather than truly argless.
          const bodyParams = (md.params || []).filter(f => f.source !== 'header');
          if (bodyParams.length === 0) {
            const chip = el('span', 'arg-chip');
            chip.textContent = (md.params || []).some(f => f.source === 'header') ? 'headers only' : 'no args';
            a.appendChild(chip);
          }
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
  textarea.addEventListener('input', () => refreshCurl());
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
      refreshCurl();
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
      const top = el('div', 'header-input-top');
      const nm = el('span', 'header-name'); nm.textContent = f.header; top.appendChild(nm);
      if (f.schemaType) { const ty = el('span', 'header-type'); ty.textContent = f.schemaType; top.appendChild(ty); }
      if (f.required) { const req = el('span', 'required'); req.textContent = 'required'; top.appendChild(req); }
      label.appendChild(top);
      const inp = document.createElement('input');
      inp.type = 'text'; inp.spellcheck = false;
      inp.placeholder = (f.example !== undefined && f.example !== '') ? String(f.example) : (f.schemaType || 'value');
      inp.addEventListener('input', () => refreshCurl());
      label.appendChild(inp);
      if (f.desc) { const hint = el('div', 'header-hint'); hint.textContent = f.desc; label.appendChild(hint); }
      hwrap.appendChild(label);
      headerInputs[f.header] = inp;
    }
    reqPane.appendChild(hwrap);
  }

  const row = el('div', 'execute-row');
  const exec = el('button', 'execute'); exec.textContent = 'Execute'; row.appendChild(exec);
  if (md.params && md.params.some(f => f.example !== undefined && f.example !== '')) {
    const fill = el('button', 'execute ghost'); fill.textContent = 'Fill example';
    fill.addEventListener('click', () => { textarea.value = seedBody(activeShape, true); refreshCurl(); });
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
  let headersOpen = false; // sticky across executes so it doesn't collapse each run
  const hdrToggle = el('button', 'copy-btn'); hdrToggle.type = 'button'; hdrToggle.textContent = 'headers'; hdrToggle.hidden = true;
  resHead.appendChild(hdrToggle);
  const copyResp = copyButton(() => lastResponse); resHead.appendChild(copyResp);
  resPane.appendChild(resHead);
  const out = el('pre', 'placeholder'); out.textContent = '// response appears here'; resPane.appendChild(out);
  const headersBox = el('div', 'headers-box'); headersBox.hidden = true; resPane.appendChild(headersBox);
  hdrToggle.addEventListener('click', () => {
    headersOpen = !headersOpen;
    headersBox.hidden = !headersOpen;
    hdrToggle.classList.toggle('active', headersOpen);
  });
  grid.appendChild(resPane);

  detail.appendChild(grid);

  const setStatus = (label, cls, ms, size) => {
    statusPill.hidden = false; statusPill.textContent = label; statusPill.className = 'status-pill ' + cls;
    if (ms === undefined) { timing.textContent = ''; timing.className = 'timing'; return; }
    timing.className = 'timing ' + (ms < 300 ? 'fast' : ms < 1000 ? 'med' : 'slow');
    timing.textContent = fmtDur(ms) + (size !== undefined ? '  ·  ' + fmtBytes(size) : '');
  };
  const showErr = msg => { lastResponse = msg; out.className = ''; out.textContent = msg; setStatus('ERR', 'err'); };

  // buildReqObj mirrors what doExecute sends — parses the body, folds in header
  // params, and runs every extension requestHook. Shared with the curl button so
  // the copied command is byte-for-byte the request the explorer would fire.
  const buildReqObj = () => {
    let args;
    try { args = JSON.parse(textarea.value); }
    catch (e) { return { err: 'json parse error: ' + e.message }; }
    const hdrs = { 'Content-Type': 'application/json' };
    for (const name in headerInputs) { const v = headerInputs[name].value; if (v !== '') hdrs[name] = v; }
    // Let extensions mutate the outgoing request (inject auth, rewrite, etc.).
    const reqObj = { path: md.postPath, headers: hdrs, body: JSON.stringify({ args }) };
    for (const fn of sovx._requestHooks) { try { fn(reqObj); } catch (e) { console.error('sov: request hook failed', e); } }
    return { reqObj };
  };

  const doExecute = async () => {
    const built = buildReqObj();
    if (built.err) { showErr(built.err); return; }
    const reqObj = built.reqObj;
    statusPill.hidden = true; timing.textContent = '';
    out.className = 'placeholder'; out.textContent = '// executing…';
    const t0 = performance.now();
    try {
      const resp = await fetch(reqObj.path, { method: 'POST', headers: reqObj.headers, body: reqObj.body });
      const txt = await resp.text();
      const ms = Math.round(performance.now() - t0);
      const pretty = prettyJSON(txt);
      lastResponse = pretty;
      out.className = ''; out.innerHTML = highlightJSON(pretty);
      setStatus(String(resp.status), resp.status < 300 ? 'ok' : resp.status < 500 ? 'warn' : 'err', ms, new Blob([txt]).size);
      const respH = {}; resp.headers.forEach((v, k) => { respH[k] = v; });
      renderHeaders(headersBox, reqObj.headers, respH);
      hdrToggle.hidden = false;
      headersBox.hidden = !headersOpen; // keep whatever the user had open
      hdrToggle.classList.toggle('active', headersOpen);
    } catch (e) { showErr('fetch error: ' + e.message); }
  };
  exec.addEventListener('click', doExecute);
  textarea.addEventListener('keydown', e => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); doExecute(); }
  });

  // One batched "Schema" card: TypeScript + every registered codegen, tabbed by
  // language, each with pretty-printed Request + Response panes.
  const schemaGroup = methodCodegenGroup(md);
  if (schemaGroup) { detail.appendChild(sectionHead('Schema')); detail.appendChild(schemaGroup); }

  // curl — an always-visible, colorized command for the CURRENT request, rendered
  // like the TypeScript blocks (not a button). It tracks the body / header params /
  // shape live and folds in whatever an extension requestHook injects. The DISPLAY
  // masks sensitive header values (so it never leaks on a screenshot); the copy
  // button copies the real, runnable command.
  let curlReal = '';
  const curlBlk = el('div', 'ts-block');
  const curlHead = el('div', 'ts-head');
  const curlLabel = el('span'); curlLabel.textContent = 'curl'; curlHead.appendChild(curlLabel);
  curlHead.appendChild(el('span', 'spacer'));
  curlHead.appendChild(copyButton(() => curlReal));
  curlBlk.appendChild(curlHead);
  const curlPre = el('pre', 'code-block');
  curlBlk.appendChild(curlPre);
  detail.appendChild(curlBlk);
  refreshCurl();

  // refreshCurl is a hoisted declaration so the body/header/shape handlers above
  // can call it; it no-ops until curlPre exists (only fires on interaction).
  function refreshCurl() {
    if (!curlPre) return;
    const built = buildReqObj();
    if (built.err) { curlReal = ''; curlPre.className = 'code-block err'; curlPre.textContent = built.err; return; }
    curlPre.className = 'code-block';
    curlReal = buildCurl(built.reqObj);                                        // real (copy target)
    curlPre.innerHTML = sovx.highlight('bash', buildCurlMasked(built.reqObj)); // masked display
  }

  renderActions(detail, 'method', { router: rd.router, method: md.method, descriptor: md });
}

// buildCurlMasked is buildCurl with sensitive header VALUES dotted out, for the
// on-screen curl block. The copy button still copies the unmasked command.
function buildCurlMasked(reqObj) {
  const masked = { path: reqObj.path, body: reqObj.body, headers: {} };
  for (const k of Object.keys(reqObj.headers || {})) {
    const v = String(reqObj.headers[k]);
    masked.headers[k] = SENSITIVE_HEADER.test(k) ? maskHeaderValue(v) : v;
  }
  return buildCurl(masked);
}

// safeGen runs a codegen's render(shape) with a guard so a throwing emitter
// degrades to an inline comment instead of blanking the page.
function safeGen(g, shape) {
  try { return String(g.render(shape) || ''); }
  catch (e) { return '// ' + g.label + ' failed: ' + ((e && e.message) || e); }
}

// subBlock is one titled, copyable, colorized pane inside a codegen card. An empty
// title (types have no request/response split) leaves just the copy button.
function subBlock(title, code, lang) {
  const wrap = el('div', 'codegen-sub');
  const head = el('div', 'codegen-sub-head');
  const label = el('span'); label.textContent = title || ''; head.appendChild(label);
  head.appendChild(el('span', 'spacer'));
  head.appendChild(copyButton(() => code));
  wrap.appendChild(head);
  const pre = el('pre', 'code-block');
  pre.innerHTML = lang ? sovx.highlight(lang, code) : escapeHTML(code);
  wrap.appendChild(pre);
  return wrap;
}

// tabbedCode batches languages into ONE card: a tab per language, and below it the
// active language's blocks. The picked language sticks across navigation (via
// state.codeLang) so switching methods keeps you on, say, C#.
// langs: [{label, lang, blocks:[{title, code}]}]
function tabbedCode(langs) {
  const card = el('div', 'codegen-group');
  const tabs = el('div', 'codegen-tabs');
  const body = el('div', 'codegen-body');
  let active = langs.some(l => l.label === state.codeLang) ? state.codeLang : langs[0].label;
  const paint = () => {
    body.innerHTML = '';
    const l = langs.find(x => x.label === active) || langs[0];
    for (const blk of l.blocks) body.appendChild(subBlock(blk.title, blk.code, l.lang));
    tabs.querySelectorAll('button').forEach(b => b.classList.toggle('active', b.dataset.label === active));
  };
  for (const l of langs) {
    const b = el('button', 'codegen-tab'); b.type = 'button'; b.textContent = l.label; b.dataset.label = l.label;
    b.addEventListener('click', () => { active = l.label; state.codeLang = l.label; paint(); });
    tabs.appendChild(b);
  }
  card.appendChild(tabs); card.appendChild(body);
  paint();
  return card;
}

// methodCodegenGroup builds the batched schema card for a method: one tab per
// registered codegen (TypeScript built-in first, then extensions), each with
// Request + Response panes derived from the method's actual bodies. null if empty.
function methodCodegenGroup(md) {
  const shapes = methodCodeShapes(md);
  const langs = [];
  for (const g of sovx._codegens) {
    const blocks = [];
    if (shapes.request) blocks.push({ title: 'Request', code: safeGen(g, shapes.request) });
    if (shapes.response) blocks.push({ title: 'Response', code: safeGen(g, shapes.response) });
    if (blocks.length) langs.push({ label: g.label, lang: g.lang, blocks });
  }
  return langs.length ? tabbedCode(langs) : null;
}

// typeCodegenGroup is the same batched card for a type page: one tab per registered
// codegen, a single pane each (types have no request/response split).
function typeCodegenGroup(name, td) {
  const shape = { name: name, fields: td.fields || [], nested: catalogNested() };
  const langs = [];
  for (const g of sovx._codegens) langs.push({ label: g.label, lang: g.lang, blocks: [{ title: '', code: safeGen(g, shape) }] });
  return langs.length ? tabbedCode(langs) : null;
}

// catalogNested flattens the whole type catalog to {typeName: fields} so a codegen
// emitter can resolve any referenced type, wherever it is rendered.
function catalogNested() {
  const out = {};
  const types = (state.catalog && state.catalog.types) || {};
  for (const k of Object.keys(types)) out[k] = types[k].fields || [];
  return out;
}

// methodCodeShapes derives the request + response codegen shapes for a method.
// request = its non-header params; response = the declared response type resolved
// from the method's nestedTypes (merged with the full catalog). Either may be null.
function methodCodeShapes(md) {
  const nested = Object.assign(catalogNested(), md.nestedTypes || {});
  const reqFields = (md.params || []).filter(f => f.source !== 'header');
  const request = reqFields.length ? { name: (md.method || 'request') + 'Request', fields: reqFields, nested } : null;
  const response = md.responseTypeName ? { name: md.responseTypeName, fields: nested[md.responseTypeName] || [], nested } : null;
  return { request, response };
}

// Header names whose VALUE is a credential — masked by default in the headers
// view so a screenshot / shoulder-surf never leaks a token.
const SENSITIVE_HEADER = /^(authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key|x-auth-token|x-amz-security-token|x-csrf-token)$/i;

// maskHeaderValue keeps an auth SCHEME visible (Bearer/Basic/…) and dots out the
// credential; a bare value is fully dotted.
function maskHeaderValue(value) {
  const m = /^(\S+)\s+(.+)$/.exec(value);
  if (m && /^(bearer|basic|digest|token)$/i.test(m[1])) {
    return m[1] + ' ' + '•'.repeat(Math.min(10, m[2].length));
  }
  return '•'.repeat(Math.min(12, Math.max(6, value.length)));
}

// renderHeaders shows the request headers ACTUALLY sent (including anything an
// extension's requestHook injected) alongside the response headers. Sensitive
// values are masked; click one to reveal/hide. Built as DOM (raw values live in
// closures, never in markup) so nothing is injectable and nothing leaks.
function renderHeaders(box, reqH, respH) {
  box.innerHTML = '';
  box.appendChild(headerGroup('request', reqH));
  box.appendChild(headerGroup('response', respH));
}

function headerGroup(title, obj) {
  const g = el('div', 'hgroup');
  const t = el('div', 'hgroup-title'); t.textContent = title; g.appendChild(t);
  const keys = Object.keys(obj || {}).sort((a, b) => a.toLowerCase() < b.toLowerCase() ? -1 : 1);
  if (!keys.length) {
    const none = el('div', 'hrow hempty'); none.textContent = 'none'; g.appendChild(none);
    return g;
  }
  for (const k of keys) {
    const val = String(obj[k]);
    const row = el('div', 'hrow');
    const kk = el('span', 'hk'); kk.textContent = k; row.appendChild(kk);
    const vv = el('span', 'hv');
    if (SENSITIVE_HEADER.test(k)) {
      vv.classList.add('sensitive');
      vv.title = 'click to reveal / hide';
      let revealed = false;
      const paint = () => { vv.textContent = revealed ? val : maskHeaderValue(val); };
      paint();
      vv.addEventListener('click', () => { revealed = !revealed; paint(); vv.classList.toggle('revealed', revealed); });
    } else {
      vv.textContent = val;
    }
    row.appendChild(vv);
    g.appendChild(row);
  }
  return g;
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

  // Extension codegen for the whole type — one batched card, tabbed by language,
  // same look as the method schema card.
  const codeGroup = typeCodegenGroup(name, td);
  if (codeGroup) { detail.appendChild(sectionHead('Codegen')); detail.appendChild(codeGroup); }

  renderActions(detail, 'type', { name: name, descriptor: td });
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
    if (e.key === 'Escape') { closeSettings(); }
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
  const setBtn = $('#settings-btn');
  if (setBtn) setBtn.addEventListener('click', openSettings);
  const setModal = $('#settings-modal');
  if (setModal) setModal.addEventListener('click', ev => { if (ev.target.id === 'settings-modal') closeSettings(); });
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
