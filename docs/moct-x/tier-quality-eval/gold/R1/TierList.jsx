export default function TierList({ tiers, onEdit, onRemove }) {
  const ordered = [...tiers].sort((a, b) => (a.idx - b.idx) || a.name.localeCompare(b.name));
  return (
    <ul>
      {ordered.map((t) => (
        <li key={t.id}>
          <span>{t.name}</span>
          {t.enabled ? (
            <>
              <button type="button" onClick={() => onEdit(t.id)}>Edit</button>
              <button type="button" onClick={() => onRemove(t.id)}>Remove</button>
            </>
          ) : (
            <>
              <span>unavailable</span>
              <button type="button" disabled>Edit</button>
              <button type="button" disabled>Remove</button>
            </>
          )}
        </li>
      ))}
    </ul>
  );
}
