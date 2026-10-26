import type { ResolvedSectionLayout } from "@/api/types";

function planNextHomeSectionRequests(input: {
  prioritizedIds: string[];
  loadedIds: Set<string>;
  inFlightIds: Set<string>;
  limit: number;
}): string[] {
  return input.prioritizedIds
    .filter((id) => !input.loadedIds.has(id) && !input.inFlightIds.has(id))
    .slice(0, input.limit);
}

function getPrioritizedHomeSectionIds(layout: ResolvedSectionLayout[]): string[] {
  const featured = layout.find((section) => section.featured);
  if (!featured) {
    return layout.map((section) => section.id);
  }

  return [
    featured.id,
    ...layout.filter((section) => section.id !== featured.id).map((section) => section.id),
  ];
}

export function planNextHomeSectionBatch(input: {
  layout: ResolvedSectionLayout[];
  loadedIds: Set<string>;
  inFlightIds: Set<string>;
  maxConcurrentRequests: number;
}): string[] {
  return planNextHomeSectionRequests({
    prioritizedIds: getPrioritizedHomeSectionIds(input.layout),
    loadedIds: input.loadedIds,
    inFlightIds: input.inFlightIds,
    limit: Math.max(0, input.maxConcurrentRequests - input.inFlightIds.size),
  });
}
