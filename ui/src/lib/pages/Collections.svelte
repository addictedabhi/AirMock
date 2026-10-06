<script>
  import { onMount, tick } from 'svelte';
  import { api, collectionExportUrl, collectionsExportBulkUrl, loadTestResultExportUrl } from '../api.js';
  import { buildLoadTestReportHtml } from '../loadTestReport.js';
  import { showToast } from '../toast.js';
  import ImportSource from '../ImportSource.svelte';
  import Modal from '../Modal.svelte';
  import Suggestions from '../Suggestions.svelte';
  import InfoTooltip from '../InfoTooltip.svelte';
  import ContextMenu from '../ContextMenu.svelte';
  import { applyExtractRules } from '../jsonPath.js';
  import { copyText } from '../clipboard.js';
  import { pendingFocus, consumePendingFocus } from '../router.js';
  import { randomId } from '../id.js';
  import WorkspaceLockModal from '../WorkspaceLockModal.svelte';
  import WorkspaceUnlockModal from '../WorkspaceUnlockModal.svelte';
  import { runWithWorkspaceUnlock } from '../workspaceLock.js';
  import LatencyOverTimeChart from '../charts/LatencyOverTimeChart.svelte';
  import LatencyHistogram from '../charts/LatencyHistogram.svelte';
  import StatusBreakdownChart from '../charts/StatusBreakdownChart.svelte';

  const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];
  const WORKSPACE_STORAGE_KEY = 'airmock-active-workspace';
  const DEFAULT_WORKSPACE_ID = 'default'; // must match apiclient.DefaultWorkspaceID
  const TABS_STORAGE_PREFIX = 'airmock-open-tabs:';
  const FAVORITES_STORAGE_KEY = 'airmock-favorite-items';
  const EXPANDED_COLLECTIONS_STORAGE_PREFIX = 'airmock-expanded-collections:';
  const ACTIVE_ENV_STORAGE_PREFIX = 'airmock-active-env:';
  // Mirrors internal/apiclient.MaxLoadTestConcurrency/MaxLoadTestRequests/
  // MaxLoadTestDurationSecs — shown as input caps here purely for guidance;
  // the server clamps regardless, so drifting out of sync would be a minor
  // UX wart, never a way to bypass the real limit.
  const MAX_LOADTEST_CONCURRENCY = 50;
  const MAX_LOADTEST_REQUESTS = 2000;
  const MAX_LOADTEST_DURATION_SECS = 60;

  const COMMON_HEADER_NAMES = [
    'Content-Type', 'Authorization', 'Accept', 'Accept-Encoding', 'Cache-Control',
    'User-Agent', 'X-Api-Key', 'X-Request-Id', 'Cookie', 'Origin', 'Referer', 'X-Forwarded-For',
  ];
  const COMMON_CONTENT_TYPES = [
    'application/json', 'application/xml', 'text/plain', 'text/html',
    'application/x-www-form-urlencoded', 'multipart/form-data',
  ];
  const COMMON_QUERY_PARAMS = ['page', 'limit', 'offset', 'sort', 'order', 'filter', 'q', 'search', 'apiKey'];

  let workspaces = [];
  let activeWorkspaceId = '';
  let showNewWorkspaceForm = false;
  let newWorkspaceName = '';
  // Non-null while the "lock/change lock/remove lock" modal is open for
  // the active workspace — see WorkspaceLockModal.svelte.
  let showLockModal = false;
  // Non-null while prompting for a locked workspace's password before
  // OPENING it (switching to it, or restoring it on page load) — separate
  // from pendingDeleteWorkspaceId below, since opening only ever needs the
  // ordinary session-scoped unlock (see ensureWorkspaceOpenable).
  let pendingWorkspaceUnlock = null;
  // Non-null while confirming the CURRENT password to actually DELETE a
  // locked workspace — deliberately its own flow rather than reusing
  // ensureWorkspaceOpenable/pendingWorkspaceUnlock: deleting destroys the
  // lock itself, so it must demand the password fresh every time (see
  // api.deleteWorkspace), even for a browser that already unlocked this
  // workspace earlier in the session just to view/edit it.
  let pendingDeleteWorkspaceId = '';
  $: activeWorkspace = workspaces.find((w) => w.id === activeWorkspaceId);

  let collections = [];
  let environments = [];
  let loading = true;
  let searchQuery = '';

  // Certificates a request can present for mTLS — only ones with a private
  // key (hasKey) are usable here; a pure trust-anchor import (a CA's public
  // cert with no key) can't be presented as a client certificate.
  let clientCertOptions = [];
  async function loadClientCertOptions() {
    try {
      clientCertOptions = ((await api.listCertificates()) ?? []).filter((c) => c.hasKey);
    } catch {
      // non-fatal: the picker just falls back to empty, same as the Log
      // History mock-name dropdown when its own list call fails
    }
  }

  // Only collections with at least one surviving match (by request/folder
  // name, or the collection's own name) show up while searching — the
  // items each one renders are pre-filtered too, so a folder with no
  // matching children collapses out of view along with its non-matching
  // siblings instead of the search leaving a mostly-empty tree visible.
  $: visibleCollectionsWithItems = collections
    .map((c) => ({
      collection: c,
      items: searchQuery.trim() ? filterTreeBySearch(c.items, searchQuery) : c.items,
    }))
    .filter(
      ({ collection: c, items }) =>
        !searchQuery.trim() || items.length > 0 || c.name.toLowerCase().includes(searchQuery.toLowerCase()),
    );

  let expandedCollectionIds = new Set();
  let renamingCollectionId = '';
  let renameCollectionName = '';
  let openCollMenuId = '';
  // Position for the portaled menu (see portal() below) — captured from
  // the clicked button's own getBoundingClientRect() rather than tracked
  // continuously, since unlike Suggestions.svelte's dropdown this menu
  // isn't open during any animation/layout shift, so a single snapshot at
  // click time is already accurate.
  let collMenuRect = { bottom: 0, right: 0 };
  function toggleCollMenu(id, ev) {
    if (openCollMenuId === id) {
      openCollMenuId = '';
      return;
    }
    collMenuRect = ev.currentTarget.getBoundingClientRect();
    openCollMenuId = id;
    armMenuScrollGuard();
  }
  function closeCollMenu() {
    openCollMenuId = '';
  }

  // Same portaled-"⋮"-menu pattern as the collection-level menu above, for
  // a folder row's less-frequently-used actions (Duplicate/Delete) — added
  // once "▶ Run" pushed the folder row's always-visible button count to 6,
  // which no longer fit inline in a normal-width sidebar without wrapping
  // awkwardly over the folder name (row-actions has no flex-wrap, by
  // design, since a hover-only action row growing to two lines looks worse
  // than trimming which actions stay inline).
  let openItemMenuId = '';
  let itemMenuRect = { bottom: 0, right: 0 };
  function toggleItemMenu(id, ev) {
    if (openItemMenuId === id) {
      openItemMenuId = '';
      return;
    }
    itemMenuRect = ev.currentTarget.getBoundingClientRect();
    openItemMenuId = id;
    armMenuScrollGuard();
  }
  function closeItemMenu() {
    openItemMenuId = '';
  }

  // Right-click anywhere on a collection/folder/request row opens the same
  // "⋮" menu, positioned at the cursor instead of a button's rect — the
  // conventional way a context menu behaves (the tab bar already does this
  // via openTabContextMenu), rather than requiring a precise click on the
  // small "⋮" button itself. Reuses the exact same menu state/markup as the
  // button, just a different way to open it — a zero-size rect at the
  // cursor position is all clampToViewport needs to place it correctly.
  function openCollMenuAtCursor(id, e) {
    e.preventDefault();
    collMenuRect = { top: e.clientY, bottom: e.clientY, left: e.clientX, right: e.clientX };
    openCollMenuId = id;
    armMenuScrollGuard();
  }
  function openItemMenuAtCursor(id, e) {
    e.preventDefault();
    itemMenuRect = { top: e.clientY, bottom: e.clientY, left: e.clientX, right: e.clientX };
    openItemMenuId = id;
    armMenuScrollGuard();
  }

  // A "⋮" menu near the bottom of the page previously always opened
  // downward from its anchor button regardless of how much room was
  // actually left below it, cutting its lower options off past the
  // visible page. Runs once right after the (freshly-mounted, since it's
  // behind an {#if}) menu node exists, when its real rendered height is
  // known — flips it to open upward instead if downward would overflow,
  // and nudges it back on-screen horizontally too.
  function clampToViewport(node, anchorRect) {
    const menuRect = node.getBoundingClientRect();
    if (anchorRect.bottom + 4 + menuRect.height > window.innerHeight) {
      node.style.top = Math.max(4, Math.round(anchorRect.top - menuRect.height - 4)) + 'px';
    }
    if (anchorRect.right - menuRect.width < 0) {
      node.style.left = Math.round(anchorRect.left) + 'px';
    }
  }

  // Scrolling the page/a list while a "⋮" menu is open previously left the
  // menu pinned at its original screen position (it's portaled to <body>
  // with a one-time-computed fixed top/left) while the button it belongs
  // to moved away underneath it. Closing on any scroll — capture phase, so
  // this also catches an inner scrollable list's own scroll, which doesn't
  // bubble — matches how most dropdown menus behave rather than trying to
  // keep repositioning it live.
  //
  // menuJustOpened guards against a false trigger right at open time:
  // portaling the menu into <body> can itself nudge the browser's own
  // scroll-anchoring (Chrome adjusts scroll position to avoid a visible
  // content jump when something is inserted), which fires a genuine
  // `scroll` event with no user action at all — without this guard, a menu
  // opened near the bottom of the page closed itself the instant it
  // appeared. Two rAFs is enough to get past that incidental settling
  // while still catching any real scroll the user causes afterward.
  let menuJustOpened = false;
  function armMenuScrollGuard() {
    menuJustOpened = true;
    requestAnimationFrame(() => requestAnimationFrame(() => { menuJustOpened = false; }));
  }
  function closeAllMenusOnScroll() {
    if (menuJustOpened) return;
    if (openCollMenuId) openCollMenuId = '';
    if (openItemMenuId) openItemMenuId = '';
  }

  // {{variable}} insertion for the URL field and the raw body editor — the
  // two places a template placeholder is most useful. Inserts at the
  // input/textarea's actual cursor position (not just appended to the end)
  // so it works naturally in the middle of an existing value too, e.g.
  // "https://{{host}}/orders" with the cursor left where you clicked.
  let urlInputEl;
  let bodyTextareaEl;
  let openVarPicker = ''; // '' | 'url' | 'body'

  function toggleVarPicker(key) {
    openVarPicker = openVarPicker === key ? '' : key;
  }

  function insertVariable(el, currentValue, setValue, varName) {
    if (!el) return;
    const start = el.selectionStart ?? currentValue.length;
    const end = el.selectionEnd ?? currentValue.length;
    const token = `{{${varName}}}`;
    openVarPicker = '';
    // execCommand('insertText'), not a plain setValue(...) assignment: the
    // latter doesn't just fail to be a Ctrl+Z-able step itself, it wipes
    // out the field's ENTIRE native undo history (setting .value
    // programmatically clears the browser's undo stack) — so anything
    // typed before inserting a variable became unrecoverable too.
    // execCommand fires a genuine `input` event, so this field's own
    // bind:value (setValue) picks it up the normal way, no manual sync.
    if (document.execCommand) {
      el.focus();
      el.setSelectionRange(start, end);
      if (document.execCommand('insertText', false, token)) return;
    }
    // Fallback for a browser without execCommand support — still correct,
    // just not undoable.
    setValue(currentValue.slice(0, start) + token + currentValue.slice(end));
    requestAnimationFrame(() => {
      el.focus();
      const pos = start + token.length;
      el.setSelectionRange(pos, pos);
    });
  }
  // .coll-card-outer clips its contents (overflow:hidden, for its own
  // rounded corners) — a collapsed card is only as tall as its summary
  // row, so a menu positioned normally right below the "⋮" button rendered
  // taller than that gets silently clipped away entirely. Portaling
  // straight to <body> (the same fix already used for Suggestions.svelte's
  // dropdown, and for the same reason) sidesteps any ancestor's overflow
  // or transform.
  function portal(node) {
    document.body.appendChild(node);
    return {
      destroy() {
        node.remove();
      },
    };
  }
  let expandedFolderIds = new Set();
  function toggleFolderExpand(id) {
    const next = new Set(expandedFolderIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expandedFolderIds = next;
  }
  let renamingItemId = '';
  let renameItemName = '';
  let expandedEnvIds = new Set();
  let renamingEnvId = '';
  let renameEnvName = '';
  let newEnvVarKeyByEnv = {};
  let newEnvVarValueByEnv = {};
  let activeEnvId = '';

  let openTabs = []; // { id, collectionId, itemId } for saved items, or { id, draft } for unsaved scratch requests
  let activeTabId = '';
  let requestSection = 'headers'; // 'headers' | 'query' | 'body' | 'auth' | 'extract' | 'notes'
  let responseSection = 'body'; // 'body' | 'headers'

  // Below the mobile breakpoint, .workspace's two permanent columns
  // (collections-col + request-col — see their own CSS comment) can't
  // both fit at once, so only one shows at a time: the tree/list until a
  // request is actually opened, then the editor, with a "Back" affordance
  // to return. Ignored above the breakpoint entirely (both columns stay
  // permanently visible there via CSS, same as before this existed).
  // Reassigning activeTabId (opening/switching/reselecting a tab, from
  // ANY of the several places that do so) re-triggers this even when the
  // new value happens to equal the old one — Svelte's reactivity tracks
  // "was this assigned", not "did the value actually change" — so this
  // doesn't need its own call at every one of those call sites.
  let mobileShowRequestPane = false;
  $: if (activeTabId) mobileShowRequestPane = true;

  // Pretty-prints a JSON response body for readability; falls back to the
  // raw text untouched for a non-JSON (or empty) body rather than erroring.
  function formatResponseBody(text) {
    if (!text) return '';
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  }

  // A live ExecutionResult's headers are map[string][]string (Go's
  // http.Header shape, possibly several values per key); a saved Example's
  // are map[string]string (one value each, see saveExample). Both need to
  // render the same way in the Headers tab.
  function responseHeaderEntries(shown) {
    const headers = shown?.headers ?? {};
    return Object.entries(headers).map(([k, v]) => [k, Array.isArray(v) ? v.join(', ') : v]);
  }

  // Auto-saves every open tab (including in-progress edits to an unsaved
  // scratch tab's draft) to localStorage per workspace, so a refresh or
  // restart never loses work-in-progress — only closing a tab manually
  // drops it. tabsRestored gates this off until a workspace's tabs have
  // actually been loaded once, so the empty initial state can't stomp on
  // whatever was already saved before the restore runs.
  //
  // openTabs/activeTabId/activeItem are passed as arguments (rather than
  // just read inside persistTabs) so Svelte's static dependency analysis
  // — which only looks at identifiers appearing directly in the reactive
  // statement, not inside a called function's body — actually re-runs
  // this on every tab open/close/switch and on every edit to the active
  // item (including a draft tab's nested fields, which only invalidate
  // activeItem, not openTabs itself).
  let tabsRestored = false;
  // response/wsResponse/viewingExample/showSaveExampleForm are passed
  // explicitly (not just read inside persistTabs) for the same static-
  // dependency-analysis reason activeItem is — otherwise a Send() updating
  // response wouldn't itself re-trigger a save, and a refresh right after
  // sending (before switching tabs, which is the only other thing that used
  // to write it into responseCache) would lose that response entirely.
  $: if (activeWorkspaceId && tabsRestored) {
    persistTabs(openTabs, activeTabId, activeItem, response, wsResponse, viewingExample, showSaveExampleForm);
  }

  function tabsStorageKey(workspaceId) {
    return TABS_STORAGE_PREFIX + workspaceId;
  }

  function persistTabs(tabs, activeId, _activeItem, resp, wsResp, viewEx, showSaveEx) {
    try {
      // responseCache only reflects the CURRENTLY active tab up through the
      // last tab switch — merge in its live state here so what's on screen
      // right now is never a tab-switch behind what actually gets saved.
      const cache = activeId
        ? { ...responseCache, [activeId]: { response: resp, wsResponse: wsResp, viewingExample: viewEx, showSaveExampleForm: showSaveEx } }
        : responseCache;
      localStorage.setItem(tabsStorageKey(activeWorkspaceId), JSON.stringify({ openTabs: tabs, activeTabId: activeId, responseCache: cache }));
    } catch {
      // localStorage can throw (quota, private mode) — losing tab persistence isn't fatal
    }
  }

  function restoreTabs(workspaceId) {
    try {
      const raw = localStorage.getItem(tabsStorageKey(workspaceId));
      if (!raw) return { openTabs: [], activeTabId: '', responseCache: {} };
      const parsed = JSON.parse(raw);
      return {
        openTabs: Array.isArray(parsed.openTabs) ? parsed.openTabs : [],
        activeTabId: typeof parsed.activeTabId === 'string' ? parsed.activeTabId : '',
        responseCache: parsed.responseCache && typeof parsed.responseCache === 'object' ? parsed.responseCache : {},
      };
    } catch {
      return { openTabs: [], activeTabId: '', responseCache: {} };
    }
  }

  // Favorites are a personal, device-local shortcut list (same
  // localStorage-only persistence as open tabs) — pinning a request you
  // reach for constantly makes it one click away from the top of the page
  // instead of hunting through a folder tree every time.
  let favoriteIds = loadFavoriteIds();

  function loadFavoriteIds() {
    try {
      const raw = localStorage.getItem(FAVORITES_STORAGE_KEY);
      return new Set(raw ? JSON.parse(raw) : []);
    } catch {
      return new Set();
    }
  }

  function toggleFavorite(id) {
    const next = new Set(favoriteIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    favoriteIds = next;
    try {
      localStorage.setItem(FAVORITES_STORAGE_KEY, JSON.stringify([...next]));
    } catch {
      // localStorage can throw (quota, private mode) — losing favorites isn't fatal
    }
  }

  // Flattens favorited request items out of every collection's tree (not
  // just the active one) so the favorites bar works across the whole
  // workspace, tagging each with its collection's name/id so a click can
  // jump straight to it via openItemTab.
  function collectFavoritedItems(collectionsList, ids) {
    if (!ids.size) return [];
    const out = [];
    function walk(items, collection) {
      for (const it of items) {
        if (it.type === 'folder') walk(it.items ?? [], collection);
        else if (ids.has(it.id)) out.push({ collectionId: collection.id, collectionName: collection.name, item: it });
      }
    }
    for (const c of collectionsList) walk(c.items ?? [], c);
    return out;
  }

  let showCurlImportModal = false;
  // '' while showCurlImportModal is true means "import as a new draft tab,
  // not tied to any collection yet" (opened from "+ New request"'s curl
  // option) rather than "closed" — showCurlImportModal is the open/closed
  // flag now so this can validly be empty while the modal is still open.
  let curlImportTargetCollectionId = '';
  let curlImportText = '';
  let curlImportError = '';

  let postmanImportJson = '';
  let showPostmanImport = false;
  let soapUIImportXml = '';
  let showSoapUIImport = false;
  let wsdlImportContent = '';
  let wsdlImportURL = '';
  let wsdlImportName = '';
  let showWsdlImport = false;
  // Extra schema files (*.xsd) a WSDL references via <xsd:include>/
  // <xsd:import> instead of declaring its schema inline — this can't be
  // resolved from the pasted WSDL text alone, so picking the WSDL's own
  // folder (its typical on-disk shape: the .wsdl beside a Schemas/ folder)
  // hands back every file in one go, and every *.xsd among them is read
  // and sent alongside the main WSDL content. {name, content}[].
  let wsdlExtraSchemas = [];
  let wsdlFolderInput;
  let postmanEnvImportJson = '';
  let showPostmanEnvImport = false;
  let showEnvEditor = false;

  // Bulk collection export: multi-select checkboxes on the collection list
  // (same Set-based pattern as Mocks.svelte's bulk-action bar) feeding one
  // "Export selected" action instead of exporting one collection at a time.
  let selectedCollectionIds = new Set();
  function toggleCollectionSelect(id) {
    const next = new Set(selectedCollectionIds);
    next.has(id) ? next.delete(id) : next.add(id);
    selectedCollectionIds = next;
  }
  function clearCollectionSelection() {
    selectedCollectionIds = new Set();
  }
  // "Select all" only ever acts on what's currently visible (respecting an
  // active search filter) — selecting a collection the search has hidden
  // would be invisible and surprising to undo. A single toggle: already
  // all selected -> clear; anything else -> select every visible one.
  $: allVisibleCollectionsSelected =
    visibleCollectionsWithItems.length > 0 &&
    visibleCollectionsWithItems.every(({ collection: c }) => selectedCollectionIds.has(c.id));
  function toggleSelectAllVisibleCollections() {
    if (allVisibleCollectionsSelected) {
      clearCollectionSelection();
    } else {
      selectedCollectionIds = new Set(visibleCollectionsWithItems.map(({ collection: c }) => c.id));
    }
  }
  function exportSelectedCollections() {
    const a = document.createElement('a');
    a.href = collectionsExportBulkUrl([...selectedCollectionIds]);
    a.click();
    clearCollectionSelection();
  }

  // Bulk collection import: a hidden <input webkitdirectory> lets picking a
  // folder hand back every file under it (Chrome/Edge/Firefox); each *.json
  // file is read client-side (same as ImportSource's single-file path) and
  // sent together in one request rather than one importPostmanCollection
  // call per file.
  let bulkImportInput;
  let bulkImportBusy = false;
  async function onBulkImportFilesChosen(e) {
    const chosen = [...(e.target.files ?? [])].filter((f) => f.name.toLowerCase().endsWith('.json'));
    e.target.value = ''; // so picking the same folder again re-fires the change event
    if (chosen.length === 0) {
      showToast('No .json files found in that folder', 'err');
      return;
    }
    bulkImportBusy = true;
    try {
      const files = await Promise.all(chosen.map(async (f) => ({ fileName: f.name, content: await f.text() })));
      const result = await api.importPostmanCollectionsBulk(files, activeWorkspaceId);
      const n = result.imported?.length ?? 0;
      const failed = result.failed ?? [];
      if (failed.length) {
        showToast(`Imported ${n} of ${chosen.length} — failed: ${failed.map((f) => f.fileName).join(', ')}`, failed.length === chosen.length ? 'err' : 'ok');
      } else {
        showToast(`Imported ${n} collection${n === 1 ? '' : 's'}`, 'ok');
      }
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      bulkImportBusy = false;
    }
  }

  // Keyed by tab id, not a single shared flag — previously one `sending`
  // boolean meant sending a request in tab A showed "Sending…" on EVERY
  // open tab's Send button until A's response came back, and switching to
  // tab B mid-flight let you fire a SECOND request that then raced the
  // first for which one's result actually landed in the (also shared)
  // `response` variable. Each tab now tracks its own in-flight state
  // (and, via abortControllersByTabId below, its own way to cancel it),
  // so tabs behave as the independent, concurrent requests they should be.
  let sendingByTabId = {};
  let abortControllersByTabId = {}; // tabId -> AbortController, only while that tab's request is in flight
  $: sending = sendingByTabId[activeTabId] ?? false;
  let response = null; // ExecutionResult
  let wsResponse = null; // WSExchangeResult
  let viewingExample = null;
  let showSaveExampleForm = false;
  let newExampleName = '';
  // Not per-tab (unlike viewingExample/showSaveExampleForm above) — a
  // real request can carry dozens of saved examples (a Postman import's
  // documented happy path plus every error case), and always rendering
  // that whole list took up a lot of the detail pane's vertical space
  // whether or not anyone was looking at it right then. A single shared
  // collapse preference (rather than caching it per tab) is simplest and
  // matches how a "show/hide this section" toggle reads elsewhere in the
  // app — expanded by default so the list isn't hidden by surprise.
  let showExamplesList = true;

  // Each open tab keeps its own last-seen response rather than sharing one
  // response/wsResponse pair across every tab — previously switching tabs
  // either wiped a response you'd already gotten (openItemTab/openBlankTab
  // called resetResponsePanels() on every switch) or, when closing a tab
  // fell back to a neighboring one, silently kept showing the CLOSED tab's
  // response under the wrong tab (closeTab never reset anything at all).
  // This one reactive block is the single place activeTabId's effect on the
  // response panels is handled, replacing every ad-hoc resetResponsePanels()
  // call at each tab-switching call site.
  // Load test config/results are cached per tab the same way — otherwise
  // every open tab would show the same load-test table, and its results
  // table wouldn't tell you which request it actually came from.
  let responseCache = {}; // tabId -> { response, wsResponse, viewingExample, showSaveExampleForm, loadTestResult, showLoadTestPanel, ltConcurrency, ltTotalRequests, ltDurationSecs, ltDetailed }
  let previousTabId = '';
  $: if (activeTabId !== previousTabId) {
    if (previousTabId) {
      responseCache = {
        ...responseCache,
        [previousTabId]: {
          response, wsResponse, viewingExample, showSaveExampleForm,
          loadTestResult, showLoadTestPanel, ltConcurrency, ltTotalRequests, ltDurationSecs, ltDetailed,
        },
      };
    }
    const cached = responseCache[activeTabId];
    response = cached?.response ?? null;
    wsResponse = cached?.wsResponse ?? null;
    viewingExample = cached?.viewingExample ?? null;
    showSaveExampleForm = cached?.showSaveExampleForm ?? false;
    loadTestResult = cached?.loadTestResult ?? null;
    showLoadTestPanel = cached?.showLoadTestPanel ?? false;
    ltConcurrency = cached?.ltConcurrency ?? 5;
    ltTotalRequests = cached?.ltTotalRequests ?? 50;
    ltDurationSecs = cached?.ltDurationSecs ?? 0;
    ltDetailed = cached?.ltDetailed ?? false;
    // Load-test history is per-item, not per-tab-cached like the result above —
    // always close it and clear the list on tab switch so a stale history panel
    // (and another tab's runs) never bleed into the newly active tab; the next
    // "History" click reloads it fresh for whichever item is now active.
    showLoadTestHistory = false;
    loadTestHistory = [];
    loadingLoadTestHistory = false;
    previousTabId = activeTabId;
  }

  let showSaveDraftPicker = false;
  let saveDraftTargetCollectionId = '';

  // Load test — reuses the active request exactly as Send would, just fired
  // repeatedly through the concurrency-controlled runner instead of once.
  let showLoadTestPanel = false;
  let loadTestResult = null; // LoadTestResult
  let ltConcurrency = 5;
  let ltTotalRequests = 50;
  let ltDurationSecs = 0; // 0 = governed by ltTotalRequests instead
  let ltDetailed = false; // capture per-request samples, not just the aggregate summary

  // Keyed by tab id, same reasoning as sendingByTabId above — a load test
  // fired from tab A shouldn't show "Running…" on tab B, and switching away
  // from A mid-run shouldn't let a second test fire there and race the first.
  let loadTestingByTabId = {};
  $: loadTesting = loadTestingByTabId[activeTabId] ?? false;
  let loadTestAbortControllersByTabId = {}; // tabId -> AbortController, only while that tab's load test is in flight

  async function runLoadTest() {
    const isWS = activeItem?.type === 'wsrequest';
    const spec = isWS ? activeItem?.wsRequest : activeItem?.request;
    if (!spec) return;
    // Snapshot the tab and its config now — activeTabId/ltConcurrency/etc.
    // are the ACTIVE tab's live vars and change the instant the user
    // switches tabs, so reading them again after the await below would
    // silently apply to the wrong tab's request (see sendRequest above).
    const tabId = activeTabId;
    const concurrency = ltConcurrency;
    const totalRequests = ltTotalRequests;
    const durationSecs = ltDurationSecs;
    const detailed = ltDetailed;
    const vars = effectiveVariables;
    // '' for a draft tab (activeTab.itemId is unset) — still runs and still
    // gets persisted (see api.js's runLoadTest), just invisible to any
    // item's own history list.
    const context = { itemId: activeTab?.itemId ?? '', collectionId: activeTab?.collectionId ?? '' };

    const controller = new AbortController();
    loadTestAbortControllersByTabId = { ...loadTestAbortControllersByTabId, [tabId]: controller };
    loadTestingByTabId = { ...loadTestingByTabId, [tabId]: true };
    updateTabDisplayState(tabId, { loadTestResult: null });
    try {
      const runner = isWS ? api.runWSLoadTest : api.runLoadTest;
      const result = await runner(spec, vars, concurrency, durationSecs > 0 ? 0 : totalRequests, durationSecs, detailed, controller.signal, context);
      updateTabDisplayState(tabId, { loadTestResult: result });
      // Only refresh the shared loadTestHistory list if this tab is still
      // the active one — the user may have switched tabs while this run
      // was in flight, and loadLoadTestHistory would otherwise overwrite
      // whatever the now-active tab is showing with THIS tab's item history.
      if (context.itemId && tabId === activeTabId) await loadLoadTestHistory(context.itemId);
    } catch (e) {
      if (e.name === 'AbortError') {
        showToast('Load test stopped', 'ok');
      } else {
        showToast(e.message, 'err');
      }
    } finally {
      loadTestingByTabId = { ...loadTestingByTabId, [tabId]: false };
      const { [tabId]: _discard, ...rest } = loadTestAbortControllersByTabId;
      loadTestAbortControllersByTabId = rest;
    }
  }

  // Mirrors forceStop above — aborting the fetch cancels the load test's
  // request context server-side, which runLoadTestWorker checks between
  // iterations, so a long/uncapped run can actually be cut short instead of
  // only ever stopping at its configured request/duration cap.
  function forceStopLoadTest() {
    loadTestAbortControllersByTabId[activeTabId]?.abort();
  }

  function clearLoadTestResult() {
    loadTestResult = null;
  }

  let loadTestHistory = []; // LoadTestRunSummary[] for the active item
  let showLoadTestHistory = false;
  let loadingLoadTestHistory = false;

  async function loadLoadTestHistory(itemId) {
    if (!itemId) {
      loadTestHistory = [];
      return;
    }
    loadingLoadTestHistory = true;
    try {
      loadTestHistory = (await api.listLoadTestRuns(itemId)) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loadingLoadTestHistory = false;
    }
  }

  async function toggleLoadTestHistory() {
    showLoadTestHistory = !showLoadTestHistory;
    if (showLoadTestHistory && activeTab?.itemId) await loadLoadTestHistory(activeTab.itemId);
  }

  async function deleteLoadTestRun(runId) {
    if (!confirm('Delete this past load test run?')) return;
    try {
      await api.deleteLoadTestRun(runId);
      loadTestHistory = loadTestHistory.filter((r) => r.id !== runId);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function viewLoadTestRun(runId) {
    // Captured now, before the await below — activeTabId is reactive and
    // can change the instant the user switches tabs during this network
    // round-trip, so reading it again after the await would write the
    // result into the wrong (now-active) tab's state.
    const tabId = activeTabId;
    try {
      const run = await api.getLoadTestRun(runId);
      updateTabDisplayState(tabId, { loadTestResult: run.result });
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // downloadDetailedLoadTestResult is only for a Detailed, just-ran result:
  // response bodies/headers live only in loadTestResult itself (never
  // persisted), so the plain loadTestResultExportUrl <a download> link
  // (GET-by-id against the DB) can never include them — this instead posts
  // the in-memory result back to the server to render as a file. A result
  // reloaded from History is never Detailed (see GetLoadTestRun), so the
  // template only ever calls this for a genuinely live result.
  async function downloadDetailedLoadTestResult(format) {
    try {
      const { blob, filename } = await api.exportLoadTestResult(loadTestResult, format);
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = filename;
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // downloadLoadTestReport builds a single, self-contained HTML file —
  // stat tiles plus the status breakdown / latency histogram / latency-
  // over-time charts drawn as real PNGs (Canvas, no chart.js needed) — so
  // it's shareable and viewable with no AirMock instance running. Entirely
  // client-side: works for both a live result and one reloaded from
  // History (same loadTestResult state either way).
  function downloadLoadTestReport() {
    const isWS = activeItem?.type === 'wsrequest';
    const spec = isWS ? activeItem?.wsRequest : activeItem?.request;
    const html = buildLoadTestReportHtml(loadTestResult, {
      method: isWS ? 'WS' : spec?.method,
      url: spec?.url,
      generatedAt: new Date(),
    });
    const blob = new Blob([html], { type: 'text/html' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `airmock-loadtest-report${loadTestResult.runId ? '-' + loadTestResult.runId : ''}.html`;
    a.click();
    URL.revokeObjectURL(a.href);
  }

  // Which detailed-output sample row is expanded to show its full response
  // body/headers — transient UI state, not part of the per-tab cache above
  // (same reasoning as openVarPicker/expandedProjectIds elsewhere: nothing
  // meaningful is lost if it resets when you switch tabs or re-run).
  let expandedSampleIndex = -1;
  function toggleSample(i) {
    expandedSampleIndex = expandedSampleIndex === i ? -1 : i;
  }

  $: activeTab = openTabs.find((t) => t.id === activeTabId) ?? null;
  // .collectionId (not .draft) is what distinguishes "never saved to a
  // collection" from "saved" now that a saved tab also carries a .draft —
  // see openItemTab's comment for why every tab has one.
  $: activeCollection = activeTab?.collectionId ? (collections.find((c) => c.id === activeTab.collectionId) ?? null) : null;
  // .draft is the live edit buffer for BOTH a never-saved scratch tab and a
  // saved item with unsaved in-progress changes — falling back to the
  // collection's own copy only covers the brief window after a saved tab
  // is restored from localStorage before the backfill below seeds its draft.
  $: activeItem = activeTab?.draft ?? findItemById(activeCollection?.items ?? [], activeTab?.itemId) ?? null;
  $: activeEnv = environments.find((e) => e.id === activeEnvId) ?? null;
  // Collection variables are the default, the active environment overrides
  // them — the same precedence Postman itself uses between a collection's
  // own `variable[]` and whichever environment is selected alongside it.
  $: effectiveVariables = { ...(activeCollection?.variables ?? {}), ...(activeEnv?.variables ?? {}) };
  $: undefinedVars = activeItem?.request ? findUndefinedVars(activeItem.request, effectiveVariables) : [];
  $: favoritedItems = collectFavoritedItems(collections, favoriteIds);

  // Postman (and any future) imports can nest requests inside folders
  // arbitrarily deep — these walk that tree rather than assuming every
  // item lives at the collection's top level, which used to make anything
  // inside a subfolder invisible, unremovable, and unmockable.
  // A scratch tab (an unsaved "+ New request", never saved to a collection
  // — no collectionId) always resolves — nothing to delete out from under
  // it. A saved tab resolves only if its collection still exists AND still
  // contains that item somewhere in its tree — checked via collectionId,
  // not draft presence, since a saved tab has a draft too now (its
  // in-progress edit buffer).
  function tabHasLiveTarget(tab) {
    if (!tab.collectionId) return true;
    const coll = collections.find((c) => c.id === tab.collectionId);
    return !!coll && !!findItemById(coll.items ?? [], tab.itemId);
  }

  function findItemById(items, id) {
    for (const it of items) {
      if (it.id === id) return it;
      if (it.items?.length) {
        const found = findItemById(it.items, id);
        if (found) return found;
      }
    }
    return null;
  }
  function mapItemById(items, id, fn) {
    return items.map((it) => {
      if (it.id === id) return fn(it);
      if (it.items?.length) return { ...it, items: mapItemById(it.items, id, fn) };
      return it;
    });
  }
  function filterItemById(items, id) {
    return items
      .filter((it) => it.id !== id)
      .map((it) => (it.items?.length ? { ...it, items: filterItemById(it.items, id) } : it));
  }
  // Inserts newItem at the top level (parentFolderId falsy) or nested inside
  // the folder matching parentFolderId, wherever in the tree that is.
  function insertItemInFolder(items, parentFolderId, newItem) {
    if (!parentFolderId) return [...items, newItem];
    return items.map((it) => {
      if (it.id === parentFolderId && it.type === 'folder') return { ...it, items: [...(it.items ?? []), newItem] };
      if (it.items?.length) return { ...it, items: insertItemInFolder(it.items, parentFolderId, newItem) };
      return it;
    });
  }
  // Deep-clones an item (and, for a folder, everything nested inside it)
  // with fresh ids throughout — reusing the original ids on a duplicate
  // would leave two tree nodes claiming the same id, breaking every
  // id-keyed lookup (findItemById, tab identity, the #each keys) the
  // moment both copies exist in the same collection.
  function cloneItemWithNewIds(it) {
    const clone = { ...it, id: randomId() };
    if (it.request) clone.request = JSON.parse(JSON.stringify(it.request));
    if (it.examples) clone.examples = JSON.parse(JSON.stringify(it.examples));
    if (it.type === 'folder') clone.items = (it.items ?? []).map(cloneItemWithNewIds);
    return clone;
  }
  // Inserts a clone of the item matching targetId directly after it, in
  // whichever array (top level or nested inside some folder) actually
  // contains it.
  function duplicateItemInTree(items, targetId) {
    const idx = items.findIndex((it) => it.id === targetId);
    if (idx !== -1) {
      const clone = { ...cloneItemWithNewIds(items[idx]), name: `${items[idx].name} (copy)` };
      return [...items.slice(0, idx + 1), clone, ...items.slice(idx + 1)];
    }
    return items.map((it) => (it.items?.length ? { ...it, items: duplicateItemInTree(it.items, targetId) } : it));
  }
  function countLeafItems(items) {
    let count = 0;
    for (const it of items) {
      if (it.type === 'folder') count += countLeafItems(it.items ?? []);
      else count++;
    }
    return count;
  }

  // Keeps a request/folder whose own name matches, or a folder that still
  // has a surviving match somewhere inside it after filtering — so a
  // matching request deep in a folder stays reachable instead of the
  // search just hiding its whole ancestor chain.
  function filterTreeBySearch(items, query) {
    const needle = query.toLowerCase();
    const out = [];
    for (const it of items) {
      if (it.type === 'folder') {
        const filteredChildren = filterTreeBySearch(it.items ?? [], query);
        if (filteredChildren.length > 0 || it.name.toLowerCase().includes(needle)) {
          out.push({ ...it, items: filteredChildren });
        }
      } else if (itemMatchesSearch(it, needle)) {
        out.push(it);
      }
    }
    return out;
  }

  // Matches on the request's own name/URL/method too, not just its name —
  // finding "orders" previously required a request literally named
  // "orders"; a request named "Get invoice" hitting /api/orders/{id} was
  // invisible to search even though it's exactly what "orders" means here.
  function itemMatchesSearch(it, needle) {
    if (it.name.toLowerCase().includes(needle)) return true;
    const spec = it.type === 'wsrequest' ? it.wsRequest : it.request;
    if (spec?.url?.toLowerCase().includes(needle)) return true;
    if (spec?.method?.toLowerCase().includes(needle)) return true;
    return false;
  }
  function flattenRequestItems(items) {
    const out = [];
    for (const it of items) {
      if (it.type === 'folder') out.push(...flattenRequestItems(it.items ?? []));
      else if (it.type === 'request' && it.request?.url) out.push(it);
    }
    return out;
  }

  // Kept in sync with apiclient.DynamicVarNames on the backend — these
  // {{$...}} placeholders resolve at send time with no environment setup
  // at all, so they must never be flagged as "undefined".
  const DYNAMIC_VAR_HINTS = [
    { name: 'timestamp', description: 'Current Unix time in seconds, e.g. 1733839200' },
    { name: 'isoTimestamp', description: 'Current time as RFC3339, e.g. 2026-08-14T12:00:00Z' },
    { name: 'randomUUID', description: 'A fresh random UUID v4, e.g. 3fa85f64-5717-4562-b3fc-2c963f66afa6' },
    { name: 'randomInt', description: 'A random integer from 0 to 999' },
  ];
  const DYNAMIC_VAR_NAMES = new Set(DYNAMIC_VAR_HINTS.map((h) => h.name));
  const VAR_PATTERN = /\{\{\s*([^}]+?)\s*\}\}/g;

  // Scans every place a request substitutes {{var}} — URL, query, headers,
  // body, form fields, auth — and reports which referenced names won't
  // resolve against the active environment (or a dynamic variable) at send
  // time. Catches the easy-to-miss mistake of typing {{authToken}} in one
  // request while the extract rule that's supposed to populate it actually
  // writes to a differently-spelled variable name — a request that just
  // silently sends the literal text "{{authToken}}" instead of erroring.
  function findUndefinedVars(request, envVars) {
    if (!request) return [];
    const texts = [
      request.url,
      request.body,
      ...(request.headers ?? []).filter((h) => !h.disabled).flatMap((h) => [h.key, h.value]),
      ...(request.query ?? []).filter((q) => !q.disabled).flatMap((q) => [q.key, q.value]),
      ...(request.formFields ?? []).filter((f) => !f.disabled).flatMap((f) => [f.key, f.value]),
    ];
    if (request.auth) {
      texts.push(request.auth.token, request.auth.username, request.auth.password, request.auth.keyName, request.auth.keyValue);
    }
    const undefinedNames = new Set();
    for (const text of texts) {
      if (!text) continue;
      for (const match of text.matchAll(VAR_PATTERN)) {
        const name = match[1];
        if (name.startsWith('$')) {
          if (!DYNAMIC_VAR_NAMES.has(name.slice(1))) undefinedNames.add(name);
        } else if (!(name in envVars)) {
          undefinedNames.add(name);
        }
      }
    }
    return [...undefinedNames];
  }

  // Backfill fields onto items saved before Auth/body-mode existed, so the
  // template can always bind to them without null checks everywhere.
  $: if (activeItem?.request) {
    if (activeItem.request.auth === undefined) activeItem.request.auth = { type: 'none' };
    // addTo has no visible "unset" state in the Add-to <select> — the browser
    // just shows its first option ("Header") without ever writing that back
    // to the bound value, so an API-Key auth left untouched here silently
    // fell through both branches of the addTo check on the backend and the
    // key was dropped from the request entirely.
    if (activeItem.request.auth.type === 'apikey' && !activeItem.request.auth.addTo) {
      activeItem.request.auth.addTo = 'header';
    }
    if (activeItem.request.formFields === undefined) activeItem.request.formFields = [];
    if (activeItem.request.rawContentType === undefined) activeItem.request.rawContentType = 'json';
    if (activeItem.request.bodyMode === undefined) activeItem.request.bodyMode = '';
  }

  // Which format (if any) the raw body should be validated/beautified as —
  // only meaningful in Raw mode; url-encoded/form-data/none bodies have no
  // format to check.
  $: bodyFormatKind = (activeItem?.request?.bodyMode ?? '') === '' ? (activeItem?.request?.rawContentType ?? '') : '';
  $: bodyHasContent = !!(activeItem?.request?.body ?? '').trim();
  $: bodyFormatIssue =
    !bodyHasContent ? null :
    bodyFormatKind === 'json' ? jsonError(activeItem.request.body) :
    (bodyFormatKind === 'xml' || bodyFormatKind === 'html') ? markupError(activeItem.request.body, bodyFormatKind) :
    null;
  $: bodyFormatValid =
    bodyHasContent && !bodyFormatIssue && (bodyFormatKind === 'json' || bodyFormatKind === 'xml' || bodyFormatKind === 'html');

  // The engine's own JSON.parse error message reports a character offset
  // ("...at position 42"), not a line number — translated here by counting
  // newlines up to that offset, since a body of more than a line or two
  // makes "position 42" useless for actually finding the mistake.
  const JSON_ERROR_POSITION_RE = /position\s+(\d+)/i;
  function jsonError(text) {
    try {
      JSON.parse(text);
      return null;
    } catch (e) {
      const match = e.message.match(JSON_ERROR_POSITION_RE);
      const line = match ? text.slice(0, Number(match[1])).split('\n').length : null;
      return { message: e.message, line };
    }
  }

  function errorAt(text, index, message) {
    return { message, line: text.slice(0, index).split('\n').length };
  }

  // Generic tag-stack validator shared by XML and HTML: walks every tag in
  // source order, tracking a stack of still-open tags, and reports the
  // first structural mistake it finds — a closing tag that doesn't match
  // what's currently open, a stray closing tag with nothing open to match,
  // or a tag that's still open at the end of the document. This is not a
  // spec-compliant parser (attribute values containing a literal '>' will
  // confuse it, same simplification as beautifyMarkup below) — it's aimed
  // at the mistake this box actually exists to catch: a typo'd, swapped, or
  // forgotten closing tag.
  const TAG_RE = /<([^>]+)>/g;
  function markupError(text, kind) {
    // A body with zero tags (plain JSON, for instance) never enters the
    // loop below at all, so the tag stack stays empty and the function
    // would otherwise fall through to "valid" — vacuously true, not
    // actually checked. Require the content to actually start with a tag
    // first, which also catches "JSON pasted into an XML-typed body" since
    // JSON always starts with {, [, a quote, a digit, or true/false/null.
    const leadingWs = text.length - text.trimStart().length;
    if (!text.trim().startsWith('<')) {
      return errorAt(text, leadingWs, `Doesn't look like ${kind.toUpperCase()} — expected to start with a tag`);
    }
    const stack = [];
    TAG_RE.lastIndex = 0;
    let match;
    while ((match = TAG_RE.exec(text))) {
      const raw = match[1];
      if (/^[?!]/.test(raw)) continue; // declaration / comment / doctype
      const isClosing = raw.startsWith('/');
      const isSelfClosing = raw.endsWith('/');
      const name = raw.match(/^\/?\s*([a-zA-Z0-9:_-]+)/)?.[1]?.toLowerCase();
      if (!name) continue;
      if (isClosing) {
        if (!stack.length) return errorAt(text, match.index, `Unexpected closing tag </${name}> — nothing is open`);
        const top = stack[stack.length - 1];
        if (top.name !== name) return errorAt(text, top.index, `<${top.name}> is never closed (found </${name}> instead)`);
        stack.pop();
        continue;
      }
      if (isSelfClosing) continue;
      if (kind === 'html' && HTML_VOID_TAGS.has(name)) continue;
      stack.push({ name, index: match.index });
    }
    if (stack.length) {
      const open = stack[stack.length - 1];
      return errorAt(text, open.index, `<${open.name}> is never closed`);
    }
    return null;
  }

  // Pixel geometry mirrors the .body-textarea rule below exactly (line
  // height and top padding) so the overlay lines up with the real text —
  // if that CSS ever changes, these must change with it.
  const BODY_LINE_HEIGHT = 19.5; // 13px font-size * 1.5 line-height
  const BODY_PADDING_TOP = 8;
  let bodyScrollTop = 0;
  $: bodyErrorHighlightTop =
    bodyFormatIssue?.line ? BODY_PADDING_TOP + (bodyFormatIssue.line - 1) * BODY_LINE_HEIGHT - bodyScrollTop : null;

  // Void HTML elements never have a matching closing tag (and aren't
  // self-closed with `/>` either), so the indenter must know about them to
  // avoid over-indenting everything that follows one.
  const HTML_VOID_TAGS = new Set([
    'area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input',
    'link', 'meta', 'param', 'source', 'track', 'wbr',
  ]);

  // Small hand-rolled tag-depth indenter — good enough for "beautify this
  // body" the way Postman's does it, not a full XML/HTML parser. Handles
  // declarations/comments/doctypes and self-closing/void tags without
  // touching indentation for them.
  function beautifyMarkup(text) {
    const collapsed = text.trim().replace(/>\s*</g, '><');
    const tokens = collapsed.split(/(<[^>]+>)/).map((t) => t.trim()).filter(Boolean);
    let depth = 0;
    const lines = [];
    for (const token of tokens) {
      if (!token.startsWith('<')) {
        lines.push('  '.repeat(depth) + token);
        continue;
      }
      const isDeclaration = /^<[?!]/.test(token);
      const isClosing = /^<\//.test(token);
      const isSelfClosing = /\/>$/.test(token);
      const tagName = (token.match(/^<\/?\s*([a-zA-Z0-9:_-]+)/) ?? [])[1]?.toLowerCase();
      const isVoid = HTML_VOID_TAGS.has(tagName);

      if (isClosing) {
        depth = Math.max(0, depth - 1);
        lines.push('  '.repeat(depth) + token);
        continue;
      }
      lines.push('  '.repeat(depth) + token);
      if (!isDeclaration && !isSelfClosing && !isVoid) depth += 1;
    }
    return lines.join('\n');
  }

  // Replaces the body textarea's content via execCommand('insertText')
  // instead of a plain `activeItem.request.body = ...` assignment — a
  // programmatic `.value` set doesn't just fail to register as an undo
  // step, it clears the browser's ENTIRE native undo history for that
  // field, so Ctrl+Z couldn't undo anything typed before clicking Beautify
  // either. execCommand('insertText') fires a genuine `input` event
  // (indistinguishable from real typing), which both keeps it Ctrl+Z-able
  // and lets Svelte's own bind:value pick up the change normally — no
  // manual sync needed. Falls back to a plain assignment only if
  // execCommand is unavailable/fails (still correct, just not undoable).
  function applyUndoableBodyEdit(newValue) {
    const el = bodyTextareaEl;
    if (el && document.execCommand) {
      el.focus();
      el.select();
      if (document.execCommand('insertText', false, newValue)) return;
    }
    activeItem.request.body = newValue;
    activeItem = activeItem; // eslint-disable-line no-self-assign -- trigger Svelte reactivity
  }

  function beautifyBody() {
    if (!activeItem?.request) return;
    const mode = activeItem.request.rawContentType;
    try {
      let formatted;
      if (mode === 'xml' || mode === 'html') {
        const issue = markupError(activeItem.request.body, mode);
        if (issue) throw new Error(issue.line ? `line ${issue.line}: ${issue.message}` : issue.message);
        formatted = beautifyMarkup(activeItem.request.body);
      } else {
        formatted = JSON.stringify(JSON.parse(activeItem.request.body), null, 2);
      }
      applyUndoableBodyEdit(formatted);
    } catch (e) {
      showToast(`Cannot format: ${e.message}`, 'err');
    }
  }

  async function loadWorkspaces() {
    try {
      workspaces = (await api.listWorkspaces()) ?? [];
      const remembered = localStorage.getItem(WORKSPACE_STORAGE_KEY);
      activeWorkspaceId = workspaces.some((w) => w.id === remembered) ? remembered : (workspaces[0]?.id ?? '');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function loadAll() {
    loading = true;
    try {
      [collections, environments] = await Promise.all([
        api.listCollections(activeWorkspaceId),
        api.listEnvironments(activeWorkspaceId),
      ]);
      collections = collections ?? [];
      environments = environments ?? [];
      switchAwayFromDeadActiveTab();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  // loadAll() runs after nearly every mutation (see its own comment above),
  // so this is the one place that reliably catches every way the active
  // tab can stop pointing at anything real — its own collection/item
  // deleted directly, deleted as part of a parent folder/collection (which
  // may have also closed the tab outright, per removeItem's own leaf-id
  // cleanup — activeTabId then matches no tab at all), or moved elsewhere
  // by moveCopyItem — without needing a reactive statement that both reads
  // and writes activeTabId (Svelte's compiler rejects that as a cyclical
  // dependency, since activeTab itself is derived from activeTabId).
  // Switches to another still-live open tab instead of leaving the whole
  // detail pane on the generic "Select a request…" empty state while
  // perfectly good other tabs remain a click away; only ever lands on a tab
  // that actually still resolves (tabHasLiveTarget), so it can't bounce
  // back and forth between two dead tabs, and does nothing if every
  // remaining tab (or none at all) is in the same boat.
  function switchAwayFromDeadActiveTab() {
    const tab = openTabs.find((t) => t.id === activeTabId);
    if (tab && tabHasLiveTarget(tab)) return;
    const fallback = openTabs.find((t) => t.id !== activeTabId && tabHasLiveTarget(t));
    if (fallback) activeTabId = fallback.id;
  }

  function activeEnvStorageKey(workspaceId) {
    return ACTIVE_ENV_STORAGE_PREFIX + workspaceId;
  }

  // Restores whichever environment was last marked active for this
  // workspace (see selectActiveEnv) — must run AFTER loadAll() has
  // populated `environments`, since a remembered id that no longer
  // exists (its environment was deleted, or this is a completely
  // different server/dataset) falls back to '' rather than pointing at
  // nothing. Only called from the two places that actually need a fresh
  // restore (initial mount, workspace switch) — loadAll() itself is
  // called after nearly every mutation and must NOT re-run this, or
  // picking a different active environment would keep getting silently
  // reverted the next time anything saved.
  function restoreActiveEnv(workspaceId) {
    let remembered = '';
    try {
      remembered = localStorage.getItem(activeEnvStorageKey(workspaceId)) ?? '';
    } catch {
      // localStorage can throw (private mode) — just means nothing to restore
    }
    activeEnvId = environments.some((e) => e.id === remembered) ? remembered : '';
  }

  // The one place activeEnvId is ever assigned from a user action — always
  // paired with persisting the choice, so a later restoreActiveEnv (next
  // load, next refresh) picks the same one back up instead of the active
  // environment silently reverting to "none".
  function selectActiveEnv(id) {
    activeEnvId = id;
    try {
      localStorage.setItem(activeEnvStorageKey(activeWorkspaceId), id);
    } catch {
      // localStorage can throw (quota, private mode) — losing this preference isn't fatal
    }
  }

  // A saved tab persisted to localStorage before saved tabs carried a
  // .draft (openItemTab now seeds one on open) restores with none —
  // backfill it from the just-loaded collections so it behaves like any
  // other saved tab instead of falling through to activeItem's live-lookup
  // fallback forever. Only collections is available in time for this (not
  // yet in scope when restoreTabs runs, before loadAll has fetched
  // anything), so this must run after loadAll(), not inside restoreTabs.
  function backfillTabDrafts() {
    openTabs = openTabs.map((t) => {
      if (t.draft || !t.collectionId) return t;
      const coll = collections.find((c) => c.id === t.collectionId);
      const found = coll ? findItemById(coll.items ?? [], t.itemId) : null;
      return found ? { ...t, draft: JSON.parse(JSON.stringify(found)) } : t;
    });
  }

  onMount(async () => {
    loadClientCertOptions();
    await loadWorkspaces();
    if (!(await ensureWorkspaceOpenable(activeWorkspaceId))) {
      // Locked, and the user cancelled entering its password on initial
      // load — fall back to an openable workspace instead of leaving the
      // page stuck showing nothing for one we won't display.
      const fallback = workspaces.find((w) => !w.locked || w.unlocked) ?? workspaces[0];
      activeWorkspaceId = fallback?.id ?? activeWorkspaceId;
      localStorage.setItem(WORKSPACE_STORAGE_KEY, activeWorkspaceId);
    }
    const restored = restoreTabs(activeWorkspaceId);
    openTabs = restored.openTabs;
    responseCache = restored.responseCache;
    activeTabId = restored.activeTabId;
    tabsRestored = true;
    expandedCollectionIds = restoreExpandedCollections(activeWorkspaceId);
    await loadAll();
    backfillTabDrafts();
    restoreActiveEnv(activeWorkspaceId);
  });

  // Reactive rather than a one-shot onMount check: a command-palette jump
  // to a request already on THIS page (no navigation, so no remount) still
  // needs to react to a new pendingFocus value, and gating on
  // collections.length too means this naturally waits for the loadAll()
  // above to finish before it ever finds a match.
  $: if ($pendingFocus?.pageId === 'collections' && collections.length) {
    const focus = consumePendingFocus('collections');
    if (focus?.extra?.collectionId) {
      openItemTab(focus.extra.collectionId, focus.itemId);
    } else if (focus?.itemId) {
      // A bare collection jump (e.g. from the Dashboard's "Top collections
      // by traffic") — no specific request to open, just bring the
      // collection into view.
      setExpandedCollections(new Set([...expandedCollectionIds, focus.itemId]));
    }
  }

  // Resolves true immediately for an unlocked (or already-unlocked-this-
  // session — see the `unlocked` flag api.listWorkspaces() now returns)
  // workspace. Otherwise prompts for its password via pendingWorkspaceUnlock
  // and resolves once that's settled: true on a correct password, false on
  // Cancel. A locked workspace's collections/environments are also now
  // enforced server-side (see internal/web/api/apiclient.go's
  // listCollections/listEnvironments) — this is the client-side gate that
  // gets the password prompt shown in the first place, instead of the page
  // just silently failing to load.
  function ensureWorkspaceOpenable(id) {
    const w = workspaces.find((x) => x.id === id);
    if (!w?.locked || w.unlocked) return true;
    return new Promise((resolve) => {
      pendingWorkspaceUnlock = {
        workspaceId: id,
        onUnlock: () => {
          pendingWorkspaceUnlock = null;
          w.unlocked = true;
          resolve(true);
        },
        onCancel: () => {
          pendingWorkspaceUnlock = null;
          resolve(false);
        },
      };
    });
  }

  async function switchWorkspace(id) {
    if (!(await ensureWorkspaceOpenable(id))) return;
    activeWorkspaceId = id;
    localStorage.setItem(WORKSPACE_STORAGE_KEY, id);
    const restored = restoreTabs(id);
    openTabs = restored.openTabs;
    responseCache = restored.responseCache;
    activeTabId = restored.activeTabId;
    expandedCollectionIds = restoreExpandedCollections(id);
    await loadAll();
    backfillTabDrafts();
    restoreActiveEnv(id);
  }

  async function createWorkspace() {
    if (!newWorkspaceName.trim()) return;
    try {
      const w = await api.createWorkspace({ name: newWorkspaceName.trim() });
      newWorkspaceName = '';
      showNewWorkspaceForm = false;
      await loadWorkspaces();
      await switchWorkspace(w.id);
      showToast(`Workspace "${w.name}" created`, 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function removeWorkspace(id) {
    const w = workspaces.find((x) => x.id === id);
    const collCount = collections.length;
    const envCount = environments.length;
    const warning =
      collCount || envCount
        ? ` This also deletes ${collCount} collection${collCount === 1 ? '' : 's'} and ${envCount} environment${envCount === 1 ? '' : 's'} in it.`
        : '';
    if (!confirm(`Delete workspace "${w?.name ?? id}"?${warning} This cannot be undone.`)) return;
    // A locked workspace always demands its current password to actually
    // delete it — even for a browser that already unlocked it earlier this
    // session just to edit something in it (see api.deleteWorkspace) —
    // so this confirms via its own password prompt rather than deleting
    // outright the way an unlocked workspace does below.
    if (w?.locked) {
      pendingDeleteWorkspaceId = id;
      return;
    }
    try {
      await confirmDeleteWorkspace(id);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Deliberately lets its error propagate (rather than catching it and
  // toasting, the way every other action here does) — when called from
  // the pendingDeleteWorkspaceId password prompt below, WorkspaceUnlockModal
  // needs that rejection to show "Incorrect password" and let the user
  // retry, instead of the modal silently closing as if it had succeeded.
  async function confirmDeleteWorkspace(id, password) {
    await api.deleteWorkspace(id, password);
    await loadWorkspaces();
    await switchWorkspace(activeWorkspaceId);
    showToast('Workspace deleted', 'ok');
  }

  function toggleCollectionExpand(id) {
    const next = new Set(expandedCollectionIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setExpandedCollections(next);
  }

  // Which collections are expanded is otherwise lost on every page
  // refresh (a fresh `new Set()` on load, same as tabs/favorites would be
  // without their own persistence) — persisted per-workspace, same
  // localStorage-only pattern as open tabs, since a collection expanded in
  // one workspace has no bearing on another.
  function setExpandedCollections(next) {
    expandedCollectionIds = next;
    try {
      localStorage.setItem(EXPANDED_COLLECTIONS_STORAGE_PREFIX + activeWorkspaceId, JSON.stringify([...next]));
    } catch {
      // localStorage can throw (quota, private mode) — losing this isn't fatal
    }
  }

  function restoreExpandedCollections(workspaceId) {
    try {
      const raw = localStorage.getItem(EXPANDED_COLLECTIONS_STORAGE_PREFIX + workspaceId);
      return new Set(raw ? JSON.parse(raw) : []);
    } catch {
      return new Set();
    }
  }

  function resetResponsePanels() {
    response = null;
    wsResponse = null;
    viewingExample = null;
    showSaveExampleForm = false;
  }

  async function copyResponseBody(text) {
    try {
      await copyText(text ?? '');
      showToast('Response copied to clipboard', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Copies whichever section (Body or Headers) the response viewer's tabs
  // currently show, rather than always the body — copying "Headers" while
  // looking at the headers tab should copy what's on screen, not the body
  // underneath it.
  function currentResponseText(shown) {
    if (responseSection === 'headers') {
      return responseHeaderEntries(shown).map(([k, v]) => `${k}: ${v}`).join('\n');
    }
    return formatResponseBody(shown.body);
  }

  // Writes a patch of response-panel state into whichever tab it belongs
  // to — directly into the live response/wsResponse/etc. variables when
  // that tab is still the active one (so it's visible immediately), or
  // into responseCache otherwise (so it's there when the user switches
  // back to it) — regardless of which tab happens to be active by the time
  // a send()/sendWS() call actually resolves. This is what makes
  // concurrent tabs actually concurrent: sending in tab A and switching to
  // tab B while A is still in flight must not let A's eventual response
  // land in B's display (or vice versa).
  function updateTabDisplayState(tabId, patch) {
    if (tabId === activeTabId) {
      if ('response' in patch) response = patch.response;
      if ('wsResponse' in patch) wsResponse = patch.wsResponse;
      if ('viewingExample' in patch) viewingExample = patch.viewingExample;
      if ('showSaveExampleForm' in patch) showSaveExampleForm = patch.showSaveExampleForm;
      if ('loadTestResult' in patch) loadTestResult = patch.loadTestResult;
    } else {
      const existing = responseCache[tabId] ?? {};
      responseCache = { ...responseCache, [tabId]: { ...existing, ...patch } };
    }
  }

  function openItemTab(collectionId, itemId) {
    let tab = openTabs.find((t) => t.collectionId === collectionId && t.itemId === itemId);
    if (!tab) {
      // draft is a deep-cloned edit buffer, seeded from the collection's
      // current copy — every edit in this tab mutates the clone, not the
      // shared collections tree, so an unsaved change survives a refresh
      // (persistTabs/restoreTabs already round-trip every tab's own draft
      // through localStorage) instead of only ever living in memory until
      // the next loadAll() silently overwrote it with the backend's copy.
      const found = findItemById(collections.find((c) => c.id === collectionId)?.items ?? [], itemId);
      tab = { id: randomId(), collectionId, itemId, draft: found ? JSON.parse(JSON.stringify(found)) : null };
      openTabs = [...openTabs, tab];
    }
    activeTabId = tab.id;
  }

  function openBlankTab(kind = 'rest') {
    const tab = { id: randomId(), draft: emptyRequestItem(kind === 'soap' ? 'Untitled SOAP request' : 'Untitled request', kind) };
    openTabs = [...openTabs, tab];
    activeTabId = tab.id;
  }

  function closeTab(tabId, ev) {
    ev?.stopPropagation();
    const idx = openTabs.findIndex((t) => t.id === tabId);
    openTabs = openTabs.filter((t) => t.id !== tabId);
    // Drop its cached response too — a closed tab's response can't be
    // switched back to, so there's no reason to keep it around.
    const { [tabId]: _discard, ...rest } = responseCache;
    responseCache = rest;
    // Stop whatever this tab still had in flight rather than leaving it to
    // run to completion against a tab that no longer exists to show the
    // result, and drop its now-meaningless sending/abort-controller state.
    abortControllersByTabId[tabId]?.abort();
    const { [tabId]: _discardAbort, ...restAbort } = abortControllersByTabId;
    abortControllersByTabId = restAbort;
    const { [tabId]: _discardSending, ...restSending } = sendingByTabId;
    sendingByTabId = restSending;
    if (activeTabId === tabId) {
      activeTabId = (openTabs[idx] ?? openTabs[idx - 1])?.id ?? '';
    }
  }

  // Shared by closeOtherTabs/closeTabsToRight/closeAllTabs — same per-tab
  // cleanup closeTab does (cached response, in-flight abort, sending
  // state), just applied to a whole batch of ids at once instead of one
  // tabId at a time, and switching activeTabId only if it was among the
  // ones actually removed.
  function closeTabsWhere(idsToClose) {
    const idSet = new Set(idsToClose);
    if (idSet.size === 0) return;
    for (const tabId of idSet) {
      abortControllersByTabId[tabId]?.abort();
    }
    const keep = ([id]) => !idSet.has(id);
    openTabs = openTabs.filter((t) => !idSet.has(t.id));
    responseCache = Object.fromEntries(Object.entries(responseCache).filter(keep));
    abortControllersByTabId = Object.fromEntries(Object.entries(abortControllersByTabId).filter(keep));
    sendingByTabId = Object.fromEntries(Object.entries(sendingByTabId).filter(keep));
    if (idSet.has(activeTabId)) {
      activeTabId = openTabs[0]?.id ?? '';
    }
  }

  function closeOtherTabs(tabId) {
    closeTabsWhere(openTabs.filter((t) => t.id !== tabId).map((t) => t.id));
  }

  function closeTabsToRight(tabId) {
    const idx = openTabs.findIndex((t) => t.id === tabId);
    if (idx === -1) return;
    closeTabsWhere(openTabs.slice(idx + 1).map((t) => t.id));
  }

  function closeAllTabs() {
    closeTabsWhere(openTabs.map((t) => t.id));
  }

  // Right-click on an open request tab — see ContextMenu.svelte. Mirrors
  // the classic browser/editor tab-strip menu (Close/Close others/Close
  // to the right/Close all) rather than anything collection-specific,
  // since a tab here IS just that: an open request tab.
  let tabContextMenu = null;
  function openTabContextMenu(e, tab) {
    e.preventDefault();
    tabContextMenu = { x: e.clientX, y: e.clientY, tab };
  }
  function tabContextMenuItems(tab) {
    const idx = openTabs.findIndex((t) => t.id === tab.id);
    const tItem = tab.draft ?? findItemById(collections.find((c) => c.id === tab.collectionId)?.items ?? [], tab.itemId);
    return [
      { label: 'Rename', onClick: () => startRenameTab(tab, tItem?.name) },
      { divider: true },
      { label: 'Close', onClick: () => closeTab(tab.id) },
      { label: 'Close others', disabled: openTabs.length <= 1, onClick: () => closeOtherTabs(tab.id) },
      { label: 'Close tabs to the right', disabled: idx === -1 || idx >= openTabs.length - 1, onClick: () => closeTabsToRight(tab.id) },
      { divider: true },
      { label: 'Close all', danger: true, onClick: () => closeAllTabs() },
    ];
  }

  // Renaming an open tab — via its context menu's "Rename", or a
  // double-click directly on the tab — swaps its name into an inline
  // input right there in the tab strip. Deliberately its OWN state
  // (renamingTabId/renameTabName), not reusing the sidebar tree's
  // renamingItemId/renameItemName: those two rename UIs can be far apart
  // on screen and are conceptually independent surfaces, even though for
  // a saved item they end up writing the same underlying name.
  let renamingTabId = '';
  let renameTabName = '';

  function startRenameTab(tab, currentName) {
    renamingTabId = tab.id;
    renameTabName = currentName ?? '';
  }

  function cancelRenameTab() {
    renamingTabId = '';
  }

  // A scratch tab (no collectionId — never saved) has nothing saved to
  // rename via the API yet, so this just rewrites its draft's name in
  // place. A saved item's tab renames the underlying collection item
  // directly and immediately (the same write startRenameItem/saveRenameItem
  // make from the sidebar tree) rather than waiting for the request's own
  // Save button — renaming a tab is a lightweight, always-live action, kept
  // separate from whatever else is still unsaved in that tab's draft. The
  // draft's own name is updated too so the open editor doesn't keep showing
  // the old name until some other action happens to loadAll() again.
  async function saveRenameTab(tab) {
    const name = renameTabName.trim();
    renamingTabId = '';
    if (!name) return;
    if (!tab.collectionId) {
      openTabs = openTabs.map((t) => (t.id === tab.id ? { ...t, draft: { ...t.draft, name } } : t));
      return;
    }
    const collection = collections.find((c) => c.id === tab.collectionId);
    if (!collection) return;
    try {
      await withFreshCollection(collection.id, (items) => mapItemById(items, tab.itemId, (it) => ({ ...it, name })));
      openTabs = openTabs.map((t) => (t.id === tab.id && t.draft ? { ...t, draft: { ...t.draft, name } } : t));
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function onRenameTabKeydown(e, tab) {
    if (e.key === 'Enter') saveRenameTab(tab);
    else if (e.key === 'Escape') cancelRenameTab();
  }

  // Unlike the sidebar tree's rename inputs (collection/item/environment),
  // which rely on the click that opened them to have already placed focus
  // nearby, a tab rename can start from a double-click OR "Rename" in the
  // context menu — neither leaves focus anywhere near the new input — so
  // this is the one rename surface here that actually needs to grab focus
  // itself. select() additionally highlights the current name so typing
  // immediately replaces it, matching the rename-in-place UX of an editor
  // tab strip.
  function autofocus(node) {
    node.focus();
    node.select();
  }

  let tabBarEl;

  // Opening a new/blank tab (or any other tab switch) sets activeTabId,
  // but with enough tabs open the strip's own scroll position previously
  // never followed it — the newly-active tab could land off-screen to the
  // right, with the visibly-scrolled tab bar still showing older ones.
  // Reactive on activeTabId (not called ad-hoc from each tab-opening
  // function) so every path that switches tabs is covered uniformly.
  $: if (activeTabId && tabBarEl) {
    tick().then(() => {
      document.getElementById(`tab-${activeTabId}`)?.scrollIntoView({ behavior: 'instant', inline: 'nearest', block: 'nearest' });
    });
  }

  function scrollTabs(direction) {
    tabBarEl?.scrollBy({ left: direction * 160, behavior: 'smooth' });
  }

  // MouseEvent.detail is the browser's own consecutive-click counter (2 on
  // a double-click, 3 on a triple-click, using whatever multi-click timing
  // the OS/browser already applies) — a rapid triple-click on an arrow
  // jumps straight to that end of the tab strip instead of nudging by one
  // more increment, since by the third click in a row "just keep scrolling
  // a bit" clearly isn't really what's wanted.
  function onScrollArrowClick(e, direction) {
    if (e.detail >= 3) {
      tabBarEl?.scrollTo({ left: direction === -1 ? 0 : tabBarEl.scrollWidth, behavior: 'smooth' });
      return;
    }
    scrollTabs(direction);
  }

  // A plain mouse's vertical wheel does nothing over a horizontally
  // scrolling strip by default (only a trackpad's horizontal swipe or the
  // native scrollbar thumb could move it) — redirecting vertical wheel
  // delta into horizontal scroll here is what most tab strips (browser
  // tabs, editor tabs) already do, so it isn't surprising.
  function onTabBarWheel(e) {
    if (e.deltaY === 0 || !tabBarEl) return;
    e.preventDefault();
    tabBarEl.scrollBy({ left: e.deltaY });
  }

  // Retries a fixed-default-name create ("New collection", "New
  // environment", ...) under "<name> (2)", "<name> (3)", ... on a 409 name
  // collision, rather than surfacing an error for a name the user never
  // typed themselves — they just clicked a "+ New X" button.
  async function createWithDedupedName(baseName, createFn) {
    for (let attempt = 1; attempt <= 100; attempt++) {
      const name = attempt === 1 ? baseName : `${baseName} (${attempt})`;
      try {
        return await createFn(name);
      } catch (e) {
        if (e.status !== 409 || attempt === 100) throw e;
      }
    }
  }

  // "+ New collection" asks for a name first instead of silently creating
  // one called "New collection".
  let showNewCollectionForm = false;
  let newCollectionNameInput = '';
  function openNewCollectionForm() {
    newCollectionNameInput = '';
    showNewCollectionForm = true;
  }
  // After a tick, so it also wins against the modal's own mount-time focus handling.
  function focusOnMount(node) {
    setTimeout(() => node.focus(), 30);
  }
  async function confirmNewCollection() {
    const name = newCollectionNameInput.trim();
    if (!name) return;
    showNewCollectionForm = false;
    await newCollection(name);
  }

  async function newCollection(baseName = 'New collection') {
    try {
      const c = await createWithDedupedName(baseName, (name) =>
        api.createCollection({ name, items: [], workspaceId: activeWorkspaceId }),
      );
      await loadAll();
      setExpandedCollections(new Set([...expandedCollectionIds, c.id]));
      // Scrolled into view rather than just expanded in state — with
      // several collections already in the list, a new one can land
      // anywhere in it (or below the fold) with nothing on screen to show
      // it was actually created.
      await tick();
      document.getElementById(`collection-${c.id}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Shared by every rename input (collection/item/environment) — only one
  // of the three renaming*Id vars is ever non-empty at a time, so clearing
  // all three on Escape is simpler than threading a per-context cancel
  // function through four separate call sites.
  function cancelRename() {
    renamingCollectionId = '';
    renamingItemId = '';
    renamingEnvId = '';
  }

  function startRenameCollection(c) {
    renamingCollectionId = c.id;
    renameCollectionName = c.name;
  }

  async function saveRenameCollection(c) {
    const name = renameCollectionName.trim() || c.name;
    try {
      const fresh = await api.getCollection(c.id);
      await api.updateCollection(c.id, { ...fresh, name });
      renamingCollectionId = '';
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function duplicateCollection(c) {
    try {
      const fresh = await api.getCollection(c.id);
      await createWithDedupedName(`${fresh.name} (copy)`, (name) =>
        api.createCollection({ name, workspaceId: fresh.workspaceId, items: (fresh.items ?? []).map(cloneItemWithNewIds) }),
      );
      await loadAll();
      showToast('Collection duplicated', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Move/copy — a collection to a different workspace, or a single request/
  // folder to a different collection (same workspace or another one).
  // moveCopyTarget shapes what the modal below asks for: kind picks
  // collection-vs-item, action picks move-vs-copy (move additionally
  // removes the source once the copy lands, everything else is identical).
  let moveCopyTarget = null; // { kind: 'collection'|'item', action: 'move'|'copy', sourceCollectionId, itemId, itemName } | null
  let moveCopyWorkspaceId = '';
  let moveCopyCollectionId = '';
  let moveCopyWorkspaceCollections = [];
  let moveCopyBusy = false;
  $: moveCopyModalTitle = moveCopyTarget
    ? `${moveCopyTarget.action === 'move' ? 'Move' : 'Copy'} ${moveCopyTarget.kind === 'collection' ? 'collection' : 'request'} "${moveCopyTarget.itemName}"`
    : '';

  function openMoveCopyCollection(c, action) {
    moveCopyTarget = { kind: 'collection', action, sourceCollectionId: c.id, itemId: '', itemName: c.name };
    moveCopyWorkspaceId = workspaces.find((w) => w.id !== c.workspaceId)?.id ?? '';
  }

  async function openMoveCopyItem(collection, itemId, action) {
    const it = findItemById(collection.items ?? [], itemId);
    moveCopyTarget = { kind: 'item', action, sourceCollectionId: collection.id, itemId, itemName: it?.name ?? '(item)' };
    moveCopyWorkspaceId = collection.workspaceId;
    moveCopyCollectionId = '';
    await loadMoveCopyCollectionOptions();
  }

  function closeMoveCopyModal() {
    moveCopyTarget = null;
  }

  // Re-fetched whenever the target workspace changes — a collection list
  // for a DIFFERENT workspace was never loaded into `collections` (that's
  // scoped to activeWorkspaceId only), so the picker's options can't just
  // filter the already-loaded list the way an in-workspace picker would.
  async function loadMoveCopyCollectionOptions() {
    try {
      moveCopyWorkspaceCollections = (await api.listCollections(moveCopyWorkspaceId)) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function confirmMoveCopy() {
    if (!moveCopyTarget) return;
    moveCopyBusy = true;
    try {
      if (moveCopyTarget.kind === 'collection') {
        if (!moveCopyWorkspaceId) {
          showToast('Pick a destination workspace', 'err');
          return;
        }
        if (moveCopyTarget.action === 'move') {
          // Both ends can be locked (see internal/web/api/apiclient.go's
          // moveCollection) — a 403 names whichever one isn't unlocked yet;
          // runWithWorkspaceUnlock prompts for it and retries, which will
          // prompt again for the OTHER one too if both happen to be locked.
          await runWithWorkspaceUnlock(
            () => api.moveCollection(moveCopyTarget.sourceCollectionId, moveCopyWorkspaceId),
            (p) => (pendingWorkspaceUnlock = p),
          );
        } else {
          const fresh = await api.getCollection(moveCopyTarget.sourceCollectionId);
          await createWithDedupedName(fresh.name, (name) =>
            runWithWorkspaceUnlock(
              () => api.createCollection({ name, workspaceId: moveCopyWorkspaceId, items: (fresh.items ?? []).map(cloneItemWithNewIds) }),
              (p) => (pendingWorkspaceUnlock = p),
            ),
          );
        }
      } else {
        if (!moveCopyCollectionId) {
          showToast('Pick a destination collection', 'err');
          return;
        }
        const source = findItemById(
          collections.find((c) => c.id === moveCopyTarget.sourceCollectionId)?.items ?? [],
          moveCopyTarget.itemId,
        );
        if (!source) throw new Error('That item no longer exists');
        await withFreshCollection(moveCopyCollectionId, (items) => [...items, cloneItemWithNewIds(source)]);
        if (moveCopyTarget.action === 'move') {
          await withFreshCollection(moveCopyTarget.sourceCollectionId, (items) => filterItemById(items, moveCopyTarget.itemId));
          openTabs = openTabs.filter((t) => !(t.collectionId === moveCopyTarget.sourceCollectionId && t.itemId === moveCopyTarget.itemId));
        }
      }
      showToast(
        `${moveCopyTarget.kind === 'collection' ? 'Collection' : 'Request'} ${moveCopyTarget.action === 'move' ? 'moved' : 'copied'}`,
        'ok',
      );
      closeMoveCopyModal();
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      moveCopyBusy = false;
    }
  }

  async function removeCollection(id) {
    const c = collections.find((x) => x.id === id);
    if (!confirm(`Delete collection "${c?.name ?? id}" and all its requests? This cannot be undone.`)) return;
    try {
      await api.deleteCollection(id);
      openTabs = openTabs.filter((t) => t.collectionId !== id);
      // Drop any now-dangling favorites this collection's items held, same
      // reasoning as removeItem's single-item cleanup.
      for (const f of collectFavoritedItems(c ? [c] : [], favoriteIds)) toggleFavorite(f.item.id);
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // A SOAP call is just an HTTP POST with an XML envelope body and a
  // SOAPAction header — not a distinct wire protocol — so it reuses the
  // exact same 'request' item type and RequestSpec as any REST call rather
  // than needing its own item type; this only prefills the fields a SOAP
  // request actually needs so starting one from scratch isn't "hunt down
  // the right Content-Type/SOAPAction header and envelope shape by hand".
  const SOAP_ENVELOPE_TEMPLATE = `<?xml version="1.0" encoding="utf-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Header/>
  <soapenv:Body>
    <!-- request payload here -->
  </soapenv:Body>
</soapenv:Envelope>`;

  // A SOAP request is recognized three ways, most to least explicit —
  // relying on the SOAPAction header alone (the original check here)
  // missed real collections that don't use it:
  //  1. An explicit SOAPAction header — SOAP 1.1's own convention, and the
  //     one AirMock's own engine reads at dispatch time (see
  //     internal/engine/http/soap_matcher.go's matchSOAPOperation).
  //  2. A Content-Type of application/soap+xml — SOAP 1.2 folds the action
  //     into a `action="..."` parameter on Content-Type instead of a
  //     separate header, so a SOAP 1.2 collection has no SOAPAction header
  //     at all yet is unmistakably SOAP.
  //  3. A body that's structurally a SOAP envelope in either version's
  //     namespace — the last-resort net for a hand-built/older collection
  //     with neither of the above, mirroring how the engine itself falls
  //     back to matching by the body's own operation element name
  //     (detectOperationName) whenever no SOAPAction header is present.
  // Getting this right matters beyond just picking the right protocolType:
  // every SOAP operation in a collection conventionally POSTs to the SAME
  // URL (the one service endpoint), disambiguated only by SOAPAction/body
  // content — a request wrongly classified as plain REST collides with
  // its siblings on the (method, path) uniqueness check REST mocks enforce
  // (see internal/mock/store.go's restEndpointTaken, which SOAP mocks are
  // deliberately exempt from), so only the first ever got created and
  // every other one silently failed as a "duplicate endpoint" conflict.
  function isSoapRequest(it) {
    const headers = it?.request?.headers ?? [];
    if (headers.some((h) => h.key?.trim().toLowerCase() === 'soapaction')) return true;
    const contentType = headerValue(headers, 'content-type');
    if (/application\/soap\+xml/i.test(contentType)) return true;
    const body = it?.request?.body ?? '';
    return (
      // Prefix char class includes "-" for the common legacy SOAP-ENV:
      // prefix (Axis/.NET/zeep-generated bodies) — without it this missed
      // exactly the bodies most likely to need this last-resort fallback
      // at all, since a hand-rolled/older SOAP client is also the most
      // likely to omit both a SOAPAction header and a soap+xml Content-Type.
      /<[a-zA-Z0-9-]*:?Envelope[\s>]/.test(body) &&
      /schemas\.xmlsoap\.org\/soap\/envelope|w3\.org\/2003\/05\/soap-envelope/.test(body)
    );
  }

  // SOAP 1.2's counterpart to a SOAP 1.1 SOAPAction header: the same
  // action value, just folded into Content-Type as a parameter instead of
  // its own header (e.g. `application/soap+xml; action="urn:Op1"`). Stored
  // on the mock purely for display/reference — AirMock's own SOAP engine
  // dispatches by the literal SOAPAction header first, then by the body's
  // operation element name (extractSoapOperationName below), never by
  // Content-Type, so a mock created this way is correctly disambiguated by
  // operationName regardless of whether this ever gets populated.
  function soapContentTypeAction(contentType) {
    // Anchored to "start-of-string or a preceding ';'" so this matches the
    // actual "action" PARAMETER NAME, not any other parameter that merely
    // ends in those letters — e.g. without the anchor, "...; transaction=5;
    // action=..." would wrongly extract "5" (the leftmost "action" match,
    // inside "transaction") instead of the real action value.
    const m = /(?:^|;)\s*action\s*=\s*"?([^";]+)"?/i.exec(contentType || '');
    return m ? m[1] : '';
  }

  function emptyRequestItem(name, kind = 'rest') {
    if (kind === 'soap') {
      return {
        id: randomId(),
        type: 'request',
        name: name || 'New SOAP request',
        request: {
          method: 'POST', url: '', query: [], body: SOAP_ENVELOPE_TEMPLATE,
          headers: [
            { key: 'Content-Type', value: 'text/xml; charset=utf-8' },
            { key: 'SOAPAction', value: '' },
          ],
          bodyMode: '', rawContentType: 'xml', formFields: [], auth: { type: 'none' },
        },
      };
    }
    return {
      id: randomId(),
      type: 'request',
      name: name || 'New request',
      request: {
        method: 'GET', url: '', headers: [], query: [], body: '',
        bodyMode: '', rawContentType: 'json', formFields: [], auth: { type: 'none' },
      },
    };
  }

  function emptyFolderItem(name) {
    return { id: randomId(), type: 'folder', name: name || 'New folder', items: [] };
  }

  // Every mutation below patches a freshly-fetched copy of the collection
  // rather than the client's live `collections`/`activeCollection` object.
  // `activeItem` (and every other tab's item) is a direct reference into
  // that live tree, so it mutates on every keystroke, in every open tab,
  // regardless of which tab is active — PUTting it wholesale used to mean
  // clicking "Save" on one tab silently persisted whatever unsaved edits
  // happened to be sitting in every OTHER open tab of the same collection
  // too. Re-fetching first and applying only this one specific change
  // keeps each action scoped to what the user actually asked to save.
  async function withFreshCollection(collectionId, mutateItems) {
    const fresh = await api.getCollection(collectionId);
    const items = mutateItems(fresh.items ?? []);
    return api.updateCollection(collectionId, { ...fresh, items });
  }

  // parentFolderId targets a specific folder to add into; omit (or pass '')
  // to add at the collection's top level. The new item's id is generated
  // client-side (rather than left for the server to assign) specifically
  // so it's known immediately without having to search the tree afterwards
  // for "whichever item is new".
  async function addRequest(collection, parentFolderId = '', kind = 'rest') {
    const item = emptyRequestItem(undefined, kind);
    try {
      await withFreshCollection(collection.id, (items) => insertItemInFolder(items, parentFolderId, item));
      await loadAll();
      openItemTab(collection.id, item.id);
      if (parentFolderId) expandedFolderIds = new Set([...expandedFolderIds, parentFolderId]);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function addFolder(collection, parentFolderId = '') {
    const folder = emptyFolderItem();
    try {
      await withFreshCollection(collection.id, (items) => insertItemInFolder(items, parentFolderId, folder));
      await loadAll();
      setExpandedCollections(new Set([...expandedCollectionIds, collection.id]));
      expandedFolderIds = new Set([...expandedFolderIds, folder.id, ...(parentFolderId ? [parentFolderId] : [])]);
      startRenameItem(folder);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function startRenameItem(it) {
    renamingItemId = it.id;
    renameItemName = it.name;
  }

  async function saveRenameItem(collection, id) {
    const name = renameItemName.trim();
    renamingItemId = '';
    if (!name) return;
    try {
      await withFreshCollection(collection.id, (items) => mapItemById(items, id, (it) => ({ ...it, name })));
      // Any tab already open on this item has its own draft now (rather
      // than sharing the live collections object this rename just wrote
      // to) — sync its name too, so a rename from the sidebar tree still
      // shows up immediately in an already-open tab, as it always has.
      openTabs = openTabs.map((t) => (t.collectionId === collection.id && t.itemId === id && t.draft ? { ...t, draft: { ...t.draft, name } } : t));
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function saveActiveItem() {
    if (!activeCollection || !activeItem) return;
    const itemId = activeItem.id;
    const snapshot = JSON.parse(JSON.stringify(activeItem));
    try {
      await withFreshCollection(activeCollection.id, (items) => mapItemById(items, itemId, () => snapshot));
      showToast('Saved', 'ok');
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // A sentinel rather than '' for "create a new collection" in the picker
  // select — '' already legitimately meant "modal closed" for other pickers
  // in this file, and re-using it here would be an easy source of bugs.
  const NEW_COLLECTION_SENTINEL = '__new__';
  let saveDraftNewCollectionName = '';

  // Ctrl/Cmd+Enter to send, Ctrl/Cmd+S to save — the two actions reached
  // for constantly while iterating on a request, previously mouse-only.
  // Scoped to "a request tab is actually open" so the shortcuts don't
  // hijack Ctrl+S on, say, the Environments tab where there's nothing of
  // this page's to save.
  function handleGlobalKeydown(e) {
    if (!(e.ctrlKey || e.metaKey)) return;
    if (!activeItem || activeItem.type !== 'request') return;
    // A rename input (folder/collection/item/environment — all share this
    // class) has its own unconditional Enter handler that doesn't check
    // for modifier keys; without this guard, Ctrl+Enter while renaming
    // something unrelated in the sidebar tree fired BOTH that rename-save
    // AND this handler's send() on the currently active request tab.
    if (e.target?.classList?.contains('rename-input')) return;
    if (e.key.toLowerCase() === 's') {
      e.preventDefault();
      onClickSave();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (!sending) send();
    }
  }

  function onClickSave() {
    if (!activeTab?.collectionId) {
      // Never saved to a collection yet (a scratch tab — every tab has a
      // .draft now, so that alone no longer distinguishes this case).
      // Default to "create new" when there's nothing to pick from yet,
      // rather than blocking the save entirely with an error toast telling
      // the user to go create one first — this modal can now do that
      // itself in the same step.
      saveDraftTargetCollectionId = collections.length > 0 ? collections[0].id : NEW_COLLECTION_SENTINEL;
      saveDraftNewCollectionName = '';
      showSaveDraftPicker = true;
      return;
    }
    saveActiveItem();
  }

  async function confirmSaveDraft() {
    if (!activeTab || activeTab.collectionId) return;
    const draft = activeTab.draft;
    try {
      let target;
      if (saveDraftTargetCollectionId === NEW_COLLECTION_SENTINEL) {
        const name = saveDraftNewCollectionName.trim();
        if (!name) {
          showToast('Enter a name for the new collection', 'err');
          return;
        }
        target = await api.createCollection({ name, items: [], workspaceId: activeWorkspaceId });
      } else {
        target = collections.find((c) => c.id === saveDraftTargetCollectionId);
        if (!target) {
          showToast('That collection no longer exists', 'err');
          return;
        }
      }
      const updated = await withFreshCollection(target.id, (items) => [...items, draft]);
      const savedItem = updated.items[updated.items.length - 1];
      const tabId = activeTabId;
      // Keeps draft (now the just-saved item) rather than dropping it —
      // this tab is no longer a scratch tab (collectionId/itemId make that
      // true on their own now), but it still needs a draft to edit into.
      openTabs = openTabs.map((t) => (t.id === tabId ? { id: t.id, collectionId: updated.id, itemId: savedItem.id, draft: savedItem } : t));
      setExpandedCollections(new Set([...expandedCollectionIds, updated.id]));
      showSaveDraftPicker = false;
      showToast('Saved to collection', 'ok');
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Flattens id (a request/wsrequest) or every leaf under id (a folder) —
  // used by removeItem to find every favorite that needs cleaning up when
  // a whole folder, not just a single request, gets deleted.
  function collectLeafIds(item) {
    if (!item) return [];
    if (item.type === 'folder') return (item.items ?? []).flatMap(collectLeafIds);
    return [item.id];
  }

  async function removeItem(collection, id) {
    try {
      const target = findItemById(collection.items ?? [], id);
      await withFreshCollection(collection.id, (items) => filterItemById(items, id));
      // Deleting a folder deletes everything nested inside it too, so its
      // descendants' own open tabs need closing here as well — matching on
      // itemId === id alone only ever caught a deleted single request's own
      // tab, leaving every tab for a request inside a deleted folder open
      // and pointing at nothing (the "Select a request…"/dead-tab fallback
      // above is a safety net for cases like this one, but closing them
      // outright here is the more correct fix at the source).
      const deletedIds = new Set(collectLeafIds(target).concat(id));
      openTabs = openTabs.filter((t) => !(t.collectionId === collection.id && deletedIds.has(t.itemId)));
      // Drop any now-dangling favorites rather than leaving them in
      // localStorage forever — id itself when deleting a single request,
      // or every request nested inside it when id is a whole folder.
      for (const leafId of collectLeafIds(target)) {
        if (favoriteIds.has(leafId)) toggleFavorite(leafId);
      }
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function duplicateItem(collection, id) {
    try {
      await withFreshCollection(collection.id, (items) => duplicateItemInTree(items, id));
      await loadAll();
      showToast('Duplicated', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function addHeaderRow() {
    activeItem.request.headers = [...(activeItem.request.headers ?? []), { key: '', value: '' }];
  }
  function removeHeaderRow(i) {
    activeItem.request.headers = activeItem.request.headers.filter((_, idx) => idx !== i);
  }
  function addQueryRow() {
    activeItem.request.query = [...(activeItem.request.query ?? []), { key: '', value: '' }];
  }
  function removeQueryRow(i) {
    activeItem.request.query = activeItem.request.query.filter((_, idx) => idx !== i);
  }
  function addExtractRuleRow() {
    activeItem.request.extractRules = [...(activeItem.request.extractRules ?? []), { path: '', variable: '' }];
  }
  function removeExtractRuleRow(i) {
    activeItem.request.extractRules = activeItem.request.extractRules.filter((_, idx) => idx !== i);
  }
  function addFormFieldRow() {
    activeItem.request.formFields = [...(activeItem.request.formFields ?? []), { key: '', value: '', type: 'text' }];
  }
  function removeFormFieldRow(i) {
    activeItem.request.formFields = activeItem.request.formFields.filter((_, idx) => idx !== i);
  }

  // Reads the picked file as base64 (the request builder runs entirely in
  // the browser, so this is how a file's bytes travel to the server inside
  // the same JSON RequestSpec as everything else — see apiclient.KV's Type/
  // FileName fields) and stores it directly on the form field row. Manual
  // mutation (not a bind:value), so activeItem is reassigned afterward to
  // trigger Svelte reactivity, the same pattern used elsewhere in this file
  // for nested-object edits.
  function onFormFieldFileChange(field, event) {
    const file = event.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      const commaIdx = reader.result.indexOf(',');
      field.value = commaIdx >= 0 ? reader.result.slice(commaIdx + 1) : '';
      field.fileName = file.name;
      activeItem = activeItem; // eslint-disable-line no-self-assign -- trigger Svelte reactivity
    };
    reader.onerror = () => showToast(`Could not read file "${file.name}"`, 'err');
    reader.readAsDataURL(file);
  }

  // Scopes the cookie jar to workspace+environment (or "no-env" when none is
  // selected, so cookies still persist by default rather than requiring an
  // environment pick first) — matches how variables are already scoped, and
  // keeps different workspaces' sessions from bleeding into each other.
  $: cookieJarKey = `${activeWorkspaceId}:${activeEnvId || 'no-env'}`;

  async function send() {
    if (!activeItem?.request) return;
    // Captured now, before any await — activeItem/activeTabId are reactive
    // and change the instant the user switches tabs, so reading them again
    // after the await below would silently apply to the WRONG tab's request.
    const tabId = activeTabId;
    const requestSpec = { ...activeItem.request, cookieJarKey };
    const extractRules = activeItem.request.extractRules;
    const vars = effectiveVariables;
    // Captured alongside the spec, before the same tab-switch race the
    // comment above already guards against — hit-log attribution only,
    // never sent to the real target.
    const hitContext = { collectionId: activeCollection?.id ?? '', collectionName: activeCollection?.name ?? '', requestName: activeItem.name ?? '' };

    const controller = new AbortController();
    abortControllersByTabId = { ...abortControllersByTabId, [tabId]: controller };
    sendingByTabId = { ...sendingByTabId, [tabId]: true };
    updateTabDisplayState(tabId, { response: null, wsResponse: null, viewingExample: null, showSaveExampleForm: false });

    try {
      const result = await api.executeRequest(requestSpec, vars, controller.signal, hitContext);
      updateTabDisplayState(tabId, { response: result });
      if (result && !result.error) {
        await applyExtractedVariables(extractRules, result.body);
      }
    } catch (e) {
      if (e.name === 'AbortError') {
        showToast('Request stopped', 'ok');
      } else {
        showToast(e.message, 'err');
      }
    } finally {
      sendingByTabId = { ...sendingByTabId, [tabId]: false };
      const { [tabId]: _discard, ...rest } = abortControllersByTabId;
      abortControllersByTabId = rest;
    }
  }

  // forceStop aborts whichever tab is currently active's in-flight
  // request/WS-exchange, if any — the fetch abort propagates all the way
  // to the real outbound network call server-side (see api.js's
  // executeRequest and internal/apiclient.ExecuteContext).
  function forceStop() {
    abortControllersByTabId[activeTabId]?.abort();
  }

  // Writes whatever ExtractRules resolved against a response body into the
  // active environment — run after every send (not just inside the
  // Collection Runner) so a login request's extracted token is available
  // for the very next manual request too, not just during a batch run.
  async function applyExtractedVariables(rules, bodyText) {
    if (!activeEnv || !rules?.length) return;
    const extracted = applyExtractRules(rules, bodyText);
    const keys = Object.keys(extracted);
    if (keys.length === 0) return;
    const variables = { ...activeEnv.variables, ...extracted };
    try {
      await api.updateEnvironment(activeEnv.id, { ...activeEnv, variables });
      await loadAll();
      showToast(`Extracted ${keys.join(', ')} into "${activeEnv.name}"`, 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Collection Runner — runs every request in a folder/collection
  // sequentially, in document order, feeding each one's ExtractRules
  // forward into the next request's variable substitution the same way a
  // login-then-use-the-token flow would work by hand. wsrequest items are
  // skipped: there's no simple request/response cycle to sequence them
  // into the same pass/fail table.
  let showRunResults = false;
  let runInProgress = false;
  let runningLabel = '';
  let runResults = [];
  $: runPassedCount = runResults.filter((r) => r.pass).length;

  // Shared pass/fail criterion for both the Collection Runner and a single
  // send()'s own pass/fail indicator — an exact ExpectedStatus match if
  // configured, else the generic "2xx/3xx with no transport error" default.
  function checkPass(result, expectedStatus) {
    if (result.error) return false;
    if (expectedStatus) return result.statusCode === expectedStatus;
    return result.statusCode >= 200 && result.statusCode < 400;
  }

  function collectRunnableItems(items) {
    const out = [];
    for (const it of items) {
      if (it.type === 'request' && it.request) out.push(it);
      else if (it.type === 'folder' && it.items?.length) out.push(...collectRunnableItems(it.items));
    }
    return out;
  }

  async function runItems(items, label, collectionId = '', collectionName = '') {
    const runnable = collectRunnableItems(items);
    if (runnable.length === 0) {
      showToast('No requests to run in there', 'err');
      return;
    }
    runInProgress = true;
    runningLabel = label;
    runResults = [];
    showRunResults = true;

    // A local snapshot, not `activeEnv.variables` directly — so a variable
    // extracted from request 1 is visible to request 2's substitution
    // within this same pass, without needing a round-trip save+reload of
    // the environment between every single request in the run. Seeded from
    // effectiveVariables (collection vars, then env vars on top) same as
    // every other substitution call site.
    let runVars = { ...effectiveVariables };

    for (const item of runnable) {
      const start = performance.now();
      let result;
      try {
        result = await api.executeRequest({ ...item.request, cookieJarKey }, runVars, undefined, { collectionId, collectionName, requestName: item.name ?? '' });
      } catch (e) {
        runResults = [...runResults, {
          name: item.name, method: item.request.method, url: item.request.url,
          status: null, latencyMs: Math.round(performance.now() - start), pass: false, error: e.message,
        }];
        continue;
      }
      const latencyMs = result.timing?.totalMs ?? Math.round(performance.now() - start);
      const pass = checkPass(result, item.request.expectedStatus);
      if (!result.error && item.request.extractRules?.length) {
        runVars = { ...runVars, ...applyExtractRules(item.request.extractRules, result.body) };
      }
      runResults = [...runResults, {
        name: item.name, method: item.request.method, url: item.request.url,
        status: result.statusCode, latencyMs, pass, error: result.error || '',
      }];
    }

    runInProgress = false;

    // Persist whatever this run extracted back to the real environment —
    // so it's available for an ad-hoc single request afterward too, same
    // as a lone send() does — but only if something actually changed.
    if (activeEnv) {
      const before = activeEnv.variables ?? {};
      const changed = Object.keys(runVars).some((k) => runVars[k] !== before[k]);
      if (changed) {
        try {
          await api.updateEnvironment(activeEnv.id, { ...activeEnv, variables: runVars });
          await loadAll();
        } catch (e) {
          showToast(e.message, 'err');
        }
      }
    }
  }

  let showCurlPreview = false;
  let curlPreviewText = '';
  let snippetLanguage = 'curl';
  const SNIPPET_LANGUAGES = [
    { id: 'curl', label: 'curl' },
    { id: 'js', label: 'JavaScript (fetch)' },
    { id: 'python', label: 'Python (requests)' },
    { id: 'go', label: 'Go (net/http)' },
  ];

  // Shows the generated snippet in a scrollable box before copying — a real
  // curl command (or fetch/requests call) is usually several lines or one
  // very long one, so blind-copying straight to the clipboard (the
  // previous, curl-only behavior) never actually let you see what you were
  // about to paste elsewhere. {{var}} placeholders are left unresolved in
  // every language, same as curl's own copy-as-curl always did — a snippet
  // is meant to be portable to wherever it's pasted, not pre-resolved
  // against whatever environment happened to be active right now.
  async function viewCurl(language = snippetLanguage) {
    if (!activeItem?.request) return;
    snippetLanguage = language;
    try {
      const { code } = await api.codeSnippet(activeItem.request, language);
      curlPreviewText = code;
      showCurlPreview = true;
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function copyCurlPreview() {
    try {
      await copyText(curlPreviewText);
      showToast(`${SNIPPET_LANGUAGES.find((l) => l.id === snippetLanguage)?.label ?? 'Snippet'} copied to clipboard`, 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // collectionId '' opens the modal in "draft" mode — used from "+ New
  // request"'s curl option, where there's no collection to add into yet
  // (matches openBlankTab's own no-collection-required design).
  function openCurlImportModal(collectionId = '') {
    showCurlImportModal = true;
    curlImportTargetCollectionId = collectionId;
    curlImportText = '';
    curlImportError = '';
  }

  function closeCurlImportModal() {
    showCurlImportModal = false;
    curlImportTargetCollectionId = '';
  }

  async function confirmCurlImport() {
    if (!curlImportText.trim()) {
      curlImportError = 'Paste a curl command first.';
      return;
    }
    let spec;
    try {
      spec = await api.curlImport(curlImportText);
    } catch (e) {
      curlImportError = e.message;
      return;
    }
    if (!spec?.url) {
      curlImportError = 'Could not find a URL in that curl command — check it and try again.';
      return;
    }

    if (!curlImportTargetCollectionId) {
      const tab = { id: randomId(), draft: { id: randomId(), type: 'request', name: spec.url, request: spec } };
      openTabs = [...openTabs, tab];
      activeTabId = tab.id;
      closeCurlImportModal();
      showToast('Imported from curl', 'ok');
      return;
    }

    const collection = collections.find((c) => c.id === curlImportTargetCollectionId);
    if (!collection) {
      curlImportError = 'That collection no longer exists.';
      return;
    }
    const item = { type: 'request', name: spec.url, request: spec };
    try {
      const updated = await withFreshCollection(collection.id, (items) => [...items, item]);
      closeCurlImportModal();
      await loadAll();
      openItemTab(updated.id, updated.items[updated.items.length - 1].id);
      showToast('Imported from curl', 'ok');
    } catch (e) {
      curlImportError = e.message;
    }
  }

  function cancelPostmanImport() {
    showPostmanImport = false;
    postmanImportJson = '';
  }

  // Shared by every "import a whole collection" flow — after the reload,
  // the newly imported collection can land anywhere in the list (or below
  // the fold) with nothing on screen to show it actually appeared, so it's
  // expanded and scrolled into view the same way newCollection() does for
  // a freshly created one.
  async function revealImportedCollection(imported) {
    await loadAll();
    setExpandedCollections(new Set([...expandedCollectionIds, imported.id]));
    await tick();
    document.getElementById(`collection-${imported.id}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  async function importPostman() {
    try {
      const imported = await api.importPostmanCollection(postmanImportJson, activeWorkspaceId);
      postmanImportJson = '';
      showPostmanImport = false;
      showToast('Collection imported', 'ok');
      await revealImportedCollection(imported);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function cancelSoapUIImport() {
    showSoapUIImport = false;
    soapUIImportXml = '';
  }

  async function importSoapUI() {
    try {
      const imported = await api.importSoapUICollection(soapUIImportXml, activeWorkspaceId);
      soapUIImportXml = '';
      showSoapUIImport = false;
      showToast('Collection imported', 'ok');
      await revealImportedCollection(imported);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function cancelWsdlImport() {
    showWsdlImport = false;
    wsdlImportContent = '';
    wsdlImportURL = '';
    wsdlImportName = '';
    wsdlExtraSchemas = [];
  }

  async function importWsdlCollection() {
    try {
      const imported = await api.importWSDLCollection(
        wsdlImportContent,
        wsdlImportURL.trim(),
        wsdlImportName.trim(),
        wsdlExtraSchemas.map((s) => s.content),
        activeWorkspaceId,
      );
      cancelWsdlImport();
      showToast('Collection imported', 'ok');
      await revealImportedCollection(imported);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Picking a folder hands back every file under it (Chrome/Edge/Firefox),
  // same webkitdirectory pattern as the Postman bulk import above — the
  // WSDL's own on-disk shape is typically the .wsdl beside a Schemas/
  // folder of *.xsd companions it <xsd:include>s, so this reads whichever
  // one file is the main WSDL (or lets the user pick which, if more than
  // one) plus every *.xsd found anywhere in the tree as schema companions.
  async function onWsdlFolderFilesChosen(e) {
    const chosen = [...(e.target.files ?? [])];
    e.target.value = '';
    const wsdlFiles = chosen.filter((f) => f.name.toLowerCase().endsWith('.wsdl'));
    const xsdFiles = chosen.filter((f) => f.name.toLowerCase().endsWith('.xsd'));
    if (wsdlFiles.length === 0) {
      showToast('No .wsdl file found in that folder', 'err');
      return;
    }
    if (wsdlFiles.length > 1) {
      showToast(`Found ${wsdlFiles.length} .wsdl files — picked "${wsdlFiles[0].name}"; import the others separately`, 'ok');
    }
    wsdlImportContent = await wsdlFiles[0].text();
    wsdlExtraSchemas = await Promise.all(xsdFiles.map(async (f) => ({ name: f.name, content: await f.text() })));
    showWsdlImport = true;
    showToast(`Loaded ${wsdlFiles[0].name}${xsdFiles.length ? ` + ${xsdFiles.length} schema file${xsdFiles.length === 1 ? '' : 's'}` : ''}`, 'ok');
  }

  function cancelPostmanEnvImport() {
    showPostmanEnvImport = false;
    postmanEnvImportJson = '';
  }

  // Closes the whole Environments panel, not just its nested Postman
  // import sub-card — also cancels that nested import (rather than
  // leaving it silently open) so reopening the panel later doesn't
  // resurface a stale in-progress paste.
  function cancelEnvEditor() {
    showEnvEditor = false;
    cancelPostmanEnvImport();
  }

  async function importPostmanEnvironment() {
    try {
      await api.importPostmanEnvironment(postmanEnvImportJson, activeWorkspaceId);
      postmanEnvImportJson = '';
      showPostmanEnvImport = false;
      showToast('Environment imported', 'ok');
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function sendWS() {
    if (!activeItem?.wsRequest) return;
    // Same capture-before-await / per-tab tracking as send() — see its
    // comments for why.
    const tabId = activeTabId;
    const wsRequest = activeItem.wsRequest;
    const vars = effectiveVariables;

    const controller = new AbortController();
    abortControllersByTabId = { ...abortControllersByTabId, [tabId]: controller };
    sendingByTabId = { ...sendingByTabId, [tabId]: true };
    updateTabDisplayState(tabId, { wsResponse: null });

    try {
      const result = await api.wsExchange(wsRequest, vars, 3, controller.signal);
      updateTabDisplayState(tabId, { wsResponse: result });
    } catch (e) {
      if (e.name === 'AbortError') {
        showToast('Request stopped', 'ok');
      } else {
        showToast(e.message, 'err');
      }
    } finally {
      sendingByTabId = { ...sendingByTabId, [tabId]: false };
      const { [tabId]: _discard, ...rest } = abortControllersByTabId;
      abortControllersByTabId = rest;
    }
  }

  // --- environments ---

  async function newEnvironment() {
    try {
      const env = await createWithDedupedName('New environment', (name) =>
        api.createEnvironment({ name, variables: {}, workspaceId: activeWorkspaceId }),
      );
      expandedEnvIds = new Set([...expandedEnvIds, env.id]);
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function toggleEnvExpand(id) {
    const next = new Set(expandedEnvIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expandedEnvIds = next;
  }

  function startRenameEnv(env) {
    renamingEnvId = env.id;
    renameEnvName = env.name;
  }

  async function saveRenameEnv(env) {
    try {
      await api.updateEnvironment(env.id, { ...env, name: renameEnvName.trim() || env.name });
      renamingEnvId = '';
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function addEnvVar(env) {
    const key = newEnvVarKeyByEnv[env.id]?.trim();
    if (!key) return;
    const variables = { ...env.variables, [key]: newEnvVarValueByEnv[env.id] ?? '' };
    try {
      await api.updateEnvironment(env.id, { ...env, variables });
      newEnvVarKeyByEnv = { ...newEnvVarKeyByEnv, [env.id]: '' };
      newEnvVarValueByEnv = { ...newEnvVarValueByEnv, [env.id]: '' };
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }
  async function removeEnvVar(env, key) {
    const variables = { ...env.variables };
    delete variables[key];
    try {
      await api.updateEnvironment(env.id, { ...env, variables });
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Editing an already-added variable in place — previously the only way
  // to change one was delete-then-re-add (losing its position, and easy
  // to fumble the value while retyping the key). editingEnvVar tracks
  // {envId, key} for at most one row at a time; editEnvVarDraft holds the
  // in-progress key/value so an edit can be cancelled without touching
  // the saved environment at all.
  let editingEnvVar = null;
  let editEnvVarDraft = { key: '', value: '' };

  function startEditEnvVar(env, key, value) {
    editingEnvVar = { envId: env.id, key };
    editEnvVarDraft = { key, value };
  }

  function cancelEditEnvVar() {
    editingEnvVar = null;
  }

  async function saveEditEnvVar(env, originalKey) {
    const newKey = editEnvVarDraft.key.trim();
    if (!newKey) return;
    // Rebuilt key-by-key (rather than just deleting the old key and
    // setting the new one at the end) so a plain value edit — the common
    // case, key unchanged — doesn't reorder the variable to the bottom
    // of the list.
    const variables = {};
    for (const [k, v] of Object.entries(env.variables ?? {})) {
      if (k === originalKey) variables[newKey] = editEnvVarDraft.value;
      else variables[k] = v;
    }
    editingEnvVar = null;
    try {
      await api.updateEnvironment(env.id, { ...env, variables });
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function onEditEnvVarKeydown(e, env, originalKey) {
    if (e.key === 'Enter') saveEditEnvVar(env, originalKey);
    else if (e.key === 'Escape') cancelEditEnvVar();
  }
  async function duplicateEnvironment(env) {
    try {
      await createWithDedupedName(`${env.name} (copy)`, (name) =>
        api.createEnvironment({ name, workspaceId: env.workspaceId, variables: { ...env.variables } }),
      );
      await loadAll();
      showToast('Environment duplicated', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function removeEnvironment(id) {
    const env = environments.find((x) => x.id === id);
    if (!confirm(`Delete environment "${env?.name ?? id}"? This cannot be undone.`)) return;
    try {
      await api.deleteEnvironment(id);
      if (activeEnvId === id) selectActiveEnv('');
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // --- create mocks from collection requests ---

  function urlToPathPattern(url) {
    let path = url || '/';
    path = path.replace(/^\{\{[^}]+\}\}/, ''); // strip a leading {{baseUrl}}-style var
    const m = path.match(/^https?:\/\/[^/]+(\/.*)?$/i);
    if (m) path = m[1] || '/';
    path = path.split('?')[0];
    if (!path.startsWith('/')) path = '/' + path;
    path = path.replace(/\{\{(\w+)\}\}/g, '{$1}'); // Postman {{name}} -> chi {name}
    return path || '/';
  }

  // A SOAP request item's real dispatch key is its SOAPAction header (or,
  // failing that, the envelope body's own root element name — see
  // mock.Definition.OperationName's doc comment) — creating the mock as
  // protocolType "rest" (the old, only, behavior here) meant it could
  // never actually match a SOAP client's request: SOAP mocks all share one
  // POST endpoint, disambiguated by SOAPAction/operationName, neither of
  // which a "rest" mock has any concept of.
  function headerValue(headers, key) {
    const h = (headers ?? []).find((hh) => hh.key?.trim().toLowerCase() === key.toLowerCase());
    return h?.value ?? '';
  }

  // The Auth tab's fields never land in request.headers — applyAuth (Go,
  // internal/apiclient/runner.go) sets the Authorization/API-key header
  // itself at send time, after the manual Headers loop. That's correct for
  // sending, but it meant the Headers tab never showed what was actually
  // going out — e.g. Basic Auth's real Base64-encoded value was invisible
  // until you inspected the raw request some other way. Mirrors that same
  // encoding here, live, as a read-only preview row.
  function computedAuthHeader(auth) {
    if (!auth || auth.type === 'none') return null;
    if (auth.type === 'bearer') {
      if (!auth.token) return null;
      return { key: 'Authorization', value: `Bearer ${auth.token}` };
    }
    if (auth.type === 'basic') {
      if (!auth.username && !auth.password) return null;
      let encoded = '';
      try {
        encoded = btoa(unescape(encodeURIComponent(`${auth.username ?? ''}:${auth.password ?? ''}`)));
      } catch {
        encoded = '(unable to encode — non-Latin1 characters?)';
      }
      return { key: 'Authorization', value: `Basic ${encoded}` };
    }
    if (auth.type === 'apikey' && auth.addTo === 'header') {
      if (!auth.keyName) return null;
      return { key: auth.keyName, value: auth.keyValue ?? '' };
    }
    return null;
  }

  // applyAuth (Go) sets the Authorization/API-key header via req.Header.Set
  // AFTER the manual Headers loop, so it always wins on a same-name
  // collision — a manually typed "Authorization" row next to a configured
  // Auth tab is dead weight that's silently never sent. Flag that row so it
  // reads as overridden instead of implying both are live.
  function headerOverriddenByAuth(header, auth) {
    const computed = computedAuthHeader(auth);
    if (!computed || !header.key) return false;
    return header.key.trim().toLowerCase() === computed.key.toLowerCase();
  }

  // The envelope's own Body root element is usually the real operation
  // name (SoapUI/WSDL-imported request bodies alike) — falls back to the
  // item's own name (already the operation name for a WSDL import, see
  // wsdlOperationsToItems) if the body doesn't parse as XML or has no
  // Body element to look at.
  function extractSoapOperationName(item) {
    const fallback = item.name;
    try {
      const doc = new DOMParser().parseFromString(item.request?.body || '', 'text/xml');
      if (doc.querySelector('parsererror')) return fallback;
      const bodyEl = Array.from(doc.getElementsByTagName('*')).find((el) => el.localName === 'Body');
      return bodyEl?.children?.[0]?.localName || fallback;
    } catch {
      return fallback;
    }
  }

  // Mirrors wsdl.go's stubSOAPEnvelope on the Go side — same "obviously a
  // stub" shape, so a freshly created SOAP mock at least has a plausible
  // envelope to edit rather than the REST-mock default `{}` (not valid XML
  // at all for a SOAP response).
  function stubSoapMockResponse(operationName) {
    return `<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <${operationName}Response xmlns="urn:airmock:stub">
      <result>stub response for ${operationName}</result>
    </${operationName}Response>
  </soap:Body>
</soap:Envelope>`;
  }

  // Builds the protocol-specific fields (protocolType plus, for SOAP,
  // soapAction/operationName and an XML stub instead of `{}`) shared by
  // both createMockFromItem and createMocksFromCollection, so a SOAP
  // request mocked either one-at-a-time or in bulk gets the same correct
  // treatment.
  // A saved Example (Postman's own "response[]" per request, imported by
  // apiclient/postman — previously dropped entirely, now carried over) is
  // real recorded behavior for this exact request, so it's a far better
  // default mock response than a generic `{}`/stub envelope whenever one
  // is available — that's the whole point of a collection commonly
  // carrying a documented happy path plus every error case as saved
  // examples. Prefers a 2xx example (the "normal" case a fresh mock should
  // return by default) over whichever happened to be saved first, falling
  // back to the first example of any status when there's no 2xx at all.
  function pickDefaultExample(item) {
    const examples = item?.examples ?? [];
    if (examples.length === 0) return null;
    return examples.find((e) => e.statusCode >= 200 && e.statusCode < 300) ?? examples[0];
  }

  function mockFieldsForItem(item) {
    const example = pickDefaultExample(item);
    if (!isSoapRequest(item)) {
      return {
        protocolType: 'rest',
        response: example
          ? { statusCode: example.statusCode || 200, bodyTemplate: example.body ?? '{}', headers: example.headers }
          : { statusCode: 200, bodyTemplate: '{}' },
      };
    }
    const operationName = extractSoapOperationName(item);
    return {
      protocolType: 'soap',
      soapAction: headerValue(item.request.headers, 'soapaction') || soapContentTypeAction(headerValue(item.request.headers, 'content-type')),
      operationName,
      response: example
        ? { statusCode: example.statusCode || 200, bodyTemplate: example.body || stubSoapMockResponse(operationName), headers: example.headers }
        : { statusCode: 200, bodyTemplate: stubSoapMockResponse(operationName) },
    };
  }

  // A request still carrying one of the app's placeholder names ("New request",
  // "New SOAP request", ...) says nothing about the mock made from it, so name
  // that mock after what it does instead.
  const PLACEHOLDER_REQUEST_NAME = /^new (soap |ws |websocket )?request( \(\d+\))?$/i;
  function mockNameForItem(item, pathPattern) {
    const n = (item?.name ?? '').trim();
    if (n && !PLACEHOLDER_REQUEST_NAME.test(n)) return n;
    return `${item?.request?.method ?? 'GET'} ${pathPattern}`;
  }

  async function createMockFromItem(item) {
    if (!item?.request) return;
    const pathPattern = urlToPathPattern(item.request.url);
    const useLiveResponse = response && item === activeItem && !response.error;
    const { response: defaultResponse, ...protocolFields } = mockFieldsForItem(item);
    try {
      await api.createMock({
        name: mockNameForItem(item, pathPattern),
        method: item.request.method,
        pathPattern,
        enabled: true,
        ...protocolFields,
        response: {
          statusCode: useLiveResponse ? response.statusCode : defaultResponse.statusCode,
          bodyTemplate: useLiveResponse ? response.body : defaultResponse.bodyTemplate,
          headers: useLiveResponse ? undefined : defaultResponse.headers,
        },
      });
      showToast(`Mock created: ${item.request.method} ${pathPattern} — see the Mocks page`, 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Reuses a Mock Project already named after this collection if one
  // exists (e.g. from a previous "Mock this collection" run, or one the
  // user made by hand), rather than creating a duplicate every time.
  async function findOrCreateProjectForCollection(collection) {
    const projects = (await api.listMockProjects()) ?? [];
    const existing = projects.find((p) => p.name.trim().toLowerCase() === collection.name.trim().toLowerCase());
    if (existing) return existing;
    return api.createMockProject({ name: collection.name });
  }

  async function createMocksFromCollection(collection) {
    const requestItems = flattenRequestItems(collection.items);
    if (requestItems.length === 0) {
      showToast('No requests with a URL in this collection', 'err');
      return;
    }
    if (!confirm(`Create ${requestItems.length} mock${requestItems.length === 1 ? '' : 's'} from "${collection.name}", grouped under a "${collection.name}" project?`)) return;

    let project;
    try {
      project = await findOrCreateProjectForCollection(collection);
    } catch (e) {
      showToast(e.message, 'err');
      return;
    }

    let created = 0;
    for (const it of requestItems) {
      const pathPattern = urlToPathPattern(it.request.url);
      try {
        await api.createMock({
          name: mockNameForItem(it, pathPattern),
          method: it.request.method,
          pathPattern,
          enabled: true,
          projectId: project.id,
          ...mockFieldsForItem(it),
        });
        created++;
      } catch (e) {
        showToast(`Failed for "${it.name}": ${e.message}`, 'err');
      }
    }
    showToast(`Created ${created} of ${requestItems.length} mocks in project "${project.name}" — see the Mocks page`, 'ok');
  }

  // --- response examples ---

  function openSaveExampleForm() {
    newExampleName = `Example ${(activeItem?.examples?.length ?? 0) + 1}`;
    showSaveExampleForm = true;
  }

  // "Save as example" from the item's own row/context menu in the tree —
  // previously only reachable after opening the item's tab AND sending a
  // request yourself, buried in the response panel alongside Copy/Clear.
  // Still needs an actual response to save (an example is a snapshot of
  // one), so this switches to the item's tab first — cheap and a no-op if
  // it's already the active one — and uses whatever response that tab
  // already has cached from a previous send, rather than sending a new
  // request itself. await tick() so activeTabId's own reactive block (see
  // its comment above) has actually repopulated `response` from
  // responseCache before this checks it.
  async function saveExampleFromItemRow(collection, it) {
    openItemTab(collection.id, it.id);
    await tick();
    if (!response) {
      showToast('Send this request first — there\'s no response yet to save as an example', 'err');
      return;
    }
    openSaveExampleForm();
  }

  async function saveExample() {
    if (!activeItem || !response || !newExampleName.trim()) return;
    const headers = {};
    for (const [k, vs] of Object.entries(response.headers ?? {})) headers[k] = vs?.[0] ?? '';
    const example = {
      id: randomId(),
      name: newExampleName.trim(),
      statusCode: response.statusCode,
      headers,
      body: response.body ?? '',
    };

    if (!activeTab?.collectionId) {
      activeTab.draft.examples = [...(activeTab.draft.examples ?? []), example];
      openTabs = openTabs; // trigger reactivity for the nested mutation
      showSaveExampleForm = false;
      showToast('Example saved (persists once you save this request to a collection)', 'ok');
      return;
    }

    if (!activeCollection) return;
    const itemId = activeItem.id;
    try {
      await withFreshCollection(activeCollection.id, (items) =>
        mapItemById(items, itemId, (it) => ({ ...it, examples: [...(it.examples ?? []), example] })),
      );
      // Examples save immediately (unlike the rest of the request, which
      // waits for the Save button) — mirror the same append into the open
      // draft so it shows up right away instead of only after some other
      // action happens to loadAll() again.
      if (activeTab.draft) activeTab.draft.examples = [...(activeTab.draft.examples ?? []), example];
      showSaveExampleForm = false;
      showToast('Example saved', 'ok');
      await loadAll();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Viewing an example must NOT touch the live `response` — the response
  // panel already prefers viewingExample over it when both are set (see
  // `{@const shown = viewingExample ?? response}`), so response only needs
  // to stay around underneath, ready for "Back to live response" to
  // reveal again. Nulling it out here used to mean that button had
  // nothing left to go back to — the live response was gone for good
  // until the request was sent again.
  function viewExample(ex) {
    viewingExample = ex;
  }

  async function removeExample(ex) {
    if (!confirm(`Delete example "${ex.name}"?`)) return;

    if (!activeTab?.collectionId) {
      activeTab.draft.examples = (activeTab.draft.examples ?? []).filter((e) => e.id !== ex.id);
      openTabs = openTabs;
      if (viewingExample?.id === ex.id) viewingExample = null;
      return;
    }

    const itemId = activeItem.id;
    try {
      await withFreshCollection(activeCollection.id, (items) =>
        mapItemById(items, itemId, (it) => ({ ...it, examples: (it.examples ?? []).filter((e) => e.id !== ex.id) })),
      );
      if (activeTab.draft) activeTab.draft.examples = (activeTab.draft.examples ?? []).filter((e) => e.id !== ex.id);
      if (viewingExample?.id === ex.id) viewingExample = null;
      await loadAll();
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
</script>

<div class="head-row" class:mobile-hide={mobileShowRequestPane}>
  <div>
    <h1>Collections</h1>
    <p class="sub">Build and send requests, organize them into collections, import/export curl and Collection v2.1.</p>
  </div>
  <div class="head-actions">
    <button class="btn btn-ghost" on:click={() => (showEnvEditor ? cancelEnvEditor() : (showEnvEditor = true))}>
      {showEnvEditor ? 'Cancel' : 'Environments'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showPostmanImport ? cancelPostmanImport() : (showPostmanImport = true))}>
      {showPostmanImport ? 'Cancel' : 'Import Collection'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showSoapUIImport ? cancelSoapUIImport() : (showSoapUIImport = true))}>
      {showSoapUIImport ? 'Cancel' : 'Import SOAP Project XML'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showWsdlImport ? cancelWsdlImport() : (showWsdlImport = true))}>
      {showWsdlImport ? 'Cancel' : 'Import WSDL'}
    </button>
    <button class="btn btn-ghost" on:click={() => wsdlFolderInput.click()} title="Pick the WSDL's own folder — its .wsdl file plus every .xsd schema file anywhere in it (e.g. a Schemas/ subfolder) are loaded together">
      Import WSDL from folder
    </button>
    <input aria-label="Choose a file"
      bind:this={wsdlFolderInput}
      type="file"
      webkitdirectory
      multiple
      hidden
      on:change={onWsdlFolderFilesChosen}
    />
    <button class="btn btn-ghost" disabled={bulkImportBusy} on:click={() => bulkImportInput.click()} title="Pick a folder — every .json collection export in it is imported">
      {bulkImportBusy ? 'Importing…' : 'Import folder'}
    </button>
    <input aria-label="Choose a file"
      bind:this={bulkImportInput}
      type="file"
      webkitdirectory
      multiple
      hidden
      on:change={onBulkImportFilesChosen}
    />
    <button class="btn btn-primary" on:click={openNewCollectionForm}>+ New collection</button>
  </div>
</div>

<div class="workspace-bar" class:mobile-hide={mobileShowRequestPane}>
  <label class="workspace-label">
    Workspace
    <select value={activeWorkspaceId} on:change={(e) => switchWorkspace(e.target.value)}>
      {#each workspaces as w (w.id)}<option value={w.id}>{w.name}{w.locked ? ' 🔒' : ''}</option>{/each}
    </select>
  </label>
  <input aria-label="Search collections, folders, requests, URLs, methods" type="text" class="search-input" bind:value={searchQuery} placeholder="Search collections, folders, requests, URLs, methods…" />
  <button class="btn btn-ghost small" on:click={() => (showNewWorkspaceForm = !showNewWorkspaceForm)}>
    {showNewWorkspaceForm ? 'Cancel' : '+ New workspace'}
  </button>
  {#if activeWorkspaceId !== DEFAULT_WORKSPACE_ID}
    <button
      class="btn btn-ghost small"
      on:click={() => (showLockModal = true)}
      title={activeWorkspace?.locked ? "Change this workspace's PIN/password, or remove its lock" : 'Require a PIN/password before mocks mapped to this workspace can be edited or deleted'}
    >
      {activeWorkspace?.locked ? '🔒 Manage lock' : '🔓 Lock workspace'}
    </button>
  {/if}
  {#if workspaces.length > 1 && activeWorkspaceId !== DEFAULT_WORKSPACE_ID}
    <button class="btn btn-ghost small remove" on:click={() => removeWorkspace(activeWorkspaceId)}>Delete workspace</button>
  {/if}
</div>

{#if favoritedItems.length}
  <div class="favorites-bar" class:mobile-hide={mobileShowRequestPane}>
    <span class="favorites-label">★ Favorites</span>
    {#each favoritedItems as f (f.item.id)}
      <button
        class="chip chip-tls favorite-chip"
        title={`${f.collectionName} → ${f.item.name}`}
        on:click={() => openItemTab(f.collectionId, f.item.id)}
      >{f.item.name}</button>
    {/each}
  </div>
{/if}

{#if showNewWorkspaceForm}
  <div class="card form-card">
    <div class="field-row">
      <label class="flex-1">
        Workspace name
        <input
          type="text"
          bind:value={newWorkspaceName}
          placeholder="e.g. Client B project"
          on:keydown={(e) => e.key === 'Enter' && createWorkspace()}
        />
      </label>
      <button class="btn btn-primary" on:click={createWorkspace}>Create</button>
    </div>
  </div>
{/if}

{#if showEnvEditor}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={cancelEnvEditor} title="Cancel" aria-label="Cancel">−</button>
    <div class="head-row">
      <h3>Environments</h3>
      <div class="head-actions">
        <button class="btn btn-ghost" on:click={() => (showPostmanEnvImport ? cancelPostmanEnvImport() : (showPostmanEnvImport = true))}>
          {showPostmanEnvImport ? 'Cancel' : 'Import Environment'}
        </button>
        <button class="btn btn-ghost" on:click={newEnvironment}>+ New environment</button>
      </div>
    </div>
    {#if showPostmanEnvImport}
      <div class="card form-card import-card">
        <button class="card-minus" on:click={cancelPostmanEnvImport} title="Cancel" aria-label="Cancel">−</button>
        <label>
          Environment JSON
          <ImportSource bind:content={postmanEnvImportJson} rows={6} placeholder="Paste an environment JSON export…" />
        </label>
        <button class="btn btn-primary" on:click={importPostmanEnvironment}>Import</button>
      </div>
    {/if}
    {#each environments as env (env.id)}
      <div class="env-card">
        <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
        <div
          class="env-summary"
          on:click={() => toggleEnvExpand(env.id)}
        >
          <button type="button" class="expand-arrow" class:expanded={expandedEnvIds.has(env.id)} aria-expanded={!!(expandedEnvIds.has(env.id))} aria-label="Show or hide details" on:click|stopPropagation={() => toggleEnvExpand(env.id)}>▸</button>
          <!-- The radio inside already provides full keyboard operability;
               this handler only stops the click from also toggling the
               parent row's expand/collapse, so no separate keyboard
               handler applies here. -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
          <label class="rule-required" on:click|stopPropagation>
            <input type="radio" name="activeEnv" checked={activeEnvId === env.id} on:change={() => selectActiveEnv(env.id)} />
            active
          </label>
          {#if renamingEnvId === env.id}
            <input aria-label="Environment name"
              type="text"
              class="env-rename-input"
              bind:value={renameEnvName}
              on:click|stopPropagation
              on:keydown={(e) => { if (e.key === 'Enter') saveRenameEnv(env); else if (e.key === 'Escape') cancelRename(); }}
            />
            <button class="btn btn-ghost small" on:click|stopPropagation={() => saveRenameEnv(env)}>Save</button>
          {:else}
            <span class="env-name">{env.name}</span>
            <button class="btn btn-ghost small" on:click|stopPropagation={() => startRenameEnv(env)} title="Rename" aria-label="Rename">✏️</button>
          {/if}
          <span class="chip chip-stat">{Object.keys(env.variables ?? {}).length} vars</span>
          <button class="btn btn-ghost small" on:click|stopPropagation={() => duplicateEnvironment(env)}>Duplicate</button>
          <button class="btn btn-ghost small remove" on:click|stopPropagation={() => removeEnvironment(env.id)}>Delete</button>
        </div>
        {#if expandedEnvIds.has(env.id)}
          <div class="env-detail">
            {#each Object.entries(env.variables ?? {}) as [k, v]}
              {#if editingEnvVar?.envId === env.id && editingEnvVar.key === k}
                <div class="field-row kv-row">
                  <input aria-label="Variable name"
                    type="text"
                    class="kv-edit-input"
                    bind:value={editEnvVarDraft.key}
                    on:keydown={(e) => onEditEnvVarKeydown(e, env, k)}
                  />
                  <input aria-label="Variable value"
                    type="text"
                    class="kv-edit-input"
                    bind:value={editEnvVarDraft.value}
                    on:keydown={(e) => onEditEnvVarKeydown(e, env, k)}
                  />
                  <button class="btn btn-ghost small" on:click={() => saveEditEnvVar(env, k)}>Save</button>
                  <button class="btn btn-ghost small" on:click={cancelEditEnvVar}>Cancel</button>
                </div>
              {:else}
                <div class="field-row kv-row">
                  <span class="kv-key">{k}</span>
                  <span class="kv-value">{v}</span>
                  <button class="btn btn-ghost small" on:click={() => startEditEnvVar(env, k, v)} title="Edit" aria-label="Edit">✏️</button>
                  <button class="btn btn-ghost" on:click={() => removeEnvVar(env, k)}>×</button>
                </div>
              {/if}
            {/each}
            <div class="field-row">
              <input aria-label="New variable name" type="text" placeholder="key" bind:value={newEnvVarKeyByEnv[env.id]} />
              <input aria-label="New variable value" type="text" placeholder="value" bind:value={newEnvVarValueByEnv[env.id]} />
              <button class="btn btn-ghost" on:click={() => addEnvVar(env)}>Add var</button>
            </div>
          </div>
        {/if}
      </div>
    {/each}
    {#if environments.length === 0}<p class="sub">No environments yet.</p>{/if}
  </div>
{/if}

{#if showPostmanImport}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={cancelPostmanImport} title="Cancel" aria-label="Cancel">−</button>
    <label>
      Collection v2.1 JSON
      <ImportSource bind:content={postmanImportJson} rows={6} placeholder="Paste a Collection v2.1 JSON export…" />
    </label>
    <button class="btn btn-primary" on:click={importPostman}>Import</button>
  </div>
{/if}

{#if showSoapUIImport}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={cancelSoapUIImport} title="Cancel" aria-label="Cancel">−</button>
    <label>
      SOAP Project XML
      <ImportSource bind:content={soapUIImportXml} rows={6} placeholder="Paste a SOAP Project XML export…" />
    </label>
    <p class="sub">Each TestSuite becomes a folder, each TestCase a nested folder, each SOAP/REST request step a request — one per project TestSuite/TestCase/TestStep.</p>
    <button class="btn btn-primary" on:click={importSoapUI}>Import</button>
  </div>
{/if}

{#if showWsdlImport}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={cancelWsdlImport} title="Cancel" aria-label="Cancel">−</button>
    <label>
      WSDL document
      <ImportSource bind:content={wsdlImportContent} rows={6} placeholder="Paste a WSDL .wsdl/.xml document…" />
    </label>
    <label>
      Collection name <span class="sub">(optional — auto-detected from the WSDL's own name, or its &lt;service&gt; name, if left blank)</span>
      <input type="text" bind:value={wsdlImportName} placeholder="Leave blank to auto-detect" />
    </label>
    <label>
      Endpoint URL <span class="sub">(optional — defaults to the WSDL's own &lt;service&gt; address, if it declares one)</span>
      <input type="text" bind:value={wsdlImportURL} placeholder="https://example.com/soap or http://localhost:8081/your-mock-path" />
    </label>
    {#if wsdlExtraSchemas.length}
      <p class="sub">
        Schema files attached: {wsdlExtraSchemas.map((s) => s.name).join(', ')}
        <button class="btn btn-ghost small" on:click={() => (wsdlExtraSchemas = [])}>Remove</button>
      </p>
    {/if}
    <p class="sub">
      One request per portType operation, with Content-Type/SOAPAction headers set and the full nested request body when the WSDL's schema is known — inline, or via "Import WSDL from folder" for one that splits its schema out into separate .xsd files — otherwise a flat placeholder per expected parameter.
    </p>
    <button class="btn btn-primary" on:click={importWsdlCollection}>Import</button>
  </div>
{/if}

{#if showCurlImportModal}
  <Modal title="Import from curl" onClose={closeCurlImportModal}>
    <p class="sub">
      Paste a full curl command — it's parsed and validated before anything is added.
      {#if !curlImportTargetCollectionId}Opens as a new unsaved tab; save it into a collection afterwards.{/if}
    </p>
    <textarea aria-label="curl command"
      rows="6"
      class="curl-modal-textarea"
      bind:value={curlImportText}
      placeholder="curl -X POST https://example.com/orders -H 'Content-Type: application/json' --data-raw '...'"
    ></textarea>
    {#if curlImportError}<p class="test-error">{curlImportError}</p>{/if}
    <div class="modal-actions">
      <button class="btn btn-primary" on:click={confirmCurlImport}>Validate &amp; import</button>
      <button class="btn btn-ghost" on:click={closeCurlImportModal}>Cancel</button>
    </div>
  </Modal>
{/if}

{#if showNewCollectionForm}
  <Modal title="New collection" onClose={() => (showNewCollectionForm = false)}>
    <label>
      Collection name
      <input
        type="text"
        use:focusOnMount
        bind:value={newCollectionNameInput}
        placeholder="e.g. Billing API"
        on:keydown={(e) => e.key === 'Enter' && confirmNewCollection()}
      />
    </label>
    <div class="modal-actions">
      <button class="btn btn-primary" on:click={confirmNewCollection} disabled={!newCollectionNameInput.trim()}>Create</button>
      <button class="btn btn-ghost" on:click={() => (showNewCollectionForm = false)}>Cancel</button>
    </div>
  </Modal>
{/if}

{#if showSaveDraftPicker}
  <Modal title="Save request to a collection" onClose={() => (showSaveDraftPicker = false)}>
    <label>
      Collection
      <select bind:value={saveDraftTargetCollectionId}>
        <option value={NEW_COLLECTION_SENTINEL}>+ Create new collection…</option>
        {#each collections as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
      </select>
    </label>
    {#if saveDraftTargetCollectionId === NEW_COLLECTION_SENTINEL}
      <label>
        New collection name
        <input
          type="text"
          bind:value={saveDraftNewCollectionName}
          placeholder="My collection"
          on:keydown={(e) => e.key === 'Enter' && confirmSaveDraft()}
        />
      </label>
    {/if}
    <div class="modal-actions">
      <button class="btn btn-primary" on:click={confirmSaveDraft}>Save</button>
      <button class="btn btn-ghost" on:click={() => (showSaveDraftPicker = false)}>Cancel</button>
    </div>
  </Modal>
{/if}

{#if moveCopyTarget}
  <Modal title={moveCopyModalTitle} onClose={closeMoveCopyModal}>
    <label>
      Workspace
      <select
        bind:value={moveCopyWorkspaceId}
        on:change={() => moveCopyTarget.kind === 'item' && loadMoveCopyCollectionOptions()}
      >
        {#each workspaces as w (w.id)}<option value={w.id}>{w.name}</option>{/each}
      </select>
    </label>
    {#if moveCopyTarget.kind === 'item'}
      <label>
        Collection
        <select bind:value={moveCopyCollectionId}>
          <option value="">Select a collection…</option>
          {#each moveCopyWorkspaceCollections as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
        </select>
      </label>
      {#if moveCopyWorkspaceCollections.length === 0}
        <p class="sub">No collections in this workspace yet — create one there first.</p>
      {/if}
    {/if}
    <div class="modal-actions">
      <button class="btn btn-primary" on:click={confirmMoveCopy} disabled={moveCopyBusy}>
        {moveCopyBusy ? 'Working…' : moveCopyTarget.action === 'move' ? 'Move' : 'Copy'}
      </button>
      <button class="btn btn-ghost" on:click={closeMoveCopyModal}>Cancel</button>
    </div>
  </Modal>
{/if}

{#if showCurlPreview}
  <Modal title="Code snippet" width="720px" onClose={() => (showCurlPreview = false)}>
    <div class="section-tabs">
      {#each SNIPPET_LANGUAGES as lang}
        <button
          type="button"
          class:active={snippetLanguage === lang.id}
          on:click={() => viewCurl(lang.id)}
        >{lang.label}</button>
      {/each}
    </div>
    <pre class="curl-preview">{curlPreviewText}</pre>
    <div class="modal-actions">
      <button class="btn btn-primary" on:click={copyCurlPreview}>Copy</button>
      <button class="btn btn-ghost" on:click={() => (showCurlPreview = false)}>Close</button>
    </div>
  </Modal>
{/if}

{#if showRunResults}
  <Modal title="Run: {runningLabel}" onClose={() => (showRunResults = false)}>
    <div class="run-summary">
      {#if runInProgress}
        <span class="sub"><span class="loader-spin"></span>&nbsp; Running…</span>
      {/if}
      <span class="chip {!runInProgress && runPassedCount === runResults.length ? 'chip-run' : 'badge-warn'}">
        {runPassedCount}/{runResults.length} passed
      </span>
    </div>
    <div class="run-results-list">
      {#each runResults as r}
        <div class="run-result-row">
          <span class="chip {r.pass ? 'chip-run' : 'chip-stop'}">{r.pass ? 'PASS' : 'FAIL'}</span>
          <span class="badge {methodClass(r.method)}">{r.method}</span>
          <span class="run-result-name" title={r.name}>{r.name}</span>
          <span class="run-result-status">{r.status ?? '—'}</span>
          <span class="run-result-latency">{r.latencyMs}ms</span>
        </div>
        {#if r.error}<p class="run-result-error">{r.error}</p>{/if}
      {/each}
    </div>
    <div class="modal-actions">
      <button class="btn btn-ghost" on:click={() => (showRunResults = false)}>Close</button>
    </div>
  </Modal>
{/if}

{#snippet varPickerButton(key, el, currentValue, setValue)}
  <div class="var-picker-wrap">
    <button
      type="button"
      class="var-picker-btn"
      title="Insert environment variable"
      on:click|stopPropagation={() => toggleVarPicker(key)}
    >{'{{}}'}</button>
    {#if openVarPicker === key}
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="var-picker-menu" on:click|stopPropagation>
        {#if !activeEnv}
          <p class="sub var-picker-empty">Select an environment first.</p>
        {:else if Object.keys(activeEnv.variables ?? {}).length === 0}
          <p class="sub var-picker-empty">"{activeEnv.name}" has no variables yet.</p>
        {:else}
          {#each Object.keys(activeEnv.variables) as name}
            <button type="button" class="var-picker-item" on:click={() => insertVariable(el, currentValue, setValue, name)}>
              {'{{'}{name}{'}}'}
            </button>
          {/each}
        {/if}
        <p class="var-picker-group-label">Dynamic — no environment needed</p>
        {#each DYNAMIC_VAR_HINTS as hint}
          <button
            type="button"
            class="var-picker-item"
            title={hint.description}
            on:click={() => insertVariable(el, currentValue, setValue, '$' + hint.name)}
          >
            {'{{$'}{hint.name}{'}}'}
          </button>
        {/each}
      </div>
    {/if}
  </div>
{/snippet}

{#snippet loadTestSamplesTable(result)}
  {#if result.samples?.length}
    <div class="lt-samples">
      <div class="lt-samples-head">
        {#if result.request}<code class="lt-request-line">{result.request.method} {result.request.url}</code>{/if}
        <span class="sub">{result.samples.length} of {result.totalRequests} request{result.totalRequests === 1 ? '' : 's'} captured — click a row for the full response</span>
      </div>
      <div class="lt-samples-scroll">
        <table class="lt-samples-table">
          <thead><tr><th></th><th>#</th><th>Latency</th><th>Status</th><th>Error</th></tr></thead>
          <tbody>
            {#each result.samples as s (s.index)}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <tr class="lt-sample-row" on:click={() => toggleSample(s.index)}>
                <td class="lt-sample-arrow"><span class="expand-arrow" class:expanded={expandedSampleIndex === s.index}>▸</span></td>
                <td>{s.index + 1}</td>
                <td>{s.latencyMs}ms</td>
                <td>{#if s.error}<span class="chip badge-err">error</span>{:else}<span class="badge {statusClass(s.statusCode)}">{s.statusCode}</span>{/if}</td>
                <td class="lt-sample-error">{s.error || ''}</td>
              </tr>
              {#if expandedSampleIndex === s.index}
                <tr class="lt-sample-detail-row">
                  <td colspan="5">
                    {#if s.responseHeaders && Object.keys(s.responseHeaders).length}
                      <div class="lt-sample-detail-section">
                        <span class="lt-sample-detail-label">Response headers</span>
                        <div class="kv-rows-scroll">
                          {#each Object.entries(s.responseHeaders) as [k, v]}
                            <div class="resp-header-row"><span class="resp-header-key">{k}</span><span class="resp-header-val">{Array.isArray(v) ? v.join(', ') : v}</span></div>
                          {/each}
                        </div>
                      </div>
                    {/if}
                    <div class="lt-sample-detail-section">
                      <span class="lt-sample-detail-label">Response body</span>
                      <pre class="lt-sample-body">{s.responseBody || (s.error ? '' : '(empty)')}</pre>
                    </div>
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
{/snippet}

{#snippet itemTree(items, collection, depth)}
  {#each items as it (it.id)}
    {#if it.type === 'folder'}
      <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
      <div
        class="folder-row"
        style="padding-left: {depth * 16}px"
        on:click={() => toggleFolderExpand(it.id)}
        on:contextmenu={(e) => openItemMenuAtCursor(it.id, e)}
      >
        <div class="folder-row-main">
          <button type="button" class="expand-arrow" class:expanded={expandedFolderIds.has(it.id) || !!searchQuery.trim()} aria-expanded={!!(expandedFolderIds.has(it.id) || !!searchQuery.trim())} aria-label="Show or hide details" on:click|stopPropagation={() => toggleFolderExpand(it.id)}>▸</button>
          <span class="chip chip-stat">folder</span>
          {#if renamingItemId === it.id}
            <input aria-label="New name"
              type="text"
              class="rename-input"
              bind:value={renameItemName}
              on:click|stopPropagation
              on:keydown={(e) => { if (e.key === 'Enter') saveRenameItem(collection, it.id); else if (e.key === 'Escape') cancelRename(); }}
            />
            <button class="btn btn-ghost small" on:click|stopPropagation={() => saveRenameItem(collection, it.id)}>Save</button>
          {:else}
            <span class="item-name" title={it.name}>{it.name}</span>
            <span class="chip badge-warn">{countLeafItems(it.items ?? [])}</span>
          {/if}
        </div>
        <div class="row-actions">
          <div class="item-menu-wrap">
            <button
              class="btn btn-ghost small item-menu-btn"
              on:click|stopPropagation={(ev) => toggleItemMenu(it.id, ev)}
              title="More options"
              aria-label="More options"
            >⋮</button>
            {#if openItemMenuId === it.id}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div
                use:portal
                use:clampToViewport={itemMenuRect}
                class="coll-menu"
                style="top: {Math.round(itemMenuRect.bottom + 4)}px; left: {Math.round(itemMenuRect.right - 140)}px;"
                on:click|stopPropagation
              >
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); startRenameItem(it); }}>✏️ Rename</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); runItems(it.items ?? [], it.name, collection.id, collection.name); }}>▶ Run</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); addRequest(collection, it.id); }}>+ Request</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); addFolder(collection, it.id); }}>+ Folder</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); duplicateItem(collection, it.id); }}>Duplicate</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); openMoveCopyItem(collection, it.id, 'move'); }}>Move to…</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); openMoveCopyItem(collection, it.id, 'copy'); }}>Copy to…</button>
                <button class="coll-menu-item remove" on:click={() => { closeItemMenu(); removeItem(collection, it.id); }}>Delete</button>
              </div>
            {/if}
          </div>
        </div>
      </div>
      {#if expandedFolderIds.has(it.id) || !!searchQuery.trim()}
        {@render itemTree(it.items ?? [], collection, depth + 1)}
        {#if (it.items ?? []).length === 0}<p class="sub empty-items" style="padding-left: {(depth + 1) * 16}px">Empty folder.</p>{/if}
      {/if}
    {:else}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="item-row"
        style="padding-left: {depth * 16}px"
        class:active={activeTab?.collectionId === collection.id && activeTab?.itemId === it.id}
        on:contextmenu={(e) => openItemMenuAtCursor(it.id, e)}
      >
        {#if renamingItemId === it.id}
          <input aria-label="New name"
            type="text"
            class="rename-input"
            bind:value={renameItemName}
            on:keydown={(e) => { if (e.key === 'Enter') saveRenameItem(collection, it.id); else if (e.key === 'Escape') cancelRename(); }}
          />
          <button class="btn btn-ghost small" on:click={() => saveRenameItem(collection, it.id)}>Save</button>
        {:else}
          <button
            class="fav-star"
            class:favorited={favoriteIds.has(it.id)}
            title={favoriteIds.has(it.id) ? 'Remove from favorites' : 'Add to favorites'}
            aria-label="Toggle favorite"
            on:click|stopPropagation={() => toggleFavorite(it.id)}
          >{favoriteIds.has(it.id) ? '★' : '☆'}</button>
          <!-- A plain div (role="button"), not a real <button> — a real
               button's content box clips overflow in Chromium in a way a
               div's doesn't, silently defeating .item-name's wrapping fix
               below (the name would get hard-clipped mid-word instead of
               wrapping) even though the exact same CSS on the equivalent
               .folder-row-main (already a div) worked correctly. -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
          <button
            type="button"
            class="item-row-main"
            on:click={() => openItemTab(collection.id, it.id)}
          >
            {#if it.type === 'request'}<span class="badge {methodClass(it.request?.method)}">{it.request?.method}</span>{/if}
            {#if it.type === 'wsrequest'}<span class="chip chip-stat">WS</span>{/if}
            {#if isSoapRequest(it)}<span class="chip badge-info">SOAP</span>{/if}
            <span class="item-name" title={it.name}>{it.name}</span>
            {#if it.examples?.length}<span class="chip badge-warn">{it.examples.length} ex</span>{/if}
          </button>
          <div class="item-menu-wrap">
            <button
              class="btn btn-ghost small item-menu-btn"
              on:click|stopPropagation={(ev) => toggleItemMenu(it.id, ev)}
              title="More options"
              aria-label="More options"
            >⋮</button>
            {#if openItemMenuId === it.id}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div
                use:portal
                use:clampToViewport={itemMenuRect}
                class="coll-menu"
                style="top: {Math.round(itemMenuRect.bottom + 4)}px; left: {Math.round(itemMenuRect.right - 140)}px;"
                on:click|stopPropagation
              >
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); startRenameItem(it); }}>✏️ Rename</button>
                {#if it.type === 'request'}
                  <button class="coll-menu-item" on:click={() => { closeItemMenu(); saveExampleFromItemRow(collection, it); }}>Save as example</button>
                {/if}
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); duplicateItem(collection, it.id); }}>Duplicate</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); openMoveCopyItem(collection, it.id, 'move'); }}>Move to…</button>
                <button class="coll-menu-item" on:click={() => { closeItemMenu(); openMoveCopyItem(collection, it.id, 'copy'); }}>Copy to…</button>
                <button class="coll-menu-item remove" on:click={() => { closeItemMenu(); removeItem(collection, it.id); }}>Delete</button>
              </div>
            {/if}
          </div>
        {/if}
        <button class="remove-x" on:click={() => removeItem(collection, it.id)}>×</button>
      </div>
    {/if}
  {/each}
{/snippet}

<svelte:window
  on:click={() => { closeCollMenu(); closeItemMenu(); openVarPicker = ''; }}
  on:keydown={handleGlobalKeydown}
  on:scroll|capture={closeAllMenusOnScroll}
/>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else}
  <div class="workspace" class:mobile-show-request={mobileShowRequestPane}>
    <div class="collections-col">
      {#if selectedCollectionIds.size > 0}
        <div class="card bulk-bar">
          <label class="select-all-label">
            <input type="checkbox" checked={allVisibleCollectionsSelected} on:change={toggleSelectAllVisibleCollections} />
            Select all
          </label>
          <span class="sub">{selectedCollectionIds.size} selected</span>
          <button class="btn btn-primary small" on:click={exportSelectedCollections}>Export selected</button>
          <button class="btn btn-ghost small" on:click={clearCollectionSelection}>Clear selection</button>
        </div>
      {/if}
      {#each visibleCollectionsWithItems as { collection: c, items: filteredItems } (c.id)}
        <div id="collection-{c.id}" class="card coll-card-outer row-enter">
          <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
          <div
            class="coll-summary"
            on:click={() => toggleCollectionExpand(c.id)}
            on:contextmenu={(e) => openCollMenuAtCursor(c.id, e)}
          >
            <input
              type="checkbox"
              class="coll-select-box"
              checked={selectedCollectionIds.has(c.id)}
              on:change|stopPropagation={() => toggleCollectionSelect(c.id)}
              on:click|stopPropagation
              title="Select for bulk export"
              aria-label="Select {c.name} for bulk export"
            />
            <button type="button" class="expand-arrow" class:expanded={expandedCollectionIds.has(c.id) || !!searchQuery.trim()} aria-expanded={!!(expandedCollectionIds.has(c.id) || !!searchQuery.trim())} aria-label="Show or hide details" on:click|stopPropagation={() => toggleCollectionExpand(c.id)}>▸</button>
            {#if renamingCollectionId === c.id}
              <input aria-label="New name"
                type="text"
                class="rename-input"
                bind:value={renameCollectionName}
                on:click|stopPropagation
                on:keydown={(e) => { if (e.key === 'Enter') saveRenameCollection(c); else if (e.key === 'Escape') cancelRename(); }}
              />
              <button class="btn btn-ghost small" on:click|stopPropagation={() => saveRenameCollection(c)}>Save</button>
            {:else}
              <span class="coll-name-text" title={c.name}>{c.name}</span>
              <button class="btn btn-ghost small" on:click|stopPropagation={() => startRenameCollection(c)} title="Rename" aria-label="Rename">✏️</button>
            {/if}
            <span class="chip chip-stat">{countLeafItems(filteredItems)}</span>
            <div class="coll-menu-wrap">
              <button
                class="btn btn-ghost small coll-menu-btn"
                on:click|stopPropagation={(ev) => toggleCollMenu(c.id, ev)}
                title="More options"
                aria-label="More options"
              >⋮</button>
              {#if openCollMenuId === c.id}
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div
                  use:portal
                  use:clampToViewport={collMenuRect}
                  class="coll-menu"
                  style="top: {Math.round(collMenuRect.bottom + 4)}px; left: {Math.round(collMenuRect.right - 140)}px;"
                  on:click|stopPropagation
                >
                  <button class="coll-menu-item" on:click={() => { closeCollMenu(); runItems(c.items, c.name, c.id, c.name); }}>▶ Run collection</button>
                  <a class="coll-menu-item" href={collectionExportUrl(c.id)} download on:click={closeCollMenu}>Export</a>
                  <button class="coll-menu-item" on:click={() => { closeCollMenu(); duplicateCollection(c); }}>Duplicate</button>
                  {#if workspaces.length > 1}
                    <button class="coll-menu-item" on:click={() => { closeCollMenu(); openMoveCopyCollection(c, 'move'); }}>Move to workspace…</button>
                    <button class="coll-menu-item" on:click={() => { closeCollMenu(); openMoveCopyCollection(c, 'copy'); }}>Copy to workspace…</button>
                  {/if}
                  <button class="coll-menu-item remove" on:click={() => { closeCollMenu(); removeCollection(c.id); }}>Delete</button>
                </div>
              {/if}
            </div>
          </div>
          {#if expandedCollectionIds.has(c.id) || !!searchQuery.trim()}
            <div class="coll-detail">
              <div class="coll-items-scroll">
                {@render itemTree(filteredItems, c, 0)}
                {#if filteredItems.length === 0}
                  <p class="sub empty-items">{searchQuery.trim() ? `No matches for "${searchQuery}".` : 'No requests yet.'}</p>
                {/if}
              </div>
              <div class="coll-detail-actions">
                <button class="btn btn-ghost small" on:click={() => addRequest(c)}>+ Request</button>
                <button class="btn btn-ghost small" on:click={() => addRequest(c, '', 'soap')}>+ SOAP request</button>
                <button class="btn btn-ghost small" on:click={() => addFolder(c)}>+ Folder</button>
                <button class="btn btn-ghost small" on:click={() => openCurlImportModal(c.id)}>Import from curl</button>
                <button class="btn btn-ghost small" on:click={() => createMocksFromCollection(c)}>Mock this collection</button>
              </div>
            </div>
          {/if}
        </div>
      {/each}
      {#if collections.length === 0}
        <div class="card empty"><p>No collections yet — create one above.</p></div>
      {/if}
    </div>

    <div class="request-col">
      <button class="mobile-back-btn" on:click={() => (mobileShowRequestPane = false)}>← Collections</button>
      <div class="tab-bar-row">
        {#if openTabs.length > 0}
          <button class="tab-scroll-btn" on:click={(e) => onScrollArrowClick(e, -1)} title="Scroll tabs left (triple-click for the first tab)" aria-label="Scroll tabs left">‹</button>
          <div class="tab-bar" bind:this={tabBarEl} on:wheel={onTabBarWheel}>
            {#each openTabs as tab (tab.id)}
              {@const tItem = tab.draft ?? findItemById(collections.find((c) => c.id === tab.collectionId)?.items ?? [], tab.itemId)}
              {#if renamingTabId === tab.id}
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div id="tab-{tab.id}" class="tab-btn active renaming">
                  {#if tItem?.type === 'request'}<span class="badge small {methodClass(tItem.request?.method)}">{tItem.request?.method}</span>{/if}
                  <input aria-label="New name"
                    class="tab-rename-input"
                    type="text"
                    bind:value={renameTabName}
                    on:click|stopPropagation
                    on:keydown={(e) => onRenameTabKeydown(e, tab)}
                    on:blur={() => saveRenameTab(tab)}
                    use:autofocus
                  />
                </div>
              {:else}
                <button
                  id="tab-{tab.id}"
                  class="tab-btn"
                  class:active={tab.id === activeTabId}
                  on:click={() => (activeTabId = tab.id)}
                  on:dblclick|stopPropagation={() => startRenameTab(tab, tItem?.name)}
                  on:contextmenu={(e) => openTabContextMenu(e, tab)}
                >
                  {#if tItem?.type === 'request'}<span class="badge small {methodClass(tItem.request?.method)}">{tItem.request?.method}</span>{/if}
                  <span class="tab-name">{tItem?.name ?? '(deleted)'}</span>
                  {#if !tab.collectionId}<span class="unsaved-dot" title="Not saved to a collection yet"></span>{/if}
                  <span class="tab-close" on:click|stopPropagation={(e) => closeTab(tab.id, e)} role="presentation">×</span>
                </button>
              {/if}
            {/each}
          </div>
          <button class="tab-scroll-btn" on:click={(e) => onScrollArrowClick(e, 1)} title="Scroll tabs right (triple-click for the last tab)" aria-label="Scroll tabs right">›</button>
        {/if}
        <div class="new-tab-actions">
          <button class="btn btn-ghost small new-tab-btn" on:click={() => openBlankTab()} title="Start a request without picking a collection first">
            + New request
          </button>
          <button class="btn btn-ghost small new-tab-btn" on:click={() => openBlankTab('soap')} title="Start a SOAP request without picking a collection first">
            + SOAP request
          </button>
          <button class="btn btn-ghost small new-tab-btn" on:click={() => openCurlImportModal()} title="Start a new request by pasting a curl command">
            + From curl
          </button>
        </div>
      </div>

      {#if activeItem?.type === 'request'}
        <div class="card">
          <div class="field-row">
            <label class="method-field">
              Method
              <select bind:value={activeItem.request.method}>
                {#each METHODS as m}<option value={m}>{m}</option>{/each}
              </select>
            </label>
            <label class="flex-1">
              URL
              <div class="input-with-var-picker">
                <input type="text" bind:value={activeItem.request.url} bind:this={urlInputEl} placeholder="https://example.com/orders/{'{'}id{'}'}" />
                {@render varPickerButton('url', urlInputEl, activeItem.request.url, (v) => (activeItem.request.url = v))}
              </div>
            </label>
          </div>

          <div class="section-tabs">
            <button type="button" class:active={requestSection === 'headers'} on:click={() => (requestSection = 'headers')}>
              Headers{#if activeItem.request.headers?.length}<span class="section-count">{activeItem.request.headers.length}</span>{/if}
            </button>
            <button type="button" class:active={requestSection === 'query'} on:click={() => (requestSection = 'query')}>
              Query{#if activeItem.request.query?.length}<span class="section-count">{activeItem.request.query.length}</span>{/if}
            </button>
            <button type="button" class:active={requestSection === 'auth'} on:click={() => (requestSection = 'auth')}>
              Auth{#if activeItem.request.auth?.type && activeItem.request.auth.type !== 'none'}<span class="section-count">●</span>{/if}
            </button>
            <button type="button" class:active={requestSection === 'body'} on:click={() => (requestSection = 'body')}>
              Body{#if activeItem.request.bodyMode === 'none' ? false : (activeItem.request.bodyMode === 'urlencoded' || activeItem.request.bodyMode === 'formdata') ? activeItem.request.formFields?.some((f) => f.key) : !!activeItem.request.body?.trim()}<span class="section-count">●</span>{/if}
            </button>
            <button type="button" class:active={requestSection === 'extract'} on:click={() => (requestSection = 'extract')}>
              Tests{#if activeItem.request.extractRules?.length || activeItem.request.expectedStatus}<span class="section-count">●</span>{/if}
            </button>
            <button type="button" class:active={requestSection === 'notes'} on:click={() => (requestSection = 'notes')}>
              Notes{#if activeItem.description}<span class="section-count">●</span>{/if}
            </button>
          </div>

          {#if requestSection === 'headers'}
            <div class="section-panel">
              {#if computedAuthHeader(activeItem.request.auth)}
                {@const authHeader = computedAuthHeader(activeItem.request.auth)}
                <div class="field-row kv-row auto-header-row" title="Computed live from the Auth tab's current values — sent with this request, but not editable here.">
                  <input aria-label="Header name" type="text" value={authHeader.key} readonly />
                  <input aria-label="Header value" type="text" value={authHeader.value} readonly />
                  <span class="auto-header-badge">auto · Auth</span>
                </div>
              {/if}
              <div class="kv-rows-scroll">
                {#each activeItem.request.headers ?? [] as h, i (h)}
                  <div
                    class="field-row kv-row"
                    class:row-disabled={h.disabled}
                    class:row-overridden={!h.disabled && headerOverriddenByAuth(h, activeItem.request.auth)}
                    title={!h.disabled && headerOverriddenByAuth(h, activeItem.request.auth) ? 'Overridden by the Auth tab — that value is sent instead of this one.' : undefined}
                  >
                    <Suggestions placeholder="key" options={COMMON_HEADER_NAMES} bind:value={h.key} />
                    <Suggestions placeholder="value" options={COMMON_CONTENT_TYPES} bind:value={h.value} />
                    {#if !h.disabled && headerOverriddenByAuth(h, activeItem.request.auth)}
                      <span class="auto-header-badge">overridden</span>
                    {/if}
                    <label class="rule-required"><input type="checkbox" checked={h.disabled} on:change={(e) => (h.disabled = e.currentTarget.checked)} /> off</label>
                    <button class="btn btn-ghost" on:click={() => removeHeaderRow(i)}>×</button>
                  </div>
                {/each}
              </div>
              <button class="btn btn-ghost small" on:click={addHeaderRow}>+ Header</button>
            </div>
          {:else if requestSection === 'query'}
            <div class="section-panel">
              <div class="kv-rows-scroll">
                {#each activeItem.request.query ?? [] as q, i (q)}
                  <div class="field-row kv-row" class:row-disabled={q.disabled}>
                    <Suggestions placeholder="key" options={COMMON_QUERY_PARAMS} bind:value={q.key} />
                    <input aria-label="Query parameter value" type="text" placeholder="value" bind:value={q.value} />
                    <label class="rule-required"><input type="checkbox" checked={q.disabled} on:change={(e) => (q.disabled = e.currentTarget.checked)} /> off</label>
                    <button class="btn btn-ghost" on:click={() => removeQueryRow(i)}>×</button>
                  </div>
                {/each}
              </div>
              <button class="btn btn-ghost small" on:click={addQueryRow}>+ Query param</button>
            </div>
          {:else if requestSection === 'auth'}
            <div class="section-panel">
              <label class="auth-type-field">
                Type
                <select bind:value={activeItem.request.auth.type}>
                  <option value="none">No Auth</option>
                  <option value="bearer">Bearer Token</option>
                  <option value="basic">Basic Auth</option>
                  <option value="apikey">API Key</option>
                </select>
              </label>
              {#if activeItem.request.auth.type === 'bearer'}
                <label>
                  Token
                  <input type="text" bind:value={activeItem.request.auth.token} placeholder="{'{{'}apiToken{'}}'}" />
                </label>
              {:else if activeItem.request.auth.type === 'basic'}
                <div class="field-row">
                  <label>Username <input type="text" bind:value={activeItem.request.auth.username} /></label>
                  <label>Password <input type="text" bind:value={activeItem.request.auth.password} /></label>
                </div>
              {:else if activeItem.request.auth.type === 'apikey'}
                <div class="field-row">
                  <label>Key <input type="text" bind:value={activeItem.request.auth.keyName} placeholder="X-Api-Key" /></label>
                  <label>Value <input type="text" bind:value={activeItem.request.auth.keyValue} /></label>
                  <label class="add-to-field">
                    Add to
                    <select bind:value={activeItem.request.auth.addTo}>
                      <option value="header">Header</option>
                      <option value="query">Query param</option>
                    </select>
                  </label>
                </div>
              {:else}
                <p class="sub">No authorization will be added to this request.</p>
              {/if}
              <label class="rule-required insecure-field">
                <input type="checkbox" bind:checked={activeItem.request.insecure} />
                <span class="label-text">Skip TLS certificate verification<InfoTooltip text="Like curl's -k/--insecure — lets this request reach a local or self-signed HTTPS endpoint that would otherwise fail with a certificate error. Only affects this one request." /></span>
              </label>
              <label class="client-cert-field">
                <span class="label-text">Client certificate (mTLS)<InfoTooltip text="Presents a certificate from the Certificates page as this request's TLS client certificate — for calling an endpoint that requires mutual TLS. Only certificates with a private key can be picked here." /></span>
                <select bind:value={activeItem.request.clientCertId}>
                  <option value="">None</option>
                  {#each clientCertOptions as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
                </select>
              </label>
              <div class="field-row read-timeout-row">
                <label class="read-timeout-field">
                  <span class="label-text">Read timeout (seconds)<InfoTooltip text="How long to wait for this request before giving up — defaults to 30s if left blank. Raise it for a legitimately slow endpoint (a report generator, a long-poll), or lower it to fail fast against one you already know is slow or hung. Capped at 300s." /></span>
                  <input type="number" min="1" max="300" placeholder="30" bind:value={activeItem.request.readTimeoutSecs} />
                </label>
              </div>
            </div>
          {:else if requestSection === 'extract'}
            <div class="section-panel">
              <label class="expected-status-field">
                <span class="label-text">Expected status (optional)<InfoTooltip text="If set, the Collection Runner (and this request's own pass/fail indicator) requires an exact match to pass. Left blank, any 2xx/3xx response with no transport error counts as a pass." /></span>
                <input type="number" placeholder="e.g. 200" bind:value={activeItem.request.expectedStatus} />
              </label>

              <p class="sub">
                After this request runs (a single send, or as part of a Collection Runner pass), each rule below pulls
                one field out of the JSON response body and writes it into the active environment — e.g. a login
                request extracting <code>data.token</code> into a <code>authToken</code> variable so later requests'
                <code>&#123;&#123;authToken&#125;&#125;</code> placeholders resolve automatically.
              </p>
              {#each activeItem.request.extractRules ?? [] as rule, i (rule)}
                <div class="field-row kv-row">
                  <input aria-label="Response path" type="text" placeholder="response path, e.g. data.token" bind:value={rule.path} />
                  <input aria-label="Variable to set" type="text" placeholder="variable name, e.g. authToken" bind:value={rule.variable} />
                  <button class="btn btn-ghost" on:click={() => removeExtractRuleRow(i)}>×</button>
                </div>
              {/each}
              <button class="btn btn-ghost small" on:click={addExtractRuleRow}>+ Extract rule</button>
            </div>
          {:else if requestSection === 'notes'}
            <div class="section-panel">
              <label>
                Description
                <textarea rows="6" placeholder="What this request is for, quirks to remember, anything worth writing down…" bind:value={activeItem.description}></textarea>
              </label>
            </div>
          {:else}
            <div class="section-panel">
              <div class="body-toolbar">
                <select aria-label="Body type" class="body-mode-select" bind:value={activeItem.request.bodyMode}>
                  <option value="">Raw</option>
                  <option value="none">None</option>
                  <option value="urlencoded">Form URL-Encoded</option>
                  <option value="formdata">Form Data</option>
                </select>
                {#if activeItem.request.bodyMode === ''}
                  <select aria-label="Raw body format" class="raw-type-select" bind:value={activeItem.request.rawContentType}>
                    <option value="json">JSON</option>
                    <option value="text">Text</option>
                    <option value="xml">XML</option>
                    <option value="html">HTML</option>
                  </select>
                  {#if activeItem.request.rawContentType !== 'text'}
                    <button class="btn btn-ghost small" on:click={beautifyBody}>Beautify {activeItem.request.rawContentType.toUpperCase()}</button>
                  {/if}
                  {@render varPickerButton('body', bodyTextareaEl, activeItem.request.body, (v) => (activeItem.request.body = v))}
                  {#if bodyFormatIssue}
                    <span class="json-badge invalid">
                      Invalid {bodyFormatKind.toUpperCase()}{bodyFormatIssue.line ? ` (line ${bodyFormatIssue.line})` : ''}: {bodyFormatIssue.message}
                    </span>
                  {:else if bodyFormatValid}
                    <span class="json-badge valid">Valid {bodyFormatKind.toUpperCase()}</span>
                  {/if}
                {/if}
              </div>

              {#if activeItem.request.bodyMode === 'none'}
                <p class="sub">This request has no body.</p>
              {:else if activeItem.request.bodyMode === 'urlencoded' || activeItem.request.bodyMode === 'formdata'}
                {#each activeItem.request.formFields ?? [] as f, i (f)}
                  <div class="field-row kv-row" class:row-disabled={f.disabled}>
                    <input aria-label="Form field name" type="text" placeholder="key" bind:value={f.key} />
                    {#if activeItem.request.bodyMode === 'formdata' && f.type === 'file'}
                      <div class="file-field-value">
                        <input aria-label="Choose a file" type="file" on:change={(e) => onFormFieldFileChange(f, e)} />
                        {#if f.fileName}<span class="sub file-field-name" title={f.fileName}>{f.fileName}</span>{/if}
                      </div>
                    {:else}
                      <input aria-label="Form field value" type="text" placeholder="value" bind:value={f.value} />
                    {/if}
                    {#if activeItem.request.bodyMode === 'formdata'}
                      <select aria-label="Form field type" class="field-type-select" bind:value={f.type}>
                        <option value="text">Text</option>
                        <option value="file">File</option>
                      </select>
                    {/if}
                    <label class="rule-required">
                      <input type="checkbox" checked={f.disabled} on:change={(e) => (f.disabled = e.currentTarget.checked)} /> off
                    </label>
                    <button class="btn btn-ghost" on:click={() => removeFormFieldRow(i)}>×</button>
                  </div>
                {/each}
                <button class="btn btn-ghost small" on:click={addFormFieldRow}>+ Field</button>
              {:else}
                <div class="body-editor-wrap">
                  {#if bodyErrorHighlightTop !== null}
                    <div class="body-error-line" style="top: {bodyErrorHighlightTop}px"></div>
                  {/if}
                  <textarea aria-label="Request body"
                    rows="8"
                    class="body-textarea"
                    bind:value={activeItem.request.body}
                    bind:this={bodyTextareaEl}
                    on:scroll={(e) => (bodyScrollTop = e.currentTarget.scrollTop)}
                  ></textarea>
                </div>
              {/if}
            </div>
          {/if}

          <div class="field-row action-row">
            <button class="btn btn-primary" on:click={send} disabled={sending} title="Ctrl/Cmd+Enter">{sending ? 'Sending…' : 'Send'}</button>
            {#if sending}<button class="btn btn-ghost btn-stop" on:click={forceStop}>Force stop</button>{/if}
            <button class="btn btn-ghost" on:click={() => (showLoadTestPanel = !showLoadTestPanel)}>{showLoadTestPanel ? 'Hide load test' : 'Load test'}</button>
            <button class="btn btn-ghost" on:click={() => viewCurl()}>Code snippet</button>
            <button class="btn btn-ghost" on:click={onClickSave} title="Ctrl/Cmd+S">Save</button>
            <button class="btn btn-ghost" on:click={() => createMockFromItem(activeItem)}>Create mock</button>
            {#if activeEnv}<span class="chip chip-tls">env: {activeEnv.name}</span>{/if}
            {#if undefinedVars.length}
              <span class="chip badge-warn" title={`Not resolvable with the current environment: ${undefinedVars.map((v) => '{{' + v + '}}').join(', ')}`}>
                ⚠ {undefinedVars.length} undefined var{undefinedVars.length > 1 ? 's' : ''}
              </span>
            {/if}
          </div>
        </div>

        {#if showLoadTestPanel}
          <div class="card loadtest-card row-enter">
            <h4>Load test</h4>
            <p class="sub">Fires this request repeatedly through a concurrency-controlled runner and reports latency percentiles and a status-code breakdown — capped at {MAX_LOADTEST_CONCURRENCY} concurrent workers, {MAX_LOADTEST_REQUESTS} requests, {MAX_LOADTEST_DURATION_SECS}s.</p>
            <div class="field-row">
              <label class="lt-field">
                Concurrency
                <input type="number" min="1" max={MAX_LOADTEST_CONCURRENCY} bind:value={ltConcurrency} />
              </label>
              <label class="lt-field">
                Total requests
                <input type="number" min="1" max={MAX_LOADTEST_REQUESTS} bind:value={ltTotalRequests} disabled={ltDurationSecs > 0} />
              </label>
              <label class="lt-field">
                Or duration (secs)
                <input type="number" min="0" max={MAX_LOADTEST_DURATION_SECS} bind:value={ltDurationSecs} placeholder="0 = use total requests" />
              </label>
              <button class="btn btn-primary" on:click={runLoadTest} disabled={loadTesting}>{loadTesting ? 'Running…' : 'Run'}</button>
              {#if loadTesting}<button class="btn btn-ghost btn-stop" on:click={forceStopLoadTest}>Stop</button>{/if}
              {#if loadTestResult && !loadTesting}<button class="btn btn-ghost" on:click={clearLoadTestResult}>Clear</button>{/if}
              {#if activeTab?.itemId}
                <button class="btn btn-ghost" on:click={toggleLoadTestHistory}>{showLoadTestHistory ? 'Hide history' : 'History'}</button>
              {/if}
            </div>
            <label class="toggle-label">
              <input type="checkbox" bind:checked={ltDetailed} />
              Detailed output (capture every request/response, not just the summary)
            </label>

            {#if showLoadTestHistory}
              <div class="lt-history">
                {#if loadingLoadTestHistory}
                  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading history…</p>
                {:else if loadTestHistory.length === 0}
                  <p class="sub">No past runs for this request yet.</p>
                {:else}
                  <table class="lt-history-table">
                    <thead><tr><th>When</th><th>Requests</th><th>Req/s</th><th>p95</th><th>Error rate</th><th></th></tr></thead>
                    <tbody>
                      {#each loadTestHistory as run (run.id)}
                        <tr>
                          <td>{new Date(run.createdAt).toLocaleString()}</td>
                          <td>{run.totalRequests}</td>
                          <td>{run.requestsPerSec.toFixed(1)}</td>
                          <td>{run.p95Ms}ms</td>
                          <td>{run.errorRate.toFixed(1)}%</td>
                          <td class="lt-history-actions">
                            <button class="btn btn-ghost small" on:click={() => viewLoadTestRun(run.id)}>View</button>
                            <a class="btn btn-ghost small" href={loadTestResultExportUrl(run.id, 'json')} download title="Download full result (every sample) as JSON">JSON</a>
                            <a class="btn btn-ghost small" href={loadTestResultExportUrl(run.id, 'csv')} download title="Download full result (every sample) as CSV">CSV</a>
                            <button class="btn btn-ghost small danger" on:click={() => deleteLoadTestRun(run.id)} title="Delete this past run">Delete</button>
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                {/if}
              </div>
            {/if}

            {#if loadTesting}
              <p class="sub"><span class="loader-spin"></span>&nbsp; Running…</p>
            {:else if loadTestResult}
              <div class="lt-summary">
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.totalRequests}</span><span class="lt-stat-label">requests</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.requestsPerSec.toFixed(1)}</span><span class="lt-stat-label">req/s</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.durationMs}ms</span><span class="lt-stat-label">duration</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.minMs}ms</span><span class="lt-stat-label">min</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.avgMs}ms</span><span class="lt-stat-label">avg</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p50Ms}ms</span><span class="lt-stat-label">p50</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p90Ms}ms</span><span class="lt-stat-label">p90</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p95Ms}ms</span><span class="lt-stat-label">p95</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p99Ms}ms</span><span class="lt-stat-label">p99</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.maxMs}ms</span><span class="lt-stat-label">max</span></div>
              </div>
              {#if loadTestResult.runId}
                <div class="lt-download">
                  <span class="sub">Download full result{loadTestResult.detailed ? ' (incl. every response)' : ''}:</span>
                  {#if loadTestResult.detailed}
                    <button class="btn btn-ghost small" on:click={() => downloadDetailedLoadTestResult('json')}>JSON</button>
                    <button class="btn btn-ghost small" on:click={() => downloadDetailedLoadTestResult('csv')}>CSV</button>
                  {:else}
                    <a class="btn btn-ghost small" href={loadTestResultExportUrl(loadTestResult.runId, 'json')} download>JSON</a>
                    <a class="btn btn-ghost small" href={loadTestResultExportUrl(loadTestResult.runId, 'csv')} download>CSV</a>
                  {/if}
                  <button class="btn btn-ghost small" on:click={downloadLoadTestReport} title="A single HTML file with the stat summary and charts, viewable with no AirMock instance running">Full report (HTML)</button>
                </div>
              {/if}
              <div class="lt-statuses">
                {#if loadTestResult.statuses.count2xx}<span class="chip badge-ok">{loadTestResult.statuses.count2xx} 2xx</span>{/if}
                {#if loadTestResult.statuses.count3xx}<span class="chip badge-info">{loadTestResult.statuses.count3xx} 3xx</span>{/if}
                {#if loadTestResult.statuses.count4xx}<span class="chip badge-warn">{loadTestResult.statuses.count4xx} 4xx</span>{/if}
                {#if loadTestResult.statuses.count5xx}<span class="chip badge-err">{loadTestResult.statuses.count5xx} 5xx</span>{/if}
                {#if loadTestResult.statuses.countError}<span class="chip badge-err">{loadTestResult.statuses.countError} network error</span>{/if}
              </div>
              <div class="lt-charts">
                <StatusBreakdownChart statuses={loadTestResult.statuses} />
                {#if loadTestResult.samples?.length}
                  <LatencyOverTimeChart samples={loadTestResult.samples} />
                  <LatencyHistogram samples={loadTestResult.samples} />
                {/if}
              </div>
              {#if loadTestResult.detailed}
                {@render loadTestSamplesTable(loadTestResult)}
              {/if}
            {/if}
          </div>
        {/if}

        {#if activeItem.examples?.length}
          <div class="card examples-card row-enter">
            <div class="examples-card-head">
              <h4>Saved examples <span class="sub">({activeItem.examples.length})</span></h4>
              <button class="btn btn-ghost small" on:click={() => (showExamplesList = !showExamplesList)}>
                {showExamplesList ? 'Hide' : 'Show'}
              </button>
            </div>
            {#if showExamplesList}
              <div class="examples-list">
                {#each activeItem.examples as ex (ex.id)}
                  <div class="example-row" class:active={viewingExample?.id === ex.id}>
                    <button class="example-main" on:click={() => viewExample(ex)}>
                      <span class="badge {statusClass(ex.statusCode)}">{ex.statusCode}</span>
                      <span class="example-name">{ex.name}</span>
                    </button>
                    <button class="remove-x" on:click={() => removeExample(ex)}>×</button>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}

        {#if response || viewingExample}
          {@const shown = viewingExample ?? response}
          <div class="card response-card row-enter">
            <div class="field-row">
              <span class="badge {statusClass(shown.statusCode)}">{shown.statusCode || 'error'}</span>
              {#if !viewingExample && activeItem?.request?.expectedStatus}
                <span class="chip {checkPass(response, activeItem.request.expectedStatus) ? 'chip-run' : 'chip-stop'}">
                  {checkPass(response, activeItem.request.expectedStatus) ? 'PASS' : 'FAIL'} (expected {activeItem.request.expectedStatus})
                </span>
              {/if}
              {#if viewingExample}
                <span class="chip chip-stat">saved example: {viewingExample.name}</span>
                <button class="btn btn-ghost small" on:click={() => (viewingExample = null)}>Back to live response</button>
              {:else}
                {#if response.timing}<span class="sub">total {response.timing.totalMs}ms · dns {response.timing.dnsMs}ms · connect {response.timing.connectMs}ms</span>{/if}
                {#if !response.error}
                  {#if showSaveExampleForm}
                    <input aria-label="Example name" type="text" class="example-name-input" bind:value={newExampleName} placeholder="Example name" />
                    <button class="btn btn-ghost small" on:click={saveExample}>Save example</button>
                    <button class="btn btn-ghost small" on:click={() => (showSaveExampleForm = false)}>Cancel</button>
                  {:else}
                    <button class="btn btn-ghost small" on:click={openSaveExampleForm}>Save as example</button>
                  {/if}
                {/if}
                <button class="btn btn-ghost small" on:click={() => copyResponseBody(currentResponseText(shown))}>Copy</button>
                <button class="btn btn-ghost small" on:click={resetResponsePanels}>Clear</button>
              {/if}
            </div>
            {#if !viewingExample && response.error}
              <p class="test-error">{response.error}</p>
            {:else}
              <div class="section-tabs">
                <button type="button" class:active={responseSection === 'body'} on:click={() => (responseSection = 'body')}>Body</button>
                <button type="button" class:active={responseSection === 'headers'} on:click={() => (responseSection = 'headers')}>
                  Headers{#if responseHeaderEntries(shown).length}<span class="section-count">{responseHeaderEntries(shown).length}</span>{/if}
                </button>
              </div>
              {#if responseSection === 'headers'}
                <div class="kv-rows-scroll">
                  {#if responseHeaderEntries(shown).length === 0}
                    <p class="sub">No headers.</p>
                  {:else}
                    {#each responseHeaderEntries(shown) as [k, v]}
                      <div class="resp-header-row"><span class="resp-header-key">{k}</span><span class="resp-header-val">{v}</span></div>
                    {/each}
                  {/if}
                </div>
              {:else}
                <pre class="resp-body">{formatResponseBody(shown.body)}</pre>
              {/if}
            {/if}
          </div>
        {/if}
      {:else if activeItem?.type === 'wsrequest'}
        <div class="card">
          <label>
            WebSocket URL
            <input type="text" bind:value={activeItem.wsRequest.url} placeholder="wss://example.com/events" />
          </label>
          <label>
            Initial message (optional)
            <textarea rows="3" bind:value={activeItem.wsRequest.message}></textarea>
          </label>
          <div class="field-row action-row">
            <button class="btn btn-primary" on:click={sendWS} disabled={sending}>{sending ? 'Connecting…' : 'Connect & send'}</button>
            {#if sending}<button class="btn btn-ghost btn-stop" on:click={forceStop}>Force stop</button>{/if}
            <button class="btn btn-ghost" on:click={() => (showLoadTestPanel = !showLoadTestPanel)}>{showLoadTestPanel ? 'Hide load test' : 'Load test'}</button>
            <button class="btn btn-ghost" on:click={saveActiveItem}>Save</button>
          </div>
        </div>

        {#if showLoadTestPanel}
          <div class="card loadtest-card row-enter">
            <h4>Load test</h4>
            <p class="sub">Opens this connection repeatedly through a concurrency-controlled runner (dial + send, no reply wait) and reports latency percentiles and a connect/error breakdown — capped at {MAX_LOADTEST_CONCURRENCY} concurrent workers, {MAX_LOADTEST_REQUESTS} connections, {MAX_LOADTEST_DURATION_SECS}s.</p>
            <div class="field-row">
              <label class="lt-field">
                Concurrency
                <input type="number" min="1" max={MAX_LOADTEST_CONCURRENCY} bind:value={ltConcurrency} />
              </label>
              <label class="lt-field">
                Total connections
                <input type="number" min="1" max={MAX_LOADTEST_REQUESTS} bind:value={ltTotalRequests} disabled={ltDurationSecs > 0} />
              </label>
              <label class="lt-field">
                Or duration (secs)
                <input type="number" min="0" max={MAX_LOADTEST_DURATION_SECS} bind:value={ltDurationSecs} placeholder="0 = use total connections" />
              </label>
              <button class="btn btn-primary" on:click={runLoadTest} disabled={loadTesting}>{loadTesting ? 'Running…' : 'Run'}</button>
              {#if loadTesting}<button class="btn btn-ghost btn-stop" on:click={forceStopLoadTest}>Stop</button>{/if}
              {#if loadTestResult && !loadTesting}<button class="btn btn-ghost" on:click={clearLoadTestResult}>Clear</button>{/if}
              {#if activeTab?.itemId}
                <button class="btn btn-ghost" on:click={toggleLoadTestHistory}>{showLoadTestHistory ? 'Hide history' : 'History'}</button>
              {/if}
            </div>
            <label class="toggle-label">
              <input type="checkbox" bind:checked={ltDetailed} />
              Detailed output (capture every connection's result, not just the summary)
            </label>

            {#if showLoadTestHistory}
              <div class="lt-history">
                {#if loadingLoadTestHistory}
                  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading history…</p>
                {:else if loadTestHistory.length === 0}
                  <p class="sub">No past runs for this request yet.</p>
                {:else}
                  <table class="lt-history-table">
                    <thead><tr><th>When</th><th>Requests</th><th>Req/s</th><th>p95</th><th>Error rate</th><th></th></tr></thead>
                    <tbody>
                      {#each loadTestHistory as run (run.id)}
                        <tr>
                          <td>{new Date(run.createdAt).toLocaleString()}</td>
                          <td>{run.totalRequests}</td>
                          <td>{run.requestsPerSec.toFixed(1)}</td>
                          <td>{run.p95Ms}ms</td>
                          <td>{run.errorRate.toFixed(1)}%</td>
                          <td class="lt-history-actions">
                            <button class="btn btn-ghost small" on:click={() => viewLoadTestRun(run.id)}>View</button>
                            <a class="btn btn-ghost small" href={loadTestResultExportUrl(run.id, 'json')} download title="Download full result (every sample) as JSON">JSON</a>
                            <a class="btn btn-ghost small" href={loadTestResultExportUrl(run.id, 'csv')} download title="Download full result (every sample) as CSV">CSV</a>
                            <button class="btn btn-ghost small danger" on:click={() => deleteLoadTestRun(run.id)} title="Delete this past run">Delete</button>
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                {/if}
              </div>
            {/if}

            {#if loadTesting}
              <p class="sub"><span class="loader-spin"></span>&nbsp; Running…</p>
            {:else if loadTestResult}
              <div class="lt-summary">
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.totalRequests}</span><span class="lt-stat-label">connections</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.requestsPerSec.toFixed(1)}</span><span class="lt-stat-label">conn/s</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.durationMs}ms</span><span class="lt-stat-label">duration</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.minMs}ms</span><span class="lt-stat-label">min</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.avgMs}ms</span><span class="lt-stat-label">avg</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p50Ms}ms</span><span class="lt-stat-label">p50</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p90Ms}ms</span><span class="lt-stat-label">p90</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p95Ms}ms</span><span class="lt-stat-label">p95</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p99Ms}ms</span><span class="lt-stat-label">p99</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.maxMs}ms</span><span class="lt-stat-label">max</span></div>
              </div>
              {#if loadTestResult.runId}
                <div class="lt-download">
                  <span class="sub">Download full result{loadTestResult.detailed ? ' (incl. every response)' : ''}:</span>
                  {#if loadTestResult.detailed}
                    <button class="btn btn-ghost small" on:click={() => downloadDetailedLoadTestResult('json')}>JSON</button>
                    <button class="btn btn-ghost small" on:click={() => downloadDetailedLoadTestResult('csv')}>CSV</button>
                  {:else}
                    <a class="btn btn-ghost small" href={loadTestResultExportUrl(loadTestResult.runId, 'json')} download>JSON</a>
                    <a class="btn btn-ghost small" href={loadTestResultExportUrl(loadTestResult.runId, 'csv')} download>CSV</a>
                  {/if}
                  <button class="btn btn-ghost small" on:click={downloadLoadTestReport} title="A single HTML file with the stat summary and charts, viewable with no AirMock instance running">Full report (HTML)</button>
                </div>
              {/if}
              <div class="lt-statuses">
                {#if loadTestResult.statuses.count2xx}<span class="chip badge-ok">{loadTestResult.statuses.count2xx} connected</span>{/if}
                {#if loadTestResult.statuses.countError}<span class="chip badge-err">{loadTestResult.statuses.countError} error</span>{/if}
              </div>
              <div class="lt-charts">
                <StatusBreakdownChart statuses={loadTestResult.statuses} />
                {#if loadTestResult.samples?.length}
                  <LatencyOverTimeChart samples={loadTestResult.samples} />
                  <LatencyHistogram samples={loadTestResult.samples} />
                {/if}
              </div>
              {#if loadTestResult.detailed}
                {@render loadTestSamplesTable(loadTestResult)}
              {/if}
            {/if}
          </div>
        {/if}

        {#if wsResponse}
          <div class="card response-card row-enter">
            {#if wsResponse.error}
              <p class="test-error">{wsResponse.error}</p>
            {:else}
              <div class="field-row">
                <p class="sub">Connected: {wsResponse.connected} · Sent: {wsResponse.sent}</p>
                <button class="btn btn-ghost small" on:click={() => copyResponseBody((wsResponse.messages ?? []).join('\n'))}>Copy</button>
                <button class="btn btn-ghost small" on:click={resetResponsePanels}>Clear</button>
              </div>
              {#each wsResponse.messages ?? [] as m}<pre class="resp-body">{m}</pre>{/each}
            {/if}
          </div>
        {/if}
      {:else}
        <div class="card empty"><p>Select a request from the left, or create one.</p></div>
      {/if}
    </div>
  </div>
{/if}

{#if tabContextMenu}
  <ContextMenu x={tabContextMenu.x} y={tabContextMenu.y} items={tabContextMenuItems(tabContextMenu.tab)} onClose={() => (tabContextMenu = null)} />
{/if}

{#if showLockModal && activeWorkspace}
  <WorkspaceLockModal
    workspace={activeWorkspace}
    onDone={() => {
      showLockModal = false;
      loadWorkspaces();
    }}
    onCancel={() => (showLockModal = false)}
  />
{/if}

{#if pendingWorkspaceUnlock}
  <WorkspaceUnlockModal
    workspace={workspaces.find((w) => w.id === pendingWorkspaceUnlock.workspaceId)}
    onUnlock={pendingWorkspaceUnlock.onUnlock}
    onCancel={pendingWorkspaceUnlock.onCancel}
  />
{/if}

{#if pendingDeleteWorkspaceId}
  <WorkspaceUnlockModal
    workspace={workspaces.find((w) => w.id === pendingDeleteWorkspaceId)}
    modalTitle="Delete workspace"
    subtitle={`Deleting "${workspaces.find((w) => w.id === pendingDeleteWorkspaceId)?.name ?? ''}" permanently removes it and everything in it. Enter its current password to confirm.`}
    confirmLabel="Delete workspace"
    verify={(password) => confirmDeleteWorkspace(pendingDeleteWorkspaceId, password)}
    onUnlock={() => (pendingDeleteWorkspaceId = '')}
    onCancel={() => (pendingDeleteWorkspaceId = '')}
  />
{/if}

<style>
  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .head-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 10px; min-width: 0; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }

  .workspace-bar { display: flex; align-items: flex-end; gap: 10px; margin-top: 14px; }
  .search-input {
    flex: 1; max-width: 360px; background: var(--surface2, var(--hover)); border: 1.5px solid var(--border);
    color: var(--text); border-radius: 6px; padding: 8px 12px; font-size: 13px; font-family: inherit; outline: none;
  }
  .search-input:focus { border-color: var(--primary); }
  .workspace-label {
    display: flex; flex-direction: column; gap: 4px; font-size: 11px; font-weight: 700;
    color: var(--muted); text-transform: uppercase; letter-spacing: .3px;
  }
  .workspace-label select {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 7px 10px; font-size: 13px; font-weight: 600; font-family: inherit;
    outline: none; min-width: 180px;
  }
  .workspace-label select:focus { border-color: var(--primary); }
  .workspace-bar .remove { margin-left: auto; }

  .form-card { margin-top: 16px; display: flex; flex-direction: column; gap: 12px; }
  .import-card { position: relative; padding-right: 40px; }
  .card-minus {
    position: absolute; top: 10px; right: 10px;
    width: 22px; height: 22px; display: flex; align-items: center; justify-content: center;
    background: none; border: 1px solid var(--border); border-radius: 6px; color: var(--muted);
    cursor: pointer; font-size: 15px; line-height: 1; padding: 0;
    transition: color .15s, border-color .15s, background .15s;
  }
  .card-minus:hover { color: var(--error, #dc2626); border-color: var(--error, #dc2626); background: rgba(220,38,38,.1); }
  .field-row { display: flex; gap: 12px; align-items: center; }
  .flex-1 { flex: 1; }
  .method-field { flex: 0 0 110px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  input, select, textarea {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
  }
  textarea { font-family: 'SFMono-Regular', Consolas, monospace; resize: vertical; }
  h3, h4 { margin: 8px 0 4px; }

  .expand-arrow { display: inline-block; font-size: 11px; color: var(--muted); transition: transform .2s cubic-bezier(.4,0,.2,1); flex-shrink: 0; }
  .expand-arrow.expanded { transform: rotate(90deg); }

  .modal-actions { display: flex; gap: 10px; margin-top: 16px; }
  .curl-modal-textarea { width: 100%; font-family: 'SFMono-Regular', Consolas, monospace; }
  .curl-preview {
    margin: 12px 0 0; padding: 12px 14px; background: var(--surface2, var(--hover)); border: 1.5px solid var(--border);
    border-radius: 6px; font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12.5px;
    /* curl's own -H/-d flags land on one very long logical line while a
       pretty-printed JSON body (real newlines, from Beautify JSON) sits
       right below it at a few chars per line — with white-space:pre that
       long first line just ran off the right edge of the box instead of
       wrapping, so it looked cut off/misaligned against the short JSON
       lines beneath it. pre-wrap + break-word wraps it inside the box
       instead, so nothing overflows and every language reads top-to-bottom
       without a horizontal scrollbar. */
    white-space: pre-wrap; word-break: break-word; overflow-wrap: anywhere; max-width: 100%;
    /* Fixed height (not just max-height) so the modal card is always the
       same size regardless of snippet length — a one-line curl command and
       a huge multi-KB JSON body both get the same box, scrolling
       internally, instead of the whole modal growing/shrinking per tab. */
    height: 340px; overflow-y: auto;
  }

  .run-summary { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
  .run-results-list { display: flex; flex-direction: column; gap: 6px; max-height: 50vh; overflow-y: auto; }
  .run-result-row {
    display: flex; align-items: center; gap: 10px; padding: 8px 10px; font-size: 13px;
    background: var(--surface2, var(--hover)); border-radius: 6px;
  }
  .run-result-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .run-result-status { font-family: 'SFMono-Regular', Consolas, monospace; color: var(--muted); flex-shrink: 0; }
  .run-result-latency { font-family: 'SFMono-Regular', Consolas, monospace; color: var(--muted); flex-shrink: 0; width: 56px; text-align: right; }
  .run-result-error { margin: -3px 0 3px; padding-left: 10px; font-size: 12px; color: var(--error); }

  /* --- environments --- */
  .env-card { border: 1px solid var(--border); border-radius: 8px; margin-top: 10px; overflow: hidden; }
  .env-summary { display: flex; align-items: center; gap: 10px; padding: 10px 12px; cursor: pointer; }
  .env-name { font-weight: 700; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .env-rename-input { flex: 1; padding: 5px 8px; }
  .env-detail { padding: 10px 12px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 8px; background: var(--surface2, var(--hover)); }

  /* --- workspace layout (two-pane collections + request editor) --- */
  .workspace { display: grid; grid-template-columns: 340px 1fr; gap: 16px; margin-top: 20px; align-items: start; }
  .collections-col {
    display: flex; flex-direction: column; gap: 10px;
    position: sticky; top: 0; max-height: min(70vh, 640px); overflow-y: auto; padding-right: 4px;
  }
  .mobile-back-btn { display: none; }
  .coll-card-outer { padding: 0; overflow: hidden; flex-shrink: 0; }
  .coll-summary { display: flex; align-items: center; gap: 10px; padding: 12px 14px; cursor: pointer; }
  /* No truncation: the collection name should always be fully readable in
     the list, even if that means wrapping onto a second line — it used to
     ellipsis-truncate long names with only a hover tooltip to see the rest. */
  .coll-name-text { flex: 1; min-width: 0; font-weight: 700; font-size: 14px; word-break: break-word; }

  .coll-select-box { width: 15px; height: 15px; flex-shrink: 0; }
  .bulk-bar {
    display: flex; align-items: center; gap: 10px; margin-bottom: 12px; padding: 10px 14px;
    background: var(--surface2, var(--hover)); border: 1px solid var(--border); border-radius: 8px;
  }
  .bulk-bar .sub { margin: 0; margin-right: 4px; }
  .select-all-label {
    display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600;
    color: var(--text); cursor: pointer; user-select: none;
  }
  .select-all-label input[type=checkbox] { width: 15px; height: 15px; }

  .coll-menu-wrap { position: relative; flex-shrink: 0; }
  .coll-menu-btn { font-size: 16px; line-height: 1; padding: 6px 10px; }
  .item-menu-wrap { position: relative; flex-shrink: 0; }
  .item-menu-btn { font-size: 16px; line-height: 1; padding: 5px 8px; }
  .coll-menu {
    position: fixed; z-index: 1000;
    background: var(--card); border: 1px solid var(--border); border-radius: 8px;
    box-shadow: 0 10px 28px rgba(0,0,0,.22); width: 140px;
    padding: 4px; display: flex; flex-direction: column; gap: 1px;
    animation: theme-menu-in .15s ease both;
  }
  .coll-menu-item {
    display: block; width: 100%; text-align: left; padding: 7px 10px; border-radius: 6px;
    background: none; border: none; color: var(--text); font-size: 13px; font-weight: 500; cursor: pointer;
    font-family: inherit; text-decoration: none;
  }
  .coll-menu-item:hover { background: var(--hover); }
  .coll-menu-item.remove:hover { color: var(--error); }

  .input-with-var-picker { display: flex; gap: 6px; align-items: center; }
  .input-with-var-picker input { flex: 1; min-width: 0; }
  .var-picker-wrap { position: relative; flex-shrink: 0; }
  .var-picker-btn {
    background: var(--surface); border: 1.5px solid var(--border); color: var(--muted);
    border-radius: 6px; padding: 7px 9px; font-size: 12px; font-family: monospace; cursor: pointer;
    transition: color .15s, border-color .15s;
  }
  .var-picker-btn:hover { color: var(--primary); border-color: var(--primary); }
  .var-picker-menu {
    position: absolute; top: calc(100% + 4px); right: 0; z-index: 50;
    background: var(--card); border: 1px solid var(--border); border-radius: 8px;
    box-shadow: 0 10px 28px rgba(0,0,0,.22); min-width: 160px; max-height: 220px; overflow-y: auto;
    padding: 4px; display: flex; flex-direction: column; gap: 1px;
    animation: theme-menu-in .15s ease both;
  }
  .var-picker-item {
    display: block; width: 100%; text-align: left; padding: 7px 10px; border-radius: 6px;
    background: none; border: none; color: var(--text); font-size: 12px; font-family: monospace; cursor: pointer;
  }
  .var-picker-item:hover { background: var(--hover); }
  .var-picker-empty { margin: 4px 8px; max-width: 200px; }
  .var-picker-group-label {
    margin: 6px 8px 2px; padding-top: 6px; border-top: 1px solid var(--border);
    font-size: 10.5px; text-transform: uppercase; letter-spacing: .03em; color: var(--muted);
  }
  /* min-width:0 is load-bearing: a bare flex:1 still respects the input's
     intrinsic UA-default min-width (well over 100px), so on a narrow or
     deeply-nested (folder padding-left scales with depth) row the input
     refused to shrink and pushed its own "Save" button off the visible
     edge — only reachable by scrolling the row horizontally. */
  .rename-input { flex: 1; min-width: 0; padding: 5px 8px; font-size: 13px; }
  .row-actions { display: flex; gap: 6px; flex-shrink: 0; }
  .btn.small { padding: 5px 10px; font-size: 12px; }
  .badge.small { padding: 2px 6px; font-size: 10px; }

  .coll-detail { padding: 4px 14px 12px 34px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 4px; }
  /* overflow-x lives on each ROW below, not here — putting it here too (on
     the list everything scrolls inside) meant a plain vertical scroll
     gesture over the list, especially a trackpad's small diagonal delta,
     could get partly redirected into horizontal movement the instant the
     cursor crossed any row wide enough to overflow, visibly jittering the
     list left-right while the user just meant to scroll down. Scoping
     overflow-x to each row instead keeps this list's own scroll purely
     vertical, always. */
  .coll-items-scroll { display: flex; flex-direction: column; gap: 4px; max-height: 320px; overflow-y: auto; }
  .empty-items { margin: 6px 0; }
  .coll-detail-actions { margin-top: 6px; display: flex; gap: 8px; flex-wrap: wrap; }

  /* overflow-x: auto so a deeply nested row (SoapUI's interface > operation
     > request commonly runs 2-3 levels deep) scrolls horizontally once
     indentation + badges + the name's own floor width (see .item-name)
     exceed the sidebar's fixed 340px — the alternative, letting flexbox
     keep shrinking everything to fit, is what previously squeezed the name
     down to a sliver so narrow it wrapped one character per line. Scoped to
     the row itself (see .coll-items-scroll above for why not the list). */
  /* flex-shrink: 0 is load-bearing: .coll-items-scroll caps at max-height
     with overflow-y:auto, but flexbox's default flex-shrink:1 tries to
     squeeze children to fit BEFORE ever falling back to scrolling — with
     a large collection (many folders/requests rendered as flat siblings
     of one column-flex list), their combined natural height routinely
     exceeds that cap, so every row got crushed down to a few px tall and
     wrapped 2-line names spilled out over neighboring rows instead of the
     list just scrolling. */
  .item-row { display: flex; flex-shrink: 0; align-items: center; gap: 4px; border-radius: 6px; overflow-x: auto; }
  .item-row:hover { background: var(--hover); }
  .item-row.active { background: var(--hover); box-shadow: inset 2px 0 0 var(--primary); }
  /* min-width: auto (the default — deliberately NOT 0) so this row's own
     box is at least as wide as its content requires (the badges' own
     flex-shrink:0 floors plus .item-name's own min-width, see below). With
     min-width:0 here, flexbox happily shrinks this whole box smaller than
     that combined content width, and the overflowing content then visually
     bleeds out UNDER the next sibling in .item-row (the ⋮ menu button) —
     positioned at .item-row-main's ALLOTTED width, not its true rendered
     width — rather than the row growing/scrolling as intended. */
  .item-row-main {
    flex: 1 1 auto; display: flex; align-items: center; gap: 8px; padding: 8px 10px;
    background: none; border: none; cursor: pointer; text-align: left; color: var(--text); font-size: 13px;
    appearance: none;
  }
  /* The shared global .chip/.badge (theme.css) sets no flex-shrink, which
     was harmless everywhere else but breaks down here now that .item-name
     has its own protective min-width: with too little combined room, a
     badge/chip's own flex box gets squeezed toward zero instead — its text
     doesn't clip or wrap along with it, so it visually bleeds out over the
     next sibling (the ⋮ menu button) instead of just looking cramped.
     Scoped here rather than changed globally since nothing else reported
     an issue with chips shrinking. */
  .item-row-main .badge, .item-row-main .chip, .folder-row-main .chip { flex-shrink: 0; }
  /* No truncation, same reasoning as .coll-name-text above: a folder or
     request name wraps onto a second line rather than ellipsis-truncating
     — deeply nested items (interface > operation > request, from a SoapUI
     import) commonly share one long prefix, so an end-truncated name reads
     as identical to its siblings ("brianOcc…" for brianOccAction,
     brianOccQuery, AND brianOccWrite alike) with nothing but a hover
     tooltip to tell them apart. */
  /* min-width is a real floor, not 0 — at deep nesting, flexbox will happily
     shrink this toward zero-width before it touches any fixed-size sibling
     (the star/method/protocol badges, the ⋮ menu, the × remove button),
     and below a certain width overflow-wrap:anywhere starts breaking mid-
     word every single character, stacking the name vertically letter by
     letter. With a floor plus .item-row/.folder-row's own horizontal
     scroll, a row that doesn't fit scrolls instead of collapsing into
     that. */
  .item-name { flex: 1 1 auto; min-width: 70px; word-break: break-word; overflow-wrap: break-word; }
  .fav-star {
    flex-shrink: 0; background: none; border: none; cursor: pointer; padding: 0 2px 0 10px;
    font-size: 14px; color: var(--border); line-height: 1;
  }
  .fav-star:hover { color: var(--warn, #d97706); }
  .fav-star.favorited { color: var(--warn, #d97706); }

  .favorites-bar {
    display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
    margin-top: 10px; padding: 8px 10px; background: var(--surface2, var(--hover)); border-radius: 8px;
  }
  .favorites-label { font-size: 11px; font-weight: 600; color: var(--muted); flex-shrink: 0; }
  .favorite-chip { cursor: pointer; }

  /* overflow-x: auto, same reasoning and same scoping-to-the-row-not-the-
     list as .item-row above. */
  /* flex-shrink: 0, same reasoning as .item-row above. */
  .folder-row { border-radius: 6px; display: flex; flex-shrink: 0; align-items: center; gap: 8px; cursor: pointer; overflow-x: auto; }
  .folder-row:hover { background: var(--hover); }
  /* min-width: auto, same reasoning as .item-row-main above — its own box
     must be at least as wide as its (flex-shrink:0-protected) chips plus
     .item-name's own floor, or the overflow bleeds out under the ⋮ button
     instead of the row growing/scrolling. */
  .folder-row-main {
    flex: 1 1 auto; display: flex; align-items: center; gap: 8px; padding: 8px 10px;
    color: var(--text); font-size: 13px; font-weight: 600;
  }
  .folder-row .row-actions { flex-shrink: 0; padding-right: 8px; opacity: 0; transition: opacity .15s; }
  .folder-row:hover .row-actions, .folder-row:focus-within .row-actions { opacity: 1; }
  .remove-x { flex-shrink: 0; background: none; border: none; cursor: pointer; color: var(--muted); padding: 0 10px; font-size: 14px; }
  .remove-x:hover { color: var(--error); }

  /* min-width:0 is load-bearing: .request-col is a CSS Grid item (the
     `1fr` column of .workspace), and a grid item's default min-width is
     `auto`, not 0 — without this, the tab bar's content (tabs plus the
     "+ New request"/"+ SOAP request"/"+ From curl" buttons) growing wider
     than the available column forced the WHOLE grid track to stretch to
     fit it instead of clipping/scrolling internally, pushing everything
     in this column out past the page's right edge as more tabs opened. */
  .request-col { display: flex; flex-direction: column; gap: 16px; min-width: 0; }

  /* --- tab bar --- */
  .tab-bar-row { display: flex; align-items: center; gap: 4px; flex-wrap: nowrap; }
  .tab-bar {
    display: flex; gap: 4px; flex-wrap: nowrap; overflow-x: auto; overflow-y: hidden;
    flex: 1; min-width: 0;
    scrollbar-width: none; -ms-overflow-style: none; /* firefox / old edge */
  }
  .tab-bar::-webkit-scrollbar { display: none; } /* chrome/safari */
  .tab-scroll-btn {
    flex-shrink: 0; background: var(--surface); border: 1px solid var(--border); color: var(--muted);
    border-radius: 6px; width: 24px; height: 28px; cursor: pointer; font-size: 15px; line-height: 1;
    display: flex; align-items: center; justify-content: center; transition: color .15s, border-color .15s;
  }
  .tab-scroll-btn:hover { color: var(--primary); border-color: var(--primary); }
  .new-tab-actions { display: flex; gap: 4px; flex-shrink: 0; }
  .new-tab-btn { flex-shrink: 0; }
  .tab-btn {
    display: flex; align-items: center; gap: 6px; background: var(--card); border: 1px solid var(--border);
    border-radius: 8px 8px 0 0; padding: 7px 8px 7px 12px; font-size: 12px; color: var(--muted); cursor: pointer;
    transition: all .15s; flex: 0 0 200px; box-sizing: border-box;
  }
  .tab-btn.active { color: var(--text); border-bottom-color: var(--card); box-shadow: 0 -2px 0 var(--primary) inset; }
  .tab-btn:hover { color: var(--text); }
  .tab-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 120px; }
  .tab-btn.renaming { cursor: default; }
  .tab-rename-input {
    flex: 1; min-width: 0; background: var(--surface2, var(--hover)); border: 1.5px solid var(--primary);
    border-radius: 4px; padding: 2px 5px; font-size: 12px; font-family: inherit; color: var(--text); outline: none;
  }
  .tab-close { padding: 0 2px; border-radius: 4px; color: var(--muted); }
  .tab-close:hover { color: var(--error); background: var(--hover); }
  .unsaved-dot {
    width: 6px; height: 6px; border-radius: 50%; background: var(--warn); flex-shrink: 0;
    animation: dot-pulse 2s ease-out infinite;
  }

  /* --- headers/query/body/auth sub-tabs --- */
  .section-tabs { display: flex; gap: 4px; border-bottom: 1px solid var(--border); margin-top: 4px; }
  .section-tabs button {
    background: none; border: none; padding: 8px 12px; font-size: 12px; font-weight: 700;
    color: var(--muted); cursor: pointer; border-bottom: 2px solid transparent; margin-bottom: -1px;
    display: flex; align-items: center; gap: 6px; transition: color .15s, border-color .15s;
  }
  .section-tabs button.active { color: var(--primary); border-bottom-color: var(--primary); }
  .section-tabs button:hover { color: var(--text); }
  .section-count {
    background: var(--surface2, var(--hover)); color: var(--muted); border-radius: 10px;
    padding: 1px 6px; font-size: 10px; font-weight: 700;
  }
  .section-panel { padding-top: 10px; display: flex; flex-direction: column; gap: 8px; }

  .body-toolbar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .body-mode-select, .raw-type-select { width: auto; flex: 0 0 auto; }
  .json-badge { font-size: 11px; font-weight: 700; padding: 3px 8px; border-radius: 10px; }
  .json-badge.valid { background: rgba(22,163,74,.1); color: var(--success); }
  .json-badge.invalid { background: rgba(220,38,38,.1); color: var(--error); }

  /* .body-error-line's `top` is computed in script from the exact same
     line-height/padding-top as this textarea — keep them in lockstep. */
  .body-editor-wrap { position: relative; overflow: hidden; border-radius: 6px; }
  .body-textarea { width: 100%; display: block; line-height: 1.5; padding: 8px 10px; }
  .body-error-line {
    position: absolute; left: 0; right: 0; height: 19.5px;
    background: rgba(220,38,38,.12); pointer-events: none;
  }

  /* Both fields below sit directly inside .section-panel (a flex COLUMN),
     not inside a .field-row, so flex-basis here controls height, not width
     — the same mistake as the read-timeout field. flex: 0 0 auto lets each
     size to its natural content height instead of a forced 200px-tall box. */
  .auth-type-field { flex: 0 0 auto; }
  .add-to-field { flex: 0 0 150px; }
  .expected-status-field { flex: 0 0 auto; max-width: 200px; margin-bottom: 8px; }

  .kv-rows-scroll { display: flex; flex-direction: column; gap: 8px; max-height: 280px; overflow-y: auto; padding-right: 4px; }
  .kv-row input { flex: 1; }
  .kv-row.row-disabled { opacity: .45; transition: opacity .25s ease; }
  .kv-row.row-disabled input:not([type=checkbox]) { text-decoration: line-through; }
  .kv-row.row-overridden { opacity: .6; }
  .kv-row.row-overridden input:not([type=checkbox]) { color: var(--muted); }
  .auto-header-row { margin-bottom: 8px; }
  .auto-header-row input[readonly] { background: var(--panel-alt, rgba(127,127,127,.08)); color: var(--muted); cursor: default; }
  .auto-header-badge {
    flex: 0 0 auto; font-size: 10px; font-weight: 700; letter-spacing: .3px; text-transform: uppercase;
    color: var(--muted); background: rgba(127,127,127,.15); border-radius: 4px; padding: 3px 6px; white-space: nowrap;
  }
  .field-type-select { flex: 0 0 90px; }
  .file-field-value { flex: 1; display: flex; align-items: center; gap: 8px; min-width: 0; }
  .file-field-value input[type="file"] { flex: 1; min-width: 0; padding: 4px; }
  .file-field-name { flex: 0 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .rule-required { flex: 0 0 auto; flex-direction: row; align-items: center; gap: 6px; text-transform: none; font-weight: 600; white-space: nowrap; }
  .insecure-field { margin-top: 16px; }
  .client-cert-field { flex: 0 0 auto; max-width: 260px; margin-top: 12px; }
  /* On its own field-row (not sharing one with .insecure-field above) —
     putting a single-line checkbox and a two-line label+input side by side
     kept fighting cross-axis alignment (flex-start left the checkbox
     looking offset against the input row below the read-timeout label, and
     center left it floating oddly against the label text). Stacking them
     avoids that entirely, matching how .expected-status-field (Extract tab)
     already sits alone on its own row rather than paired with anything. */
  .read-timeout-row { margin-top: 16px; }
  .read-timeout-field { flex: 0 0 auto; }
  .read-timeout-field .label-text { white-space: nowrap; }
  .read-timeout-field input { width: 100px; }
  .action-row { margin-top: 8px; flex-wrap: wrap; }
  .action-row button { flex: 0 0 auto; }
  .btn-stop { color: var(--error); border-color: var(--error); }
  .btn-stop:hover { background: rgba(220, 38, 38, 0.08); }

  /* --- load test --- */
  .loadtest-card { display: flex; flex-direction: column; gap: 10px; }
  .toggle-label {
    display: flex; flex-direction: row; align-items: center; gap: 6px; text-transform: none;
    font-weight: 600; font-size: 12px; color: var(--muted); cursor: pointer;
  }
  .toggle-label input { width: 15px; height: 15px; }
  .lt-field { flex: 0 0 140px; }
  .lt-summary { display: flex; flex-wrap: wrap; gap: 20px; }
  .lt-stat { display: flex; flex-direction: column; align-items: flex-start; }
  .lt-stat-val { font-size: 20px; font-weight: 800; }
  .lt-stat-label { font-size: 11px; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; }
  .lt-download { display: flex; align-items: center; gap: 6px; margin: 6px 0; }
  .lt-statuses { display: flex; flex-wrap: wrap; gap: 6px; }
  .lt-charts { display: flex; flex-wrap: wrap; gap: 16px; margin: 12px 0; }
  .lt-charts > :global(.chart-wrap) { flex: 1 1 280px; min-width: 0; }
  .lt-history { margin: 10px 0; }
  .lt-history-table { width: 100%; border-collapse: collapse; font-size: 13px; }
  .lt-history-table th, .lt-history-table td { text-align: left; padding: 4px 8px; border-bottom: 1px solid var(--border); }
  .lt-history-actions { display: flex; gap: 4px; }
  .lt-samples { display: flex; flex-direction: column; gap: 6px; }
  .lt-request-line { display: block; font-size: 12px; color: var(--muted); word-break: break-all; }
  .lt-samples-scroll { max-height: 400px; overflow-y: auto; overflow-x: auto; border: 1px solid var(--border); border-radius: 8px; }
  .lt-samples-table { width: 100%; border-collapse: collapse; font-size: 12px; }
  .lt-samples-table th, .lt-samples-table td { padding: 5px 10px; text-align: left; border-bottom: 1px solid var(--border); }
  .lt-samples-table th { position: sticky; top: 0; background: var(--surface2, var(--hover)); color: var(--muted); font-weight: 700; text-transform: uppercase; font-size: 10px; letter-spacing: .3px; }
  .lt-samples-table tbody tr:last-child td { border-bottom: none; }
  .lt-sample-row { cursor: pointer; }
  .lt-sample-row:hover { background: var(--hover); }
  .lt-sample-arrow { width: 16px; }
  .lt-sample-error { color: var(--error, #dc2626); max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .lt-sample-detail-row td { background: var(--surface2, var(--hover)); padding: 10px 14px; }
  .lt-sample-detail-section { display: flex; flex-direction: column; gap: 4px; margin-bottom: 10px; }
  .lt-sample-detail-section:last-child { margin-bottom: 0; }
  .lt-sample-detail-label { font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .lt-sample-body {
    margin: 0; font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px; color: var(--text);
    white-space: pre-wrap; word-break: break-word; max-height: 200px; overflow-y: auto;
  }

  /* --- saved examples --- */
  .examples-card { display: flex; flex-direction: column; gap: 8px; }
  .examples-card-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
  .examples-card-head h4 { margin: 0; }
  .examples-list { display: flex; flex-direction: column; gap: 4px; max-height: 220px; overflow-y: auto; }
  .example-row { display: flex; align-items: center; gap: 4px; border-radius: 6px; }
  .example-row:hover, .example-row.active { background: var(--hover); }
  .example-main {
    flex: 1; display: flex; align-items: center; gap: 8px; padding: 6px 8px;
    background: none; border: none; cursor: pointer; text-align: left; color: var(--text); font-size: 13px;
  }
  .example-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .example-name-input { padding: 5px 8px; font-size: 12px; width: 160px; }

  .response-card { display: flex; flex-direction: column; gap: 8px; }
  .response-card .field-row { flex-wrap: wrap; }
  .resp-body { background: var(--surface2, var(--hover)); border-radius: 6px; padding: 10px; font-size: 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-word; font-family: 'SFMono-Regular', Consolas, monospace; }
  .resp-header-row { display: flex; gap: 10px; font-size: 12px; font-family: 'SFMono-Regular', Consolas, monospace; padding: 4px 0; border-bottom: 1px solid var(--border); }
  .resp-header-key { color: var(--muted); flex: 0 0 auto; min-width: 180px; font-weight: 600; }
  .resp-header-val { color: var(--text); word-break: break-word; }
  .test-error { color: var(--error); font-size: 13px; }

  .kv-key { font-weight: 700; min-width: 100px; }
  .kv-value { color: var(--muted); flex: 1; }
  .kv-edit-input { min-width: 0; padding: 5px 8px; font-size: 13px; }

  .empty { margin-top: 4px; }

  /* Deliberately last in this file: below this width the two permanent
     columns above can't both fit, so .workspace becomes single-column and
     only ONE of collections-col/request-col shows at a time
     (mobileShowRequestPane, in <script>), with a "Back" button in
     request-col to return to the list. This has to come AFTER
     .request-col's own unconditional "display: flex" rule earlier in this
     file — with equal specificity (both plain single-class selectors,
     same Svelte scoping hash on both), source order decides the winner
     when a media query's condition holds, and a rule declared earlier
     loses to one declared later regardless of which is inside the media
     query. The sticky/max-height/scroll treatment on collections-col only
     makes sense next to a second column of independent height; full-page
     scroll is the natural behavior once it's the only thing on screen. */
  @media (max-width: 900px) {
    .workspace { display: block; }
    .collections-col {
      position: static; max-height: none; overflow-y: visible; padding-right: 0;
    }
    .request-col, .collections-col { display: none; }
    .workspace:not(.mobile-show-request) .collections-col { display: flex; }
    .workspace.mobile-show-request .request-col { display: flex; flex-direction: column; }
    .mobile-back-btn {
      display: inline-flex; align-self: flex-start; margin-bottom: 10px;
      background: none; border: none; color: var(--primary); font-size: 13px; font-weight: 600;
      padding: 4px 0; cursor: pointer;
    }

    /* .head-actions (Environments/Import Collection/Import SOAP Project
       XML/Import WSDL/Import WSDL from folder/Import folder — six buttons)
       and .workspace-bar (workspace picker + search, min-width: 180px and
       flex: 1 respectively) both assumed desktop-width all along; neither
       had anywhere to go on a narrow screen but off the edge. */
    /* .head-actions' own base rule sets flex-shrink: 0 (never shrink) —
       flex-wrap alone doesn't help while it's still being told to hold its
       full unwrapped natural width as a flex ITEM of .head-row; forcing it
       onto its own full-width line first is what lets its wrap on ITS OWN
       children actually take effect within that now-constrained width. */
    .head-row { flex-wrap: wrap; }
    .head-actions { flex-wrap: wrap; flex: 1 1 100%; }
    .workspace-bar { flex-wrap: wrap; }
    .search-input { max-width: none; flex-basis: 100%; }

    /* .head-row/.workspace-bar/.favorites-bar sit ABOVE .workspace, not
       inside collections-col — list-browsing chrome (import buttons,
       workspace picker, search, favorites shortcuts) that's dead weight
       once a request is actually open, eating the vertical space a small
       screen most needs for the editor itself. Hidden (not removed) while
       mobileShowRequestPane is true; reappears going back to the list. */
    .mobile-hide { display: none; }

    /* Headers/Query/Auth/Body/Tests/Notes — six tabs at their desktop
       padding overflow this width enough that the last 1-2 tabs render
       fully off-screen with no visible scrollbar or other hint that
       they're reachable by swiping, so switching to them just looked
       broken. Tightening padding/gap/font-size here is enough for the
       common 6-tab case to fit with no scrolling needed at all; overflow-x
       stays on as a fallback for anything that still doesn't fit (a tab
       set with more entries, a wider font), matching the existing
       horizontally-scrolling convention this file already uses for the
       request tab-bar (.tab-bar) itself. */
    .section-tabs { overflow-x: auto; gap: 2px; }
    .section-tabs button { flex-shrink: 0; padding: 8px 8px; font-size: 11px; gap: 4px; }

    /* The Code Snippet modal reuses this same .section-tabs bar for its
       four language tabs — but "JavaScript (fetch)"/"Python (requests)"/
       "Go (net/http)" are full phrases rather than the request tab-bar's
       single short words, so even at the tightened padding above they
       still overflowed by ~25px with nothing to signal that "Go (net/
       http)" was reachable by scrolling. A little tighter still (this
       specific bar only, via the extra .modal-body specificity, so the
       six-tab request bar above — which already fits exactly — doesn't
       get squeezed for no reason) is enough for all four to fit with no
       scrolling needed here either.

       :global() on .modal-body is required, not decorative: that div is
       Modal.svelte's own markup (this component only fills its slot), so
       Svelte's scoping hash — added only to selector parts matching
       elements THIS component renders — never lands on it, and an
       unqualified ".modal-body .section-tabs button" silently never
       matches anything at all. */
    :global(.modal-body) .section-tabs button { padding: 8px 5px; }

    /* .tab-bar-row is a deliberately non-wrapping single line (see its own
       base rule) so the open-tab strip and the "+ New request/+ SOAP
       request/+ From curl" shortcuts share one row on desktop — but each
       open tab is a hardcoded 200px wide (.tab-btn's flex: 0 0 200px), and
       on a narrow screen the three "+" buttons alone already ate most of
       the row, leaving .tab-bar so little width that not even one tab's
       name was legible in the sliver that remained. The "+" buttons are
       rarely-used shortcuts, not something that needs to sit level with
       the tabs — moved to their own line above via order:-1 + flex-basis:
       100% (the same forced-line-break trick used elsewhere in this file)
       so .tab-bar gets the full row width to actually show tab names. */
    .tab-bar-row { flex-wrap: wrap; row-gap: 8px; }
    .new-tab-actions { order: -1; flex: 1 1 100%; flex-wrap: wrap; }
  }
</style>
