import { markUnauthenticated } from './auth.js';
import { queueWarnings } from './toast.js';

// A mock save can succeed with advice (e.g. a callback with no body
// template); surface it with the page's success toast.
function noteWarnings(saved) {
  queueWarnings(saved?.warnings);
  return saved;
}

async function request(method, path, body, signal, opts = {}) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
    signal,
  });
  if (!res.ok) {
    // A 401 here USUALLY means an admin login (see auth.js) was required
    // and the session is missing/expired mid-use, not just "this one call
    // failed" — flip back to the login screen instead of leaving whatever
    // page was open showing a confusing error toast for a request that
    // was never going to succeed. opts.skipAuthRedirect is the one
    // exception: the workspace-lock endpoints (see api.lockWorkspace/
    // unlockWorkspace/removeWorkspaceLock below) also legitimately return
    // 401 for "wrong workspace password", which has nothing to do with
    // the admin session — treating it the same way incorrectly booted the
    // whole app to the admin login screen on a mistyped workspace PIN.
    if (res.status === 401 && !opts.skipAuthRedirect) markUnauthenticated();
    let message = `${method} ${path} failed (${res.status})`;
    let data;
    try {
      data = await res.json();
      if (data?.error) message = data.error;
    } catch {
      // ignore non-JSON error bodies
    }
    const err = new Error(message);
    err.status = res.status;
    // A 403 workspace-locked response (see internal/web/api/mocks.go) also
    // carries `code`/`workspaceId` — surfaced on the error object so
    // workspaceLock.js's retry-after-unlock flow can specifically detect
    // this one case rather than showing every 403 as a generic failure.
    if (data?.code) err.code = data.code;
    if (data?.workspaceId) err.workspaceId = data.workspaceId;
    throw err;
  }
  if (res.status === 204) return null;
  return res.json();
}

// requestForm is request()'s counterpart for multipart bodies (file uploads
// — certificate/key/PKCS#12 import) — no Content-Type is set so the browser
// fills in its own multipart boundary, but the same JSON-error-body
// convention applies.
async function requestForm(method, path, formData) {
  const res = await fetch(path, { method, body: formData });
  if (!res.ok) {
    if (res.status === 401) markUnauthenticated();
    let message = `${method} ${path} failed (${res.status})`;
    try {
      const data = await res.json();
      if (data?.error) message = data.error;
    } catch {
      // ignore non-JSON error bodies
    }
    const err = new Error(message);
    err.status = res.status;
    throw err;
  }
  if (res.status === 204) return null;
  return res.json();
}

