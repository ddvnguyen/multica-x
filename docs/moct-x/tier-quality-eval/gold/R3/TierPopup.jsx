export function shouldOpen(prevText, nextText) {
  if (typeof prevText !== 'string' || typeof nextText !== 'string') return false;
  if (nextText.length !== prevText.length + 1) return false;
  for (let i = 0; i < nextText.length; i += 1) {
    if (nextText[i] !== prevText[i]) {
      if (nextText[i] !== '#') return false;
      const before = i === 0 ? '' : nextText[i - 1];
      return before === '' || /\s/.test(before);
    }
  }
  return false;
}

export default function TierPopup({ tiers, query, onPick }) {
  const q = (query || '').toLowerCase();
  const rows = tiers
    .filter((t) => t.enabled && t.name.toLowerCase().includes(q))
    .sort((a, b) => a.idx - b.idx);
  return (
    <div role="listbox">
      {rows.map((t) => (
        <div key={t.id} role="option" aria-selected={false} onClick={() => onPick(t.id)}>
          {t.name}
        </div>
      ))}
    </div>
  );
}
