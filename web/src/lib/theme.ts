// Colour theme: System (follows prefers-color-scheme), Light or Dark.
//
// index.html runs the same resolution inline before React renders, so the
// first paint already has the right data-theme on <html> (no flash). This
// module changes it afterwards. The choice is stored per browser.
export type ThemePref = 'system' | 'light' | 'dark';
export type Theme = 'light' | 'dark';

export const THEME_KEY = 'goload.theme';
export const THEME_PREFS: ThemePref[] = ['system', 'light', 'dark'];

export function isThemePref(v: unknown): v is ThemePref {
  return v === 'system' || v === 'light' || v === 'dark';
}

/** The effective theme for a preference. */
export function resolveTheme(pref: ThemePref, systemDark: boolean): Theme {
  if (pref === 'system') return systemDark ? 'dark' : 'light';
  return pref;
}

export function loadThemePref(): ThemePref {
  try {
    const v = localStorage.getItem(THEME_KEY);
    return isThemePref(v) ? v : 'system';
  } catch {
    return 'system';
  }
}

export function saveThemePref(p: ThemePref): void {
  try {
    if (p === 'system') localStorage.removeItem(THEME_KEY);
    else localStorage.setItem(THEME_KEY, p);
  } catch {
    /* storage unavailable: the choice lasts for this page only */
  }
}

function systemDark(): boolean {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-color-scheme: dark)').matches;
}

/** Sets data-theme / data-theme-pref on <html>. */
export function applyTheme(pref: ThemePref): Theme {
  const t = resolveTheme(pref, systemDark());
  const el = document.documentElement;
  el.dataset.theme = t;
  el.dataset.themePref = pref;
  el.style.colorScheme = t;
  return t;
}

/**
 * Keeps <html> in sync with the OS setting while the preference is
 * "system". Returns an unsubscribe function.
 */
export function watchSystemTheme(getPref: () => ThemePref): () => void {
  const mq = window.matchMedia?.('(prefers-color-scheme: dark)');
  if (!mq) return () => {};
  const on = () => {
    if (getPref() === 'system') applyTheme('system');
  };
  mq.addEventListener('change', on);
  return () => mq.removeEventListener('change', on);
}
