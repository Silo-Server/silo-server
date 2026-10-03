import type { CrewMember } from "@/api/types";

export interface CrewGroup {
  /** Divider label shown before the group (omitted for the first group). */
  label: string;
  /** Caption under each card, e.g. "Director". */
  role: string;
  members: CrewMember[];
  /** Most cards shown for this group. */
  max: number;
}

/**
 * Headline crew for a title's "Cast & Crew" row: the director (or series
 * creator) first, then writers. Series creators are stored as Director credits.
 */
export function buildCrewGroups(crew: CrewMember[], leadRole: "Director" | "Creator"): CrewGroup[] {
  const leads = crew.filter((c) => c.job === "Director");
  // A writer-director already has a card in the lead group.
  const leadKeys = new Set(leads.map((c) => c.person_id || c.name));
  return [
    {
      label: leadRole,
      role: leadRole,
      members: leads,
      max: 2,
    },
    {
      label: "Writers",
      role: "Writer",
      members: crew.filter(
        (c) =>
          (c.job === "Writer" || c.job === "Screenplay") && !leadKeys.has(c.person_id || c.name),
      ),
      max: 3,
    },
  ];
}
