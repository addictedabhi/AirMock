import { writable, get } from 'svelte/store';

const STORAGE_KEY = 'airmock-theme';
const ACCENTS_STORAGE_KEY = 'airmock-accents'; // JSON: { [themeId]: accentId }

// Each theme is a complete, self-contained palette (see theme.css) — not a
// light/dark toggle plus an accent choice, so every combination is hand-
// tuned and nothing falls back to a mismatched default.
//
// `accents` is that theme's OWN sub-palette: shades within its own color
// family (e.g. Rose's accents are other pinks — fuchsia, blush, magenta,
// coral — never an unrelated hue like green), so every accent option is
// guaranteed to suit the theme it's listed under. This is why accent ids
// are theme-scoped rather than one shared list applied to every theme —
// "azure" only ever appears (and only ever means something) under
// Daylight/Midnight, "fuchsia" only under Rose, and so on. The one
// deliberate exception is Cartoon: its whole identity is a flat black-and-
// white comic panel, so a soft two-tone color gradient (right for every
// other theme) would clash with its hard borders and flat ink shadows —
// its "accents" are different comic SPOT-color styles instead of shades of
// one hue, rendered flat rather than as a gradient (see theme.css).
export const themes = [
  {
    id: 'daylight', label: 'Daylight', swatch: '#0052cc', mode: 'light',
    accents: [
      { id: 'azure', label: 'Azure', swatch: '#0284c7' },
      { id: 'indigo', label: 'Indigo', swatch: '#4f46e5' },
      { id: 'sky', label: 'Sky', swatch: '#0ea5e9' },
      { id: 'navy', label: 'Navy', swatch: '#1e3a8a' },
    ],
  },
  {
    id: 'meadow', label: 'Meadow', swatch: '#059669', mode: 'light',
    accents: [
      { id: 'sage', label: 'Sage', swatch: '#65a30d' },
      { id: 'forest', label: 'Forest', swatch: '#15803d' },
      { id: 'mint', label: 'Mint', swatch: '#10b981' },
      { id: 'jade', label: 'Jade', swatch: '#0d9488' },
    ],
  },
  {
    id: 'rose', label: 'Rose', swatch: '#db2777', mode: 'light',
    accents: [
      { id: 'fuchsia', label: 'Fuchsia', swatch: '#c026d3' },
      { id: 'blush', label: 'Blush', swatch: '#f43f5e' },
      { id: 'magenta', label: 'Magenta', swatch: '#a21caf' },
      { id: 'coral', label: 'Coral', swatch: '#e11d48' },
    ],
  },
  {
    id: 'cartoon', label: 'Cartoon', swatch: '#000000', mode: 'light',
    accents: [
      { id: 'pop-red', label: 'Pop Red', swatch: '#c81e1e' },
      { id: 'hero-blue', label: 'Hero Blue', swatch: '#1d4ed8' },
      { id: 'villain-purple', label: 'Villain Purple', swatch: '#7c3aed' },
      { id: 'retro-gold', label: 'Retro Gold', swatch: '#b8860b' },
    ],
  },
  {
    id: 'midnight', label: 'Midnight', swatch: '#5b9eff', mode: 'dark',
    accents: [
      { id: 'cobalt', label: 'Cobalt', swatch: '#2563eb' },
      { id: 'periwinkle', label: 'Periwinkle', swatch: '#818cf8' },
      { id: 'steel', label: 'Steel', swatch: '#60a5fa' },
      { id: 'cyan-blue', label: 'Cyan', swatch: '#38bdf8' },
    ],
  },
  {
    id: 'ocean', label: 'Ocean', swatch: '#22d3ee', mode: 'dark',
    accents: [
      { id: 'turquoise', label: 'Turquoise', swatch: '#2dd4bf' },
      { id: 'aqua', label: 'Aqua', swatch: '#06b6d4' },
      { id: 'seafoam', label: 'Seafoam', swatch: '#5eead4' },
      { id: 'deepsea', label: 'Deep Sea', swatch: '#0891b2' },
    ],
  },
  {
    id: 'sunset', label: 'Sunset', swatch: '#f97316', mode: 'dark',
    accents: [
      { id: 'amber', label: 'Amber', swatch: '#f59e0b' },
      { id: 'coral-sunset', label: 'Coral', swatch: '#fb7185' },
      { id: 'crimson', label: 'Crimson', swatch: '#ef4444' },
      { id: 'peach', label: 'Peach', swatch: '#fdba74' },
    ],
  },
  {
    id: 'violet', label: 'Violet', swatch: '#a78bfa', mode: 'dark',
    accents: [
      { id: 'lavender', label: 'Lavender', swatch: '#c4b5fd' },
      { id: 'plum', label: 'Plum', swatch: '#c026d3' },
      { id: 'indigo-violet', label: 'Indigo', swatch: '#818cf8' },
      { id: 'orchid', label: 'Orchid', swatch: '#d946ef' },
    ],
  },
  {
    id: 'cocoa', label: 'Cocoa', swatch: '#d2a066', mode: 'dark',
    accents: [
      { id: 'bronze', label: 'Bronze', swatch: '#b08d57' },
      { id: 'copper', label: 'Copper', swatch: '#c2703d' },
      { id: 'mocha', label: 'Mocha', swatch: '#b58a5e' },
      { id: 'honey', label: 'Honey', swatch: '#e6b566' },
    ],
  },
];
const THEME_IDS = new Set(themes.map((t) => t.id));
const themeById = new Map(themes.map((t) => [t.id, t]));

