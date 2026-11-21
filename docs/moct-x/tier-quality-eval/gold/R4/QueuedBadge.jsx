export default function QueuedBadge({ explicitTier, queued, reason }) {
  if (!queued) return null;
  return (
    <span role="status">
      {explicitTier ? `@${explicitTier} ` : ''}queued: {reason}
    </span>
  );
}