export const api = {
  listMocks: () => request('GET', '/api/mocks'),
  createMock: (def) => request('POST', '/api/mocks', def).then(noteWarnings),
  updateMock: (id, def) => request('PUT', `/api/mocks/${id}`, def).then(noteWarnings),
  deleteMock: (id) => request('DELETE', `/api/mocks/${id}`),
  importWSDL: (wsdlContent, pathPattern, projectId) => request('POST', '/api/wsdl/import', { wsdlContent, pathPattern, projectId }),
  importSoapUIMocks: (projectXml, projectId) => request('POST', '/api/wsdl/import-soapui-mocks', { projectXml, projectId }),
  importGraphQLSchema: (sdlContent, pathPattern, projectId) => request('POST', '/api/graphql-schema/import', { sdlContent, pathPattern, projectId }),
  importOpenAPI: (openapiContent, pathPrefix, projectId) => request('POST', '/api/openapi/import', { openapiContent, pathPrefix, projectId }),
  importHAR: (content, pathPrefix, projectId) => request('POST', '/api/traffic-import/har', { content, pathPrefix, projectId }),
  importPostmanExamples: (content, pathPrefix, projectId) => request('POST', '/api/traffic-import/postman-examples', { content, pathPrefix, projectId }),
  importMocks: async (exportedJson, projectId) => {
    let parsed;
    try {
      parsed = JSON.parse(exportedJson);
    } catch {
      throw new Error('That doesn\'t look like valid JSON — check for a missing brace or quote.');
    }
    if (projectId) parsed.projectId = projectId;
    return request('POST', '/api/mocks/import', parsed);
  },
  // projectId only matters for action 'move' — '' moves the selection to
  // Ungrouped, otherwise omit it for 'enable'/'disable'/'delete'.
  bulkMockAction: (ids, action, projectId) => request('POST', '/api/mocks/bulk', { ids, action, projectId }),
  listMockVersions: (id) => request('GET', `/api/mocks/${id}/versions`),
  restoreMockVersion: (id, versionId) => request('POST', `/api/mocks/${id}/versions/${versionId}/restore`),
  // Connected-sessions panel, shared across every session-tracking protocol
  // page (TCP/WS/MQTT/SMTP/Kafka/SMPP/Diameter/JMS) — a protocol with no
  // sessions at all (REST/SOAP/GraphQL) just gets an empty list back, not
  // an error, so this is safe to poll from any mock detail panel.
  listSessions: (mockId) => request('GET', `/api/mocks/${mockId}/sessions`),
  closeSession: (mockId, sessionId) => request('POST', `/api/mocks/${mockId}/sessions/${sessionId}/close`),
  sendToSession: (mockId, sessionId, payload, extra) =>
    request('POST', `/api/mocks/${mockId}/sessions/${sessionId}/send`, { payload, extra }),

  getSettings: () => request('GET', '/api/settings'),
  saveSettings: (s) => request('PUT', '/api/settings', s),

  getConfig: () => request('GET', '/api/config'),

  listMockProjects: () => request('GET', '/api/mock-projects'),
  createMockProject: (p) => request('POST', '/api/mock-projects', p),
  updateMockProject: (id, p) => request('PUT', `/api/mock-projects/${id}`, p),
  deleteMockProject: (id) => request('DELETE', `/api/mock-projects/${id}`),

  // CSV data-source attachment backing the {{csv "column"}} template
  // function (see internal/mock/template.go) — ownerPath is either
  // `/api/mocks/${id}` or `/api/scheduled-events/${id}`, since both kinds
  // of owner expose the identical three routes. getCsvSource resolves to
  // null (not a thrown 404) when nothing is attached, since "no CSV yet" is
  // the normal/common case for CsvSourceField.svelte, not an error.
  setCsvSource: (ownerPath, mode, file) => {
    const formData = new FormData();
    formData.append('mode', mode);
    formData.append('file', file);
    return requestForm('PUT', `${ownerPath}/csv-source`, formData);
  },
  getCsvSource: (ownerPath) =>
    request('GET', `${ownerPath}/csv-source`).catch((e) => {
      if (e.status === 404) return null;
      throw e;
    }),
  deleteCsvSource: (ownerPath) => request('DELETE', `${ownerPath}/csv-source`),

  listCertificates: () => request('GET', '/api/certificates'),
  importCertificate: (formData) => requestForm('POST', '/api/certificates/import', formData),
  listCertBundles: () => request('GET', '/api/cert-bundles'),
  getGatewayTls: () => request('GET', '/api/gateway/tls'),

  fetchImportUrl: (url) => request('POST', '/api/tools/fetch-url', { url }),

  getSmtpSettings: () => request('GET', '/api/smtp/settings'),
  saveSmtpSettings: (s) => request('PUT', '/api/smtp/settings', s),
  testSmtpSend: (to) => request('POST', '/api/smtp/test', { to }),
  listEmailTemplates: () => request('GET', '/api/smtp/templates'),
  createEmailTemplate: (t) => request('POST', '/api/smtp/templates', t),
  updateEmailTemplate: (id, t) => request('PUT', `/api/smtp/templates/${id}`, t),
  deleteEmailTemplate: (id) => request('DELETE', `/api/smtp/templates/${id}`),

  listWorkspaces: () => request('GET', '/api/apiclient/workspaces'),
  createWorkspace: (w) => request('POST', '/api/apiclient/workspaces', w),
  // password is only actually required (and verified) when the workspace
  // is locked — see internal/web/api/apiclient.go's deleteWorkspace, which
  // always demands it fresh for a locked workspace regardless of whether
  // this browser already unlocked it earlier this session for editing.
  deleteWorkspace: (id, password) =>
    request('DELETE', `/api/apiclient/workspaces/${id}`, password ? { password } : undefined, undefined, { skipAuthRedirect: true }),
  // body: {credentialType, newPassword, currentPassword?} — currentPassword
  // is only required (and verified) when the workspace already has a lock.
  // skipAuthRedirect: true on all three — a 401 here means "wrong
  // workspace password", not "your admin session expired" (see request()'s
  // own comment on opts.skipAuthRedirect).
  lockWorkspace: (id, body) => request('POST', `/api/apiclient/workspaces/${id}/lock`, body, undefined, { skipAuthRedirect: true }),
  unlockWorkspace: (id, password) => request('POST', `/api/apiclient/workspaces/${id}/unlock`, { password }, undefined, { skipAuthRedirect: true }),
  removeWorkspaceLock: (id, password) => request('POST', `/api/apiclient/workspaces/${id}/remove-lock`, { password }, undefined, { skipAuthRedirect: true }),

  listCollections: (workspaceId) => {
    const query = workspaceId ? '?workspaceId=' + encodeURIComponent(workspaceId) : '';
    return request('GET', '/api/apiclient/collections' + query);
  },
  createCollection: (c) => request('POST', '/api/apiclient/collections', c),
  getCollection: (id) => request('GET', `/api/apiclient/collections/${id}`),
  updateCollection: (id, c) => request('PUT', `/api/apiclient/collections/${id}`, c),
  deleteCollection: (id) => request('DELETE', `/api/apiclient/collections/${id}`),
  moveCollection: (id, workspaceId) => request('POST', `/api/apiclient/collections/${id}/move`, { workspaceId }),
  importPostmanCollection: (postmanJson, workspaceId) =>
    request('POST', '/api/apiclient/collections/import', { postmanJson, workspaceId }),
  // files: [{fileName, content}] — content already read client-side (same as
  // ImportSource's single-file path), so this is still a plain JSON POST,
  // not a multipart upload. Returns {imported: [...], failed: [...]} —
  // a malformed file in the batch is reported, not fatal to the rest.
  importPostmanCollectionsBulk: (files, workspaceId) =>
    request('POST', '/api/apiclient/collections/import-bulk', { files, workspaceId }),
  importSoapUICollection: (projectXml, workspaceId) =>
    request('POST', '/api/apiclient/collections/import-soapui', { projectXml, workspaceId }),
  importWSDLCollection: (wsdlContent, url, name, extraSchemas, workspaceId) =>
    request('POST', '/api/apiclient/collections/import-wsdl', { wsdlContent, url, name, extraSchemas, workspaceId }),

  listEnvironments: (workspaceId) => {
    const query = workspaceId ? '?workspaceId=' + encodeURIComponent(workspaceId) : '';
    return request('GET', '/api/apiclient/environments' + query);
  },
  createEnvironment: (e) => request('POST', '/api/apiclient/environments', e),
  updateEnvironment: (id, e) => request('PUT', `/api/apiclient/environments/${id}`, e),
  deleteEnvironment: (id) => request('DELETE', `/api/apiclient/environments/${id}`),
  importPostmanEnvironment: (postmanJson, workspaceId) =>
    request('POST', '/api/apiclient/environments/import', { postmanJson, workspaceId }),

  // signal (an AbortController.signal) lets the caller force-stop a
  // request: aborting it closes the fetch to AirMock's own admin server,
  // which — via net/http's server cancelling the handler's request context
  // on client disconnect, propagated all the way into
  // apiclient.ExecuteContext — actually cancels the REAL outbound network
  // call too, not just this tab's wait for it.
  // context: optional {collectionId, collectionName, requestName} — purely
  // for hit-log attribution (Dashboard/Log History), never used to build
  // the outbound request itself. Omit it (or leave fields blank) for a
  // draft/unsaved-tab send, same as before this existed.
  executeRequest: (spec, variables, signal, context = {}) =>
    request('POST', '/api/apiclient/execute', { spec, variables, ...context }, signal),
  runLoadTest: (spec, variables, concurrency, totalRequests, durationSecs, detailed, signal, context = {}) =>
    request('POST', '/api/apiclient/loadtest', { spec, variables, concurrency, totalRequests, durationSecs, detailed, ...context }, signal),
  runWSLoadTest: (spec, variables, concurrency, totalRequests, durationSecs, detailed, signal, context = {}) =>
    request('POST', '/api/apiclient/ws-loadtest', { spec, variables, concurrency, totalRequests, durationSecs, detailed, ...context }, signal),
  listLoadTestRuns: (itemId) => request('GET', `/api/apiclient/loadtest-runs?itemId=${encodeURIComponent(itemId)}`),
  getLoadTestRun: (id) => request('GET', `/api/apiclient/loadtest-runs/${encodeURIComponent(id)}`),
  deleteLoadTestRun: (id) => request('DELETE', `/api/apiclient/loadtest-runs/${encodeURIComponent(id)}`),
  // exportLoadTestResult downloads a Detailed live result's FULL data,
  // including per-request response bodies/headers — those only ever exist
  // in memory (never persisted, see apiclient.LoadTestSample), so unlike
  // loadTestResultExportUrl below (a plain <a download> link hitting a
  // GET-by-id route), this POSTs the result the caller already has back to
  // the server to render as a file, and returns the resulting Blob for the
  // caller to trigger a download from (same two-step split as this file's
  // other Blob-based downloads).
  exportLoadTestResult: async (result, format = 'csv') => {
    const res = await fetch(`/api/apiclient/loadtest-export?format=${format}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ result }),
    });
    if (!res.ok) throw new Error(`export failed (${res.status})`);
    const match = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '');
    return { blob: await res.blob(), filename: match?.[1] ?? `airmock-loadtest.${format}` };
  },
  curlImport: (curl) => request('POST', '/api/apiclient/curl-import', { curl }),
  curlExport: (spec) => request('POST', '/api/apiclient/curl-export', spec),
  codeSnippet: (spec, language) => request('POST', '/api/apiclient/snippet', { spec, language }),
  wsExchange: (spec, variables, listenSecs, signal) => request('POST', '/api/apiclient/ws-exchange', { spec, variables, listenSecs }, signal),

  listHitLog: (opts = {}) => {
    const params = hitLogFilterParams(opts);
    if (opts.limit) params.set('limit', opts.limit);
    const query = params.toString() ? '?' + params.toString() : '';
    return request('GET', '/api/hitlog' + query);
  },
  promoteToMock: (id) => request('POST', `/api/hitlog/${id}/promote-to-mock`),
  deleteHitLogEntry: (id) => request('DELETE', `/api/hitlog/${id}`),
  // Deletes every entry matching opts' filters (mockId/since/until/
  // protocol/direction/method/statusClass/search) — deliberately no limit,
  // since a bulk delete should cover everything the filter matches, not
  // just however many rows the on-screen table happens to be showing.
  deleteHitLogMatching: (opts = {}) => {
    const query = hitLogFilterParams(opts).toString();
    return request('DELETE', '/api/hitlog' + (query ? '?' + query : ''));
  },

  listScheduledEvents: () => request('GET', '/api/scheduled-events'),
  createScheduledEvent: (e) => request('POST', '/api/scheduled-events', e),
  updateScheduledEvent: (id, e) => request('PUT', `/api/scheduled-events/${id}`, e),
  deleteScheduledEvent: (id) => request('DELETE', `/api/scheduled-events/${id}`),
  fireScheduledEventNow: (id) => request('POST', `/api/scheduled-events/${id}/fire-now`),
};

export function collectionExportUrl(id) {
  return `/api/apiclient/collections/${id}/export`;
}

// collectionsExportBulkUrl builds a single download covering every
// collection id given — a .zip, one <name>.postman_collection.json per
// collection, since triggering N separate <a download> clicks for a
// multi-select export gets throttled/blocked by most browsers past the
// first one or two.
export function collectionsExportBulkUrl(ids) {
  return '/api/apiclient/collections/export-bulk?ids=' + ids.map(encodeURIComponent).join(',');
}

export function mocksExportUrl() {
  return '/api/mocks/export';
}

// hitLogFilterParams renders the Log History filter bar's fields
// (everything except pagination) into URLSearchParams — shared by
// listHitLog, hitLogExportUrl, and api.deleteHitLogMatching so all three
// always agree on exactly what "matches the current filters" means.
function hitLogFilterParams(opts = {}) {
  const params = new URLSearchParams();
  if (opts.mockId) params.set('mockId', opts.mockId);
  if (opts.since) params.set('since', opts.since);
  if (opts.until) params.set('until', opts.until);
  if (opts.protocolType) params.set('protocolType', opts.protocolType);
  if (opts.direction) params.set('direction', opts.direction);
  if (opts.method) params.set('method', opts.method);
  if (opts.statusClass) params.set('statusClass', opts.statusClass);
  if (opts.search) params.set('search', opts.search);
  return params;
}

// hitLogExportUrl builds a downloadable-file URL (meant for an <a download>
// link, same convention as mocksExportUrl/collectionExportUrl) covering
// every hit-log entry matching the current filter bar, not just whatever
// page is currently on screen.
export function hitLogExportUrl(opts = {}, format = 'csv') {
  const params = hitLogFilterParams(opts);
  params.set('format', format);
  return '/api/hitlog/export?' + params.toString();
}

// loadTestResultExportUrl builds a downloadable-file URL (same <a download>
// convention as above) for one persisted load-test run's full result
// (config, aggregate, every sample) — runId is either a live result's own
// runId (set the moment the run finishes, see apiclient.LoadTestResult) or
// a history row's id, both the same underlying run.
export function loadTestResultExportUrl(runId, format = 'csv') {
  return `/api/apiclient/loadtest-runs/${encodeURIComponent(runId)}/export?format=${format}`;
}
