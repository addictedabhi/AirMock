// navigator.clipboard only exists in "secure contexts" — https:, or the
// browser literally recognizing the hostname as "localhost". A real
// deployment reached over plain HTTP via its actual hostname/IP (not
// localhost) has no such context, and navigator.clipboard is simply
// undefined there — not merely blocked, absent — which made every "Copy"
// button throw immediately ("Could not copy — select the text manually",
// or a silently-swallowed error for the context-menu copy items that had
// no catch at all). document.execCommand('copy'), unlike the Clipboard
// API, carries no secure-context restriction, so it's used here as a
// fallback: temporarily create an off-screen textarea, select its
// contents, and run the legacy copy command.
export async function copyText(text) {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // fall through to the legacy path below
    }
  }
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.left = '-9999px';
  document.body.appendChild(textarea);
  textarea.select();
  textarea.setSelectionRange(0, text.length);
  try {
    const ok = document.execCommand('copy');
    if (!ok) throw new Error('execCommand(copy) returned false');
  } finally {
    document.body.removeChild(textarea);
  }
}
