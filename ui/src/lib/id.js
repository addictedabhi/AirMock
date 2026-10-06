// crypto.randomUUID() only exists in "secure contexts" — https:, or the
// browser literally recognizing the hostname as "localhost". A real
// deployment reached over plain HTTP via its actual hostname/IP (not
// localhost) has no such context, and the function is simply undefined
// there — not merely blocked, absent — which crashed every "create a new
// X" action in Collections with "crypto.randomUUID is not a function".
// crypto.getRandomValues, unlike randomUUID, IS available regardless of
// secure-context status, so it's used here to build an equivalent random
// v4 UUID by hand whenever the real thing isn't available.
export function randomId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40; // version 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // variant 10xx
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
