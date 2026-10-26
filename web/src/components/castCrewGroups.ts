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

const LEAD_MAX = 2;

/**
 * Headline crew for a title's "Cast & Crew" row: the director (or series
 * creator) first, then writers. A series leads with every one of its Creator
 * credits and falls back to its Director credits, captioned "Director", when it
 * has none. Directors are capped at two cards.
 */
export function buildCrewGroups(crew: CrewMember[], leadRole: "Director" | "Creator"): CrewGroup[] {
  const lead = leadCredits(crew, leadRole);
  // A writer-director already has a card in the lead group. Only the lead
  // cards the row shows count, so a director past the cap still shows as a writer.
  const shownLeadKeys = new Set(uniqueByPerson(lead.members).slice(0, lead.max).map(creditKey));
  return [
    {
      label: lead.role,
      role: lead.role,
      members: lead.members,
      max: lead.max,
    },
    {
      label: "Writers",
      role: "Writer",
      members: crew.filter(
        (c) => (c.job === "Writer" || c.job === "Screenplay") && !shownLeadKeys.has(creditKey(c)),
      ),
      max: 3,
    },
  ];
}

function leadCredits(
  crew: CrewMember[],
  leadRole: "Director" | "Creator",
): { role: "Director" | "Creator"; members: CrewMember[]; max: number } {
  if (leadRole === "Creator") {
    const creators = crew.filter((c) => c.job === "Creator");
    // Every creator gets a card; a series rarely has more than a handful.
    if (creators.length > 0) return { role: "Creator", members: creators, max: Infinity };
  }
  return { role: "Director", members: crew.filter((c) => c.job === "Director"), max: LEAD_MAX };
}

function creditKey(credit: CrewMember): string {
  return credit.person_id || credit.name;
}

function uniqueByPerson(credits: CrewMember[]): CrewMember[] {
  const seen = new Set<string>();
  return credits.filter((credit) => {
    const key = creditKey(credit);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}
