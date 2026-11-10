import { useEffect, useRef, useState } from 'react';
import { applyTheme, loadThemePref, saveThemePref, watchSystemTheme, type ThemePref } from '../lib/theme';
import { Icon } from './ui';

const OPTIONS: { pref: ThemePref; icon: string; label: string }[] = [
  { pref: 'system', icon: 'monitor', label: 'System theme' },
  { pref: 'light', icon: 'sun', label: 'Light theme' },
  { pref: 'dark', icon: 'moon', label: 'Dark theme' },
];

/** System | Light | Dark switch (sidebar footer). */
export function ThemeToggle() {
  const [pref, setPref] = useState<ThemePref>(loadThemePref);
  const ref = useRef(pref);
  ref.current = pref;

  useEffect(() => {
    applyTheme(pref);
  }, [pref]);
  useEffect(() => watchSystemTheme(() => ref.current), []);

  const choose = (p: ThemePref) => {
    saveThemePref(p);
    setPref(p);
  };

  return (
    <div className="theme-toggle" role="radiogroup" aria-label="Colour theme">
      {OPTIONS.map((o) => (
        <button
          key={o.pref}
          role="radio"
          aria-checked={pref === o.pref}
          aria-label={o.label}
          title={o.label}
          className={pref === o.pref ? 'on' : ''}
          onClick={() => choose(o.pref)}
        >
          <Icon name={o.icon} size={13} />
        </button>
      ))}
    </div>
  );
}
