// mini-base admin dashboard. No build step: Preact + htm tagged templates.
import { html, render, useState, useEffect, useRef, useCallback } from './vendor/htm-preact.js';

// ---------------------------------------------------------------- API ----

class APIError extends Error {
  constructor(message, status, code) { super(message); this.status = status; this.code = code; }
}

async function api(method, path, body, contentType) {
  const headers = { 'X-Minibase-Admin': '1' };
  const raw = body instanceof Blob;
  if (body !== undefined) headers['Content-Type'] = raw ? (contentType || 'application/octet-stream') : 'application/json';
  const res = await fetch('api' + path, {
    method, headers, credentials: 'same-origin',
    body: body === undefined ? undefined : raw ? body : JSON.stringify(body),
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    if (res.status === 401 && path !== '/login') window.dispatchEvent(new Event('signed-out'));
    throw new APIError(data?.error?.message || `Request failed (${res.status})`, res.status, data?.error?.code);
  }
  return data;
}

const enc = encodeURIComponent;
const tablePath = (schema, name) => `/tables/${enc(schema)}/${enc(name)}`;
const tableHref = (schema, name, tab) => `#/tables/${enc(schema)}/${enc(name)}${tab ? '/' + tab : ''}`;

// Settings rarely change while the dashboard is open; fetch them once.
let settingsPromise = null;
function getSettings() {
  settingsPromise ||= api('GET', '/settings').catch((err) => { settingsPromise = null; throw err; });
  return settingsPromise;
}

const ROLE_NAMES = { minibase_anon: 'anon', minibase_authenticated: 'signed-in users', public: 'everyone' };

// ------------------------------------------------------------ helpers ----

function toast(message, bad = false) {
  window.dispatchEvent(new CustomEvent('toast', { detail: { message, bad } }));
}

function useHashRoute() {
  const read = () => (location.hash.replace(/^#\/?/, '') || 'overview').split('/').map(decodeURIComponent);
  const [route, setRoute] = useState(read);
  useEffect(() => {
    const on = () => setRoute(read());
    addEventListener('hashchange', on);
    return () => removeEventListener('hashchange', on);
  }, []);
  return route;
}

function useLoad(fn, deps) {
  const [state, setState] = useState({ loading: true, data: null, error: null });
  const reload = useCallback(() => {
    setState((s) => ({ ...s, loading: true, error: null }));
    fn().then((data) => setState({ loading: false, data, error: null }),
      (error) => setState({ loading: false, data: null, error }));
  }, deps);
  useEffect(reload, [reload]);
  return [state, reload];
}

const fmtBytes = (n) => {
  const u = ['bytes', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${i ? n.toFixed(1) : n} ${u[i]}`;
};
const fmtDate = (s) => (s ? new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : 'Never');
const plural = (n, one, many) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
const sqlText = (stmts) => stmts.map((st) => st + ';').join('\n\n');

async function copy(text, what = 'Copied') {
  try { await navigator.clipboard.writeText(text); toast(what); } catch { toast('Copy failed; select the text instead', true); }
}

function CopyButton({ text, label = 'Copy', what }) {
  return html`<button type="button" class="small" onClick=${() => copy(text, what)}>${label}</button>`;
}

// usePreview asks the server for the SQL a form would run (?preview=1),
// shortly after the form stops changing. Errors double as validation.
function usePreview(fn, deps, enabled = true) {
  const [state, setState] = useState({ sql: '', error: '' });
  useEffect(() => {
    if (!enabled) { setState({ sql: '', error: '' }); return undefined; }
    let live = true;
    const t = setTimeout(() => fn().then(
      (r) => live && setState({ sql: sqlText(r.sql), error: '' }),
      (err) => live && setState({ sql: '', error: err.message })), 300);
    return () => { live = false; clearTimeout(t); };
  }, [...deps, enabled]);
  return state;
}

function SQLPreview({ preview, idle }) {
  if (preview.error) return html`<p class="preview-note error-text">${preview.error}</p>`;
  if (!preview.sql) return idle ? html`<p class="preview-note">${idle}</p>` : null;
  return html`<div class="sql-preview"><small>Will run</small><pre>${preview.sql}</pre></div>`;
}

function display(v) {
  if (v === null || v === undefined) return html`<span class="null">null</span>`;
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}

function RLSMark({ table }) {
  if (table.kind !== 'table') {
    return html`<span class="rls na" title="Views follow the RLS of the tables they read" aria-hidden="true"></span>`;
  }
  return table.rls_enabled
    ? html`<span class="rls" title="Row level security on" aria-hidden="true"></span>`
    : html`<span class="rls off" title="Row level security off" aria-hidden="true"></span>`;
}

function Loading() { return html`<p class="boot">Loading…</p>`; }
function Failed({ error, retry }) {
  return html`<div class="empty"><h3>Couldn't load this page</h3><p>${error.message}</p>
    ${retry && html`<p style="margin-top:12px"><button onClick=${retry}>Try again</button></p>`}</div>`;
}

// ------------------------------------------------------------ sign-in ----

function SignIn({ onSignedIn }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    setBusy(true); setError('');
    try {
      const r = await api('POST', '/login', { email: f.get('email'), password: f.get('password') });
      onSignedIn(r.account);
    } catch (err) {
      setError(err.status === 429 ? 'Too many attempts. Wait a minute, then try again.' : err.message);
    } finally { setBusy(false); }
  };
  return html`
    <div class="signin">
      <form onSubmit=${submit}>
        <div class="wordmark"><i></i>mini-base</div>
        <div>
          <h1>Sign in to the dashboard</h1>
          <p>Use an admin account. Create one with <code>minibase admin create</code>.</p>
        </div>
        <label class="field"><span>Email</span><input name="email" type="email" autocomplete="username" required autofocus /></label>
        <label class="field"><span>Password</span><input name="password" type="password" autocomplete="current-password" required /></label>
        ${error && html`<p class="error-text" role="alert">${error}</p>`}
        <button class="primary" disabled=${busy}>${busy ? 'Signing in…' : 'Sign in'}</button>
      </form>
    </div>`;
}

// ------------------------------------------------------------- shell -----

const SECTIONS = [
  [['overview', 'Overview'], ['tables', 'Tables'], ['sql', 'SQL editor'], ['users', 'Users']],
  [['api', 'API and keys'], ['audit', 'Audit log'], ['admins', 'Admins'], ['settings', 'Settings']],
];

function Shell({ account, route, onSignOut, children }) {
  const [pwOpen, setPwOpen] = useState(false);
  return html`
    <div class="shell">
      <aside class="rail">
        <div class="wordmark"><i></i>mini-base</div>
        <nav aria-label="Sections">
          ${SECTIONS.map((group, i) => html`
            ${i > 0 && html`<hr />`}
            ${group.map(([id, label]) => html`
              <a href=${'#/' + id} aria-current=${route[0] === id ? 'page' : undefined}>${label}</a>`)}`)}
        </nav>
        <div class="who">
          <span class="email" title=${account.email}>${account.email}</span>
          <div class="actions">
            <button class="quiet small" onClick=${() => setPwOpen(true)}>Change password</button>
            <button class="quiet small" onClick=${onSignOut}>Sign out</button>
          </div>
        </div>
      </aside>
      <main>${children}</main>
      ${pwOpen && html`<${PasswordDialog} onClose=${() => setPwOpen(false)} />`}
    </div>`;
}

function PasswordDialog({ onClose }) {
  const ref = useRef();
  const [error, setError] = useState('');
  useEffect(() => { ref.current.showModal(); }, []);
  const submit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    if (f.get('new') !== f.get('repeat')) { setError('The new passwords don’t match.'); return; }
    try {
      await api('POST', '/password', { current_password: f.get('current'), new_password: f.get('new') });
      toast('Password changed. Sign in again.');
      window.dispatchEvent(new Event('signed-out'));
    } catch (err) { setError(err.message); }
  };
  return html`
    <dialog ref=${ref} onClose=${onClose}>
      <form onSubmit=${submit}>
        <h2>Change password</h2>
        <label class="field"><span>Current password</span><input name="current" type="password" autocomplete="current-password" required /></label>
        <label class="field"><span>New password</span><input name="new" type="password" autocomplete="new-password" minlength="12" required />
          <small>At least 12 characters. Every session, including this one, will be signed out.</small></label>
        <label class="field"><span>Repeat new password</span><input name="repeat" type="password" autocomplete="new-password" required /></label>
        ${error && html`<p class="error-text" role="alert">${error}</p>`}
        <div class="row"><button type="button" onClick=${onClose}>Cancel</button><button class="primary">Change password</button></div>
      </form>
    </dialog>`;
}

// ----------------------------------------------------------- overview ----

function Overview() {
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', '/overview'), []);
  if (loading && !data) return html`<${Loading} />`;
  if (error) return html`<${Failed} error=${error} retry=${reload} />`;
  const open = data.tables_without_rls;
  return html`
    <div class="page-head"><div><h1>Overview</h1></div></div>
    <div class="report">
      ${open.length === 0
        ? html`<section class="posture">
            <div><h2>Every table is protected by row level security</h2>
            <p>Requests with the anon key or a user's token only see rows your policies allow.</p></div>
          </section>`
        : html`<section class="posture open">
            <div>
              <h2>${plural(open.length, 'table is', 'tables are')} not protected by row level security</h2>
              <p>The API refuses anon and signed-in requests to ${open.length === 1 ? 'it' : 'them'} until RLS is turned on.
                Only the service key can reach ${open.length === 1 ? 'it' : 'them'} now.</p>
              <ul>${open.map((t) => html`<li><a href=${`#/tables/${enc(t.schema)}/${enc(t.name)}`}><code>${t.schema}.${t.name}</code></a></li>`)}</ul>
            </div>
          </section>`}

      <section class="section">
        <h2>Server</h2>
        <dl class="facts">
          <dt>Version</dt><dd><code>${data.server.version}</code></dd>
          <dt>Environment</dt><dd>${data.server.env}</dd>
          <dt>Modules</dt><dd><div class="chips">${data.server.modules.map((m) => html`<span class="chip">${m}</span>`)}</div></dd>
        </dl>
      </section>

      <section class="section">
        <h2>Database</h2>
        <dl class="facts">
          <dt>Name</dt><dd><code>${data.database.name}</code></dd>
          <dt>PostgreSQL</dt><dd>${data.database.version}</dd>
          <dt>Size</dt><dd>${fmtBytes(data.database.size_bytes)}</dd>
          <dt>Tables and views</dt><dd><a href="#/tables">${data.tables.toLocaleString()}</a></dd>
          ${data.users !== null && html`<dt>App users</dt><dd><a href="#/users">${data.users.toLocaleString()}</a></dd>`}
        </dl>
      </section>
    </div>`;
}

// ------------------------------------------------------------- tables ----

function Tables({ route }) {
  const [, schema, name, tab] = route;
  const designing = schema === 'new' && name === undefined;
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', '/tables'), []);
  const [filter, setFilter] = useState('');

  useEffect(() => {
    const on = () => reload();
    addEventListener('schema-changed', on);
    return () => removeEventListener('schema-changed', on);
  }, [reload]);

  if (loading && !data) return html`<${Loading} />`;
  if (error) return html`<${Failed} error=${error} retry=${reload} />`;
  const tables = data.tables.filter((t) => `${t.schema}.${t.name}`.includes(filter.trim().toLowerCase()));

  if (data.tables.length === 0 && !designing) {
    return html`
      <div class="page-head"><div><h1>Tables</h1></div></div>
      <div class="empty"><h3>No tables yet</h3>
        <p>Tables in the <code>public</code> schema are served at <code>/rest/v1/<i>table</i></code>.</p>
        <p style="margin-top:12px"><a class="button primary" href="#/tables/new">New table</a></p></div>`;
  }
  return html`
    <div class="split">
      <div class="table-list">
        <div class="list-head">
          <input type="search" placeholder="Filter tables" aria-label="Filter tables" value=${filter} onInput=${(e) => setFilter(e.target.value)} />
          <a class="button" href="#/tables/new" aria-current=${designing ? 'page' : undefined}>New</a>
        </div>
        <ul>
          ${tables.map((t) => html`
            <li><a href=${`#/tables/${enc(t.schema)}/${enc(t.name)}`} aria-current=${t.schema === schema && t.name === name ? 'page' : undefined}>
              <${RLSMark} table=${t} />
              <span class="name">${t.schema !== 'public' && html`<span class="schema">${t.schema}.</span>`}${t.name}</span>
              <span class="count">${t.kind === 'table' ? t.estimated_rows.toLocaleString() : t.kind.replace('_', ' ')}</span>
            </a></li>`)}
        </ul>
        <div class="legend"><span><i class="rls"></i>RLS on</span><span><i class="rls off"></i>RLS off</span></div>
      </div>
      <div>
        ${designing
          ? html`<${TableDesigner} tables=${data.tables} onCreated=${reload} />`
          : schema
            ? html`<${TableDetail} key=${schema + '.' + name} schema=${schema} name=${name} tab=${tab} onChanged=${reload} />`
            : html`<div class="empty"><h3>Choose a table</h3><p>Browse and edit its rows, see its columns and its row level security policies, or create a new table.</p></div>`}
      </div>
    </div>`;
}

const TABS = ['rows', 'structure', 'policies', 'api'];

function TableDetail({ schema, name, tab: routeTab, onChanged }) {
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', tablePath(schema, name)), [schema, name]);
  const tab = TABS.includes(routeTab) ? routeTab : 'rows';
  const setTab = (t) => { location.hash = tableHref(schema, name, t === 'rows' ? '' : t); };
  const [busy, setBusy] = useState(false);
  const changed = () => { reload(); onChanged(); };

  if (loading && !data) return html`<${Loading} />`;
  if (error) return html`<${Failed} error=${error} retry=${reload} />`;
  const { editable, primary_key: pk = [] } = data;
  const table = { ...data.table, columns: data.table.columns || [], policy_list: data.table.policy_list || [] };

  const setRLS = async (enabled) => {
    setBusy(true);
    try {
      await api('POST', tablePath(schema, name) + '/rls', { enabled });
      toast(enabled ? 'Row level security turned on' : 'Row level security turned off');
      reload(); onChanged();
    } catch (err) { toast(err.message, true); } finally { setBusy(false); }
  };

  const kindLabel = { table: 'Table', view: 'View', materialized_view: 'Materialized view', foreign_table: 'Foreign table' }[table.kind];
  return html`
    <div class="table-head">
      <div>
        <h1><span class="schema">${table.schema}.</span>${table.name}</h1>
        <p>${kindLabel} with ${plural(table.columns.length, 'column', 'columns')}.${pk.length ? ` Primary key: ${pk.join(', ')}.` : ''}</p>
      </div>
      ${table.kind === 'table' && table.rls_enabled &&
        html`<button disabled=${busy} onClick=${() => setRLS(false)}>Turn off row level security</button>`}
    </div>

    ${table.kind === 'table' && !table.rls_enabled && html`
      <div class="callout" role="note">
        <div><strong>Row level security is off.</strong> Anon and signed-in requests to this table are refused; only the service key works.
          Turn it on, then add policies to say who may read and write which rows.</div>
        <button class="primary" disabled=${busy} onClick=${() => setRLS(true)}>Turn on row level security</button>
      </div>`}

    <div class="tabs" role="tablist">
      ${[['rows', 'Rows'], ['structure', 'Structure'], ['policies', `Policies (${table.policy_list.length})`], ['api', 'API']].map(([id, label]) => html`
        <button role="tab" aria-selected=${tab === id} onClick=${() => setTab(id)}>${label}</button>`)}
    </div>

    ${tab === 'rows' && html`<${Rows} table=${table} editable=${editable} pk=${pk} onChanged=${changed} />`}
    ${tab === 'structure' && html`<${Structure} table=${table} onChanged=${changed} />`}
    ${tab === 'policies' && html`<${Policies} table=${table} onChanged=${changed} />`}
    ${tab === 'api' && html`<${TableAPI} table=${table} pk=${pk} />`}`;
}

const PAGE = 50;

const FILTER_OPS = [
  ['eq', 'equals'], ['neq', 'is not'], ['contains', 'contains'], ['lt', 'less than'], ['lte', 'at most'],
  ['gt', 'greater than'], ['gte', 'at least'], ['is_null', 'is null'], ['not_null', 'is not null'],
];
const valueless = (op) => op === 'is_null' || op === 'not_null';

function Rows({ table, editable, pk, onChanged }) {
  const [page, setPage] = useState(0);
  const [sort, setSort] = useState(null); // { column, desc }
  const [editing, setEditing] = useState(null); // { row } or { row: null } for a new row
  const [filters, setFilters] = useState([]); // { column, op, value }
  const [applied, setApplied] = useState([]);
  const [importing, setImporting] = useState(false);
  const path = tablePath(table.schema, table.name);

  // Apply filter edits shortly after typing stops; skip rows still missing a value.
  useEffect(() => {
    const t = setTimeout(() => { setPage(0); setApplied(filters.filter((f) => valueless(f.op) || f.value !== '')); }, 350);
    return () => clearTimeout(t);
  }, [filters]);

  const filterQS = applied.map((f) => `&filter=${enc(`${f.column}:${f.op}:${valueless(f.op) ? '' : f.value}`)}`).join('');
  const sortQS = sort ? `&sort=${enc(sort.column)}&dir=${sort.desc ? 'desc' : 'asc'}` : '';
  const query = `?limit=${PAGE}&offset=${page * PAGE}${sortQS}${filterQS}`;
  const exportHref = `api${path}/export?x=1${sortQS}${filterQS}`;
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', path + '/rows' + query), [path, query]);
  const setFilter = (i, patch) => setFilters((fs) => fs.map((f, j) => (j === i ? { ...f, ...patch } : f)));

  const toggleSort = (column) => {
    setPage(0);
    setSort((s) => (!s || s.column !== column ? { column, desc: false } : s.desc ? null : { column, desc: true }));
  };

  const rows = data?.rows || [];
  const unknownTotal = data?.total === -1;
  const total = unknownTotal ? Infinity : (data?.total ?? 0);
  const from = rows.length === 0 ? 0 : page * PAGE + 1;
  const to = page * PAGE + rows.length;
  const of = unknownTotal ? 'many' : `${data?.total_exact === false ? 'about ' : ''}${(data?.total ?? 0).toLocaleString()}`;

  return html`
    <div class="toolbar wrap">
      ${table.kind === 'table' && html`<button class="primary" onClick=${() => setEditing({ row: null })}>Add row</button>`}
      <button onClick=${() => setFilters([...filters, { column: table.columns[0]?.name, op: 'eq', value: '' }])}>Filter</button>
      <a class="button" href=${exportHref} download>Export CSV</a>
      ${table.kind === 'table' && html`<button onClick=${() => setImporting(true)}>Import CSV</button>`}
      <span class="spacer"></span>
      <span class="range">${from.toLocaleString()}–${to.toLocaleString()} of ${of}</span>
      <button class="small" disabled=${page === 0} onClick=${() => setPage(page - 1)}>Previous</button>
      <button class="small" disabled=${to >= total || rows.length < PAGE} onClick=${() => setPage(page + 1)}>Next</button>
    </div>
    ${filters.length > 0 && html`
      <div class="filters">
        ${filters.map((f, i) => html`
          <div class="filter">
            <span class="where">${i === 0 ? 'Where' : 'and'}</span>
            <select aria-label="Column" value=${f.column} onChange=${(e) => setFilter(i, { column: e.target.value })}>
              ${table.columns.map((c) => html`<option value=${c.name}>${c.name}</option>`)}
            </select>
            <select aria-label="Condition" value=${f.op} onChange=${(e) => setFilter(i, { op: e.target.value })}>
              ${FILTER_OPS.map(([op, label]) => html`<option value=${op}>${label}</option>`)}
            </select>
            ${!valueless(f.op) && html`<input class="mono" aria-label="Value" placeholder="value" value=${f.value}
              onInput=${(e) => setFilter(i, { value: e.target.value })} />`}
            <button class="small quiet" aria-label="Remove filter" onClick=${() => setFilters(filters.filter((_, j) => j !== i))}>Remove</button>
          </div>`)}
      </div>`}
    ${table.kind === 'table' && !editable && html`<p class="hint">Add a primary key to edit or delete rows here.</p>`}
    ${error && html`<${Failed} error=${error} retry=${reload} />`}
    <div class="grid-wrap">
      <table class="grid">
        <thead><tr>${table.columns.map((c) => html`
          <th aria-sort=${sort?.column === c.name ? (sort.desc ? 'descending' : 'ascending') : undefined}>
            <button onClick=${() => toggleSort(c.name)} title="Sort by ${c.name}">
              ${c.name} <small>${c.type}</small>${sort?.column === c.name ? (sort.desc ? ' ↓' : ' ↑') : ''}
            </button>
          </th>`)}</tr></thead>
        <tbody>
          ${rows.map((row) => html`
            <tr class=${editable ? 'clickable' : ''} onClick=${editable ? () => setEditing({ row }) : undefined}
              tabindex=${editable ? 0 : undefined} onKeyDown=${editable ? (e) => e.key === 'Enter' && setEditing({ row }) : undefined}>
              ${table.columns.map((c) => html`<td title=${typeof row[c.name] === 'string' ? row[c.name] : undefined}>${display(row[c.name])}</td>`)}
            </tr>`)}
        </tbody>
      </table>
      ${!loading && !error && rows.length === 0 && html`<div class="empty"><h3>${applied.length ? 'No rows match' : 'No rows'}</h3>
        <p>${applied.length ? 'Change or remove a filter.' : table.kind === 'table' ? 'Add one here, import a CSV file, or use the REST API.' : 'This view returns no rows.'}</p></div>`}
      ${loading && !data && html`<${Loading} />`}
    </div>
    ${editing && html`<${RowDrawer} table=${table} pk=${pk} row=${editing.row} path=${path}
      onClose=${() => setEditing(null)} onSaved=${() => { setEditing(null); reload(); }} />`}
    ${importing && html`<${ImportDialog} table=${table} path=${path}
      onClose=${() => setImporting(false)} onImported=${() => { setImporting(false); reload(); onChanged(); }} />`}`;
}

function ImportDialog({ table, path, onClose, onImported }) {
  const ref = useRef();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => { ref.current.showModal(); }, []);
  const writable = table.columns.filter((c) => !c.generated).map((c) => c.name);
  const submit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    const file = f.get('file');
    if (!file || !file.size) { setError('Choose a CSV file.'); return; }
    setBusy(true); setError('');
    try {
      const r = await api('POST', `${path}/import?empty=${f.get('empty') ? 'null' : 'string'}`, file, 'text/csv');
      toast(`Imported ${plural(r.rows, 'row', 'rows')}${r.ignored.length ? `; skipped ${r.ignored.join(', ')} (set by the database)` : ''}`);
      onImported();
    } catch (err) { setError(err.message); setBusy(false); }
  };
  return html`
    <dialog ref=${ref} onClose=${onClose}>
      <form onSubmit=${submit}>
        <h2>Import rows into ${table.name}</h2>
        <p class="muted">The first line must name the columns, for example <code>${writable.slice(0, 3).join(',')}</code>.
          Columns you leave out get their defaults. Every row is added, or none if one fails.</p>
        <label class="field"><span>CSV file</span><input name="file" type="file" accept=".csv,text/csv" required /></label>
        <label class="check"><input type="checkbox" name="empty" checked /> Treat empty cells as null</label>
        ${error && html`<p class="error-text" role="alert">${error}</p>`}
        <div class="row"><button type="button" onClick=${onClose}>Cancel</button>
          <button class="primary" disabled=${busy}>${busy ? 'Importing…' : 'Import'}</button></div>
      </form>
    </dialog>`;
}

// Values are sent as JSON. Postgres converts them with each column's type
// (json_populate_record), so text input works for numbers, booleans, dates…
// except json/jsonb columns, whose input must itself be JSON.
const isJSONType = (t) => t === 'json' || t === 'jsonb';

function initialField(col, row) {
  if (!row) return { text: '', isNull: false, touched: false };
  const v = row[col.name];
  if (v === null || v === undefined) return { text: '', isNull: true, touched: false };
  return { text: typeof v === 'object' ? JSON.stringify(v, null, 2) : String(v), isNull: false, touched: false };
}

function RowDrawer({ table, pk, row, path, onClose, onSaved }) {
  const isNew = row === null;
  const [fields, setFields] = useState(() => Object.fromEntries(table.columns.map((c) => [c.name, initialField(c, row)])));
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    const onKey = (e) => e.key === 'Escape' && onClose();
    addEventListener('keydown', onKey);
    return () => removeEventListener('keydown', onKey);
  }, [onClose]);

  const set = (name, patch) => setFields((f) => ({ ...f, [name]: { ...f[name], ...patch, touched: true } }));
  const key = () => Object.fromEntries(pk.map((k) => [k, row[k]]));

  const save = async (e) => {
    e.preventDefault();
    setError('');
    const values = {};
    for (const c of table.columns) {
      const f = fields[c.name];
      if (c.generated || !f.touched) continue;
      if (isNew && !f.isNull && f.text === '') continue; // let the default apply
      if (f.isNull) { values[c.name] = null; continue; }
      if (isJSONType(c.type)) {
        try { values[c.name] = JSON.parse(f.text); } catch { setError(`${c.name} must be valid JSON.`); return; }
      } else values[c.name] = f.text;
    }
    if (!isNew && Object.keys(values).length === 0) { onClose(); return; }
    setBusy(true);
    try {
      if (isNew) await api('POST', path + '/rows', { values });
      else await api('PATCH', path + '/rows', { key: key(), values });
      toast(isNew ? 'Row added' : 'Changes saved');
      onSaved();
    } catch (err) { setError(err.message); } finally { setBusy(false); }
  };

  const remove = async () => {
    if (!confirmDelete) { setConfirmDelete(true); return; }
    setBusy(true);
    try {
      await api('DELETE', path + '/rows', { key: key() });
      toast('Row deleted');
      onSaved();
    } catch (err) { setError(err.message); setBusy(false); }
  };

  return html`
    <div class="drawer-backdrop" onClick=${onClose}></div>
    <form class="drawer" role="dialog" aria-modal="true" aria-label=${isNew ? 'Add row' : 'Edit row'} onSubmit=${save}>
      <header><h2>${isNew ? `Add a row to ${table.name}` : `Edit row in ${table.name}`}</h2></header>
      <div class="body">
        ${table.columns.map((c, i) => {
          const f = fields[c.name];
          const Tag = isJSONType(c.type) ? 'textarea' : 'input';
          const hint = c.generated ? 'Set by the database'
            : isNew && c.default ? `Leave empty to use the default: ${c.default}` : null;
          return html`
            <label class="field">
              <span class="col-name">${c.name}${c.primary_key ? ' (key)' : ''} <code>${c.type}</code></span>
              <${Tag} class="mono" value=${f.isNull ? '' : f.text} disabled=${c.generated || f.isNull} autofocus=${i === 0 && !c.generated}
                placeholder=${f.isNull ? 'null' : ''} rows=${Tag === 'textarea' ? 4 : undefined}
                onInput=${(e) => set(c.name, { text: e.target.value })} />
              ${c.nullable && !c.generated && html`
                <span class="null-toggle"><input type="checkbox" checked=${f.isNull} onChange=${(e) => set(c.name, { isNull: e.target.checked })} /> null</span>`}
              ${hint && html`<small>${hint}</small>`}
            </label>`;
        })}
        ${error && html`<p class="error-text" role="alert">${error}</p>`}
      </div>
      <footer>
        <button class="primary" disabled=${busy}>${isNew ? 'Add row' : 'Save changes'}</button>
        <button type="button" onClick=${onClose}>Cancel</button>
        <span class="spacer"></span>
        ${!isNew && html`<button type="button" class=${'danger' + (confirmDelete ? ' confirm' : '')} disabled=${busy} onClick=${remove}>
          ${confirmDelete ? 'Confirm delete' : 'Delete row'}</button>`}
      </footer>
    </form>`;
}

function Structure({ table, onChanged }) {
  const [adding, setAdding] = useState(false);
  const [confirm, setConfirm] = useState(null);
  const path = tablePath(table.schema, table.name);
  const isTable = table.kind === 'table';
  const drop = async (col) => {
    if (confirm !== col) { setConfirm(col); return; }
    try {
      await api('DELETE', `${path}/columns/${enc(col)}`);
      toast(`Dropped column ${col}`); setConfirm(null); onChanged();
    } catch (err) { toast(err.message, true); }
  };
  return html`
    ${isTable && html`<div class="toolbar"><button class=${adding ? '' : 'primary'} onClick=${() => setAdding(!adding)}>${adding ? 'Close' : 'Add column'}</button></div>`}
    ${adding && html`<${AddColumn} table=${table} path=${path} onAdded=${() => { setAdding(false); onChanged(); }} />`}
    <table class="plain">
      <thead><tr><th>Column</th><th>Type</th><th>Nullable</th><th>Default</th>${isTable && html`<th></th>`}</tr></thead>
      <tbody>${table.columns.map((c) => html`
        <tr>
          <td><code>${c.name}</code>${c.primary_key ? html` <span class="chip">primary key</span>` : ''}${c.generated ? html` <span class="chip">generated</span>` : ''}</td>
          <td><code>${c.type}</code></td>
          <td>${c.nullable ? 'Yes' : 'No'}</td>
          <td>${c.default ? html`<code>${c.default}</code>` : html`<span class="null">none</span>`}</td>
          ${isTable && html`<td style="text-align:right">${!c.primary_key && html`
            <button class=${'small danger' + (confirm === c.name ? ' confirm' : '')} onClick=${() => drop(c.name)}>
              ${confirm === c.name ? 'Confirm drop' : 'Drop'}</button>`}</td>`}
        </tr>`)}</tbody>
    </table>
    <${DropTable} table=${table} path=${path} />`;
}

function AddColumn({ table, path, onAdded }) {
  const [col, setCol] = useState(blankColumn);
  const [busy, setBusy] = useState(false);
  const refs = useRefTargets();
  const ready = col.name.trim() !== '';
  const preview = usePreview(() => api('POST', `${path}/columns?preview=1`, columnSpec(col)), [JSON.stringify(col)], ready);
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('POST', `${path}/columns`, columnSpec(col));
      toast(`Added column ${col.name}`); onAdded();
    } catch (err) { toast(err.message, true); } finally { setBusy(false); }
  };
  return html`
    <form class="designer-card" onSubmit=${submit}>
      <div class="columns-head" aria-hidden="true"><span>Name</span><span>Type</span><span>Default</span><span>Links to</span><span></span></div>
      <${ColumnFields} col=${col} onChange=${(p) => setCol({ ...col, ...p })} refs=${refs} />
      ${!col.nullable && !col.def && html`<p class="hint">A required column without a default can only be added while the table is empty.</p>`}
      <${SQLPreview} preview=${preview} />
      <div><button class="primary" disabled=${busy || !ready || !!preview.error}>Add column</button></div>
    </form>`;
}

