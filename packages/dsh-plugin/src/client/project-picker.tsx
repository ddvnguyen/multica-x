/**
 * The header's project picker (M-173).
 *
 * One global filter for every MMX view, sitting between the back/forward pair
 * and the tab strip so it reads as chrome rather than page state. "All
 * projects" is the default and the reset; the rest is the workspace's project
 * list, which the island loads with the fork's own query so the picker and the
 * pages share one cache entry.
 */

import type { Project } from "../fork";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../fork";

/** Label of the "no project filter" row. */
export const ALL_PROJECTS_LABEL = "All projects";

/** The sentinel value the Select needs for "no filter". */
const ALL_PROJECTS_VALUE = "__all__";

export function ProjectPicker({
  projects,
  projectId,
  onSelect,
}: {
  /** The workspace's projects; empty while the list loads or when there are none. */
  projects: readonly Project[];
  /** Selected project id, or `null` for every project. */
  projectId: string | null;
  onSelect: (projectId: string | null) => void;
}) {
  const selected = projects.find((project) => project.id === projectId) ?? null;
  return (
    <Select
      items={[
        { value: ALL_PROJECTS_VALUE, label: ALL_PROJECTS_LABEL },
        ...projects.map((project) => ({
          value: project.id,
          label: project.title,
        })),
      ]}
      value={selected === null ? ALL_PROJECTS_VALUE : selected.id}
      onValueChange={(value) =>
        onSelect(value === ALL_PROJECTS_VALUE ? null : value)
      }
    >
      <SelectTrigger
        size="sm"
        className="w-40 shrink-0"
        aria-label="Project"
        // The panel is not a form: the picker only narrows what is already on
        // screen, and a submit on Enter would move the user out of the tabs.
        data-project-picker=""
      >
        <SelectValue>
          {selected === null ? ALL_PROJECTS_LABEL : selected.title}
        </SelectValue>
      </SelectTrigger>
      <SelectContent align="start">
        <SelectGroup>
          <SelectItem value={ALL_PROJECTS_VALUE}>
            {ALL_PROJECTS_LABEL}
          </SelectItem>
          {projects.map((project) => (
            <SelectItem key={project.id} value={project.id}>
              {project.title}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}
