// Wraps a mock create/update/delete call so a 403 "workspace locked"
// response (see internal/web/api/mocks.go's checkWorkspaceUnlocked) prompts
// for that workspace's password and retries automatically once entered,
// instead of surfacing a raw error toast the user has to make sense of.
//
// No separate "already unlocked" cache is needed on this side: once
// WorkspaceUnlockModal's api.unlockWorkspace call succeeds, the SERVER
// remembers it (see internal/wslock.Tracker, keyed by the browser's
// client-id cookie fetch already sends with every same-origin request) —
// every later action against that workspace just succeeds outright with no
// further 403 and so no further prompt, for the rest of this browser
// session, without this module needing to track anything itself.
//
// setPendingUnlock is the calling page's own setter for a local
// `pendingUnlock` variable — see any of the mock pages for the ~6-line
// markup that turns a non-null value into a <WorkspaceUnlockModal>. Kept
// per-page rather than a single global singleton so each page's modal
// renders in its own natural place in the DOM, the same way every other
// modal in this app already works (Collections.svelte's many local
// `showX` booleans, for example) rather than introducing a new pattern
// just for this one feature.
export function runWithWorkspaceUnlock(fn, setPendingUnlock) {
  return fn().catch((e) => {
    if (e.code !== 'workspace_locked' || !e.workspaceId) throw e;
    return new Promise((resolve, reject) => {
      setPendingUnlock({
        workspaceId: e.workspaceId,
        onUnlock: () => {
          setPendingUnlock(null);
          fn().then(resolve, reject);
        },
        onCancel: () => {
          setPendingUnlock(null);
          reject(e);
        },
      });
    });
  });
}