function DropTable({ table, path }) {
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const label = table.kind === 'table' ? 'table' : table.kind.replace('_', ' ');
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('DELETE', path, { confirm: typed });
      toast(`Dropped ${table.schema}.${table.name}`);
      window.dispatchEvent(new Event('schema-changed'));
      location.hash = '#/tables';
    } catch (err) { toast(err.message, true); setBusy(false); }
  };
  return html`
    <form class="danger-zone" onSubmit=${submit}>
      <div><h3>Drop this ${label}</h3>
        <p>Deletes it and all of its rows. This can't be undone. Postgres refuses if other objects depend on it.</p></div>
      <div class="confirm-row">
        <input class="mono" aria-label=${`Type ${table.name} to confirm`} placeholder=${table.name} value=${typed} onInput=${(e) => setTyped(e.target.value)} />
        <button class="danger" disabled=${busy || typed !== table.name}>Drop ${label}</button>
      </div>
    </form>`;
}

function Policies({ table, onChanged }) {
  const [adding, setAdding] = useState(false);
  const [confirm, setConfirm] = useState(null);
  const path = tablePath(table.schema, table.name);
  if (table.kind !== 'table') {
    return html`<div class="empty"><h3>Views don't have policies</h3>
      <p>A view created <code>WITH (security_invoker = true)</code> applies the policies of the tables it reads.</p></div>`;
  }
  const drop = async (name) => {
    if (confirm !== name) { setConfirm(name); return; }
    try {
      await api('DELETE', `${path}/policies/${enc(name)}`);
      toast(`Removed policy ${name}`); setConfirm(null); onChanged();
    } catch (err) { toast(err.message, true); }
  };
  return html`
    <div class="toolbar"><button class=${adding ? '' : 'primary'} onClick=${() => setAdding(!adding)}>${adding ? 'Close' : 'Add policy'}</button></div>
    ${adding && html`<${AddPolicy} table=${table} path=${path} onAdded=${() => { setAdding(false); onChanged(); }} />`}
    ${table.policy_list.length === 0
      ? html`<div class="empty"><h3>No policies</h3>
          <p>${table.rls_enabled ? 'With row level security on and no policies, anon and signed-in requests see no rows at all.' : 'Turn on row level security, then add policies.'}</p></div>`
      : table.policy_list.map((p) => html`
          <div class="policy">
            <div class="policy-head">
              <h3>${p.name}</h3>
              <button class=${'small danger' + (confirm === p.name ? ' confirm' : '')} onClick=${() => drop(p.name)}>${confirm === p.name ? 'Confirm remove' : 'Remove'}</button>
            </div>
            <p class="meta">${p.command === 'ALL' ? 'All commands' : p.command} for ${(p.roles || []).map((r) => ROLE_NAMES[r] || r).join(', ')}${p.permissive ? '' : ' (restrictive)'}</p>
            ${p.using && html`<div><small>Rows visible when</small><pre>${p.using}</pre></div>`}
            ${p.with_check && html`<div><small>Writes allowed when</small><pre>${p.with_check}</pre></div>`}
          </div>`)}`;
}

