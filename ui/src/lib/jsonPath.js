// Minimal dot-path field extraction against a parsed JSON value — enough
// for pulling one field out of a response body ("data.token",
// "user.id", "items.0.id" for array indices) without pulling in a full
// JSONPath library for what's otherwise a single-purpose feature.

export function getByPath(value, path) {
  if (!path) return undefined;
  const segments = path.split('.').filter(Boolean);
  let current = value;
  for (const seg of segments) {
    if (current === null || current === undefined) return undefined;
    current = current[seg];
  }
  return current;
}

// Coerces an extracted value to a plain string suitable for storing as an
// environment variable — objects/arrays are JSON-stringified rather than
// becoming "[object Object]".
export function stringifyExtracted(value) {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  return JSON.stringify(value);
}

// Applies every extract rule against a response body (raw string, parsed
// as JSON best-effort) and returns { variable: value } for whatever
// resolved — rules whose path doesn't resolve are silently skipped rather
// than erroring, since a runner shouldn't abort a whole pass over one
// missing field in one response.
export function applyExtractRules(rules, responseBodyText) {
  const out = {};
  if (!rules || rules.length === 0) return out;
  let parsed;
  try {
    parsed = JSON.parse(responseBodyText);
  } catch {
    return out; // not JSON — no rule can resolve against it
  }
  for (const rule of rules) {
    if (!rule.path || !rule.variable) continue;
    const value = getByPath(parsed, rule.path);
    if (value !== undefined) {
      out[rule.variable] = stringifyExtracted(value);
    }
  }
  return out;
}
