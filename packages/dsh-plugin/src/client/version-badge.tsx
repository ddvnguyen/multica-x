import { mmxBuild } from "../fork";

/**
 * The bottom-right build stamp, on every page `IslandPages` renders (M-155).
 *
 * Small, low contrast, and absolutely positioned against
 * `.multica-island-root` (which carries `position: relative` for it): the
 * popup seat sits at `z-index: 60` and the toast zone owns the same corner,
 * so both paint above the stamp rather than under it — the badge never covers
 * a popup. The tooltip carries what the compact stamp cannot: the UTC build
 * time and the short sha, which is what turns "it broke on dsh-z" into one
 * commit.
 *
 * A host that injected no build renders nothing; the island bundle always
 * injects one, so an empty badge in practice means a broken build, and a
 * placeholder string there would hide exactly that.
 */
export function IslandVersionBadge() {
  const build = mmxBuild();
  if (build === null) return null;
  return (
    <div
      data-testid="mmx-badge"
      title={`${build.utc} · ${build.sha}`}
      className="pointer-events-auto absolute right-3 bottom-1.5 z-0 cursor-default text-micro text-muted-foreground/60 select-none"
    >
      {build.stamp}
    </div>
  );
}
