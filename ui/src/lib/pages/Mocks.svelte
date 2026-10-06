<script>
  import { onMount, tick } from 'svelte';
  import { api, mocksExportUrl } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import ImportSource from '../ImportSource.svelte';
  import InfoTooltip from '../InfoTooltip.svelte';
  import UsageSnippet from '../UsageSnippet.svelte';
  import VersionHistory from '../VersionHistory.svelte';
  import { usageForRest, usageForSoap, usageForGraphql } from '../mockUsage.js';
  import { pendingFocus, consumePendingFocus } from '../router.js';
  import ContextMenu from '../ContextMenu.svelte';
  import WorkspaceUnlockModal from '../WorkspaceUnlockModal.svelte';
  import { runWithWorkspaceUnlock } from '../workspaceLock.js';
  import CsvSourceField from '../CsvSourceField.svelte';

  const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];
  // Lets an import form's Project dropdown offer "+ Create new project"
  // alongside existing ones — same sentinel-value pattern as Collections.svelte's
  // save-draft picker — instead of forcing "create the project first via
  // the separate + New project button, THEN come back and import" as two
  // disjoint steps.
  const NEW_PROJECT_SENTINEL = '__new__';

  // Async ack/callback bodies render through the exact same engine as the
  // main sync response body (mock.RenderBody, internal/mock/template.go) —
  // these fields previously had no help text at all, unlike that one, which
  // made this capability invisible in the UI even though it already worked.
  const asyncTemplateHelpText =
    'Rendered via the same Go template engine as the main response body: {{.Request.Body/.Query/.Header/.PathParams}} pull from the original triggering request, {{fake "..."}}/{{counter "name"}}/{{csv "column"}} generate dynamic values, and sprig helpers ({{uuidv4}}, {{now}}, {{upper .x}}, ...) all work here too.';

  // The engine (writeTemplatedResponseWithDefaultContentType in
  // rest_matcher.go) already accepts an arbitrary Response.Headers map and
  // defaults Content-Type to application/json only when nothing set it —
  // this was previously entirely unreachable from the UI, which only ever
  // bound statusCode/bodyTemplate, hardcoding every response to JSON no
  // matter what was actually in the body. A dropdown mapping to the right
  // Content-Type is far more discoverable than "add a custom header
  // yourself," and covers the common cases (SOAP/XML mocks included).
  const BODY_TYPES = [
    { value: 'json', label: 'JSON', contentType: 'application/json' },
    { value: 'xml', label: 'XML', contentType: 'application/xml' },
    { value: 'text', label: 'Text', contentType: 'text/plain' },
    { value: 'html', label: 'HTML', contentType: 'text/html' },
  ];

  function bodyTypeFromHeaders(headers) {
    const ct = Object.entries(headers ?? {}).find(([k]) => k.toLowerCase() === 'content-type')?.[1] ?? '';
    const match = BODY_TYPES.find((t) => ct.toLowerCase().includes(t.contentType.split('/')[1]));
    return match?.value ?? 'json';
  }

  function contentTypeFor(bodyType) {
    return BODY_TYPES.find((t) => t.value === bodyType)?.contentType ?? 'application/json';
  }

  function jsonError(text) {
    try {
      JSON.parse(text);
      return '';
    } catch (e) {
      return e.message;
    }
  }

  let mocks = [];
  let projects = [];
  // Collections workspaces (apiclient.Workspace) — a completely separate
  // grouping from projects above, used only to optionally map a mock to a
  // workspace so a lock on that workspace (set from the Collections page)
  // gates editing/deleting it. See workspaceLock.js.
  let workspaces = [];
  // Non-null while a locked mapped workspace needs its password before a
  // pending save/delete can proceed — see runWithWorkspaceUnlock.
  let pendingUnlock = null;
  let emailTemplates = [];
  let gatewayPort = 8081;
  let gatewayTls = { enabled: false, clientCertMode: '' };
  let loading = true;

  // A project with its own dedicated gateway port (newProjectGatewayPort /
  // p.gatewayPort, set up in the project form) serves its mocks there
  // instead of the shared default gateway — so the example command has to
  // resolve per-mock rather than always using the one shared gatewayPort.
  function portForMock(m) {
    if (m.projectId) {
      const p = projects.find((x) => x.id === m.projectId);
      if (p?.gatewayPort) return p.gatewayPort;
    }
    return gatewayPort;
  }

  // Resolves whether THIS mock's actual listener is HTTPS, and under what
  // client-cert mode — a project with its own dedicated port has its own
  // independent TLS config (or none: a dedicated port with no TLS set is
  // always plain HTTP, never inherits the shared gateway's setting); a mock
  // with no project (or a project still on the shared port) follows the
  // one shared gateway TLS setting instead.
  function tlsForMock(m) {
    if (m.projectId) {
      const p = projects.find((x) => x.id === m.projectId);
      if (p?.gatewayPort) return { enabled: !!p.tls, clientCertMode: p.tls?.clientCertMode ?? '' };
    }
    return gatewayTls;
  }

  function usageForMock(m) {
    const port = portForMock(m);
    const tls = tlsForMock(m);
    if (m.protocolType === 'soap') return usageForSoap(m, port, tls);
    if (m.protocolType === 'graphql') return usageForGraphql(m, port, tls);
    return usageForRest(m, port, tls);
  }

  // Builds the same example request usageForMock's curl command shows, but
  // as a RequestSpec object for api.executeRequest instead of a copyable
  // string — lets "Send test request" fire the exact same request a user
  // would otherwise have to paste into a terminal (or Collections) to try.
  function buildTestRequestSpec(m) {
    const isHttps = tlsForMock(m).enabled;
    const scheme = isHttps ? 'https' : 'http';
    const url = `${scheme}://localhost:${portForMock(m)}${m.pathPattern}`;
    // insecure: skips TLS certificate verification, same as the "Skip TLS
    // certificate verification" checkbox in Collections — set unconditionally
    // here rather than left for the user to discover, since this is always
    // testing AirMock's OWN mock over its own self-signed cert, never an
    // external endpoint where skipping verification should be a conscious,
    // explicit choice. Without this, "Send test request" against any
    // TLS-enabled mock fails with the exact hostname-verification error a
    // self-signed cert always produces.
    const base = isHttps ? { insecure: true } : {};

    if (m.protocolType === 'soap') {
      const op = m.operationName || 'YourOperation';
      return {
        ...base,
        method: 'POST', url,
        headers: [{ key: 'Content-Type', value: 'text/xml' }, { key: 'SOAPAction', value: m.soapAction || op }],
        body: `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><${op}/></soap:Body></soap:Envelope>`,
      };
    }
    if (m.protocolType === 'graphql') {
      const op = m.operationName || 'yourQuery';
      return { ...base, method: 'POST', url, headers: [{ key: 'Content-Type', value: 'application/json' }], body: JSON.stringify({ query: `{ ${op} }` }) };
    }
    const method = m.method || 'GET';
    if (method === 'GET' || method === 'HEAD') return { ...base, method, url };
    return { ...base, method, url, headers: [{ key: 'Content-Type', value: 'application/json' }], body: '{}' };
  }

  let testResults = {}; // mock id -> ExecutionResult (or {error} on transport failure)
  let testingIds = new Set();

  async function sendTestRequest(m) {
    testingIds = new Set([...testingIds, m.id]);
    try {
      const result = await api.executeRequest(buildTestRequestSpec(m), {});
      testResults = { ...testResults, [m.id]: result };
    } catch (e) {
      testResults = { ...testResults, [m.id]: { error: e.message } };
    } finally {
      testingIds = new Set([...testingIds].filter((id) => id !== m.id));
    }
  }

  let showForm = false;
  let editingId = '';

  let expandedId = ''; // mock id currently expanded, '' means none
  let hitsByMock = {}; // mockId -> hit-log entries, fetched fresh on every expand
  let hitsLoading = '';
  let expandedHitId = '';
  let versionsByMock = {}; // mockId -> version history, fetched fresh on every expand
  let restoringVersionId = '';

  // Pretty-prints a JSON body for readability; falls back to the raw text
  // untouched for a non-JSON (or empty) body rather than erroring.
  function formatBody(text) {
    if (!text) return '';
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  }

  async function toggleExpand(id) {
    if (expandedId === id) {
      expandedId = '';
      return;
    }
    expandedId = id;
    hitsLoading = id;
    try {
      const [hits, versions] = await Promise.all([
        api.listHitLog({ mockId: id }),
        api.listMockVersions(id).catch(() => []),
      ]);
      hitsByMock = { ...hitsByMock, [id]: hits ?? [] };
      versionsByMock = { ...versionsByMock, [id]: versions ?? [] };
    } catch (e) {
      showToast(e.message, 'err');
      hitsByMock = { ...hitsByMock, [id]: [] };
    } finally {
      hitsLoading = '';
    }
  }

  async function restoreVersion(mockId, versionId) {
    if (!confirm('Restore this version? The current state will be saved to history first, so this can be undone.')) return;
    restoringVersionId = versionId;
    try {
      await api.restoreMockVersion(mockId, versionId);
      showToast('Mock restored', 'ok');
      versionsByMock = { ...versionsByMock, [mockId]: (await api.listMockVersions(mockId)) ?? [] };
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      restoringVersionId = '';
    }
  }

  let expandedProjectIds = new Set();
  let showNewProjectForm = false;
  let newProjectName = '';
  let newProjectBasePath = '';
  let newProjectGatewayPort = '';
  let newProjectTlsEnabled = false;
  let newProjectTlsBundleId = '';
  let newProjectTlsCertificateId = '';
  let newProjectTlsClientCertMode = '';
  let newProjectTlsClientCaId = '';
  let newProjectWorkspaceId = '';
  let renamingProjectId = '';
  let renameProjectName = '';
  let renameProjectBasePath = '';
  let renameProjectGatewayPort = '';
  let renameProjectTlsEnabled = false;
  let renameProjectTlsBundleId = '';
  let renameProjectTlsCertificateId = '';
  let renameProjectTlsClientCertMode = '';
  let renameProjectTlsClientCaId = '';
  let renameProjectWorkspaceId = '';
  let certificates = [];
  let certBundles = [];
  $: serverCerts = certificates.filter((c) => c.kind === 'server');
  $: caCerts = certificates.filter((c) => c.kind === 'ca');

  async function loadCertificates() {
    try {
      certificates = (await api.listCertificates()) ?? [];
    } catch {
      certificates = []; // TLS section just shows "no certificates" — not fatal to the page
    }
  }

  async function loadCertBundles() {
    try {
      certBundles = (await api.listCertBundles()) ?? [];
    } catch {
      certBundles = []; // TLS section just falls back to picking certs individually
    }
  }

  let searchQuery = '';
  function matchesSearch(m, query) {
    const needle = query.toLowerCase();
    return (
      (m.name || '').toLowerCase().includes(needle) ||
      (m.pathPattern || '').toLowerCase().includes(needle) ||
      (m.method || '').toLowerCase().includes(needle) ||
      (m.operationName || '').toLowerCase().includes(needle)
    );
  }
  $: filteredMocks = searchQuery.trim() ? mocks.filter((m) => matchesSearch(m, searchQuery)) : mocks;
  $: mocksByProject = groupMocksByProject(filteredMocks);
  // Unfiltered, kept alongside the search-narrowed mocksByProject above —
  // the project card's own "N APIs" count and its "Enable all"/"Disable
  // all" toggle both need to reflect (and act on) EVERY mock in the
  // project regardless of what the search box currently narrows the
  // visible rows down to; acting on the filtered subset while a search is
  // active would silently leave the rest of the project untouched despite
  // the button's own "every mock in this project" wording.
  $: allMocksByProject = groupMocksByProject(mocks);

  // Bulk selection is keyed by mock id rather than by row, so it survives
  // the list re-rendering (grouped by project, filtered by search) without
  // needing to track anything beyond "which ids are checked."
  let selectedIds = new Set();
  // The project a bulk "Move to project" targets — '' means Ungrouped,
  // same convention as a mock's own ProjectID field.
  let bulkMoveProjectId = '';
  function toggleSelect(id) {
    const next = new Set(selectedIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selectedIds = next;
  }
  function clearSelection() {
    selectedIds = new Set();
  }
  async function bulkAction(action, projectId) {
    if (selectedIds.size === 0) return;
    if (action === 'delete' && !confirm(`Delete ${selectedIds.size} selected mock${selectedIds.size === 1 ? '' : 's'}? This cannot be undone.`)) return;
    try {
      const result = await api.bulkMockAction([...selectedIds], action, projectId);
      const verb = action === 'move' ? 'moved' : `${action}d`;
      if (result.failed?.length) {
        showToast(`${result.succeeded.length} succeeded, ${result.failed.length} failed`, 'err');
      } else {
        showToast(`${result.succeeded.length} mock${result.succeeded.length === 1 ? '' : 's'} ${verb}`, 'ok');
      }
      clearSelection();
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }
  // A single toggle for an entire project's mocks, rather than clicking
  // each one's own enable/disable individually — mirrors bulkAction's own
  // request/toast/reload shape but always targets every mock currently in
  // this project (via mocksByProject, not the separate row-selection
  // mechanism bulkAction reads from). If any mock in the project is
  // enabled, the action is "disable" (bring everything off together);
  // otherwise it's "enable" (bring a fully-disabled project back up).
  async function toggleProjectMocks(p) {
    const mocksInProject = allMocksByProject.byProject[p.id] ?? [];
    if (mocksInProject.length === 0) return;
    const action = mocksInProject.some((m) => m.enabled) ? 'disable' : 'enable';
    try {
      const result = await api.bulkMockAction(mocksInProject.map((m) => m.id), action);
      showToast(`${result.succeeded.length} mock${result.succeeded.length === 1 ? '' : 's'} in "${p.name}" ${action}d`, result.failed?.length ? 'err' : 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function groupMocksByProject(list) {
    const byProject = {};
    const ungrouped = [];
    for (const m of list) {
      if (m.projectId) (byProject[m.projectId] ??= []).push(m);
      else ungrouped.push(m);
    }
    return { byProject, ungrouped };
  }

  async function loadProjects() {
    try {
      projects = (await api.listMockProjects()) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function loadWorkspaces() {
    try {
      workspaces = (await api.listWorkspaces()) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function toggleProjectExpand(id) {
    const next = new Set(expandedProjectIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expandedProjectIds = next;
  }

  // Shared by createProject/saveRenameProject — a bundle's own server
  // cert/CA take precedence server-side (see resolveBundleTLSRefs), so
  // bundleId and certificateId are mutually exclusive here rather than
  // both being sent whenever a bundle happens to be selected.
  function buildProjectTls(enabled, bundleId, certificateId, clientCertMode, clientCaId) {
    if (!enabled || (!bundleId && !certificateId)) return null;
    const requiresCa = clientCertMode === 'optional' || clientCertMode === 'required';
    return {
      bundleId: bundleId || '',
      certificateId: bundleId ? '' : certificateId,
      clientCertMode: clientCertMode || '',
      clientCaId: requiresCa ? clientCaId : '',
    };
  }

  async function createProject() {
    if (!newProjectName.trim()) return;
    try {
      const p = await runWithWorkspaceUnlock(
        () =>
          api.createMockProject({
            name: newProjectName.trim(),
            basePath: newProjectBasePath.trim(),
            gatewayPort: Number(newProjectGatewayPort) || 0,
            tls: buildProjectTls(newProjectTlsEnabled, newProjectTlsBundleId, newProjectTlsCertificateId, newProjectTlsClientCertMode, newProjectTlsClientCaId),
            workspaceId: newProjectWorkspaceId,
          }),
        (pending) => (pendingUnlock = pending)
      );
      newProjectName = '';
      newProjectBasePath = '';
      newProjectGatewayPort = '';
      newProjectTlsEnabled = false;
      newProjectTlsBundleId = '';
      newProjectTlsCertificateId = '';
      newProjectTlsClientCertMode = '';
      newProjectTlsClientCaId = '';
      newProjectWorkspaceId = '';
      showNewProjectForm = false;
      expandedProjectIds = new Set([...expandedProjectIds, p.id]);
      await loadProjects();
      showToast(`Project "${p.name}" created`, 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function startRenameProject(p) {
    renamingProjectId = p.id;
    renameProjectName = p.name;
    renameProjectBasePath = p.basePath ?? '';
    renameProjectGatewayPort = p.gatewayPort ? String(p.gatewayPort) : '';
    renameProjectTlsEnabled = !!p.tls;
    renameProjectTlsBundleId = p.tls?.bundleId ?? '';
    renameProjectTlsCertificateId = p.tls?.certificateId ?? '';
    renameProjectTlsClientCertMode = p.tls?.clientCertMode ?? '';
    renameProjectTlsClientCaId = p.tls?.clientCaId ?? '';
    renameProjectWorkspaceId = p.workspaceId ?? '';
  }

  async function saveRenameProject(p) {
    try {
      await runWithWorkspaceUnlock(
        () =>
          api.updateMockProject(p.id, {
            ...p,
            name: renameProjectName.trim() || p.name,
            basePath: renameProjectBasePath.trim(),
            gatewayPort: Number(renameProjectGatewayPort) || 0,
            tls: buildProjectTls(renameProjectTlsEnabled, renameProjectTlsBundleId, renameProjectTlsCertificateId, renameProjectTlsClientCertMode, renameProjectTlsClientCaId),
            workspaceId: renameProjectWorkspaceId,
          }),
        (pending) => (pendingUnlock = pending)
      );
      renamingProjectId = '';
      await loadProjects();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function removeProject(id) {
    const p = projects.find((x) => x.id === id);
    // allMocksByProject (unfiltered), not mocksByProject — this count feeds
    // a confirm() warning about how many APIs get deleted along with the
    // project, which must reflect every mock in it regardless of what's
    // currently narrowed by the search box.
    const count = allMocksByProject.byProject[id]?.length ?? 0;
    const warning = count
      ? ` The ${count} API${count === 1 ? '' : 's'} inside will be permanently deleted too — not just unassigned.`
      : '';
    if (!confirm(`Delete project "${p?.name ?? id}"?${warning}\n\nThis cannot be undone.`)) return;
    try {
      await runWithWorkspaceUnlock(() => api.deleteMockProject(id), (pending) => (pendingUnlock = pending));
      await Promise.all([loadProjects(), load()]);
      showToast(count ? `Project and ${count} API${count === 1 ? '' : 's'} deleted` : 'Project deleted', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Clones a project and every REST/SOAP/GraphQL mock inside it — each
  // mock gets a fresh id (not literally reusing the source's) and, for
  // REST specifically, a "-copy" path suffix so it can never collide with
  // the original (whether or not the new project ends up isolated on its
  // own dedicated port). The new project always starts on the shared
  // default gateway (no dedicated port copied) — copying a dedicated port
  // verbatim would immediately collide with the original project's own
  // still-running listener on that same port.
  async function duplicateProject(p) {
    const mocksInProject = allMocksByProject.byProject[p.id] ?? [];
    if (!confirm(`Duplicate project "${p.name}" and all ${mocksInProject.length} of its API${mocksInProject.length === 1 ? '' : 's'}?`)) return;
    try {
      const newProject = await api.createMockProject({ name: `${p.name} (copy)`, basePath: p.basePath ?? '' });
      for (const m of mocksInProject) {
        await api.createMock({
          // Mock names are unique instance-wide (not just within a
          // project), same as duplicateMock's own single-mock "(copy)"
          // suffix — reusing the source name verbatim collides with the
          // still-existing original.
          name: `${m.name} (copy)`,
          protocolType: m.protocolType,
          operationName: m.operationName,
          method: m.method,
          pathPattern: m.protocolType === 'rest' ? `${m.pathPattern}-copy` : m.pathPattern,
          enabled: m.enabled,
          mode: m.mode,
          projectId: newProject.id,
          response: m.response,
          asyncConfig: m.asyncConfig,
          validation: m.validation,
          responseRules: m.responseRules,
          scenario: m.scenario,
          proxy: m.proxy,
          fault: m.fault,
        });
      }
      await Promise.all([loadProjects(), load()]);
      showToast(`Duplicated "${p.name}" (${mocksInProject.length} API${mocksInProject.length === 1 ? '' : 's'})`, 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function openCreateForm(projectId) {
    form = emptyForm();
    form.projectId = projectId ?? '';
    const project = projects.find((p) => p.id === form.projectId);
    if (project?.basePath) {
      form.pathPattern = project.basePath.replace(/\/$/, '') + '/';
    }
    form.delayMs = newMockDefaults.defaultResponseDelayMs;
    if (newMockDefaults.defaultFailureRatePercent > 0) {
      form.faultEnabled = true;
      form.faultErrorRatePercent = newMockDefaults.defaultFailureRatePercent;
    }
    editingId = '';
    showForm = true;
  }
  // Resolves an import form's Project select value to a real project ID —
  // an existing one is returned as-is; NEW_PROJECT_SENTINEL creates a fresh
  // project from newName first (added to the in-memory projects list so it
  // shows up immediately without a full reload).
  async function resolveProjectId(selectedId, newName) {
    if (selectedId !== NEW_PROJECT_SENTINEL) return selectedId;
    const created = await api.createMockProject({ name: newName.trim() });
    projects = [...projects, created];
    return created.id;
  }

  let showWsdlForm = false;
  let wsdlPathPattern = '/soap/service';
  let wsdlContent = '';
  let wsdlProjectId = '';
  let wsdlNewProjectName = '';

  async function importWsdl() {
    try {
      const projectId = await resolveProjectId(wsdlProjectId, wsdlNewProjectName);
      const created = await api.importWSDL(wsdlContent, wsdlPathPattern, projectId);
      showToast(`Imported ${created.length} SOAP operation${created.length === 1 ? '' : 's'}`, 'ok');
      wsdlContent = '';
      // Point the dropdown at the (possibly just-created) project by id and
      // clear the new-project name — otherwise a second import in the same
      // still-open form flow, done without re-touching the dropdown, would
      // stay on NEW_PROJECT_SENTINEL and silently create a SECOND,
      // duplicate project reusing the same typed name instead of adding to
      // the one just created.
      wsdlProjectId = projectId;
      wsdlNewProjectName = '';
      showWsdlForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // SoapUI mock import: unlike WSDL/GraphQL/OpenAPI import, the endpoint
  // path per scaffolded mock isn't chosen here at all — it comes from each
  // MockService's own `path` in the project XML (see
  // soapui.ImportMockServices), since a project can define several mock
  // services on different paths in one file.
  let showSoapUIMockForm = false;
  let soapUIMockProjectXml = '';
  let soapUIMockProjectId = '';
  let soapUIMockNewProjectName = '';

  async function importSoapUIMocks() {
    try {
      const projectId = await resolveProjectId(soapUIMockProjectId, soapUIMockNewProjectName);
      const created = await api.importSoapUIMocks(soapUIMockProjectXml, projectId);
      showToast(`Imported ${created.length} SOAP mock${created.length === 1 ? '' : 's'}`, 'ok');
      soapUIMockProjectXml = '';
      // See importWsdl's identical comment: avoids creating a duplicate
      // project on a second import if the dropdown is left untouched.
      soapUIMockProjectId = projectId;
      soapUIMockNewProjectName = '';
      showSoapUIMockForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  let showGraphqlForm = false;
  let graphqlPathPattern = '/graphql';
  let graphqlSdl = '';
  let graphqlProjectId = '';
  let graphqlNewProjectName = '';

  async function importGraphqlSchema() {
    try {
      const projectId = await resolveProjectId(graphqlProjectId, graphqlNewProjectName);
      const created = await api.importGraphQLSchema(graphqlSdl, graphqlPathPattern, projectId);
      showToast(`Imported ${created.length} GraphQL operation${created.length === 1 ? '' : 's'}`, 'ok');
      graphqlSdl = '';
      // See importWsdl's identical comment: avoids creating a duplicate
      // project on a second import if the dropdown is left untouched.
      graphqlProjectId = projectId;
      graphqlNewProjectName = '';
      showGraphqlForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  let showOpenapiForm = false;
  let openapiPathPrefix = '';
  let openapiContent = '';
  let openapiProjectId = '';
  let openapiNewProjectName = '';

  async function importOpenapi() {
    try {
      const projectId = await resolveProjectId(openapiProjectId, openapiNewProjectName);
      const created = await api.importOpenAPI(openapiContent, openapiPathPrefix, projectId);
      showToast(`Imported ${created.length} REST operation${created.length === 1 ? '' : 's'}`, 'ok');
      openapiContent = '';
      // See importWsdl's identical comment: avoids creating a duplicate
      // project on a second import if the dropdown is left untouched.
      openapiProjectId = projectId;
      openapiNewProjectName = '';
      showOpenapiForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  let showTrafficImportForm = false;
  let trafficImportFormat = 'har'; // "har" | "postman-examples"
  let trafficImportPathPrefix = '';
  let trafficImportContent = '';
  let trafficImportProjectId = '';
  let trafficImportNewProjectName = '';

  async function importTraffic() {
    try {
      const projectId = await resolveProjectId(trafficImportProjectId, trafficImportNewProjectName);
      const importFn = trafficImportFormat === 'har' ? api.importHAR : api.importPostmanExamples;
      const created = await importFn(trafficImportContent, trafficImportPathPrefix, projectId);
      showToast(`Scaffolded ${created.length} mock${created.length === 1 ? '' : 's'} from captured traffic`, 'ok');
      trafficImportContent = '';
      // See importWsdl's identical comment: avoids creating a duplicate
      // project on a second import if the dropdown is left untouched.
      trafficImportProjectId = projectId;
      trafficImportNewProjectName = '';
      showTrafficImportForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  let showImportMocksForm = false;
  let importMocksJson = '';
  let importMocksProjectId = '';
  let importMocksNewProjectName = '';

  async function importMocks() {
    try {
      const projectId = await resolveProjectId(importMocksProjectId, importMocksNewProjectName);
      const result = await api.importMocks(importMocksJson, projectId);
      const skippedNote = result.skipped?.length ? `, ${result.skipped.length} skipped` : '';
      showToast(`Imported ${result.imported.length} mock${result.imported.length === 1 ? '' : 's'}${skippedNote}`, result.imported.length ? 'ok' : 'err');
      importMocksJson = '';
      // See importWsdl's identical comment: avoids creating a duplicate
      // project on a second import if the dropdown is left untouched.
      importMocksProjectId = projectId;
      importMocksNewProjectName = '';
      showImportMocksForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Quick-start templates for the most common REST shapes — filling in a
  // realistic method/path/status/body (and, where it matters, a validation
  // rule) so starting a new mock is "pick the shape closest to what I need,
  // then tweak" instead of always typing every field from a blank GET/200.
  // Deliberately only patches the fields each template actually cares
  // about, leaving everything else (mode, project, enabled, ...) at
  // whatever the current form already has.
  const MOCK_TEMPLATES = [
    {
      id: 'health', label: 'Health check', description: 'GET /health → 200 { status: "ok" }',
      apply: (f) => ({ ...f, method: 'GET', pathPattern: '/health', statusCode: 200, bodyTemplate: '{\n  "status": "ok"\n}' }),
    },
    {
      id: 'get-by-id', label: 'Get by ID', description: 'GET /orders/{id} → 200, echoes the path param back',
      apply: (f) => ({
        ...f, method: 'GET', pathPattern: '/orders/{id}', statusCode: 200,
        bodyTemplate: '{\n  "id": "{{.Request.PathParams.id}}",\n  "status": "active"\n}',
      }),
    },
    {
      id: 'list', label: 'List (paginated)', description: 'GET /orders → 200, an items array plus page/limit/total',
      apply: (f) => ({
        ...f, method: 'GET', pathPattern: '/orders', statusCode: 200,
        bodyTemplate: '{\n  "items": [\n    {"id": "1", "status": "active"},\n    {"id": "2", "status": "active"}\n  ],\n  "page": {{.Request.Query.page | default "1"}},\n  "limit": {{.Request.Query.limit | default "20"}},\n  "total": 2\n}',
      }),
    },
    {
      id: 'create', label: 'Create (201)', description: 'POST /orders → 201, requires body.amount, echoes it back with a new id',
      apply: (f) => ({
        ...f, method: 'POST', pathPattern: '/orders', statusCode: 201,
        bodyTemplate: '{\n  "id": "{{uuidv4}}",\n  "amount": {{.Request.Body.amount}},\n  "status": "pending"\n}',
        validationRules: [...f.validationRules, { field: 'body.amount', required: true, type: 'number', constraint: '' }],
      }),
    },
    {
      id: 'auth', label: 'Auth / login', description: 'POST /auth/login → 200 with a token, requires body.username and body.password',
      apply: (f) => ({
        ...f, method: 'POST', pathPattern: '/auth/login', statusCode: 200,
        bodyTemplate: '{\n  "token": "{{uuidv4}}",\n  "expiresIn": 3600\n}',
        validationRules: [
          ...f.validationRules,
          { field: 'body.username', required: true, type: 'string', constraint: '' },
          { field: 'body.password', required: true, type: 'string', constraint: '' },
        ],
      }),
    },
    {
      id: 'not-found', label: 'Not found (404)', description: 'GET /orders/{id} → 404 error body',
      apply: (f) => ({
        ...f, method: 'GET', pathPattern: '/orders/{id}', statusCode: 404,
        bodyTemplate: '{\n  "error": "not found",\n  "id": "{{.Request.PathParams.id}}"\n}',
      }),
    },
  ];
  function applyMockTemplate(tpl) {
    form = tpl.apply(form);
  }

  let form = emptyForm();
  function emptyForm() {
    return {
      projectId: '',
      workspaceId: '',
      name: '',
      enabled: true,
      protocolType: 'rest',
      operationName: '',
      method: 'GET',
      pathPattern: '/hello/{name}',
      statusCode: 200,
      delayMs: 0,
      bodyTemplate: '{\n  "message": "hello {{.Request.PathParams.name}}"\n}',
      responseBodyType: 'json',
      mode: 'sync',
      ackStatusCode: 202,
      ackBodyTemplate: '{\n  "status": "accepted"\n}',
      ackBodyType: 'json',
      callbackChannel: 'http',
      callbackTargetMode: 'fixed',
      callbackFixedUrl: '',
      callbackExtractPath: 'body.callbackUrl',
      callbackBodyTemplate: '{\n  "status": "done"\n}',
      callbackDelayMs: 0,
      emailSubjectTemplate: '',
      emailTemplateId: '',
      validationRules: [],
      responseRulesJson: '',
      scenarioStepsJson: '[\n  {"statusCode": 200, "bodyTemplate": "{\\"status\\": \\"pending\\"}"},\n  {"statusCode": 200, "bodyTemplate": "{\\"status\\": \\"shipped\\"}"},\n  {"statusCode": 200, "bodyTemplate": "{\\"status\\": \\"delivered\\"}"}\n]',
      scenarioSessionKeyMode: 'ip',
      scenarioSessionKeyField: '',
      scenarioLoop: false,
      weightedResponsesJson: '[\n  {"weight": 90, "response": {"statusCode": 200, "bodyTemplate": "{\\"ok\\": true}"}},\n  {"weight": 10, "response": {"statusCode": 500, "bodyTemplate": "{\\"error\\": true}"}}\n]',
      proxyTargetBaseUrl: '',
      faultEnabled: false,
      faultLatencyJitterMs: 0,
      faultErrorRatePercent: 0,
      faultErrorStatusCodes: '500, 503',
      faultTimeoutRatePercent: 0,
    };
  }

  function addValidationRule() {
    form.validationRules = [...form.validationRules, { field: 'body.', required: true, type: 'string', constraint: '' }];
  }
  function removeValidationRule(i) {
    form.validationRules = form.validationRules.filter((_, idx) => idx !== i);
  }

  // rule.field stores the full backend path ("body.orderId",
  // "header.X-Api-Key", "query.limit", "xpath.//Order/Id") as one string —
  // these split it into a Source dropdown + a plain field-name input for
  // the UI (previously a single free-text box hinting only "body.orderId",
  // which made validating a header or query param — already fully
  // supported server-side — undiscoverable unless you already knew to type
  // the prefix yourself) and rejoin it on edit.
  const RULE_SOURCES = [
    { value: 'body', label: 'Body', placeholder: 'orderId' },
    { value: 'header', label: 'Header', placeholder: 'X-Api-Key' },
    { value: 'query', label: 'Query param', placeholder: 'limit' },
    { value: 'xpath', label: 'XPath (XML body)', placeholder: '//Order/Id' },
  ];
  function ruleSource(rule) {
    const prefix = rule.field.split('.')[0];
    return RULE_SOURCES.some((s) => s.value === prefix) ? prefix : 'body';
  }
  function ruleFieldName(rule) {
    const idx = rule.field.indexOf('.');
    return idx === -1 ? '' : rule.field.slice(idx + 1);
  }
  function setRuleSource(rule, source) {
    rule.field = `${source}.${ruleFieldName(rule)}`;
    form.validationRules = form.validationRules; // eslint-disable-line no-self-assign -- trigger Svelte reactivity
  }
  function setRuleFieldName(rule, name) {
    rule.field = `${ruleSource(rule)}.${name}`;
    form.validationRules = form.validationRules; // eslint-disable-line no-self-assign -- trigger Svelte reactivity
  }
  function constraintPlaceholder(type) {
    switch (type) {
      case 'enum': return 'a, b, c';
      case 'regex': return '^[A-Z]+$';
      case 'number': return 'min, max (either optional)';
      case 'string': return 'minLen, maxLen (either optional)';
      default: return '';
    }
  }
  function buildValidationRules() {
    return form.validationRules
      .filter((r) => r.field.trim())
      .map((r) => {
        const rule = { field: r.field.trim(), required: r.required, type: r.type };
        const parts = r.constraint.split(',').map((s) => s.trim());
        if (r.type === 'enum') rule.allowedValues = parts.filter(Boolean);
        else if (r.type === 'regex') rule.pattern = r.constraint;
        else if (r.type === 'number') {
          if (parts[0]) rule.min = Number(parts[0]);
          if (parts[1]) rule.max = Number(parts[1]);
        } else if (r.type === 'string') {
          if (parts[0]) rule.minLen = Number(parts[0]);
          if (parts[1]) rule.maxLen = Number(parts[1]);
        }
        return rule;
      });
  }

  async function load() {
    loading = true;
    try {
      // TCP/SMTP/MQTT/FTP/WS/Kafka/SMPP/Diameter/JMS mocks are a different
      // shape entirely (no method, and each uses a dedicated listener port
      // instead of method+path) — each gets its own page rather than
      // rendering garbled rows here.
      const OTHER_PAGES = new Set(['tcp', 'smtp', 'mqtt', 'ftp', 'ws', 'kafka', 'smpp', 'diameter', 'jms']);
      mocks = ((await api.listMocks()) ?? []).filter((m) => !OTHER_PAGES.has(m.protocolType));
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  onMount(async () => {
    await Promise.all([load(), loadProjects(), loadWorkspaces(), loadEmailTemplates(), loadConfig(), loadCertificates(), loadCertBundles(), loadGatewayTls(), loadNewMockDefaults()]);
  });

  // Pre-fills a brand-new mock's own response delay/fault error rate from
  // the instance-wide Settings-page defaults (see openCreateForm) — a
  // starting point new mocks inherit, not something enforced afterward.
  let newMockDefaults = { defaultResponseDelayMs: 0, defaultFailureRatePercent: 0 };
  async function loadNewMockDefaults() {
    try {
      const s = await api.getSettings();
      newMockDefaults = {
        defaultResponseDelayMs: s.defaultResponseDelayMs ?? 0,
        defaultFailureRatePercent: s.defaultFailureRatePercent ?? 0,
      };
    } catch {
      // non-fatal: "+ New mock" just starts at 0 delay / 0% failure rate, as before
    }
  }

  // Reactive rather than a one-shot onMount check: a command-palette jump
  // to a mock already on THIS page (no navigation, so no remount) still
  // needs to react to a new pendingFocus value, and gating on mocks.length
  // too means this naturally waits for the initial load() above to finish
  // before it ever finds a match, without needing to coordinate two async
  // flows by hand.
  $: if ($pendingFocus?.pageId === 'mocks' && mocks.length) {
    const focus = consumePendingFocus('mocks');
    if (focus) {
      expandedId = focus.itemId;
      const target = mocks.find((m) => m.id === focus.itemId);
      if (target?.projectId) expandedProjectIds = new Set([...expandedProjectIds, target.projectId]);
    }
  }

  async function loadEmailTemplates() {
    try {
      emailTemplates = (await api.listEmailTemplates()) ?? [];
    } catch {
      // non-fatal: the email-template picker just falls back to "Custom" only
    }
  }

  async function loadConfig() {
    try {
      const cfg = await api.getConfig();
      gatewayPort = cfg.gatewayPort;
    } catch {
      // non-fatal: the usage snippet just falls back to the default port
    }
  }

  async function loadGatewayTls() {
    try {
      gatewayTls = await api.getGatewayTls();
    } catch {
      // non-fatal: the usage snippet just falls back to assuming plain HTTP
    }
  }

  // Reverse-maps an existing mock's nested config back into the flat form
  // fields the create form already uses, so the one form serves both
  // create and edit — the same approach TCPMocks.svelte uses for TCP mocks.
  async function startEdit(m) {
    form = {
      ...emptyForm(),
      projectId: m.projectId ?? '',
      workspaceId: m.workspaceId ?? '',
      name: m.name,
      enabled: m.enabled !== false,
      protocolType: m.protocolType ?? 'rest',
      operationName: m.operationName ?? '',
      method: m.method,
      pathPattern: m.pathPattern,
      mode: m.scenario ? 'scenario' : m.weighted?.responses?.length ? 'weighted' : m.mode,
      statusCode: m.response?.statusCode ?? 200,
      delayMs: m.response?.delayMs ?? 0,
      bodyTemplate: m.response?.bodyTemplate ?? '',
      responseBodyType: bodyTypeFromHeaders(m.response?.headers),
      validationRules: (m.validation ?? []).map((r) => ({
        field: r.field,
        required: !!r.required,
        type: r.type,
        constraint:
          r.type === 'enum' ? (r.allowedValues ?? []).join(', ')
          : r.type === 'regex' ? (r.pattern ?? '')
          : r.type === 'number' ? [r.min, r.max].filter((v) => v !== undefined).join(', ')
          : [r.minLen, r.maxLen].filter((v) => v !== undefined).join(', '),
      })),
      responseRulesJson: m.responseRules?.length ? JSON.stringify(m.responseRules, null, 2) : '',
      scenarioStepsJson: m.scenario?.steps ? JSON.stringify(m.scenario.steps, null, 2) : emptyForm().scenarioStepsJson,
      scenarioSessionKeyMode: m.scenario?.sessionKeyMode || 'ip',
      scenarioSessionKeyField: m.scenario?.sessionKeyField ?? '',
      scenarioLoop: !!m.scenario?.loop,
      weightedResponsesJson: m.weighted?.responses?.length ? JSON.stringify(m.weighted.responses, null, 2) : emptyForm().weightedResponsesJson,
      proxyTargetBaseUrl: m.proxy?.targetBaseUrl ?? '',
      ackStatusCode: m.asyncConfig?.ackResponse?.statusCode ?? 202,
      ackBodyTemplate: m.asyncConfig?.ackResponse?.bodyTemplate ?? '',
      ackBodyType: bodyTypeFromHeaders(m.asyncConfig?.ackResponse?.headers),
      callbackChannel: m.asyncConfig?.callbackChannel || 'http',
      callbackTargetMode: m.asyncConfig?.callbackTargetMode ?? 'fixed',
      callbackFixedUrl: m.asyncConfig?.callbackFixedUrl ?? '',
      callbackExtractPath: m.asyncConfig?.callbackExtractPath ?? 'body.callbackUrl',
      callbackBodyTemplate: m.asyncConfig?.callbackBodyTemplate ?? '',
      callbackDelayMs: m.asyncConfig?.callbackDelayMs ?? 0,
      emailSubjectTemplate: m.asyncConfig?.emailSubjectTemplate ?? '',
      emailTemplateId: m.asyncConfig?.emailTemplateId ?? '',
      faultEnabled: !!m.fault,
      faultLatencyJitterMs: m.fault?.latencyJitterMs ?? 0,
      faultErrorRatePercent: m.fault?.errorRatePercent ?? 0,
      faultErrorStatusCodes: m.fault?.errorStatusCodes?.length ? m.fault.errorStatusCodes.join(', ') : '500, 503',
      faultTimeoutRatePercent: m.fault?.timeoutRatePercent ?? 0,
    };
    editingId = m.id;
    showForm = true;
    // The edit form renders above the (potentially long) mock list, so
    // clicking Edit on a mock scrolled far down the page would otherwise
    // silently open the form off-screen with no visible change at all —
    // wait for Svelte to actually render it, then bring it into view.
    await tick();
    document.getElementById('mock-edit-form')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  // Reuses startEdit's full field-mapping to populate the form, then
  // clears editingId so saving creates a brand-new mock instead of
  // overwriting the original — same duplicate pattern as the other mock
  // pages, adjusted for REST/SOAP/GraphQL's method+path shape.
  function duplicateMock(m) {
    startEdit(m);
    editingId = '';
    form.name = `${form.name} (copy)`;
    if (form.protocolType === 'rest') {
      form.pathPattern = `${form.pathPattern}-copy`;
    }
  }

  async function saveMock() {
    try {
      const def = {
        name: form.name || form.pathPattern,
        protocolType: form.protocolType || 'rest',
        operationName: form.operationName,
        method: form.method,
        pathPattern: form.pathPattern,
        enabled: form.enabled,
        mode: form.mode === 'scenario' || form.mode === 'weighted' ? 'sync' : form.mode,
        projectId: form.projectId,
        workspaceId: form.workspaceId,
        response: {
          statusCode: Number(form.statusCode) || 200,
          bodyTemplate: form.bodyTemplate,
          headers: { 'Content-Type': contentTypeFor(form.responseBodyType) },
          delayMs: Number(form.delayMs) || 0,
        },
      };
      const validation = buildValidationRules();
      if (validation.length > 0) def.validation = validation;
      if (form.mode === 'sync' && form.responseRulesJson.trim()) {
        try {
          def.responseRules = JSON.parse(form.responseRulesJson);
        } catch {
          showToast('Response rules must be valid JSON', 'err');
          return;
        }
      }
      if (form.mode === 'scenario') {
        let steps;
        try {
          steps = JSON.parse(form.scenarioStepsJson);
        } catch {
          showToast('Scenario steps must be valid JSON', 'err');
          return;
        }
        def.scenario = {
          steps,
          sessionKeyMode: form.scenarioSessionKeyMode,
          sessionKeyField: form.scenarioSessionKeyMode === 'ip' ? '' : form.scenarioSessionKeyField,
          loop: form.scenarioLoop,
        };
      }
      if (form.mode === 'weighted') {
        let responses;
        try {
          responses = JSON.parse(form.weightedResponsesJson);
        } catch {
          showToast('Weighted responses must be valid JSON', 'err');
          return;
        }
        def.weighted = { responses };
      }
      if (form.mode === 'proxy') {
        def.proxy = { targetBaseUrl: form.proxyTargetBaseUrl };
      }
      if (form.mode === 'async') {
        def.asyncConfig = {
          ackResponse: {
            statusCode: Number(form.ackStatusCode) || 202,
            bodyTemplate: form.ackBodyTemplate,
            headers: { 'Content-Type': contentTypeFor(form.ackBodyType) },
          },
          callbackChannel: form.callbackChannel,
          callbackTargetMode: form.callbackTargetMode,
          callbackFixedUrl: form.callbackTargetMode === 'fixed' ? form.callbackFixedUrl : '',
          callbackExtractPath: form.callbackTargetMode === 'extracted' ? form.callbackExtractPath : '',
          callbackBodyTemplate: form.callbackBodyTemplate,
          callbackDelayMs: Number(form.callbackDelayMs) || 0,
        };
        if (form.callbackChannel === 'email') {
          def.asyncConfig.emailSubjectTemplate = form.emailSubjectTemplate;
          def.asyncConfig.emailTemplateId = form.emailTemplateId;
        }
      }
      if (form.faultEnabled) {
        def.fault = {
          latencyJitterMs: Number(form.faultLatencyJitterMs) || 0,
          errorRatePercent: Number(form.faultErrorRatePercent) || 0,
          errorStatusCodes: form.faultErrorStatusCodes.split(',').map((s) => Number(s.trim())).filter(Boolean),
          timeoutRatePercent: Number(form.faultTimeoutRatePercent) || 0,
        };
      }
      await runWithWorkspaceUnlock(
        () =>
          editingId
            ? api.updateMock(editingId, { ...mocks.find((m) => m.id === editingId), ...def })
            : api.createMock(def),
        (p) => (pendingUnlock = p)
      );
      showToast(editingId ? 'Mock updated' : 'Mock created', 'ok');
      form = emptyForm();
      editingId = '';
      showForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function removeMock(id) {
    const m = mocks.find((x) => x.id === id);
    if (!confirm(`Delete mock "${m?.name ?? id}"? This cannot be undone.`)) return;
    try {
      await runWithWorkspaceUnlock(() => api.deleteMock(id), (p) => (pendingUnlock = p));
      showToast('Mock deleted', 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function toggleEnabled(m) {
    try {
      await runWithWorkspaceUnlock(() => api.updateMock(m.id, { ...m, enabled: !m.enabled }), (p) => (pendingUnlock = p));
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Right-click on a mock row — see ContextMenu.svelte. contextMenu is null
  // when closed, else { x, y, mock }: the mock is captured at open time so
  // the menu's own item actions don't need a second lookup by id.
  let contextMenu = null;
  function openContextMenu(e, m) {
    e.preventDefault();
    contextMenu = { x: e.clientX, y: e.clientY, mock: m };
  }
  function contextMenuItems(m) {
    return [
      { label: 'Edit', onClick: () => startEdit(m) },
      { label: 'Duplicate', onClick: () => duplicateMock(m) },
      { label: m.enabled ? 'Disable' : 'Enable', onClick: () => toggleEnabled(m) },
      { label: 'Copy path', onClick: () => copyText(m.pathPattern ?? '').then(() => showToast('Path copied', 'ok')) },
      { divider: true },
      { label: 'Delete', danger: true, onClick: () => removeMock(m.id) },
    ];
  }

  async function deleteHitLogEntry(mockId, hitId) {
    try {
      await api.deleteHitLogEntry(hitId);
      hitsByMock = { ...hitsByMock, [mockId]: (hitsByMock[mockId] ?? []).filter((h) => h.id !== hitId) };
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function clearMockLogs(id) {
    if (!confirm('Delete all log history for this mock? This cannot be undone.')) return;
    try {
      await api.deleteHitLogMatching({ mockId: id });
      hitsByMock = { ...hitsByMock, [id]: [] };
      showToast('Log history cleared', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function methodClass(method) {
    return method === 'GET' ? 'badge-info' : method === 'DELETE' ? 'badge-err' : method === 'POST' ? 'badge-ok' : 'badge-warn';
  }

  function statusClass(code) {
    if (!code) return 'badge-err';
    return code < 300 ? 'badge-ok' : code < 400 ? 'badge-info' : 'badge-err';
  }

  // Returns the mapped workspace's name only when it's actually locked —
  // an ungrouped mock, or one mapped to an unlocked workspace, gets no
  // chip at all, since there's nothing for the lock icon to warn about.
  function lockedWorkspaceName(workspaceId) {
    if (!workspaceId) return '';
    const w = workspaces.find((w) => w.id === workspaceId);
    return w?.locked ? w.name : '';
  }

  // A plain function call inside {#if} isn't tracked by Svelte's per-item
  // reactivity the way a directly-referenced variable is — workspaces
  // changing (loadWorkspaces() resolving after mocks already rendered, the
  // normal case on a fresh page load) silently never re-ran the check,
  // leaving every lock chip missing until some UNRELATED re-render (e.g.
  // typing in the search box) happened to force the whole list to
  // recompute. Referencing this reactive Set directly in the template's
  // {#if} (instead of only inside the function) is what makes Svelte
  // actually track it as a per-row dependency.
  $: lockedWorkspaceIds = new Set(workspaces.filter((w) => w.locked).map((w) => w.id));

  // A mock inherits its PROJECT's mapped workspace in addition to any
  // WorkspaceID set directly on itself (see internal/web/api/mocks.go's
  // checkWorkspaceUnlocked call sites) — referenced directly in each mock
  // row's {#if} below, same reactivity reasoning as lockedWorkspaceIds
  // above, so a project's workspace mapping resolving after the mock list
  // already rendered still shows the chip without needing an unrelated
  // re-render to force it.
  $: projectWorkspaceById = Object.fromEntries(projects.map((p) => [p.id, p.workspaceId || '']));
</script>

<div class="head-row">
  <div>
    <h1>Mocks</h1>
    <p class="sub">REST, SOAP, GraphQL and WebSocket mocks, served on the gateway port or a project's own port.</p>
  </div>
  <div class="head-actions">
    <button class="btn btn-ghost" on:click={() => (showWsdlForm = !showWsdlForm)}>
      {showWsdlForm ? 'Cancel' : 'Import WSDL'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showSoapUIMockForm = !showSoapUIMockForm)}>
      {showSoapUIMockForm ? 'Cancel' : 'Import SOAP Project mocks'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showGraphqlForm = !showGraphqlForm)}>
      {showGraphqlForm ? 'Cancel' : 'Import GraphQL SDL'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showOpenapiForm = !showOpenapiForm)}>
      {showOpenapiForm ? 'Cancel' : 'Import OpenAPI'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showImportMocksForm = !showImportMocksForm)}>
      {showImportMocksForm ? 'Cancel' : 'Import mocks'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showTrafficImportForm = !showTrafficImportForm)}>
      {showTrafficImportForm ? 'Cancel' : 'Import captured traffic'}
    </button>
    <a class="btn btn-ghost" href={mocksExportUrl()} download>Export all</a>
    <button class="btn btn-primary" on:click={() => (showForm ? (showForm = false) : openCreateForm(''))}>
      {showForm ? 'Cancel' : '+ New mock'}
    </button>
  </div>
</div>

<div class="projects-bar">
  <input aria-label="Search mocks by name, path, or method" type="text" class="search-input" bind:value={searchQuery} placeholder="Search mocks by name, path, or method…" />
  <button class="btn btn-ghost small" on:click={() => (showNewProjectForm = !showNewProjectForm)}>
    {showNewProjectForm ? 'Cancel' : '+ New project'}
  </button>
</div>

{#if showTrafficImportForm}
  <div class="card form-card">
    <h3 class="form-title">Import captured traffic</h3>
    <p class="sub">
      Scaffolds one REST mock per distinct endpoint found in a browser devtools HAR export, or a
      collection export whose requests carry saved example responses — the reverse of "Import mocks": turning real
      traffic captured somewhere else into working mocks here, instead of exporting AirMock's own mocks.
    </p>
    <label class="mode-field">
      Source format
      <select bind:value={trafficImportFormat}>
        <option value="har">HAR file (browser devtools "Export HAR")</option>
        <option value="postman-examples">Collection export (with saved example responses)</option>
      </select>
    </label>
    <label>
      Path prefix (optional)
      <input type="text" bind:value={trafficImportPathPrefix} placeholder="/api" />
    </label>
    <ImportSource bind:content={trafficImportContent} placeholder={'Paste HAR or collection export JSON here…'} />
    <label class="mode-field">
      Project
      <select bind:value={trafficImportProjectId}>
        <option value="">Ungrouped</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        <option value={NEW_PROJECT_SENTINEL}>+ Create new project</option>
      </select>
    </label>
    {#if trafficImportProjectId === NEW_PROJECT_SENTINEL}
      <label>
        New project name
        <input type="text" bind:value={trafficImportNewProjectName} placeholder="Billing API" />
      </label>
    {/if}
    <button
      class="btn btn-primary"
      on:click={importTraffic}
      disabled={!trafficImportContent.trim() || (trafficImportProjectId === NEW_PROJECT_SENTINEL && !trafficImportNewProjectName.trim())}
    >Scaffold mocks</button>
  </div>
{/if}

{#if showImportMocksForm}
  <div class="card form-card">
    <h3 class="form-title">Import mocks from JSON</h3>
    <p class="sub">
      Accepts a file exported via "Export all" above — includes REST/SOAP/GraphQL, TCP, and SMTP mocks. Each
      imported mock gets a fresh id; one with a name/endpoint that already exists here is skipped, not overwritten.
    </p>
    <ImportSource bind:content={importMocksJson} placeholder={'{\n  "mocks": [ ... ]\n}'} />
    <label class="mode-field">
      Project
      <select bind:value={importMocksProjectId}>
        <option value="">Ungrouped</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        <option value={NEW_PROJECT_SENTINEL}>+ Create new project</option>
      </select>
    </label>
    {#if importMocksProjectId === NEW_PROJECT_SENTINEL}
      <label>
        New project name
        <input type="text" bind:value={importMocksNewProjectName} placeholder="Billing API" />
      </label>
    {/if}
    <button class="btn btn-primary" on:click={importMocks} disabled={!importMocksJson.trim() || (importMocksProjectId === NEW_PROJECT_SENTINEL && !importMocksNewProjectName.trim())}>Import</button>
  </div>
{/if}

{#if showNewProjectForm}
  <div class="card form-card">
    <div class="field-row">
      <label>
        Project name
        <input type="text" bind:value={newProjectName} placeholder="Billing API" />
      </label>
      <label>
        <span class="label-text">Base path (optional)<InfoTooltip text="A shared prefix for every REST/SOAP/GraphQL/WS API in this project. It is added to a mock's path when the mock is saved, from this page and from the admin API or 'airmock mocks apply' alike. Changing it later does not move mocks that already exist." /></span>
        <input type="text" bind:value={newProjectBasePath} placeholder="/api/billing" />
      </label>
      <label class="port-field">
        Dedicated port (optional)
        <input type="number" bind:value={newProjectGatewayPort} placeholder="default gateway" />
      </label>
      <label>
        Workspace (optional)
        <select bind:value={newProjectWorkspaceId}>
          <option value="">Ungrouped</option>
          {#each workspaces as w (w.id)}<option value={w.id}>{w.name}{w.locked ? ' 🔒' : ''}</option>{/each}
        </select>
      </label>
      <button class="btn btn-primary" on:click={createProject}>Create</button>
    </div>
    <p class="sub">Leave the port blank to serve this project's APIs on the shared default gateway port, same as any other mock.</p>
    <p class="sub">Mapping a workspace here protects every mock in this project — locking that workspace then requires its password to edit or delete any of them, without mapping each mock individually.</p>
    {#if newProjectGatewayPort}
      <label class="toggle-label">
        <input type="checkbox" bind:checked={newProjectTlsEnabled} />
        Enable TLS on this project's dedicated port
      </label>
      {#if newProjectTlsEnabled}
        {#if certificates.length === 0}
          <p class="sub">No certificates in the store yet — create one on the Certificates page first.</p>
        {:else}
          <label>
            Bundle (optional — supplies the server cert + CA together)
            <select bind:value={newProjectTlsBundleId}>
              <option value="">None — pick individually below</option>
              {#each certBundles as b}<option value={b.id}>{b.name}</option>{/each}
            </select>
          </label>
          <label>
            Server certificate
            <select bind:value={newProjectTlsCertificateId} disabled={!!newProjectTlsBundleId}>
              <option value="">Select a certificate…</option>
              {#each serverCerts as c}
                <option value={c.id}>{c.name}</option>
              {/each}
            </select>
          </label>
          <label>
            Client certificate
            <select bind:value={newProjectTlsClientCertMode}>
              <option value="">None required</option>
              <option value="optional">Optional — verified if presented</option>
              <option value="required">Required — reject connections without one</option>
            </select>
          </label>
          {#if (newProjectTlsClientCertMode === 'optional' || newProjectTlsClientCertMode === 'required') && !newProjectTlsBundleId}
            <label>
              Trusted client CA
              <select bind:value={newProjectTlsClientCaId}>
                <option value="">Select a CA…</option>
                {#each caCerts as c}<option value={c.id}>{c.name}</option>{/each}
              </select>
            </label>
          {/if}
        {/if}
      {/if}
    {/if}
  </div>
{/if}

{#if showWsdlForm}
  <div class="card form-card">
    <label>
      Service endpoint path
      <input type="text" bind:value={wsdlPathPattern} placeholder="/soap/orders" />
    </label>
    <label>
      WSDL content
      <ImportSource bind:content={wsdlContent} placeholder="Paste WSDL XML here…" />
    </label>
    <label class="mode-field">
      Project
      <select bind:value={wsdlProjectId}>
        <option value="">Ungrouped</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        <option value={NEW_PROJECT_SENTINEL}>+ Create new project</option>
      </select>
    </label>
    {#if wsdlProjectId === NEW_PROJECT_SENTINEL}
      <label>
        New project name
        <input type="text" bind:value={wsdlNewProjectName} placeholder="Orders API" />
      </label>
    {/if}
    <button class="btn btn-primary" on:click={importWsdl} disabled={wsdlProjectId === NEW_PROJECT_SENTINEL && !wsdlNewProjectName.trim()}>Import &amp; scaffold mocks</button>
  </div>
{/if}

{#if showSoapUIMockForm}
  <div class="card form-card">
    <p class="sub">Scaffolds one SOAP mock per MockService operation in a SOAP Project XML export (*.xml), each already carrying that operation's own response body — unlike WSDL import, which has no response content to work with and falls back to a generic stub.</p>
    <label>
      SOAP Project XML
      <ImportSource bind:content={soapUIMockProjectXml} placeholder="Paste a SOAP Project XML export…" />
    </label>
    <label class="mode-field">
      Project
      <select bind:value={soapUIMockProjectId}>
        <option value="">Ungrouped</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        <option value={NEW_PROJECT_SENTINEL}>+ Create new project</option>
      </select>
    </label>
    {#if soapUIMockProjectId === NEW_PROJECT_SENTINEL}
      <label>
        New project name
        <input type="text" bind:value={soapUIMockNewProjectName} placeholder="Orders API" />
      </label>
    {/if}
    <button class="btn btn-primary" on:click={importSoapUIMocks} disabled={soapUIMockProjectId === NEW_PROJECT_SENTINEL && !soapUIMockNewProjectName.trim()}>Import &amp; scaffold mocks</button>
  </div>
{/if}

{#if showGraphqlForm}
  <div class="card form-card">
    <label>
      Endpoint path
      <input type="text" bind:value={graphqlPathPattern} placeholder="/graphql" />
    </label>
    <label>
      GraphQL SDL schema
      <ImportSource bind:content={graphqlSdl} placeholder={'type Query {\n  order(id: ID!): Order\n}'} />
    </label>
    <label class="mode-field">
      Project
      <select bind:value={graphqlProjectId}>
        <option value="">Ungrouped</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        <option value={NEW_PROJECT_SENTINEL}>+ Create new project</option>
      </select>
    </label>
    {#if graphqlProjectId === NEW_PROJECT_SENTINEL}
      <label>
        New project name
        <input type="text" bind:value={graphqlNewProjectName} placeholder="Orders API" />
      </label>
    {/if}
    <button class="btn btn-primary" on:click={importGraphqlSchema} disabled={graphqlProjectId === NEW_PROJECT_SENTINEL && !graphqlNewProjectName.trim()}>Import &amp; scaffold mocks</button>
  </div>
{/if}

{#if showOpenapiForm}
  <div class="card form-card">
    <label>
      Path prefix (optional)
      <input type="text" bind:value={openapiPathPrefix} placeholder="/api/v1" />
    </label>
    <label>
      OpenAPI document (JSON)
      <ImportSource bind:content={openapiContent} placeholder="Paste an OpenAPI 3.x JSON document here…" />
    </label>
    <label class="mode-field">
      Project
      <select bind:value={openapiProjectId}>
        <option value="">Ungrouped</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        <option value={NEW_PROJECT_SENTINEL}>+ Create new project</option>
      </select>
    </label>
    {#if openapiProjectId === NEW_PROJECT_SENTINEL}
      <label>
        New project name
        <input type="text" bind:value={openapiNewProjectName} placeholder="Orders API" />
      </label>
    {/if}
    <button class="btn btn-primary" on:click={importOpenapi} disabled={openapiProjectId === NEW_PROJECT_SENTINEL && !openapiNewProjectName.trim()}>Import &amp; scaffold mocks</button>
  </div>
{/if}

{#if showForm}
  <div class="card form-card" id="mock-edit-form">
    <h3 class="form-title">{editingId ? 'Edit mock' : 'New mock'}</h3>
    {#if !editingId && form.protocolType === 'rest'}
      <div class="template-picker">
        <span class="template-picker-label">Start from a template</span>
        <div class="template-picker-row">
          {#each MOCK_TEMPLATES as tpl}
            <button type="button" class="btn btn-ghost small" title={tpl.description} on:click={() => applyMockTemplate(tpl)}>{tpl.label}</button>
          {/each}
        </div>
      </div>
    {/if}
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={form.name} placeholder="order-status" />
      </label>
      <label class="method-field">
        Method
        <select bind:value={form.method}>
          {#each METHODS as m}<option value={m}>{m}</option>{/each}
        </select>
      </label>
      <label class="mode-field">
        Mode
        <select bind:value={form.mode}>
          <option value="sync">Sync</option>
          <option value="async">Async</option>
          <option value="scenario">Scenario</option>
          <option value="weighted">Weighted random</option>
          <option value="proxy">Proxy (record &amp; replay)</option>
        </select>
      </label>
    </div>
    <div class="field-row">
      <label>
        Path pattern
        <input type="text" bind:value={form.pathPattern} placeholder="/orders/{'{'}id{'}'}" />
      </label>
      <label class="mode-field">
        Project
        <select bind:value={form.projectId}>
          <option value="">Ungrouped</option>
          {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        </select>
      </label>
      <label class="enabled-field">
        <input type="checkbox" bind:checked={form.enabled} />
        Enabled
      </label>
    </div>
    <div class="field-row">
      <label class="mode-field">
        Workspace <InfoTooltip text="Optional — maps this mock to a Collections workspace. If that workspace is locked, editing or deleting this mock later requires its password." />
        <select bind:value={form.workspaceId}>
          <option value="">Ungrouped</option>
          {#each workspaces as w (w.id)}<option value={w.id}>{w.name}{w.locked ? ' 🔒' : ''}</option>{/each}
        </select>
      </label>
    </div>

    <div class="async-block">
      <h3>Validation rules</h3>
      {#each form.validationRules as rule, i}
        <div class="field-row rule-row">
          <label class="rule-source">
            Source
            <select value={ruleSource(rule)} on:change={(e) => setRuleSource(rule, e.currentTarget.value)}>
              {#each RULE_SOURCES as s}<option value={s.value}>{s.label}</option>{/each}
            </select>
          </label>
          <label class="rule-field">
            Field name
            <input
              type="text"
              value={ruleFieldName(rule)}
              on:input={(e) => setRuleFieldName(rule, e.currentTarget.value)}
              placeholder={RULE_SOURCES.find((s) => s.value === ruleSource(rule))?.placeholder}
            />
          </label>
          <label class="rule-type">
            <span class="label-text">Type<InfoTooltip text="Validated before the response is rendered — a failing field returns the configured error response instead. enum/regex use the constraint field to its right; number/string use it as min,max or minLen,maxLen." /></span>
            <select bind:value={rule.type}>
              <option value="string">string</option>
              <option value="number">number</option>
              <option value="boolean">boolean</option>
              <option value="enum">enum</option>
              <option value="regex">regex</option>
              <option value="email">email</option>
              <option value="uuid">uuid</option>
              <option value="date">date</option>
            </select>
          </label>
          <label class="rule-constraint">
            Constraint
            <input type="text" bind:value={rule.constraint} placeholder={constraintPlaceholder(rule.type)} />
          </label>
          <label class="rule-required">
            <input type="checkbox" checked={rule.required} on:change={(e) => (rule.required = e.currentTarget.checked)} />
            Required
          </label>
          <button class="btn btn-ghost" on:click={() => removeValidationRule(i)}>×</button>
        </div>
      {/each}
      <button class="btn btn-ghost" on:click={addValidationRule}>+ Add rule</button>
    </div>

    {#if form.mode === 'sync'}
      <div class="field-row">
        <label class="status-field">
          Status
          <input type="number" bind:value={form.statusCode} />
        </label>
        <label class="body-type-field">
          Body type
          <select bind:value={form.responseBodyType}>
            {#each BODY_TYPES as t}<option value={t.value}>{t.label}</option>{/each}
          </select>
        </label>
        <label class="status-field">
          <span class="label-text">Delay (ms)<InfoTooltip text="Adds a fixed delay before this mock responds — simulates a slow real endpoint. Defaults to whatever's configured on the Settings page for new mocks; independent of that setting once set here." /></span>
          <input type="number" min="0" bind:value={form.delayMs} />
        </label>
      </div>
      <label>
        <span class="label-text">Response body template<InfoTooltip text={`Rendered via Go templates (text/template + sprig). {{.Request.Body.field}}, {{.Request.Query.x}}, {{.Request.Header.X}}, and {{.Request.PathParams.name}} pull from the incoming request; {{fake "uuid"}}/{{fake "email"}}/{{fake "name"}}/{{fake "phone"}}/{{fake "address"}}/{{fake "company"}}/{{fake "number" "1" "100"}} (and more — see the fake() list) generate fake data. {{counter "name"}} is an auto-incrementing counter that persists across restarts ({{counter "stock" -1}} decrements instead). {{csv "column"}} pulls from an attached CSV (see below). Sprig helpers also work: {{uuidv4}}, {{now}}, {{default "x" .y}}, {{upper .x}}, {{add 1 2}}, {{randInt 1 100}}.`} /></span>
        <textarea aria-label="Response body template" rows="6" bind:value={form.bodyTemplate}></textarea>
      </label>
      {#if form.responseBodyType === 'json' && form.bodyTemplate.trim()}
        {@const err = jsonError(form.bodyTemplate)}
        {#if err && !form.bodyTemplate.includes('{{')}
          <p class="json-hint invalid">Invalid JSON: {err}</p>
        {/if}
      {/if}
      {#if editingId}
        <CsvSourceField ownerPath={`/api/mocks/${editingId}`} />
      {:else}
        <p class="sub">Save this mock first, then come back to attach a CSV data source for {'{{csv "column"}}'}.</p>
      {/if}
      <label>
        <span class="label-text">Response rules (JSON, optional) — evaluated first-match-wins before the default response above<InfoTooltip text={'Each condition\'s "field" is body.<name> (JSON body field), query.<name>, header.<Name>, or xpath.<expr> (XML body).'} /></span>
        <textarea
          rows="4"
          bind:value={form.responseRulesJson}
          placeholder={'[{"conditions":[{"field":"query.type","operator":"equals","value":"premium"}],"response":{"statusCode":200,"bodyTemplate":"{\\"tier\\":\\"premium\\"}"}}]'}
        ></textarea>
      </label>
    {:else if form.mode === 'scenario'}
      <div class="async-block">
        <h3>Scenario steps</h3>
        <p class="sub">Each hit from the same session serves the next step in order.</p>
        <label>
          Steps (JSON array of responses)
          <textarea rows="8" bind:value={form.scenarioStepsJson}></textarea>
        </label>
        <div class="field-row">
          <label class="mode-field">
            Session key
            <select bind:value={form.scenarioSessionKeyMode}>
              <option value="ip">Client IP</option>
              <option value="header">Header</option>
              <option value="query">Query param</option>
              <option value="body">Body field</option>
            </select>
          </label>
          {#if form.scenarioSessionKeyMode !== 'ip'}
            <label>
              Field name
              <input type="text" bind:value={form.scenarioSessionKeyField} placeholder="X-Session-Id" />
            </label>
          {/if}
          <label class="rule-required">
            <input type="checkbox" bind:checked={form.scenarioLoop} />
            Loop
          </label>
        </div>
      </div>
    {:else if form.mode === 'weighted'}
      <div class="async-block">
        <h3>Weighted random responses</h3>
        <p class="sub">Each hit picks one response by relative weight — e.g. weight 90 vs 10 answers success ~90% of the time and an error ~10%, for testing resilience to an endpoint that's merely unreliable rather than deterministically broken.</p>
        <label>
          Responses (JSON array of {'{'}weight, response{'}'})
          <textarea rows="8" bind:value={form.weightedResponsesJson}></textarea>
        </label>
      </div>
    {:else if form.mode === 'proxy'}
      <div class="async-block">
        <h3>Proxy target</h3>
        <p class="sub">Requests to this path are forwarded here, the real response is returned unchanged, and the exchange is captured in the Hit Log for "promote to mock" later. Path pattern should end in a wildcard, e.g. /proxy/*.</p>
        <label>
          Target base URL
          <input type="text" bind:value={form.proxyTargetBaseUrl} placeholder="https://api.example.com" />
        </label>
      </div>
    {:else}
      <div class="async-block">
        <h3>Immediate ack</h3>
        <div class="field-row">
          <label class="status-field">
            Status
            <input type="number" bind:value={form.ackStatusCode} />
          </label>
          <label class="body-type-field">
            Body type
            <select bind:value={form.ackBodyType}>
              {#each BODY_TYPES as t}<option value={t.value}>{t.label}</option>{/each}
            </select>
          </label>
        </div>
        <label>
          <span class="label-text">Ack body template<InfoTooltip text={asyncTemplateHelpText} /></span>
          <textarea rows="3" bind:value={form.ackBodyTemplate}></textarea>
        </label>
      </div>
      <div class="async-block">
        <h3>Callback</h3>
        <div class="field-row">
          <label class="mode-field">
            Channel
            <select bind:value={form.callbackChannel}>
              <option value="http">HTTP webhook</option>
              <option value="email">Email</option>
            </select>
          </label>
          <label class="mode-field">
            Target
            <select bind:value={form.callbackTargetMode}>
              <option value="fixed">Fixed {form.callbackChannel === 'email' ? 'address' : 'URL'}</option>
              <option value="extracted">Extracted from request</option>
            </select>
          </label>
          <label class="days-field">
            Delay (ms)
            <input type="number" bind:value={form.callbackDelayMs} />
          </label>
        </div>
        {#if form.callbackTargetMode === 'fixed'}
          <label>
            {form.callbackChannel === 'email' ? 'Recipient email address' : 'Callback URL'}
            <input
              type="text"
              bind:value={form.callbackFixedUrl}
              placeholder={form.callbackChannel === 'email' ? 'customer@example.com' : 'https://example.com/webhook'}
            />
          </label>
        {:else}
          <label>
            Extract path
            <input type="text" bind:value={form.callbackExtractPath} placeholder="body.callbackUrl" />
          </label>
        {/if}

        {#if form.callbackChannel === 'email'}
          <label>
            Email template
            <select bind:value={form.emailTemplateId}>
              <option value="">Custom (inline subject/body below)</option>
              {#each emailTemplates as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
            </select>
          </label>
          {#if !form.emailTemplateId}
            <label>
              Subject
              <input type="text" bind:value={form.emailSubjectTemplate} placeholder="Order {'{{'}.Request.Body.orderId{'}}'} shipped" />
            </label>
            <label>
              <span class="label-text">Email HTML body<InfoTooltip text={asyncTemplateHelpText} /></span>
              <textarea rows="4" bind:value={form.callbackBodyTemplate}></textarea>
            </label>
          {:else}
            <p class="sub">Subject and HTML body come from the selected template — manage templates on the SMTP page.</p>
          {/if}
        {:else}
          <label>
            <span class="label-text">Callback body template<InfoTooltip text={asyncTemplateHelpText} /></span>
            <textarea rows="4" bind:value={form.callbackBodyTemplate}></textarea>
          </label>
        {/if}
      </div>
    {/if}

    <div class="async-block">
      <label class="rule-required">
        <input type="checkbox" bind:checked={form.faultEnabled} />
        <h3 style="margin:0">Fault / chaos injection</h3>
      </label>
      {#if form.faultEnabled}
        <div class="field-row">
          <label>
            Latency jitter (ms)
            <input type="number" bind:value={form.faultLatencyJitterMs} />
          </label>
          <label>
            Error rate (%)
            <input type="number" bind:value={form.faultErrorRatePercent} />
          </label>
          <label>
            Timeout rate (%)
            <input type="number" bind:value={form.faultTimeoutRatePercent} />
          </label>
        </div>
        <label>
          Error status codes (comma-separated)
          <input type="text" bind:value={form.faultErrorStatusCodes} placeholder="500, 503" />
        </label>
      {/if}
    </div>

    <button class="btn btn-primary" on:click={saveMock}>{editingId ? 'Save changes' : 'Create mock'}</button>
  </div>
{/if}

{#snippet mockRow(m)}
  <div class="card mock-card row-enter">
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div
      class="mock-row"
      on:click={() => toggleExpand(m.id)}
      on:contextmenu={(e) => openContextMenu(e, m)}
    >
      <input
        type="checkbox"
        class="select-box"
        checked={selectedIds.has(m.id)}
        on:change|stopPropagation={() => toggleSelect(m.id)}
        on:click|stopPropagation
        aria-label="Select {m.name}"
      />
      <button type="button" class="expand-arrow" class:expanded={expandedId === m.id} aria-expanded={!!(expandedId === m.id)} aria-label="Show or hide details" on:click|stopPropagation={() => toggleExpand(m.id)}>▸</button>
      <span class="badge {methodClass(m.method)}">{m.method}</span>
      {#if m.name && m.name !== m.pathPattern}<span class="name" title={m.name}>{m.name}</span>{/if}
      <span class="path">{m.pathPattern}{#if m.protocolType === 'soap' || m.protocolType === 'graphql'} — {m.operationName}{/if}</span>
      {#if m.protocolType === 'soap'}<span class="chip badge-info">soap</span>{/if}
      {#if m.protocolType === 'graphql'}<span class="chip badge-info">graphql</span>{/if}
      <span class="chip {m.mode === 'async' ? 'chip-stat' : 'chip-tls'}">{m.mode}</span>
      {#if m.scenario?.steps?.length}<span class="chip badge-warn">{m.scenario.steps.length}-step scenario</span>{/if}
      {#if m.weighted?.responses?.length}<span class="chip badge-warn">{m.weighted.responses.length}-way weighted</span>{/if}
      {#if m.validation?.length}<span class="chip badge-warn">{m.validation.length} validation rule{m.validation.length > 1 ? 's' : ''}</span>{/if}
      {#if m.responseRules?.length}<span class="chip chip-stat">{m.responseRules.length} response rule{m.responseRules.length > 1 ? 's' : ''}</span>{/if}
      {#if (m.workspaceId || projectWorkspaceById[m.projectId]) && lockedWorkspaceIds.has(m.workspaceId || projectWorkspaceById[m.projectId])}<span class="chip badge-warn" title="Editing/deleting this mock requires this workspace's password">🔒 {lockedWorkspaceName(m.workspaceId || projectWorkspaceById[m.projectId])}</span>{/if}
      <button
        class="chip {m.enabled ? 'chip-run' : 'chip-stop'} chip-toggle"
        title={m.enabled ? 'Click to disable' : 'Click to enable'}
        on:click|stopPropagation={() => toggleEnabled(m)}
      >{m.enabled ? 'enabled' : 'disabled'}</button>
      <span class="status">→ {m.mode === 'async' ? m.asyncConfig?.ackResponse?.statusCode : m.response?.statusCode}</span>
      <div class="row-actions">
        <button class="btn btn-ghost" on:click|stopPropagation={() => startEdit(m)}>Edit</button>
        <button class="btn btn-ghost" on:click|stopPropagation={() => duplicateMock(m)}>Duplicate</button>
        <button class="btn btn-ghost remove" on:click|stopPropagation={() => removeMock(m.id)}>Delete</button>
      </div>
    </div>

    {#if expandedId === m.id}
      <div class="mock-detail">
        <div class="detail-section">
          <UsageSnippet command={usageForMock(m)} />
          <div class="test-request-row">
            <button class="btn btn-ghost small" on:click={() => sendTestRequest(m)} disabled={testingIds.has(m.id)}>
              {testingIds.has(m.id) ? 'Sending…' : '▶ Send test request'}
            </button>
            {#if testResults[m.id]}
              {@const r = testResults[m.id]}
              {#if r.error}
                <span class="chip chip-stop">{r.error}</span>
              {:else}
                <span class="chip {statusClass(r.statusCode)}">{r.statusCode}</span>
                <span class="sub">{r.timing?.totalMs ?? 0}ms</span>
              {/if}
            {/if}
          </div>
          {#if testResults[m.id] && !testResults[m.id].error && testResults[m.id].body}
            <pre class="test-response-body">{testResults[m.id].body}</pre>
          {/if}
        </div>

        <div class="detail-section">
          <h4>Response</h4>
          {#if m.mode === 'async'}
            <div class="detail-row"><span class="detail-label">Ack status</span><code>{m.asyncConfig?.ackResponse?.statusCode}</code></div>
            <div class="detail-row"><span class="detail-label">Ack content type</span><code>{m.asyncConfig?.ackResponse?.headers?.['Content-Type'] ?? 'application/json'}</code></div>
            <div class="detail-row"><span class="detail-label">Ack body</span><pre>{m.asyncConfig?.ackResponse?.bodyTemplate}</pre></div>
            <div class="detail-row"><span class="detail-label">Callback channel</span><code>{m.asyncConfig?.callbackChannel || 'http'}</code></div>
            <div class="detail-row"><span class="detail-label">Callback target</span><code>{m.asyncConfig?.callbackTargetMode === 'extracted' ? m.asyncConfig?.callbackExtractPath : (m.asyncConfig?.callbackFixedUrl || '(none)')}</code></div>
            <div class="detail-row"><span class="detail-label">Callback delay</span><code>{m.asyncConfig?.callbackDelayMs ?? 0}ms</code></div>
            {#if m.asyncConfig?.callbackChannel === 'email' && m.asyncConfig?.emailTemplateId}
              <div class="detail-row"><span class="detail-label">Email template</span><code>{emailTemplates.find((t) => t.id === m.asyncConfig.emailTemplateId)?.name ?? m.asyncConfig.emailTemplateId}</code></div>
            {:else if m.asyncConfig?.callbackChannel === 'email'}
              <div class="detail-row"><span class="detail-label">Email subject</span><code>{m.asyncConfig?.emailSubjectTemplate}</code></div>
              <div class="detail-row"><span class="detail-label">Email body</span><pre>{m.asyncConfig?.callbackBodyTemplate}</pre></div>
            {:else}
              <div class="detail-row"><span class="detail-label">Callback body</span><pre>{m.asyncConfig?.callbackBodyTemplate}</pre></div>
            {/if}
          {:else if m.mode === 'proxy'}
            <div class="detail-row"><span class="detail-label">Target base URL</span><code>{m.proxy?.targetBaseUrl}</code></div>
          {:else}
            <div class="detail-row"><span class="detail-label">Status</span><code>{m.response?.statusCode}</code></div>
            <div class="detail-row"><span class="detail-label">Content type</span><code>{m.response?.headers?.['Content-Type'] ?? 'application/json'}</code></div>
            <div class="detail-row"><span class="detail-label">Body template</span><pre>{m.response?.bodyTemplate}</pre></div>
          {/if}
        </div>

        {#if m.validation?.length}
          <div class="detail-section">
            <h4>Validation rules</h4>
            {#each m.validation as rule}
              <div class="detail-row">
                <span class="detail-label">{rule.field}</span>
                <code>{rule.type}{rule.required ? ', required' : ''}</code>
              </div>
            {/each}
          </div>
        {/if}

        {#if m.responseRules?.length}
          <div class="detail-section">
            <h4>Response rules (first match wins)</h4>
            <pre class="json-block">{JSON.stringify(m.responseRules, null, 2)}</pre>
          </div>
        {/if}

        {#if m.scenario?.steps?.length}
          <div class="detail-section">
            <h4>Scenario steps</h4>
            <div class="detail-row"><span class="detail-label">Session key</span><code>{m.scenario.sessionKeyMode}{m.scenario.sessionKeyField ? ': ' + m.scenario.sessionKeyField : ''}</code></div>
            <div class="detail-row"><span class="detail-label">Loop</span><code>{m.scenario.loop ? 'yes' : 'no (clamps at last step)'}</code></div>
            <pre class="json-block">{JSON.stringify(m.scenario.steps, null, 2)}</pre>
          </div>
        {/if}

        {#if m.weighted?.responses?.length}
          <div class="detail-section">
            <h4>Weighted random responses</h4>
            <pre class="json-block">{JSON.stringify(m.weighted.responses, null, 2)}</pre>
          </div>
        {/if}

        {#if m.fault}
          <div class="detail-section">
            <h4>Fault / chaos injection</h4>
            <div class="detail-row"><span class="detail-label">Latency jitter</span><code>{m.fault.latencyJitterMs ?? 0}ms</code></div>
            <div class="detail-row"><span class="detail-label">Error rate</span><code>{m.fault.errorRatePercent ?? 0}% → {(m.fault.errorStatusCodes ?? []).join(', ') || '500'}</code></div>
            <div class="detail-row"><span class="detail-label">Timeout rate</span><code>{m.fault.timeoutRatePercent ?? 0}%</code></div>
          </div>
        {/if}

        <div class="detail-section">
          <h4>Version history</h4>
          <VersionHistory
            versions={versionsByMock[m.id]}
            current={m}
            {restoringVersionId}
            onRestore={(versionId) => restoreVersion(m.id, versionId)}
          />
        </div>

        <div class="detail-section">
          <div class="detail-section-header">
            <h4>Log History</h4>
            {#if hitsByMock[m.id]?.length}
              <button class="btn btn-ghost small remove" on:click={() => clearMockLogs(m.id)}>Clear logs</button>
            {/if}
          </div>
          {#if hitsLoading === m.id}
            <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
          {:else if !hitsByMock[m.id]?.length}
            <p class="sub">No hits recorded yet — hit this mock to generate some.</p>
          {:else}
            <div class="hit-list">
              {#each hitsByMock[m.id] as h (h.id)}
                <div class="hit-row">
                  <button class="hit-delete" title="Delete this log entry" on:click|stopPropagation={() => deleteHitLogEntry(m.id, h.id)}>&times;</button>
                  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
                  <div
                    class="hit-line2"
                    on:click={() => (expandedHitId = expandedHitId === h.id ? '' : h.id)}
                  >
                    <button type="button" class="expand-arrow" class:expanded={expandedHitId === h.id} aria-expanded={!!(expandedHitId === h.id)} aria-label="Show or hide details" on:click|stopPropagation={() => (expandedHitId = expandedHitId === h.id ? '' : h.id)}>▸</button>
                    <span class="badge {h.responseStatus < 400 ? 'badge-ok' : 'badge-err'}">{h.responseStatus}</span>
                    <code class="hit-req">{h.method} {h.path}</code>
                  </div>
                  <span class="sub hit-time">{new Date(h.createdAt).toLocaleString()} · {h.latencyMs}ms</span>
                  {#if expandedHitId === h.id}
                    <div class="hit-detail">
                      <div class="detail-section">
                        <h4>Request</h4>
                        {#if Object.keys(h.requestHeaders ?? {}).length}
                          <div class="kv-block">
                            {#each Object.entries(h.requestHeaders) as [k, v]}
                              <div class="kv-row"><span class="kv-key">{k}</span><span class="kv-val">{v}</span></div>
                            {/each}
                          </div>
                        {:else}
                          <p class="sub">No headers recorded.</p>
                        {/if}
                        {#if h.requestBody}
                          <pre class="hit-body">{formatBody(h.requestBody)}</pre>
                        {:else}
                          <p class="sub">No body.</p>
                        {/if}
                      </div>
                      <div class="detail-section">
                        <h4>Response</h4>
                        {#if Object.keys(h.responseHeaders ?? {}).length}
                          <div class="kv-block">
                            {#each Object.entries(h.responseHeaders) as [k, v]}
                              <div class="kv-row"><span class="kv-key">{k}</span><span class="kv-val">{v}</span></div>
                            {/each}
                          </div>
                        {:else}
                          <p class="sub">No headers recorded.</p>
                        {/if}
                        {#if h.responseBody}
                          <pre class="hit-body">{formatBody(h.responseBody)}</pre>
                        {:else}
                          <p class="sub">No body.</p>
                        {/if}
                      </div>
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </div>
{/snippet}

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else if mocks.length === 0 && projects.length === 0}
  <div class="card empty">
    <span class="chip chip-stop">no mocks</span>
    <p>No mocks configured yet — create one above.</p>
  </div>
{:else if searchQuery.trim() && filteredMocks.length === 0}
  <div class="card empty">
    <span class="chip badge-warn">no matches</span>
    <p>No mocks match "{searchQuery}".</p>
  </div>
{:else}
  {#if selectedIds.size > 0}
    <div class="bulk-bar">
      <span class="sub">{selectedIds.size} selected</span>
      <button class="btn btn-ghost small" on:click={() => bulkAction('enable')}>Enable</button>
      <button class="btn btn-ghost small" on:click={() => bulkAction('disable')}>Disable</button>
      <select class="bulk-move-select" bind:value={bulkMoveProjectId} title="Project to move the selected mocks into">
        <option value="">Ungrouped</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
      <button class="btn btn-ghost small" on:click={() => bulkAction('move', bulkMoveProjectId)}>Move to project</button>
      <button class="btn btn-ghost small remove" on:click={() => bulkAction('delete')}>Delete</button>
      <button class="btn btn-ghost small" on:click={clearSelection}>Clear selection</button>
    </div>
  {/if}
  <div class="mock-list">
    {#each projects as p (p.id)}
      <div class="card project-card">
        <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
        <div
          class="project-summary"
          on:click={() => toggleProjectExpand(p.id)}
        >
          <button type="button" class="expand-arrow" class:expanded={expandedProjectIds.has(p.id)} aria-expanded={!!(expandedProjectIds.has(p.id))} aria-label="Show or hide details" on:click|stopPropagation={() => toggleProjectExpand(p.id)}>▸</button>
          {#if renamingProjectId === p.id}
            <input type="text" class="rename-input" bind:value={renameProjectName} placeholder="project name" title="Name" on:click|stopPropagation />
            <input type="text" class="rename-input" bind:value={renameProjectBasePath} placeholder="base path (optional)" title="Base path" on:click|stopPropagation />
            <input type="number" class="rename-input port-input" bind:value={renameProjectGatewayPort} placeholder="default gateway" title="Dedicated port (optional)" on:click|stopPropagation />
            <select bind:value={renameProjectWorkspaceId} on:click|stopPropagation title="Workspace">
              <option value="">Ungrouped</option>
              {#each workspaces as w (w.id)}<option value={w.id}>{w.name}{w.locked ? ' 🔒' : ''}</option>{/each}
            </select>
            {#if renameProjectGatewayPort}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <label class="toggle-label" on:click|stopPropagation>
                <input type="checkbox" bind:checked={renameProjectTlsEnabled} />
                TLS
              </label>
              {#if renameProjectTlsEnabled}
                <select bind:value={renameProjectTlsBundleId} on:click|stopPropagation title="Bundle">
                  <option value="">No bundle</option>
                  {#each certBundles as b}<option value={b.id}>{b.name}</option>{/each}
                </select>
                <select bind:value={renameProjectTlsCertificateId} on:click|stopPropagation disabled={!!renameProjectTlsBundleId} title="Server certificate">
                  <option value="">Select a certificate…</option>
                  {#each serverCerts as c}
                    <option value={c.id}>{c.name}</option>
                  {/each}
                </select>
                <select bind:value={renameProjectTlsClientCertMode} on:click|stopPropagation title="Client certificate">
                  <option value="">No client cert</option>
                  <option value="optional">Client cert optional</option>
                  <option value="required">Client cert required</option>
                </select>
                {#if (renameProjectTlsClientCertMode === 'optional' || renameProjectTlsClientCertMode === 'required') && !renameProjectTlsBundleId}
                  <select bind:value={renameProjectTlsClientCaId} on:click|stopPropagation title="Trusted client CA">
                    <option value="">Select a CA…</option>
                    {#each caCerts as c}<option value={c.id}>{c.name}</option>{/each}
                  </select>
                {/if}
              {/if}
            {/if}
            <button class="btn btn-ghost small" on:click|stopPropagation={() => saveRenameProject(p)}>Save</button>
            <button class="btn btn-ghost small" on:click|stopPropagation={() => (renamingProjectId = '')}>Cancel</button>
          {:else}
            <span class="project-name">{p.name}</span>
            {#if p.basePath}<span class="chip chip-stat">{p.basePath}</span>{/if}
            {#if p.gatewayPort}<span class="chip chip-tls">:{p.gatewayPort}</span>{/if}
            {#if p.tls}<span class="chip chip-tls">tls</span>{/if}
            {#if p.workspaceId && lockedWorkspaceIds.has(p.workspaceId)}<span class="chip badge-warn" title="Editing/deleting this project (or any mock in it) requires this workspace's password">🔒 {lockedWorkspaceName(p.workspaceId)}</span>{/if}
            <button
              class="btn btn-ghost small"
              on:click|stopPropagation={() => startRenameProject(p)}
              title="Edit name, base path, and port"
              aria-label="Edit project"
            >✏️ Edit</button>
          {/if}
          <span class="chip chip-stat">{allMocksByProject.byProject[p.id]?.length ?? 0} APIs</span>
          {#if allMocksByProject.byProject[p.id]?.length}
            <button
              class="btn btn-ghost small"
              title="Toggle every mock in this project at once, instead of one at a time"
              on:click|stopPropagation={() => toggleProjectMocks(p)}
            >{allMocksByProject.byProject[p.id].some((m) => m.enabled) ? 'Disable all' : 'Enable all'}</button>
          {/if}
          <button class="btn btn-ghost small" on:click|stopPropagation={() => openCreateForm(p.id)}>+ Add API</button>
          <button class="btn btn-ghost small" on:click|stopPropagation={() => duplicateProject(p)}>Duplicate</button>
          <button class="btn btn-ghost small remove" on:click|stopPropagation={() => removeProject(p.id)}>Delete</button>
        </div>
        {#if expandedProjectIds.has(p.id)}
          <div class="project-detail">
            {#each mocksByProject.byProject[p.id] ?? [] as m (m.id)}
              {@render mockRow(m)}
            {/each}
            {#if !(mocksByProject.byProject[p.id]?.length)}
              <p class="sub empty-items">
                {allMocksByProject.byProject[p.id]?.length ? `No APIs match "${searchQuery}" in this project.` : 'No APIs in this project yet.'}
              </p>
            {/if}
          </div>
        {/if}
      </div>
    {/each}

    {#if mocksByProject.ungrouped.length > 0 || projects.length === 0}
      {#if projects.length > 0}<h3 class="ungrouped-title">Ungrouped</h3>{/if}
      {#each mocksByProject.ungrouped as m (m.id)}
        {@render mockRow(m)}
      {/each}
    {/if}
  </div>
{/if}

{#if contextMenu}
  <ContextMenu x={contextMenu.x} y={contextMenu.y} items={contextMenuItems(contextMenu.mock)} onClose={() => (contextMenu = null)} />
{/if}

{#if pendingUnlock}
  <WorkspaceUnlockModal
    workspace={workspaces.find((w) => w.id === pendingUnlock.workspaceId)}
    onUnlock={pendingUnlock.onUnlock}
    onCancel={pendingUnlock.onCancel}
  />
{/if}

<style>
  .detail-section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
  .detail-section-header h4 { margin: 0; }
  .hit-row { position: relative; padding-right: 30px; }
  .hit-delete {
    position: absolute; top: 6px; right: 6px;
    background: none; border: none; color: var(--muted); cursor: pointer; font-size: 14px; line-height: 1;
    padding: 2px 5px; border-radius: 4px;
  }
  .hit-delete:hover { color: var(--error, #dc2626); background: rgba(220,38,38,.1); }
  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  /* Eight toolbar buttons are wider than a 1280-1536px window next to the title, so
     the row wraps (right-aligned) instead of pushing + New mock off-screen. */
  .head-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 10px; min-width: 0; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }
  .form-card { margin-top: 16px; display: flex; flex-direction: column; gap: 12px; }
  .field-row { display: flex; gap: 12px; }
  .method-field, .mode-field { flex: 0 0 140px; }
  .enabled-field { flex: 0 0 100px; flex-direction: row; align-items: center; gap: 6px; justify-content: flex-start; margin-top: 18px; }
  .enabled-field input[type="checkbox"] { width: 15px; height: 15px; accent-color: var(--primary); cursor: pointer; }
  .port-field { flex: 0 0 160px; }
  .status-field { flex: 0 0 100px; }
  .body-type-field { flex: 0 0 120px; }
  .json-hint { margin: 4px 0 0; font-size: 12px; }
  .json-hint.invalid { color: var(--error); }
  .days-field { flex: 0 0 140px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  .label-text { display: inline-flex; align-items: center; }
  input, select, textarea {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
    transition: border .2s;
  }
  textarea { font-family: 'SFMono-Regular', Consolas, monospace; resize: vertical; }
  input:focus, select:focus, textarea:focus { border-color: var(--primary); }

  .async-block { border-top: 1px solid var(--border); padding-top: 12px; display: flex; flex-direction: column; gap: 12px; }
  .async-block h3 { margin: 0; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }

  .rule-row { align-items: flex-end; }
  .rule-source { flex: 0 0 130px; }
  .rule-field { flex: 2; }
  .rule-type { flex: 0 0 110px; }
  .rule-constraint { flex: 1; }
  .rule-required { flex: 0 0 90px; flex-direction: row; align-items: center; gap: 6px; text-transform: none; font-weight: 600; }
  .rule-required input { width: 15px; height: 15px; }

  .mock-list { margin-top: 20px; display: flex; flex-direction: column; gap: 10px; }
  .mock-card { padding: 0; overflow: hidden; }
  .mock-row { display: flex; align-items: center; gap: 14px; padding: 14px 18px; cursor: pointer; }
  .chip-toggle { font: inherit; cursor: pointer; transition: filter .15s; }
  .chip-toggle:hover { filter: brightness(1.25); }
  .name { font-weight: 600; font-size: 13px; flex-shrink: 1; min-width: 0; max-width: 32%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .path { font-family: 'SFMono-Regular', Consolas, monospace; font-size: 13px; color: var(--muted); }
  .status { color: var(--muted); font-size: 13px; margin-left: auto; }
  .row-actions { display: flex; gap: 8px; margin-left: 12px; flex-shrink: 0; }
  .empty { margin-top: 20px; display: flex; flex-direction: column; gap: 10px; align-items: flex-start; }
  .select-box { width: 15px; height: 15px; flex-shrink: 0; }

  .bulk-bar {
    display: flex; align-items: center; gap: 10px; margin-top: 16px; padding: 10px 14px;
    background: var(--surface2, var(--hover)); border: 1px solid var(--border); border-radius: 8px;
  }
  .bulk-bar .sub { margin: 0; margin-right: 4px; }
  .bulk-move-select {
    background: var(--surface, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 5px 8px; font-size: 12px; font-family: inherit;
  }

  .mock-detail { padding: 16px 18px 18px 46px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 18px; }
  .detail-section h4 { margin: 0 0 8px; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .detail-row { display: flex; align-items: baseline; gap: 10px; font-size: 13px; margin-bottom: 6px; }
  .detail-row:last-child { margin-bottom: 0; }
  .detail-label { flex: 0 0 160px; color: var(--muted); font-size: 12px; font-weight: 600; }
  .detail-row code, .detail-row pre, .json-block {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 4px; padding: 2px 6px;
    white-space: pre-wrap; word-break: break-word; margin: 0;
  }
  .json-block { display: block; padding: 8px 10px; }

  .test-request-row { display: flex; align-items: center; gap: 10px; margin-top: 8px; }
  .test-response-body {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 6px; padding: 8px 10px;
    white-space: pre-wrap; word-break: break-word; margin: 8px 0 0; max-height: 240px; overflow-y: auto;
  }

  .hit-list { display: flex; flex-direction: column; gap: 6px; max-height: 260px; overflow-y: auto; padding-right: 4px; }
  .hit-row { display: flex; flex-direction: column; gap: 3px; font-size: 12px; padding: 8px 10px; background: var(--surface2, var(--hover)); border-radius: 6px; }
  .hit-line2 { display: flex; align-items: center; gap: 8px; cursor: pointer; }
  .hit-req { font-family: 'SFMono-Regular', Consolas, monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hit-time { color: var(--muted); }
  .hit-detail { margin-top: 6px; padding-top: 6px; border-top: 1px solid var(--border); }
  .hit-body { background: var(--card); border-radius: 6px; padding: 8px; font-size: 11px; overflow-x: auto; white-space: pre-wrap; word-break: break-word; margin-top: 4px; font-family: 'SFMono-Regular', Consolas, monospace; }
  .kv-block { display: flex; flex-direction: column; gap: 2px; }
  .kv-row { display: flex; gap: 8px; font-size: 11px; font-family: 'SFMono-Regular', Consolas, monospace; }
  .kv-key { color: var(--muted); flex: 0 0 auto; min-width: 120px; }
  .kv-val { color: var(--text); word-break: break-word; }

  .form-title { margin: 0; font-size: 15px; }
  .template-picker { margin: 10px 0; padding: 10px; background: var(--surface2, var(--hover)); border-radius: 8px; }
  .template-picker-label { display: block; font-size: 11px; font-weight: 600; color: var(--muted); margin-bottom: 6px; }
  .template-picker-row { display: flex; gap: 6px; flex-wrap: wrap; }
  .projects-bar { display: flex; gap: 10px; margin-top: 14px; align-items: center; }
  .search-input {
    flex: 1; max-width: 360px; background: var(--surface2, var(--hover)); border: 1.5px solid var(--border);
    color: var(--text); border-radius: 6px; padding: 8px 12px; font-size: 13px; font-family: inherit; outline: none;
  }
  .search-input:focus { border-color: var(--primary); }
  .btn.small { padding: 5px 10px; font-size: 12px; }

  .project-card { padding: 0; overflow: hidden; }
  .project-summary { display: flex; align-items: center; gap: 10px; padding: 12px 16px; cursor: pointer; }
  .project-name { font-weight: 700; font-size: 14px; flex-shrink: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .expand-arrow { display: inline-block; font-size: 11px; color: var(--muted); transition: transform .2s cubic-bezier(.4,0,.2,1); flex-shrink: 0; }
  .expand-arrow.expanded { transform: rotate(90deg); }
  .rename-input { padding: 5px 8px; font-size: 13px; width: 140px; }
  .rename-input.port-input { width: 80px; }
  .toggle-label {
    display: flex; flex-direction: row; align-items: center; gap: 6px; text-transform: none;
    font-weight: 600; font-size: 13px; color: var(--text); cursor: pointer;
  }
  .toggle-label input { width: 15px; height: 15px; }
  .project-detail { padding: 6px 16px 14px 34px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 8px; }
  .project-detail .mock-row { padding: 10px 14px; }
  .empty-items { margin: 6px 0; }
  .ungrouped-title { margin: 4px 0 0; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }

  /* .project-summary is a project (grouped-mocks) card's header row —
     expand arrow, name, base-path/port/tls chips, Edit, an API-count
     chip, Enable/Disable all, +Add API, Duplicate, and Delete, all as one
     unwrapped flex row. That's even more crowded than a plain .mock-row
     (already fixed globally in theme.css for the un-grouped listing
     below), so on a narrow screen it was worse: the project NAME itself
     was squeezed to near-zero width, and Duplicate/Delete rendered
     ~150px past the viewport's right edge — invisible and unreachable,
     same as .mock-card, .project-card also clips overflow rather than
     scrolling. Same fix: name gets its own full-width line, every chip
     and button wraps onto the line(s) after instead of being clipped. */
  @media (max-width: 640px) {
    .project-summary { flex-wrap: wrap; row-gap: 8px; }
    .project-name { order: -1; flex: 1 1 100%; white-space: normal; overflow-wrap: anywhere; }
  }
</style>
