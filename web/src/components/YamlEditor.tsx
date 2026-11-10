import { useMemo, useRef, type KeyboardEvent } from 'react';

/** Monospace textarea with a line-number gutter. */
export function YamlEditor({
  value,
  onChange,
  errorLines = [],
  height,
}: {
  value: string;
  onChange: (v: string) => void;
  errorLines?: number[];
  height?: number;
}) {
  const gutter = useRef<HTMLDivElement>(null);
  const lines = useMemo(() => value.split('\n').length, [value]);
  const errs = useMemo(() => new Set(errorLines), [errorLines]);

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Tab' && !e.shiftKey) {
      e.preventDefault();
      const el = e.currentTarget;
      const { selectionStart: a, selectionEnd: b } = el;
      const next = `${value.slice(0, a)}  ${value.slice(b)}`;
      onChange(next);
      requestAnimationFrame(() => {
        el.selectionStart = el.selectionEnd = a + 2;
      });
    }
  };

  return (
    <div className="code-editor" style={height ? { height } : undefined}>
      <div className="code-gutter" ref={gutter} aria-hidden="true">
        {Array.from({ length: lines }, (_, i) => (
          <div key={i} className={errs.has(i + 1) ? 'err' : undefined}>
            {i + 1}
          </div>
        ))}
        <div style={{ height: 40 }} />
      </div>
      <textarea
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        autoCorrect="off"
        wrap="off"
        value={value}
        aria-label="Scenario YAML"
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        onScroll={(e) => {
          if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
        }}
      />
    </div>
  );
}