function initial() {
  if (typeof localStorage === 'undefined') return 'daylight';
  const stored = localStorage.getItem(STORAGE_KEY);
  // Migrate the old boolean light/dark scheme transparently.
  if (stored === 'dark') return 'midnight';
  if (stored === 'light') return 'daylight';
  return THEME_IDS.has(stored) ? stored : 'daylight';
}

// Accent choices are remembered PER THEME (a plain { themeId: accentId }
// map), not as one flat global pick — since accent ids are only ever
// meaningful under the theme that defines them, "the accent" only makes
// sense relative to whichever theme is active. Switching themes restores
// whatever that theme's own last pick was (or its own default look, if it
// was never customized) instead of carrying over a pick that belongs to a
// completely different color family.
function initialAccents() {
  if (typeof localStorage === 'undefined') return {};
  try {
    const parsed = JSON.parse(localStorage.getItem(ACCENTS_STORAGE_KEY) || '{}');
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

export const theme = writable(initial());
export const accentsByTheme = writable(initialAccents());

// Applies data-theme/data-theme-mode plus whichever accent (if any) is on
// record for that specific theme — called from both stores' subscribers
// so either a theme switch or an accent pick keeps the DOM in sync.
function applyThemeAndAccent(themeId, accentMap) {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.theme = themeId;
  document.documentElement.dataset.themeMode = themeById.get(themeId)?.mode ?? 'light';
  const accentId = accentMap[themeId];
  if (accentId) {
    document.documentElement.dataset.accent = accentId;
  } else {
    // No attribute at all (not data-accent="default") — theme.css's accent
    // overrides only ever match a real accent id, so an absent attribute
    // correctly falls through to the active theme's own baked-in primary.
    delete document.documentElement.dataset.accent;
  }
}

theme.subscribe((value) => {
  applyThemeAndAccent(value, get(accentsByTheme));
  if (typeof localStorage !== 'undefined') localStorage.setItem(STORAGE_KEY, value);
});

accentsByTheme.subscribe((map) => {
  applyThemeAndAccent(get(theme), map);
  if (typeof localStorage !== 'undefined') localStorage.setItem(ACCENTS_STORAGE_KEY, JSON.stringify(map));
});

export function setTheme(id) {
  if (THEME_IDS.has(id)) theme.set(id);
}

// setAccent(themeId, accentId) — accentId of '' or 'default' clears that
// theme's override (falls back to its own native look) rather than
// storing a literal "default" id that would need special-casing later.
export function setAccent(themeId, accentId) {
  if (!THEME_IDS.has(themeId)) return;
  const validIds = new Set((themeById.get(themeId)?.accents ?? []).map((a) => a.id));
  accentsByTheme.update((map) => {
    const next = { ...map };
    if (!accentId || accentId === 'default' || !validIds.has(accentId)) {
      delete next[themeId];
    } else {
      next[themeId] = accentId;
    }
    return next;
  });
}