function AddPolicy({ table, path, onAdded }) {
  const [{ data: meta }] = useLoad(() => api('GET', '/designer'), []);
  const uuidCols = table.columns.filter((c) => c.type === 'uuid').map((c) => c.name);
  const [spec, setSpec] = useState({
    template: 'owner_all', name: '', column: uuidCols.find((c) => /user|owner/.test(c)) || uuidCols[0] || '',
    command: 'SELECT', roles: ['authenticated'], using: '', with_check: '',
  });
  const [busy, setBusy] = useState(false);
  const set = (p) => setSpec({ ...spec, ...p });
  const templates = meta?.policy_templates || [];
  const tpl = templates.find((t) => t.id === spec.template);
  const preview = usePreview(() => api('POST', `${path}/policies?preview=1`, spec), [JSON.stringify(spec)], !!meta);
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('POST', `${path}/policies`, spec);
      toast('Policy added'); onAdded();
    } catch (err) { toast(err.message, true); } finally { setBusy(false); }
  };
  const toggleRole = (r) => set({ roles: spec.roles.includes(r) ? spec.roles.filter((x) => x !== r) : [...spec.roles, r] });
  return html`
    <form class="designer-card" onSubmit=${submit}>
      ${!table.rls_enabled && html`<p class="hint">Row level security is off for this table, so policies have no effect until you turn it on.</p>`}
      <fieldset class="templates">
        <legend>Who may do what</legend>
        ${templates.map((t) => html`
          <label class=${'template' + (spec.template === t.id ? ' chosen' : '')}>
            <input type="radio" name="template" value=${t.id} checked=${spec.template === t.id} onChange=${() => set({ template: t.id })} />
            <span><strong>${t.label}</strong><small>${t.description}</small></span>
          </label>`)}
      </fieldset>
      ${tpl?.needs_column && (uuidCols.length
        ? html`<label class="field"><span>Owner column</span>
            <select value=${spec.column} onChange=${(e) => set({ column: e.target.value })}>${uuidCols.map((c) => html`<option value=${c}>${c}</option>`)}</select>
            <small>Holds the id of the user who owns the row. <code>auth.uid()</code> is the signed-in user's id.</small></label>`
        : html`<p class="hint">This table has no uuid column to hold the owner's user id. Add one on the Structure tab (for example <code>user_id</code>, default <code>auth.uid()</code>, linked to <code>auth.users</code>).</p>`)}
      ${spec.template === 'custom' && html`
        <div class="custom-policy">
          <label class="field"><span>Command</span>
            <select value=${spec.command} onChange=${(e) => set({ command: e.target.value })}>
              ${['SELECT', 'INSERT', 'UPDATE', 'DELETE', 'ALL'].map((c) => html`<option value=${c}>${c}</option>`)}</select></label>
          <div class="field"><span>Applies to</span>
            <label class="check"><input type="checkbox" checked=${spec.roles.includes('anon')} onChange=${() => toggleRole('anon')} /> anon (not signed in)</label>
            <label class="check"><input type="checkbox" checked=${spec.roles.includes('authenticated')} onChange=${() => toggleRole('authenticated')} /> signed-in users</label></div>
          ${spec.command !== 'INSERT' && html`<label class="field"><span>Rows visible when (USING)</span>
            <textarea class="mono" rows="2" placeholder="user_id = auth.uid()" value=${spec.using} onInput=${(e) => set({ using: e.target.value })}></textarea></label>`}
          ${spec.command !== 'SELECT' && spec.command !== 'DELETE' && html`<label class="field"><span>Writes allowed when (WITH CHECK)</span>
            <textarea class="mono" rows="2" placeholder="user_id = auth.uid()" value=${spec.with_check} onInput=${(e) => set({ with_check: e.target.value })}></textarea></label>`}
        </div>`}
      <label class="field"><span>Policy name</span>
        <input value=${spec.name} placeholder=${tpl?.label || ''} maxlength="63" onInput=${(e) => set({ name: e.target.value })} /></label>
      <${SQLPreview} preview=${preview} />
      <div><button class="primary" disabled=${busy || !!preview.error || !preview.sql}>Add policy</button></div>
    </form>`;
}

// ----------------------------------------------------------- designer ----

const blankColumn = () => ({ name: '', type: 'text', nullable: true, unique: false, def: '', defValue: '', ref: '', onDelete: '' });

// columnSpec turns the form state into the API's ColumnSpec.
function columnSpec(c) {
  const spec = { name: c.name.trim(), type: c.type, nullable: c.nullable, unique: c.unique };
  if (c.def === '=value') spec.default = { value: c.defValue };
  else if (c.def) spec.default = { expression: c.def };
  if (c.ref) {
    const [schema, table] = JSON.parse(c.ref);
    spec.references = { schema, table, column: 'id', on_delete: c.onDelete };
  }
  return spec;
}

const AUTH_USERS = JSON.stringify(['auth', 'users']);
const DEFAULT_EXPRESSIONS = ['now()', 'current_date', 'gen_random_uuid()', 'auth.uid()'];
const COLUMN_TYPES = ['text', 'boolean', 'smallint', 'integer', 'bigint', 'numeric', 'real', 'double precision', 'uuid', 'date', 'time',
  'timestamp', 'timestamptz', 'interval', 'jsonb', 'json', 'bytea', 'inet', 'text[]', 'integer[]', 'bigint[]', 'uuid[]'];

// useRefTargets lists tables a new column can link to (by their id column).
function useRefTargets() {
  const [targets, setTargets] = useState([]);
  useEffect(() => {
    Promise.all([api('GET', '/tables'), getSettings().catch(() => null)]).then(([t, st]) => {
      const list = t.tables.filter((x) => x.kind === 'table').map((x) => ({ value: JSON.stringify([x.schema, x.name]), label: `${x.schema}.${x.name}` }));
      if (st?.server.modules.includes('auth')) list.unshift({ value: AUTH_USERS, label: 'auth.users' });
      setTargets(list);
    }, () => {});
  }, []);
  return targets;
}

function ColumnFields({ col, onChange, refs, onRemove }) {
  return html`
    <div class="column-row">
      <input class="mono" aria-label="Column name" placeholder="column_name" value=${col.name} onInput=${(e) => onChange({ name: e.target.value })} />
      <select aria-label="Type" value=${col.type} onChange=${(e) => onChange({ type: e.target.value })}>
        ${COLUMN_TYPES.map((t) => html`<option value=${t}>${t}</option>`)}
      </select>
      <div class="pair">
        <select aria-label="Default" value=${col.def} onChange=${(e) => onChange({ def: e.target.value })}>
          <option value="">No default</option>
          <option value="=value">A value…</option>
          ${DEFAULT_EXPRESSIONS.map((d) => html`<option value=${d}>${d}</option>`)}
        </select>
        ${col.def === '=value' && html`<input class="mono" aria-label="Default value" placeholder="value" value=${col.defValue} onInput=${(e) => onChange({ defValue: e.target.value })} />`}
      </div>
      <div class="pair">
        <select aria-label="Links to" value=${col.ref} onChange=${(e) => onChange({ ref: e.target.value })}>
          <option value="">Nothing</option>
          ${refs.map((r) => html`<option value=${r.value}>${r.label}</option>`)}
        </select>
        ${col.ref && html`<select aria-label="When the linked row is deleted" title="When the linked row is deleted" value=${col.onDelete} onChange=${(e) => onChange({ onDelete: e.target.value })}>
          <option value="">Block delete</option><option value="cascade">Cascade delete</option><option value="set null">Set null</option></select>`}
      </div>
      <div class="flags">
        <label class="check"><input type="checkbox" checked=${!col.nullable} onChange=${(e) => onChange({ nullable: !e.target.checked })} /> Required</label>
        <label class="check"><input type="checkbox" checked=${col.unique} onChange=${(e) => onChange({ unique: e.target.checked })} /> Unique</label>
        ${onRemove && html`<button type="button" class="small quiet" onClick=${onRemove}>Remove</button>`}
      </div>
    </div>`;
}

function TableDesigner({ tables, onCreated }) {
  const [{ data: meta, error }] = useLoad(() => api('GET', '/designer'), []);
  const [settings, setSettings] = useState(null);
  useEffect(() => { getSettings().then(setSettings, () => {}); }, []);
  const refs = useRefTargets();
  const [spec, setSpec] = useState({ schema: 'public', name: '', primary_key: 'bigint', timestamps: true, enable_rls: true });
  const [cols, setCols] = useState([blankColumn()]);
  const [busy, setBusy] = useState(false);
  const hasAuth = settings?.server.modules.includes('auth');

  const body = { ...spec, name: spec.name.trim(), columns: cols.filter((c) => c.name.trim() !== '').map(columnSpec) };
  const ready = body.name !== '';
  const preview = usePreview(() => api('POST', '/tables?preview=1', body), [JSON.stringify(body)], ready);

  const setCol = (i, patch) => setCols(cols.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const addOwner = () => setCols([...cols.filter((c) => c.name.trim() !== ''),
    { ...blankColumn(), name: 'user_id', type: 'uuid', nullable: false, def: 'auth.uid()', ref: AUTH_USERS, onDelete: 'cascade' }]);

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('POST', '/tables', body);
      toast(`Created ${body.schema}.${body.name}`);
      onCreated();
      location.hash = tableHref(body.schema, body.name, 'policies');
    } catch (err) { toast(err.message, true); setBusy(false); }
  };
  if (error) return html`<${Failed} error=${error} />`;
  const exposed = settings ? settings.rest_schemas.includes(spec.schema) : true;
  const taken = tables.some((t) => t.schema === spec.schema && t.name === body.name);

  return html`
    <form class="designer" onSubmit=${submit}>
      <div class="page-head"><div><h1>New table</h1>
        <p>Names use lowercase letters, digits and underscores, so API clients never need to quote them.</p></div></div>

      <div class="designer-grid">
        <label class="field"><span>Schema</span>
          <select value=${spec.schema} onChange=${(e) => setSpec({ ...spec, schema: e.target.value })}>
            ${(meta?.schemas || ['public']).map((sc) => html`<option value=${sc}>${sc}</option>`)}</select>
          ${!exposed && html`<small>Not served by the REST API. Add it to <code>MINIBASE_DATABASE_SCHEMAS</code> to expose it.</small>`}</label>
        <label class="field"><span>Name</span>
          <input class="mono" placeholder="todos" autofocus value=${spec.name} onInput=${(e) => setSpec({ ...spec, name: e.target.value })} />
          ${taken && html`<small class="error-text">A table with this name already exists.</small>`}</label>
        <label class="field"><span>Primary key</span>
          <select value=${spec.primary_key} onChange=${(e) => setSpec({ ...spec, primary_key: e.target.value })}>
            <option value="bigint">id, a number the database assigns</option>
            <option value="uuid">id, a random uuid</option>
            <option value="none">No primary key</option></select></label>
      </div>

      <section class="designer-card">
        <h2>Columns</h2>
        <div class="columns-head" aria-hidden="true"><span>Name</span><span>Type</span><span>Default</span><span>Links to</span><span></span></div>
        ${spec.primary_key !== 'none' && html`<div class="column-fixed"><code>id</code><span>${spec.primary_key === 'uuid' ? 'uuid, gen_random_uuid()' : 'bigint, numbered automatically'}</span></div>`}
        ${cols.map((c, i) => html`<${ColumnFields} key=${i} col=${c} refs=${refs} onChange=${(p) => setCol(i, p)}
          onRemove=${cols.length > 1 ? () => setCols(cols.filter((_, j) => j !== i)) : null} />`)}
        ${spec.timestamps && html`<div class="column-fixed"><code>created_at</code><span>timestamptz, now()</span></div>`}
        <div class="row-actions">
          <button type="button" class="small" onClick=${() => setCols([...cols, blankColumn()])}>Add column</button>
          ${hasAuth && !cols.some((c) => c.name === 'user_id') && html`<button type="button" class="small" onClick=${addOwner}>Add owner column (user_id)</button>`}
        </div>
      </section>

      <div class="designer-options">
        <label class="check"><input type="checkbox" checked=${spec.timestamps} onChange=${(e) => setSpec({ ...spec, timestamps: e.target.checked })} /> Add <code>created_at</code></label>
        <label class="check"><input type="checkbox" checked=${spec.enable_rls} onChange=${(e) => setSpec({ ...spec, enable_rls: e.target.checked })} /> Turn on row level security</label>
        ${!spec.enable_rls && html`<p class="hint">Without row level security the API refuses anon and signed-in requests to this table.</p>`}
      </div>

      <${SQLPreview} preview=${preview} idle="Name the table to see the SQL." />
      <div class="row-actions"><button class="primary" disabled=${busy || !ready || !!preview.error || taken}>Create table</button>
        <a class="button" href="#/tables">Cancel</a></div>
      <p class="muted">Next you'll choose who may read and write its rows.</p>
    </form>`;
}

// ---------------------------------------------------------------- API ----

// curl and fetch examples for one table, built from its columns.
function sampleValue(c) {
  if (/\[\]$/.test(c.type)) return [];
  if (/^(smallint|integer|bigint)$/.test(c.type)) return 1;
  if (/^(numeric|real|double precision)$/.test(c.type)) return 1.5;
  if (c.type === 'boolean') return true;
  if (/^jsonb?$/.test(c.type)) return {};
  if (c.type === 'uuid') return '00000000-0000-0000-0000-000000000000';
  if (/^timestamp/.test(c.type)) return new Date().toISOString();
  if (c.type === 'date') return new Date().toISOString().slice(0, 10);
  return 'text';
}

function examples(table, pk, settings, lang) {
  const base = `${location.origin}/rest/v1/${table.name}`;
  const writable = table.columns.filter((c) => !c.generated && !c.default && c.name !== 'created_at');
  const body = Object.fromEntries((writable.length ? writable : table.columns.filter((c) => !c.generated)).slice(0, 4).map((c) => [c.name, sampleValue(c)]));
  const key = pk[0] || table.columns[0]?.name || 'id';
  const cols = table.columns.slice(0, 3).map((c) => c.name).join(',');
  const firstText = table.columns.find((c) => c.type === 'text')?.name;
  const bodyJSON = JSON.stringify(body);
  const patch = JSON.stringify(Object.fromEntries(Object.entries(body).slice(0, 1)));
  const items = [
    ['Read rows', 'GET', `?select=${cols}&order=${key}.desc&limit=20`, null],
    ...(firstText ? [['Filter', 'GET', `?${firstText}=ilike.*milk*`, null]] : []),
    ['Insert a row', 'POST', '', bodyJSON],
    ['Update matching rows', 'PATCH', `?${key}=eq.1`, patch],
    ['Delete matching rows', 'DELETE', `?${key}=eq.1`, null],
  ];
  return items.map(([title, method, qs, data]) => {
    if (lang === 'curl') {
      const lines = [`curl${method === 'GET' ? '' : ` -X ${method}`} '${base}${qs}'`, `  -H "apikey: $ANON_KEY"`, `  -H "Authorization: Bearer $ACCESS_TOKEN"`];
      if (data) lines.push(`  -H 'Content-Type: application/json' -H 'Prefer: return=representation'`, `  -d '${data}'`);
      return [title, lines.join(' \\\n')];
    }
    const opts = [`method: '${method}'`, `headers: {\n    apikey: ANON_KEY,\n    Authorization: \`Bearer \${accessToken}\`,${data ? `\n    'Content-Type': 'application/json',\n    Prefer: 'return=representation',` : ''}\n  }`];
    if (data) opts.push(`body: JSON.stringify(${data})`);
    return [title, `const res = await fetch('${base}${qs}', {\n  ${opts.join(',\n  ')},\n});\nconst rows = await res.json();`];
  });
}

