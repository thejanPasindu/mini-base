// mini-base admin dashboard. No build step: Preact + htm tagged templates.
import { html, render, useState, useEffect, useRef, useCallback } from './vendor/htm-preact.js';

// ---------------------------------------------------------------- API ----

class APIError extends Error {
  constructor(message, status, code) { super(message); this.status = status; this.code = code; }
}

async function api(method, path, body) {
  const headers = { 'X-Minibase-Admin': '1' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const res = await fetch('api' + path, {
    method, headers, credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
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
  ['overview', 'Overview'],
  ['tables', 'Tables'],
  ['sql', 'SQL editor'],
  ['users', 'Users'],
];

function Shell({ account, route, onSignOut, children }) {
  const [pwOpen, setPwOpen] = useState(false);
  return html`
    <div class="shell">
      <aside class="rail">
        <div class="wordmark"><i></i>mini-base</div>
        <nav aria-label="Sections">
          ${SECTIONS.map(([id, label]) => html`
            <a href=${'#/' + id} aria-current=${route[0] === id ? 'page' : undefined}>${label}</a>`)}
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
  const [, schema, name] = route;
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

  if (data.tables.length === 0) {
    return html`
      <div class="page-head"><div><h1>Tables</h1></div></div>
      <div class="empty"><h3>No tables yet</h3>
        <p>Create one in the <a href="#/sql">SQL editor</a>. Tables in the <code>public</code> schema are served at <code>/rest/v1/<i>table</i></code>.</p></div>`;
  }
  return html`
    <div class="split">
      <div class="table-list">
        <input type="search" placeholder="Filter tables" aria-label="Filter tables" value=${filter} onInput=${(e) => setFilter(e.target.value)} />
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
        ${schema
          ? html`<${TableDetail} key=${schema + '.' + name} schema=${schema} name=${name} onChanged=${reload} />`
          : html`<div class="empty"><h3>Choose a table</h3><p>Browse and edit its rows, see its columns and its row level security policies.</p></div>`}
      </div>
    </div>`;
}

function TableDetail({ schema, name, onChanged }) {
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', tablePath(schema, name)), [schema, name]);
  const [tab, setTab] = useState('rows');
  const [busy, setBusy] = useState(false);

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
      ${[['rows', 'Rows'], ['structure', 'Structure'], ['policies', `Policies (${table.policy_list.length})`]].map(([id, label]) => html`
        <button role="tab" aria-selected=${tab === id} onClick=${() => setTab(id)}>${label}</button>`)}
    </div>

    ${tab === 'rows' && html`<${Rows} table=${table} editable=${editable} pk=${pk} />`}
    ${tab === 'structure' && html`<${Structure} table=${table} />`}
    ${tab === 'policies' && html`<${Policies} table=${table} />`}`;
}

const PAGE = 50;

function Rows({ table, editable, pk }) {
  const [page, setPage] = useState(0);
  const [sort, setSort] = useState(null); // { column, desc }
  const [editing, setEditing] = useState(null); // { row } or { row: null } for a new row
  const path = tablePath(table.schema, table.name);
  const query = `?limit=${PAGE}&offset=${page * PAGE}${sort ? `&sort=${enc(sort.column)}&dir=${sort.desc ? 'desc' : 'asc'}` : ''}`;
  const [{ loading, data, error }, reload] = useLoad(() => api('GET', path + '/rows' + query), [path, query]);

  const toggleSort = (column) => {
    setPage(0);
    setSort((s) => (!s || s.column !== column ? { column, desc: false } : s.desc ? null : { column, desc: true }));
  };

  if (error) return html`<${Failed} error=${error} retry=${reload} />`;
  const rows = data?.rows || [];
  const total = data?.total ?? 0;
  const from = total === 0 ? 0 : page * PAGE + 1;
  const to = Math.min((page + 1) * PAGE, total);

  return html`
    <div class="toolbar">
      ${table.kind === 'table' && html`<button class="primary" onClick=${() => setEditing({ row: null })}>Add row</button>`}
      ${table.kind === 'table' && !editable && html`<span class="range">Add a primary key to edit or delete rows here.</span>`}
      <span class="spacer"></span>
      <span class="range">${from.toLocaleString()}–${to.toLocaleString()} of ${data?.total_exact === false ? 'about ' : ''}${total.toLocaleString()}</span>
      <button class="small" disabled=${page === 0} onClick=${() => setPage(page - 1)}>Previous</button>
      <button class="small" disabled=${to >= total} onClick=${() => setPage(page + 1)}>Next</button>
    </div>
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
      ${!loading && rows.length === 0 && html`<div class="empty"><h3>No rows</h3>
        <p>${table.kind === 'table' ? 'Add one here, or through the REST API.' : 'This view returns no rows.'}</p></div>`}
      ${loading && !data && html`<${Loading} />`}
    </div>
    ${editing && html`<${RowDrawer} table=${table} pk=${pk} row=${editing.row} path=${path}
      onClose=${() => setEditing(null)} onSaved=${() => { setEditing(null); reload(); }} />`}`;
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

function Structure({ table }) {
  return html`
    <table class="plain">
      <thead><tr><th>Column</th><th>Type</th><th>Nullable</th><th>Default</th></tr></thead>
      <tbody>${table.columns.map((c) => html`
        <tr>
          <td><code>${c.name}</code>${c.primary_key ? html` <span class="chip">primary key</span>` : ''}${c.generated ? html` <span class="chip">generated</span>` : ''}</td>
          <td><code>${c.type}</code></td>
          <td>${c.nullable ? 'Yes' : 'No'}</td>
          <td>${c.default ? html`<code>${c.default}</code>` : html`<span class="null">none</span>`}</td>
        </tr>`)}</tbody>
    </table>`;
}

function policyTemplate(t) {
  const q = (s) => `"${s.replace(/"/g, '""')}"`;
  return `-- Example: each signed-in user can read and change only their own rows.
-- Assumes a user_id uuid column; adjust to your table.
CREATE POLICY own_rows ON ${q(t.schema)}.${q(t.name)}
  FOR ALL TO minibase_authenticated
  USING (user_id = auth.uid())
  WITH CHECK (user_id = auth.uid());`;
}

function Policies({ table }) {
  const openInEditor = () => {
    try { sessionStorage.setItem('minibase.sql.prefill', policyTemplate(table)); } catch { /* storage unavailable */ }
    location.hash = '#/sql';
  };
  if (table.kind !== 'table') {
    return html`<div class="empty"><h3>Views don't have policies</h3>
      <p>A view created <code>WITH (security_invoker = true)</code> applies the policies of the tables it reads.</p></div>`;
  }
  return html`
    ${table.policy_list.length === 0
      ? html`<div class="empty"><h3>No policies</h3>
          <p>${table.rls_enabled ? 'With row level security on and no policies, anon and signed-in requests see no rows at all.' : 'Turn on row level security, then add policies.'}</p></div>`
      : table.policy_list.map((p) => html`
          <div class="policy">
            <h3><code>${p.name}</code></h3>
            <p class="meta">${p.command === 'ALL' ? 'All commands' : p.command} for ${p.roles.join(', ')}${p.permissive ? '' : ' (restrictive)'}</p>
            ${p.using && html`<div><small>Rows visible when</small><pre>${p.using}</pre></div>`}
            ${p.with_check && html`<div><small>Writes allowed when</small><pre>${p.with_check}</pre></div>`}
          </div>`)}
    <p style="margin-top:16px"><button onClick=${openInEditor}>Write a policy in the SQL editor</button></p>`;
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

  useEffect(() => { try { localStorage.setItem(SQL_KEY, query); } catch { /* storage unavailable */ } }, [query]);

  const execute = async () => {
    setBusy(true); setFailure('');
    try {
      const r = await api('POST', '/sql', { query });
      setRun(r);
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
      <p>Runs as the database owner, so row level security does not apply here. Separate statements with semicolons.</p></div></div>
    <div class="sql-editor">
      <textarea class="mono" spellcheck="false" aria-label="SQL" value=${query}
        onInput=${(e) => setQuery(e.target.value)} onKeyDown=${onKey}></textarea>
      <div class="toolbar">
        <button class="primary" disabled=${busy} onClick=${execute}>${busy ? 'Running…' : 'Run'}</button>
        <span class="range">Ctrl or ⌘ + Enter</span>
        <span class="spacer"></span>
        ${run && html`<span class="range">${plural(run.results.length, 'statement', 'statements')} in ${run.duration_ms} ms</span>`}
      </div>
    </div>
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
      <div><h1>Users</h1><p>People who signed up to your app. Dashboard admins are managed separately with <code>minibase admin</code>.</p></div>
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
    <div class="grid-wrap">
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
    default: page = html`<${Overview} />`;
  }

  return html`
    ${account === undefined && html`<p class="boot">Loading the dashboard…</p>`}
    ${account === null && html`<${SignIn} onSignedIn=${setAccount} />`}
    ${account && html`<${Shell} account=${account} route=${route} onSignOut=${signOut}>${page}<//>`}
    <${Toasts} />`;
}

render(html`<${App} />`, document.getElementById('app'));
