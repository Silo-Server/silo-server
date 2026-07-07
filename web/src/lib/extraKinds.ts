/**
 * Shared kind vocabulary for remote provider videos (ItemVideo) and local
 * extras files (ItemExtra). Keep in sync with the server's kind list.
 */
export const EXTRA_KINDS = [
  "trailer",
  "teaser",
  "featurette",
  "clip",
  "behind_the_scenes",
  "bloopers",
  "deleted_scene",
  "other",
] as const;

export type ExtraKind = (typeof EXTRA_KINDS)[number];

/** Singular label, used for individual video/extra cards. */
const EXTRA_KIND_LABELS: Record<string, string> = {
  trailer: "Trailer",
  teaser: "Teaser",
  featurette: "Featurette",
  clip: "Clip",
  behind_the_scenes: "Behind the Scenes",
  bloopers: "Bloopers",
  deleted_scene: "Deleted Scene",
  other: "Extra",
};

/** Plural group label, used for section headings when grouping by kind. */
const EXTRA_KIND_GROUP_LABELS: Record<string, string> = {
  trailer: "Trailers",
  teaser: "Teasers",
  featurette: "Featurettes",
  clip: "Clips",
  behind_the_scenes: "Behind the Scenes",
  bloopers: "Bloopers",
  deleted_scene: "Deleted Scenes",
  other: "Other",
};

/**
 * Kinds providers can return, offered in the library allow-list picker.
 * "deleted_scene" is local-only — providers never emit it — so it is omitted.
 */
export const PROVIDER_TRAILER_KINDS: ExtraKind[] = EXTRA_KINDS.filter(
  (kind) => kind !== "deleted_scene",
);

export function extraKindLabel(kind: string): string {
  return EXTRA_KIND_LABELS[kind] ?? "Extra";
}

export function extraKindGroupLabel(kind: string): string {
  return EXTRA_KIND_GROUP_LABELS[kind] ?? "Other";
}
