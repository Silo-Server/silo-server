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
 * creator) first, then writers. A series leads with its Creator credits and
 * falls back to its Director credits when it has none.
 */
export function buildCrewGroups(crew: CrewMember[], leadRole: "Director" | "Creator"): CrewGroup[] {
  const leads = leadCredits(crew, leadRole);
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

function leadCredits(crew: CrewMember[], leadRole: "Director" | "Creator"): CrewMember[] {
  if (leadRole === "Creator") {
    const creators = crew.filter((c) => c.job === "Creator");
    if (creators.length > 0) return creators;
  }
  return crew.filter((c) => c.job === "Director");
}
