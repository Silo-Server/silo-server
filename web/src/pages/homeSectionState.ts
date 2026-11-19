import type { ResolvedSection, ResolvedSectionLayout } from "@/api/types";
import { holdsCollections } from "@/lib/sectionTypes";

export type HomeSectionSlotState = "loading" | "ready" | "empty" | "error";

export interface HomeSectionSlot {
  layout: ResolvedSectionLayout;
  state: HomeSectionSlotState;
  section?: ResolvedSection;
}

export interface HomeSectionViewModel {
  hero: HomeSectionSlot | null;
  rows: HomeSectionSlot[];
}

export function buildHomeSectionViewModel(input: {
  layout: ResolvedSectionLayout[];
  loadedSections: Map<string, ResolvedSection>;
  failedIds: Set<string>;
}): HomeSectionViewModel {
  // A row of collections has no title to show as the hero.
  const heroLayout =
    input.layout.find((section) => section.featured && !holdsCollections(section.section_type)) ??
    null;
  const rowLayouts = input.layout.filter((section) => section !== heroLayout);

  return {
    hero: heroLayout ? toSlot(heroLayout, input) : null,
    rows: rowLayouts.map((section) => toSlot(section, input)),
  };
}

function toSlot(
  layout: ResolvedSectionLayout,
  input: {
    loadedSections: Map<string, ResolvedSection>;
    failedIds: Set<string>;
  },
): HomeSectionSlot {
  const section = input.loadedSections.get(layout.id);
  if (section) {
    if (section.items.length === 0 && !section.collections?.length) {
      return { layout, state: "empty", section };
    }
    return { layout, state: "ready", section };
  }

  if (input.failedIds.has(layout.id)) {
    return { layout, state: "error" };
  }

  return { layout, state: "loading" };
}
