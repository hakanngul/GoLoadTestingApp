import type { KV } from '../lib/scenario';
import { Icon } from './ui';

/** Editable key/value rows (headers). */
export function KVEditor({
  rows,
  onChange,
  keyPlaceholder = 'Header',
  valuePlaceholder = 'Value',
  addLabel = 'Add header',
}: {
  rows: KV[];
  onChange: (rows: KV[]) => void;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  addLabel?: string;
}) {
  const set = (i: number, patch: Partial<KV>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="stack" style={{ gap: 6 }}>
      {rows.map((r, i) => (
        <div className="row" key={i}>
          <input
            className="input mono"
            style={{ flex: '0 0 38%' }}
            placeholder={keyPlaceholder}
            value={r.key}
            onChange={(e) => set(i, { key: e.target.value })}
          />
          <input
            className="input mono"
            placeholder={valuePlaceholder}
            value={r.value}
            onChange={(e) => set(i, { value: e.target.value })}
          />
          <button
            className="btn ghost icon sm"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
            aria-label="Remove row"
            title="Remove"
          >
            <Icon name="x" size={14} />
          </button>
        </div>
      ))}
      <div>
        <button className="btn sm ghost" onClick={() => onChange([...rows, { key: '', value: '' }])}>
          <Icon name="plus" size={12} /> {addLabel}
        </button>
      </div>
    </div>
  );
}
