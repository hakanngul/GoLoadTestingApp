// Tests for the colour theme resolution (src/lib/theme.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { isThemePref, loadThemePref, resolveTheme, saveThemePref, THEME_KEY } from '../src/lib/theme.ts';

test('resolveTheme', () => {
  assert.equal(resolveTheme('system', true), 'dark');
  assert.equal(resolveTheme('system', false), 'light');
  assert.equal(resolveTheme('light', true), 'light');
  assert.equal(resolveTheme('dark', false), 'dark');
});

test('isThemePref', () => {
  assert.ok(isThemePref('system') && isThemePref('light') && isThemePref('dark'));
  assert.ok(!isThemePref('blue') && !isThemePref(null));
});

test('load/save survive missing or throwing storage', () => {
  const saved = globalThis.localStorage;
  try {
    delete globalThis.localStorage;
    assert.equal(loadThemePref(), 'system');
    saveThemePref('dark'); // must not throw
    const m = new Map();
    globalThis.localStorage = {
      getItem: (k) => m.get(k) ?? null,
      setItem: (k, v) => m.set(k, v),
      removeItem: (k) => m.delete(k),
    };
    saveThemePref('dark');
    assert.equal(m.get(THEME_KEY), 'dark');
    assert.equal(loadThemePref(), 'dark');
    m.set(THEME_KEY, 'bogus');
    assert.equal(loadThemePref(), 'system');
    saveThemePref('system');
    assert.ok(!m.has(THEME_KEY));
    globalThis.localStorage = {
      getItem: () => {
        throw new Error('denied');
      },
      setItem: () => {
        throw new Error('denied');
      },
      removeItem: () => {
        throw new Error('denied');
      },
    };
    assert.equal(loadThemePref(), 'system');
    saveThemePref('light');
  } finally {
    if (saved === undefined) delete globalThis.localStorage;
    else globalThis.localStorage = saved;
  }
});