function TableAPI({ table, pk }) {
  const [settings, setSettings] = useState(null);
  const [error, setError] = useState(null);
  const [lang, setLang] = useState('curl');
  useEffect(() => { getSettings().then(setSettings, setError); }, []);
  if (error) return html`<${Failed} error=${error} />`;
  if (!settings) return html`<${Loading} />`;
  const mods = settings.server.modules;
  const exposed = settings.rest_schemas.includes(table.schema);
  return html`
    <div class="api-doc">
      ${!mods.includes('database')
        ? html`<p class="hint">The REST API is off. Add <code>database</code> to <code>MINIBASE_MODULES</code> to serve this table.</p>`
        : !exposed
          ? html`<p class="hint">The <code>${table.schema}</code> schema isn't served by the API. Add it to <code>MINIBASE_DATABASE_SCHEMAS</code>.</p>`
          : table.kind === 'table' && !table.rls_enabled
            ? html`<p class="hint">Row level security is off, so these requests are refused unless they use the service key.</p>`
            : null}
      <p class="muted">Endpoint <code>/rest/v1/${table.name}</code>. <code>ANON_KEY</code> is on the <a href="#/api">API and keys</a> page.
        The access token comes from signing in; without it, requests run as anon. Your policies decide which rows each caller sees.</p>
      <div class="seg" role="group" aria-label="Example language">
        ${[['curl', 'curl'], ['js', 'JavaScript']].map(([id, label]) => html`<button aria-pressed=${lang === id} onClick=${() => setLang(id)}>${label}</button>`)}
      </div>
      ${examples(table, pk, settings, lang).map(([title, code]) => html`
        <div class="example"><div class="example-head"><h3>${title}</h3><${CopyButton} text=${code} /></div><pre>${code}</pre></div>`)}
      <p class="muted">More query options: <code>select</code>, <code>eq</code>, <code>neq</code>, <code>gt</code>, <code>lte</code>, <code>ilike</code>, <code>in.(1,2)</code>, <code>is.null</code>, <code>not.</code>, <code>order</code>, <code>limit</code>, <code>offset</code>. See the getting started guide.</p>
    </div>`;
}

function APIPage() {
  const [{ loading, data, error }, reload] = useLoad(() => getSettings(), []);
  const [tables, setTables] = useState([]);
  useEffect(() => { api('GET', '/tables').then((r) => setTables(r.tables), () => {}); }, []);
  if (loading && !data) return html`<${Loading} />`;
  if (error) return html`<${Failed} error=${error} retry=${reload} />`;
  const mods = data.server.modules;
  const auth = (path, body) => `curl -X POST '${location.origin}/auth/v1/${path}' \\\n  -H "apikey: $ANON_KEY" -H 'Content-Type: application/json' \\\n  -d '${body}'`;
  const signup = auth('signup', '{"email":"you@example.com","password":"correct horse battery"}');
  const signin = auth('token?grant_type=password', '{"email":"you@example.com","password":"correct horse battery"}');
  const exposed = tables.filter((t) => data.rest_schemas.includes(t.schema));
  return html`
    <div class="page-head"><div><h1>API and keys</h1>
      <p>Apps send a key in the <code>apikey</code> header of every request, plus a user's access token once they sign in.</p></div></div>
    <div class="report">
      <section class="section">
        <h2>Connection</h2>
        <dl class="facts keys">
          <dt>URL</dt><dd><code>${location.origin}</code> <${CopyButton} text=${location.origin} what="URL copied" /></dd>
          <dt>Anon key</dt>
          <dd>${data.anon_key
            ? html`<code class="key">${data.anon_key}</code> <${CopyButton} text=${data.anon_key} what="Anon key copied" />
              <small>Safe to ship in browser and mobile apps. Requests with it can only do what your row level security policies allow.</small>`
            : html`<span class="null">not set</span>`}</dd>
          <dt>Service key</dt>
          <dd>${data.service_key ? html`<code>${data.service_key}</code>` : html`<span class="null">not set</span>`}
            <small>Bypasses row level security. Use it only on servers you control. It's in your <code>.env</code> as <code>MINIBASE_SERVICE_KEY</code>; the dashboard never shows it in full.</small></dd>
        </dl>
      </section>
      ${mods.includes('auth') && html`
        <section class="section">
          <h2>Sign users in</h2>
          <div class="example"><div class="example-head"><h3>Sign up</h3><${CopyButton} text=${signup} /></div><pre>${signup}</pre></div>
          <div class="example"><div class="example-head"><h3>Sign in</h3><${CopyButton} text=${signin} /></div><pre>${signin}</pre></div>
          <p class="muted">Both return an <code>access_token</code> (send it as <code>Authorization: Bearer</code>) and a <code>refresh_token</code>.</p>
        </section>`}
      <section class="section">
        <h2>Tables</h2>
        ${!mods.includes('database')
          ? html`<p class="muted">The REST API is off. Add <code>database</code> to <code>MINIBASE_MODULES</code>.</p>`
          : exposed.length === 0
            ? html`<p class="muted">No tables in ${data.rest_schemas.join(', ')} yet. <a href="#/tables/new">Create one</a>.</p>`
            : html`<ul class="link-list">${exposed.map((t) => html`<li><${RLSMark} table=${t} />
                <a href=${tableHref(t.schema, t.name, 'api')}><code>/rest/v1/${t.name}</code></a></li>`)}</ul>`}
      </section>
    </div>`;
}

// ---------------------------------------------------------------- SQL ----

const SQL_KEY = 'minibase.sql.draft';

function SQLEditor() {
  const [query, setQuery] = useState(() => {
    try {
      const prefill = sessionStorage.getItem('minibase.sql.prefill');
      if (prefill) { sessionStorage.removeItem('minibase.sql.prefill'); return prefill; }
      return localStorage.getItem(SQL_KEY) || 'SELECT now();';
    } catch { return 'SELECT now();'; }
  });
  const [run, setRun] = useState(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState('');
  const [snippet, setSnippet] = useState(null); // the saved snippet being edited
  const [saving, setSaving] = useState(false);
  const [historyKey, setHistoryKey] = useState(0);

  useEffect(() => { try { localStorage.setItem(SQL_KEY, query); } catch { /* storage unavailable */ } }, [query]);
  const load = (q, sn = null) => { setQuery(q); setSnippet(sn); setRun(null); setFailure(''); };

  const execute = async () => {
    setBusy(true); setFailure('');
    try {
      const r = await api('POST', '/sql', { query });
      setRun(r);
      setHistoryKey((k) => k + 1);
      if (r.results.some((x) => /^(CREATE|ALTER|DROP|GRANT|REVOKE|COMMENT|TRUNCATE)\b/.test(x.command))) {
        window.dispatchEvent(new Event('schema-changed'));
      }
    } catch (err) { setFailure(err.message); } finally { setBusy(false); }
  };
  const onKey = (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); execute(); }
  };
  const lineOf = (pos) => query.slice(0, pos - 1).split('\n').length;

  return html`
    <div class="page-head"><div><h1>SQL editor</h1>
      <p>Runs as the database owner, so row level security does not apply here. Separate statements with semicolons. Every run is recorded in the audit log.</p></div></div>
    <div class="sql-layout">
      <div class="sql-editor">
        ${snippet && html`<p class="editing-snippet">Editing <strong>${snippet.name}</strong> <button class="small quiet" onClick=${() => setSnippet(null)}>Detach</button></p>`}
        <textarea class="mono" spellcheck="false" aria-label="SQL" value=${query}
          onInput=${(e) => setQuery(e.target.value)} onKeyDown=${onKey}></textarea>
        <div class="toolbar wrap">
          <button class="primary" disabled=${busy} onClick=${execute}>${busy ? 'Running…' : 'Run'}</button>
          <span class="range">Ctrl or ⌘ + Enter</span>
          <span class="spacer"></span>
          ${run && html`<span class="range">${plural(run.results.length, 'statement', 'statements')} in ${run.duration_ms} ms</span>`}
          <button onClick=${() => setSaving(true)}>${snippet ? 'Save snippet' : 'Save as snippet'}</button>
        </div>
      </div>
      <${SQLSidebar} historyKey=${historyKey} current=${snippet} onPick=${load} />
    </div>
    ${saving && html`<${SaveSnippetDialog} query=${query} snippet=${snippet}
      onClose=${() => setSaving(false)} onSaved=${(sn) => { setSaving(false); setSnippet(sn); setHistoryKey((k) => k + 1); }} />`}
    ${failure && html`<div class="sql-error" role="alert"><strong>${failure}</strong></div>`}
    ${run && run.results.map((r, i) => html`<${SQLResult} key=${i} result=${r} />`)}
    ${run?.error && html`
      <div class="sql-error" role="alert">
        <strong>${run.error.message}</strong>
        ${run.error.position ? html`<span>At line ${lineOf(run.error.position)}, character ${run.error.position}.</span>` : null}
        ${run.error.detail && html`<span>${run.error.detail}</span>`}
        ${run.error.hint && html`<span>Hint: ${run.error.hint}</span>`}
        ${run.results.length > 0 && html`<span>Statements before this one ran.</span>`}
      </div>`}`;
}

function SQLSidebar({ historyKey, current, onPick }) {
  const [tab, setTab] = useState('snippets');
  const [snips, reloadSnips] = useLoad(() => api('GET', '/snippets'), [historyKey]);
  const [hist] = useLoad(() => api('GET', '/sql/history?limit=50'), [historyKey]);
  const [confirm, setConfirm] = useState(null);
  const firstLine = (q) => q.split('\n').find((l) => l.trim() && !l.trim().startsWith('--'))?.trim() || q.trim();
  const remove = async (sn) => {
    if (confirm !== sn.id) { setConfirm(sn.id); return; }
    try { await api('DELETE', `/snippets/${enc(sn.id)}`); toast(`Deleted ${sn.name}`); setConfirm(null); reloadSnips(); } catch (err) { toast(err.message, true); }
  };
  return html`
    <aside class="sql-side">
      <div class="seg" role="tablist">
        <button role="tab" aria-pressed=${tab === 'snippets'} onClick=${() => setTab('snippets')}>Saved</button>
        <button role="tab" aria-pressed=${tab === 'history'} onClick=${() => setTab('history')}>History</button>
      </div>
      ${tab === 'snippets' && html`
        <ul class="side-list">
          ${(snips.data?.snippets || []).map((sn) => html`
            <li class=${current?.id === sn.id ? 'current' : ''}>
              <button class="pick" onClick=${() => onPick(sn.query, sn)} title=${sn.query}>
                <strong>${sn.name}</strong><code>${firstLine(sn.query)}</code></button>
              <button class=${'small quiet' + (confirm === sn.id ? ' danger' : '')} onClick=${() => remove(sn)}>${confirm === sn.id ? 'Confirm' : 'Delete'}</button>
            </li>`)}
        </ul>
        ${snips.data?.snippets.length === 0 && html`<p class="muted side-empty">Queries you save appear here. Only you can see them.</p>`}`}
      ${tab === 'history' && html`
        <ul class="side-list">
          ${(hist.data?.history || []).map((h) => html`
            <li><button class="pick" onClick=${() => onPick(h.query)} title=${h.query}>
              <span class="when"><i class=${h.ok ? 'ok' : 'bad'}></i>${fmtDate(h.at)}${h.ok ? '' : ', failed'}</span>
              <code>${firstLine(h.query)}</code></button></li>`)}
        </ul>
        ${hist.data?.history.length === 0 && html`<p class="muted side-empty">Your recent runs appear here.</p>`}`}
    </aside>`;
}

function SaveSnippetDialog({ query, snippet, onClose, onSaved }) {
  const ref = useRef();
  const [error, setError] = useState('');
  useEffect(() => { ref.current.showModal(); }, []);
  const submit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    const asNew = e.submitter?.value === 'new' || !snippet;
    try {
      const r = asNew
        ? await api('POST', '/snippets', { name: f.get('name'), query })
        : await api('PUT', `/snippets/${enc(snippet.id)}`, { name: f.get('name'), query });
      toast(`Saved ${r.snippet.name}`); onSaved(r.snippet);
    } catch (err) { setError(err.message); }
  };
  return html`
    <dialog ref=${ref} onClose=${onClose}>
      <form onSubmit=${submit}>
        <h2>${snippet ? 'Save snippet' : 'Save as snippet'}</h2>
        <label class="field"><span>Name</span><input name="name" required maxlength="200" autofocus value=${snippet?.name || ''} /></label>
        ${error && html`<p class="error-text" role="alert">${error}</p>`}
        <div class="row"><button type="button" onClick=${onClose}>Cancel</button>
          ${snippet && html`<button value="new">Save as new</button>`}
          <button class="primary" value="update">Save</button></div>
      </form>
    </dialog>`;
}

function SQLResult({ result }) {
  if (result.columns.length === 0) {
    return html`<div class="result"><h3>${result.command || 'Done'}</h3></div>`;
  }
  return html`
    <div class="result">
      <h3>${result.command}${result.truncated ? ` — showing the first ${result.rows.length.toLocaleString()} rows` : ''}</h3>
      <div class="grid-wrap">
        <table class="grid">
          <thead><tr>${result.columns.map((c) => html`<th><span class="static">${c.name} <small>${c.type}</small></span></th>`)}</tr></thead>
          <tbody>${result.rows.map((row) => html`<tr>${row.map((v) => html`<td title=${v ?? undefined}>${v === null ? html`<span class="null">null</span>` : v}</td>`)}</tr>`)}</tbody>
        </table>
        ${result.rows.length === 0 && html`<div class="empty"><p>No rows returned.</p></div>`}
      </div>
    </div>`;
}

// -------------------------------------------------------------- users ----

const USERS_PAGE = 50;

function Users() {
  const [search, setSearch] = useState('');
  const [term, setTerm] = useState('');
  const [page, setPage] = useState(0);
  const [adding, setAdding] = useState(false);
  const [confirming, setConfirming] = useState(null);
  const [{ loading, data, error }, reload] = useLoad(
    () => api('GET', `/users?search=${enc(term)}&limit=${USERS_PAGE}&offset=${page * USERS_PAGE}`), [term, page]);

  useEffect(() => {
    const t = setTimeout(() => { setPage(0); setTerm(search); }, 250);
    return () => clearTimeout(t);
  }, [search]);

  if (error?.code === 'admin.users_unavailable') {
    return html`<div class="page-head"><div><h1>Users</h1></div></div>
      <div class="empty"><h3>User accounts are turned off</h3><p>Add <code>auth</code> to <code>MINIBASE_MODULES</code> and restart to manage app users here.</p></div>`;
  }
  if (error) return html`<${Failed} error=${error} retry=${reload} />`;

  const act = async (fn, done) => {
    try { await fn(); toast(done); setConfirming(null); reload(); } catch (err) { toast(err.message, true); }
  };
  const total = data?.total ?? 0;

  return html`
    <div class="page-head">
      <div><h1>Users</h1><p>People who signed up to your app. Dashboard admins are managed on the <a href="#/admins">Admins</a> page.</p></div>
      <button class="primary" onClick=${() => setAdding(!adding)}>${adding ? 'Close' : 'Add user'}</button>
    </div>
    ${adding && html`<${AddUser} onAdded=${() => { setAdding(false); reload(); }} />`}
    <div class="toolbar">
      <input type="search" placeholder="Search by email" aria-label="Search users by email" style="max-width:320px"
        value=${search} onInput=${(e) => setSearch(e.target.value)} />
      <span class="spacer"></span>
      <span class="range">${plural(total, 'user', 'users')}</span>
      <button class="small" disabled=${page === 0} onClick=${() => setPage(page - 1)}>Previous</button>
      <button class="small" disabled=${(page + 1) * USERS_PAGE >= total} onClick=${() => setPage(page + 1)}>Next</button>
    </div>
    <div class="grid-wrap people">
      <table class="grid">
        <thead><tr><th><span class="static">Email</span></th><th><span class="static">Signed up</span></th>
          <th><span class="static">Last sign-in</span></th><th><span class="static">Active sessions</span></th><th></th></tr></thead>
        <tbody>${(data?.users || []).map((u) => html`
          <tr>
            <td>${u.email}</td>
            <td>${fmtDate(u.created_at)}</td>
            <td>${fmtDate(u.last_sign_in_at)}</td>
            <td>${u.active_sessions}</td>
            <td style="text-align:right">
              <button class="small" disabled=${u.active_sessions === 0}
                onClick=${() => act(() => api('POST', `/users/${enc(u.id)}/signout`), `Signed ${u.email} out everywhere`)}>Sign out everywhere</button>
              ${' '}
              ${confirming === u.id
                ? html`<button class="small danger confirm" onClick=${() => act(() => api('DELETE', `/users/${enc(u.id)}`), `Deleted ${u.email}`)}>Confirm delete</button>`
                : html`<button class="small danger" onClick=${() => setConfirming(u.id)}>Delete</button>`}
            </td>
          </tr>`)}</tbody>
      </table>
      ${!loading && data?.users.length === 0 && html`<div class="empty">
        <h3>${term ? 'No users match' : 'No users yet'}</h3>
        <p>${term ? 'Try part of the email address.' : 'Users appear here after they sign up through /auth/v1/signup.'}</p></div>`}
      ${loading && !data && html`<${Loading} />`}
    </div>`;
}

function AddUser({ onAdded }) {
  const [error, setError] = useState('');
  const submit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    try {
      await api('POST', '/users', { email: f.get('email'), password: f.get('password') });
      toast(`Added ${f.get('email')}`);
      onAdded();
    } catch (err) { setError(err.message); }
  };
  return html`
    <form class="inline-form" onSubmit=${submit}>
      <label class="field"><span>Email</span><input name="email" type="email" required autofocus /></label>
      <label class="field"><span>Password</span><input name="password" type="password" autocomplete="new-password" required /></label>
      <button class="primary">Add user</button>
      ${error && html`<p class="error-text" role="alert">${error}</p>`}
    </form>`;
}

// ---------------------------------------------------------- audit log ----

const ACTIONS = {
  'admin.sign_in': 'Signed in', 'admin.sign_in_failed': 'Failed sign-in', 'admin.sign_out': 'Signed out',
  'admin.password_change': 'Changed own password', 'admin.create': 'Added admin', 'admin.delete': 'Deleted admin',
  'admin.password_reset': 'Reset admin password', 'admin.sign_out_everywhere': 'Signed admin out everywhere',
  'sql.run': 'Ran SQL',
  'schema.table_create': 'Created table', 'schema.table_drop': 'Dropped table', 'schema.column_add': 'Added column',
  'schema.column_drop': 'Dropped column', 'schema.rls_enable': 'Turned on RLS', 'schema.rls_disable': 'Turned off RLS',
  'schema.policy_create': 'Added policy', 'schema.policy_drop': 'Removed policy',
  'data.row_insert': 'Added row', 'data.row_update': 'Edited row', 'data.row_delete': 'Deleted row',
  'data.import': 'Imported CSV', 'data.export': 'Exported CSV',
  'users.create': 'Added user', 'users.delete': 'Deleted user', 'users.sign_out': 'Signed user out',
};
const GROUPS = [['', 'Everything'], ['admin.', 'Sign-ins and admins'], ['sql.', 'SQL editor'], ['schema.', 'Schema and policies'], ['data.', 'Rows and files'], ['users.', 'App users']];
const RISKY = new Set(['admin.sign_in_failed', 'schema.table_drop', 'schema.column_drop', 'schema.rls_disable', 'schema.policy_drop', 'users.delete', 'admin.delete']);

function summary(e) {
  const d = e.detail || {};
  if (e.action === 'sql.run') return (d.ok ? '' : 'Failed: ') + ((d.query || '').split('\n').find((l) => l.trim()) || '');
  if (d.sql) return d.sql[0];
  if (d.rows !== undefined) return plural(d.rows, 'row', 'rows');
  if (d.key) return `key ${JSON.stringify(d.key)}`;
  if (d.user_agent) return d.user_agent;
  return '';
}

// detailText shows an entry in full: SQL as written, everything else as JSON.
function detailText(e) {
  const { sql, query, ...rest } = e.detail || {};
  const parts = [`${e.action}${e.account_id ? '' : ' (no dashboard account)'}`];
  if (query) parts.push(query);
  if (sql) parts.push(sqlText(sql));
  if (Object.keys(rest).length) parts.push(JSON.stringify(rest, null, 2));
  return parts.join('\n\n');
}

function AuditLog() {
  const [group, setGroup] = useState('');
  const [actor, setActor] = useState('');
  const [term, setTerm] = useState('');
  const [entries, setEntries] = useState([]);
  const [state, setState] = useState({ loading: true, error: null, more: false });
  const [open, setOpen] = useState(null);

  useEffect(() => { const t = setTimeout(() => setTerm(actor.trim()), 300); return () => clearTimeout(t); }, [actor]);
  const fetchPage = async (before) => {
    setState((s) => ({ ...s, loading: true, error: null }));
    try {
      const r = await api('GET', `/audit?limit=50&action=${enc(group)}&actor=${enc(term)}${before ? `&before=${before}` : ''}`);
      setEntries((prev) => (before ? [...prev, ...r.entries] : r.entries));
      setState({ loading: false, error: null, more: r.entries.length === 50 });
    } catch (error) { setState({ loading: false, error, more: false }); }
  };
  useEffect(() => { fetchPage(0); }, [group, term]);

  return html`
    <div class="page-head"><div><h1>Audit log</h1>
      <p>What admins did in the dashboard and with <code>minibase admin</code>, newest first. Entries are kept for <code>MINIBASE_ADMIN_AUDIT_RETENTION</code> (90 days unless changed).</p></div></div>
    <div class="toolbar wrap">
      <select aria-label="Kind of action" style="max-width:220px" value=${group} onChange=${(e) => setGroup(e.target.value)}>
        ${GROUPS.map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select>
      <input type="search" placeholder="Admin email" aria-label="Filter by admin email" style="max-width:260px" value=${actor} onInput=${(e) => setActor(e.target.value)} />
    </div>
    ${state.error && html`<${Failed} error=${state.error} retry=${() => fetchPage(0)} />`}
    <div class="grid-wrap audit">
      <table class="grid">
        <thead><tr><th><span class="static">When</span></th><th><span class="static">Who</span></th><th><span class="static">Action</span></th>
          <th><span class="static">On</span></th><th><span class="static">Details</span></th></tr></thead>
        <tbody>${entries.map((e) => html`
          <tr class="clickable" tabindex="0" onClick=${() => setOpen(open === e.id ? null : e.id)} onKeyDown=${(ev) => ev.key === 'Enter' && setOpen(open === e.id ? null : e.id)}>
            <td>${fmtDate(e.at)}</td>
            <td>${e.actor}${e.ip && html` <small class="faint">${e.ip}</small>`}</td>
            <td class=${RISKY.has(e.action) || e.detail?.ok === false ? 'risky' : ''}>${ACTIONS[e.action] || e.action}</td>
            <td>${e.target}</td>
            <td>${summary(e)}</td>
          </tr>
          ${open === e.id && html`<tr class="detail"><td colspan="5"><pre>${detailText(e)}</pre></td></tr>`}`)}
        </tbody>
      </table>
      ${!state.loading && entries.length === 0 && !state.error && html`<div class="empty"><h3>Nothing recorded</h3><p>${group || term ? 'Nothing matches these filters.' : 'Actions appear here as admins work.'}</p></div>`}
      ${state.loading && entries.length === 0 && html`<${Loading} />`}
    </div>
    ${state.more && html`<p style="margin-top:12px"><button disabled=${state.loading} onClick=${() => fetchPage(entries[entries.length - 1].id)}>Load older</button></p>`}`;
}

// ------------------------------------------------------------- admins ----

function Admins({ account }) {
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', '/admins'), []);
  const [adding, setAdding] = useState(false);
  const [confirm, setConfirm] = useState(null);
  const [secret, setSecret] = useState(null); // { email, password } shown once

  if (loading && !data) return html`<${Loading} />`;
  if (error) return html`<${Failed} error=${error} retry=${reload} />`;
  const act = async (fn, done) => {
    try { const r = await fn(); if (done) toast(done); setConfirm(null); reload(); return r; } catch (err) { toast(err.message, true); return null; }
  };
  const reset = async (a) => {
    const r = await act(() => api('POST', `/admins/${enc(a.id)}/password`, { password: '' }));
    if (r) setSecret({ email: a.email, password: r.generated_password, reset: true });
  };
  const admins = data.admins;
  return html`
    <div class="page-head">
      <div><h1>Admins</h1><p>People who can sign in to this dashboard. They can do anything, including run SQL, so add only people you trust.</p></div>
      <button class="primary" onClick=${() => setAdding(!adding)}>${adding ? 'Close' : 'Add admin'}</button>
    </div>
    ${adding && html`<${AddAdmin} onAdded=${(r, email) => { setAdding(false); reload(); if (r.generated_password) setSecret({ email, password: r.generated_password }); }} />`}
    <div class="grid-wrap people">
      <table class="grid">
        <thead><tr><th><span class="static">Email</span></th><th><span class="static">Added</span></th><th><span class="static">Last sign-in</span></th><th></th></tr></thead>
        <tbody>${admins.map((a) => html`
          <tr>
            <td>${a.email}${a.id === account.id && html` <span class="chip">you</span>`}</td>
            <td>${fmtDate(a.created_at)}</td>
            <td>${fmtDate(a.last_sign_in_at)}</td>
            <td style="text-align:right">${a.id !== account.id && html`
              <button class="small" onClick=${() => reset(a)}>Reset password</button>${' '}
              <button class="small" onClick=${() => act(() => api('POST', `/admins/${enc(a.id)}/signout`), `Signed ${a.email} out everywhere`)}>Sign out everywhere</button>${' '}
              ${confirm === a.id
                ? html`<button class="small danger confirm" onClick=${() => act(() => api('DELETE', `/admins/${enc(a.id)}`), `Removed ${a.email}`)}>Confirm remove</button>`
                : html`<button class="small danger" disabled=${admins.length === 1} onClick=${() => setConfirm(a.id)}>Remove</button>`}`}</td>
          </tr>`)}</tbody>
      </table>
    </div>
    <p class="muted" style="margin-top:12px">To change your own password, use Change password at the bottom of the sidebar. Locked out? Run <code>minibase admin reset-password</code> on the server.</p>
    ${secret && html`<${SecretDialog} secret=${secret} onClose=${() => setSecret(null)} />`}`;
}

function AddAdmin({ onAdded }) {
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    setBusy(true);
    try {
      const r = await api('POST', '/admins', { email: f.get('email'), password: f.get('password') });
      toast(`Added ${r.admin.email}`); onAdded(r, r.admin.email);
    } catch (err) { setError(err.message); setBusy(false); }
  };
  return html`
    <form class="inline-form" onSubmit=${submit}>
      <label class="field"><span>Email</span><input name="email" type="email" required autofocus /></label>
      <label class="field"><span>Password</span><input name="password" type="password" autocomplete="new-password" minlength="12" placeholder="Leave empty to generate one" /></label>
      <button class="primary" disabled=${busy}>Add admin</button>
      ${error && html`<p class="error-text" role="alert">${error}</p>`}
    </form>`;
}

function SecretDialog({ secret, onClose }) {
  const ref = useRef();
  useEffect(() => { ref.current.showModal(); }, []);
  return html`
    <dialog ref=${ref} onClose=${onClose}>
      <form method="dialog">
        <h2>${secret.reset ? 'New password' : 'Admin added'}</h2>
        <p>Give this password to <strong>${secret.email}</strong> over a private channel. It won't be shown again${secret.reset ? ', and their sessions have been signed out' : ''}.</p>
        <div class="secret"><code>${secret.password}</code><${CopyButton} text=${secret.password} what="Password copied" /></div>
        <div class="row"><button class="primary">Done</button></div>
      </form>
    </dialog>`;
}

// ----------------------------------------------------------- settings ----

function SettingsPage() {
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', '/settings'), []);
  const [changedOnly, setChangedOnly] = useState(false);
  if (loading && !data) return html`<${Loading} />`;
  if (error) return html`<${Failed} error=${error} retry=${reload} />`;
  const mods = data.server.modules;
  const groupOf = (name) => mods.find((m) => name.startsWith(`MINIBASE_${m.toUpperCase()}_`)) || 'server';
  const groups = ['server', ...mods].map((g) => [g, data.settings.filter((st) => groupOf(st.name) === g && (!changedOnly || st.is_set))]).filter(([, l]) => l.length);
  const value = (st) => {
    if (st.secret && !st.value) return st.is_set ? html`<span class="null">set, hidden</span>` : html`<span class="null">not set</span>`;
    if (st.value === '') return html`<span class="null">empty</span>`;
    return html`<code>${st.value}</code>`;
  };
  return html`
    <div class="page-head"><div><h1>Settings</h1>
      <p>The configuration this server is running with, read from environment variables. Change them in <code>.env</code> and restart. Secrets are hidden or shortened.</p></div></div>
    <div class="toolbar"><label class="check"><input type="checkbox" checked=${changedOnly} onChange=${(e) => setChangedOnly(e.target.checked)} /> Only show values set in the environment</label></div>
    ${groups.map(([g, list]) => html`
      <section class="settings-group">
        <h2>${g === 'server' ? 'Server' : html`<code>${g}</code> module`}</h2>
        <table class="plain settings">
          <tbody>${list.map((st) => html`
            <tr>
              <td><code>${st.name}</code></td>
              <td>${value(st)}</td>
              <td class="source">${st.is_set ? (st.secret || st.value === st.default ? 'set' : html`set; default <code>${st.default || 'empty'}</code>`) : 'default'}</td>
            </tr>`)}</tbody>
        </table>
      </section>`)}`;
}

// ---------------------------------------------------------------- app ----

function Toasts() {
  const [t, setT] = useState(null);
  useEffect(() => {
    let timer;
    const on = (e) => { clearTimeout(timer); setT(e.detail); timer = setTimeout(() => setT(null), 3200); };
    addEventListener('toast', on);
    return () => { removeEventListener('toast', on); clearTimeout(timer); };
  }, []);
  return t && html`<div class=${'toast' + (t.bad ? ' bad' : '')} role="status">${t.message}</div>`;
}

function App() {
  const [account, setAccount] = useState(undefined); // undefined = checking
  const route = useHashRoute();

  useEffect(() => {
    api('GET', '/me').then((r) => setAccount(r.account), () => setAccount(null));
    const out = () => setAccount(null);
    addEventListener('signed-out', out);
    return () => removeEventListener('signed-out', out);
  }, []);

  const signOut = async () => {
    try { await api('POST', '/logout'); } finally { setAccount(null); }
  };

  let page;
  switch (route[0]) {
    case 'tables': page = html`<${Tables} route=${route} />`; break;
    case 'sql': page = html`<${SQLEditor} />`; break;
    case 'users': page = html`<${Users} />`; break;
    case 'api': page = html`<${APIPage} />`; break;
    case 'audit': page = html`<${AuditLog} />`; break;
    case 'admins': page = html`<${Admins} account=${account} />`; break;
    case 'settings': page = html`<${SettingsPage} />`; break;
    default: page = html`<${Overview} />`;
  }

  return html`
    ${account === undefined && html`<p class="boot">Loading the dashboard…</p>`}
    ${account === null && html`<${SignIn} onSignedIn=${setAccount} />`}
    ${account && html`<${Shell} account=${account} route=${route} onSignOut=${signOut}>${page}<//>`}
    <${Toasts} />`;
}

render(html`<${App} />`, document.getElementById('app'));
