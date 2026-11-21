export default function UseTiersSwitch({ useTiers, tiers, value, onToggle }) {
  const selected = Array.isArray(value) ? value : [];
  return (
    <div>
      <button
        type="button"
        role="switch"
        aria-label="Use tiers"
        aria-checked={useTiers}
        onClick={() => onToggle(!useTiers)}
      >
        Use tiers
      </button>
      {useTiers ? (
        <div>
          {tiers.map((t) => (
            <label key={t.id}>
              <input type="checkbox" checked={selected.includes(t.id)} readOnly />
              {t.name}
            </label>
          ))}
        </div>
      ) : (
        <select aria-label="model tier" value={value || ''} onChange={() => {}}>
          <option value="">none</option>
          {tiers.map((t) => (
            <option key={t.id} value={t.id}>{t.name}</option>
          ))}
        </select>
      )}
    </div>
  );
}
