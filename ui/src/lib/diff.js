// Recursive field-by-field diff between two plain JSON-shaped values — the
// "what changed" behind a mock's Version history entry, comparing an old
// snapshot against the mock's current live state (so it doubles as "here's
// what Restore would actually change," not just a log of past edits).

const IGNORED_KEYS = new Set(['id', 'createdAt', 'updatedAt']);

function isPlainObject(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

// Arrays (rule lists, header lists, etc.) are compared as a whole value
// rather than index-by-index — a mock's rules are typically a handful of
// small objects, and "index 2 changed" is far less readable than "rules
// changed from [...] to [...]" for something this size.
function valuesEqual(a, b) {
  return JSON.stringify(a) === JSON.stringify(b);
}

// Walks both objects' keys (union, so an added/removed field is caught too),
// recursing into nested plain objects and reporting a flat list of leaf
// changes as { path, oldValue, newValue }.
export function diffObjects(oldObj, newObj, pathPrefix = '') {
  const changes = [];
  const oldO = isPlainObject(oldObj) ? oldObj : {};
  const newO = isPlainObject(newObj) ? newObj : {};
  const keys = new Set([...Object.keys(oldO), ...Object.keys(newO)]);

  for (const key of keys) {
    if (!pathPrefix && IGNORED_KEYS.has(key)) continue;
    const path = pathPrefix ? `${pathPrefix}.${key}` : key;
    const oldVal = oldO[key];
    const newVal = newO[key];

    if (isPlainObject(oldVal) && isPlainObject(newVal)) {
      changes.push(...diffObjects(oldVal, newVal, path));
      continue;
    }
    if (!valuesEqual(oldVal, newVal)) {
      changes.push({ path, oldValue: oldVal, newValue: newVal });
    }
  }
  return changes;
}

// Renders one diffed value compactly for display — objects/arrays as
// single-line JSON, everything else as its plain string form, with a
// distinct label for "field didn't exist before/after" rather than showing
// a bare "undefined".
export function formatDiffValue(v) {
  if (v === undefined) return '(none)';
  if (v === null) return 'null';
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}
