import { t, type MessageKey } from "@/i18n";

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
const EXTRA_KIND_LABELS: Record<string, MessageKey> = {
  trailer: "web.detail.extras.kind.trailer",
  teaser: "web.detail.extras.kind.teaser",
  featurette: "web.detail.extras.kind.featurette",
  clip: "web.detail.extras.kind.clip",
  behind_the_scenes: "web.detail.extras.kind.behindTheScenes",
  bloopers: "web.detail.extras.kind.bloopers",
  deleted_scene: "web.detail.extras.kind.deletedScene",
  other: "web.detail.extras.kind.otherKind",
};

/** Plural group label, used for section headings when grouping by kind. */
const EXTRA_KIND_GROUP_LABELS: Record<string, MessageKey> = {
  trailer: "web.detail.extras.kindGroup.trailer",
  teaser: "web.detail.extras.kindGroup.teaser",
  featurette: "web.detail.extras.kindGroup.featurette",
  clip: "web.detail.extras.kindGroup.clip",
  behind_the_scenes: "web.detail.extras.kindGroup.behindTheScenes",
  bloopers: "web.detail.extras.kindGroup.bloopers",
  deleted_scene: "web.detail.extras.kindGroup.deletedScene",
  other: "web.detail.extras.kindGroup.otherKind",
};

/**
 * Kinds providers can return, offered in the library allow-list picker.
 * "deleted_scene" is local-only — providers never emit it — so it is omitted.
 */
export const PROVIDER_TRAILER_KINDS: ExtraKind[] = EXTRA_KINDS.filter(
  (kind) => kind !== "deleted_scene",
);

export function extraKindLabel(kind: string): string {
  return t(EXTRA_KIND_LABELS[kind] ?? "web.detail.extras.kind.otherKind");
}

export function extraKindGroupLabel(kind: string): string {
  return t(EXTRA_KIND_GROUP_LABELS[kind] ?? "web.detail.extras.kindGroup.otherKind");
}
