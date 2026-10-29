import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SettingsRow,
} from "../fork";
import { useZoom, zoomStore, ZOOM_LEVELS } from "./zoom-store";

/**
 * "Panel zoom" in Preferences → Appearance.
 *
 * Host-owned copy, like the MOCT settings group: this row describes a property
 * of the dsh panel, which the fork's translations know nothing about. It is
 * threaded in through `PreferencesTab`'s `extraRows`, so the web and desktop
 * apps keep the page they have.
 *
 * The description names the limit out loud — dialogs and menus stay at their
 * own size — because a reader who scales the panel will notice it, and a row
 * that promised more would be wrong rather than merely terse.
 */
export function PanelZoomRow() {
  const zoom = useZoom();
  const options = ZOOM_LEVELS.map((level) => ({
    value: String(level),
    label: `${level}%`,
  }));

  return (
    <SettingsRow
      anchor="panel-zoom"
      label="Panel zoom"
      description="Scales this panel. Dialogs and menus keep their own size."
      size="select"
    >
      <Select
        items={options}
        value={String(zoom)}
        onValueChange={(next) => {
          if (next) zoomStore.set(Number(next));
        }}
      >
        <SelectTrigger size="sm" className="w-full" aria-label="Panel zoom">
          <SelectValue>{`${zoom}%`}</SelectValue>
        </SelectTrigger>
        <SelectContent align="end">
          {options.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </SettingsRow>
  );
}
