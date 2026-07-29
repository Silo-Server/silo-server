import type { AutoscanPathRewrite, AutoscanWebhookProvider, Library } from "@/api/types";

/**
 * The arr notification triggers Silo actually consumes.
 *
 * Kept in sync with internal/autoscan/arrwebhook/parse.go: importEventTypes
 * ("Download"/"Import"/"Upgrade"/"DownloadComplete"), the "Rename" event, and
 * deleteEventTypes ("EpisodeFileDelete"/"MovieFileDelete"). Anything the host
 * ignores is deliberately absent — telling an operator to tick a box that does
 * nothing is how "I followed the steps and it didn't work" starts.
 *
 * Labels are the checkbox captions as they appear in Sonarr/Radarr's Connect →
 * Webhook screen, so they can be matched by eye without translation.
 */
export interface WebhookTrigger {
  /** Caption in the arr UI. */
  label: string;
  /** Why Silo wants it, shown as helper text. */
  reason: string;
  /** False for triggers that are useful but not needed for the basic case. */
  required: boolean;
}

const SHARED_TRIGGERS: WebhookTrigger[] = [
  {
    label: "On Import",
    reason: "The main one — fires when a download finishes and is moved into your library.",
    required: true,
  },
  {
    label: "On Upgrade",
    reason: "Fires when an existing file is replaced by a better quality version.",
    required: true,
  },
  {
    label: "On Rename",
    reason: "Fires when files are renamed, so Silo does not lose track of them.",
    required: false,
  },
];

const SONARR_TRIGGERS: WebhookTrigger[] = [
  ...SHARED_TRIGGERS,
  {
    label: "On Episode File Delete",
    reason: "Lets Silo drop episodes you removed, instead of leaving dead entries.",
    required: false,
  },
];

const RADARR_TRIGGERS: WebhookTrigger[] = [
  ...SHARED_TRIGGERS,
  {
    label: "On Movie File Delete",
    reason: "Lets Silo drop movies you removed, instead of leaving dead entries.",
    required: false,
  },
];

/**
 * Triggers to tick for a provider. "auto" shows the union, since the operator
 * has not told us which service will post and either set is plausible.
 */
export function triggersFor(provider: AutoscanWebhookProvider | "auto"): WebhookTrigger[] {
  if (provider === "sonarr") return SONARR_TRIGGERS;
  if (provider === "radarr") return RADARR_TRIGGERS;
  return [
    ...SHARED_TRIGGERS,
    {
      label: "On Episode File Delete / On Movie File Delete",
      reason: "Whichever your service offers — lets Silo drop files you removed.",
      required: false,
    },
  ];
}

/** Where the webhook is configured, for the on-screen instructions. */
export function settingsPathFor(provider: AutoscanWebhookProvider | "auto"): string {
  const service =
    provider === "sonarr" ? "Sonarr" : provider === "radarr" ? "Radarr" : "Sonarr/Radarr";
  return `${service} → Settings → Connect → + → Webhook`;
}

// --- Path mapping ----------------------------------------------------------

/**
 * One row of the mapping the operator fills in: the path the arr reports
 * (`from`) and the Silo path it corresponds to (`to`).
 *
 * Webhook sources cannot use the /suggest endpoint — it resolves an arr's root
 * folders over the API and requires a bound connection, which a webhook source
 * by definition does not have. So the `to` side is seeded from real library
 * paths and the operator supplies the `from` side.
 */
export interface MappingDraft {
  /**
   * Stable client-side identity for React keys. Rows are reorderable and
   * removable, so keying on array index would remap DOM nodes by position and
   * throw focus (and IME composition) into the wrong field mid-edit.
   */
  id: string;
  from: string;
  to: string;
}

let mappingSeq = 0;

/** Build a draft row with a fresh client-side id. */
export function newMapping(to = "", from = ""): MappingDraft {
  mappingSeq += 1;
  return { id: `map-${mappingSeq}`, from, to };
}

function libraryKind(type: string): "movie" | "tv" | "mixed" | null {
  switch (type.trim().toLowerCase()) {
    case "movie":
    case "movies":
      return "movie";
    case "series":
    case "show":
    case "shows":
    case "tv":
    case "tvshows":
      return "tv";
    case "mixed":
      return "mixed";
    default:
      return null;
  }
}

/**
 * Collapse paths to their shared ancestors.
 *
 * A rewrite replaces a matched prefix and keeps the remaining suffix verbatim
 * (see applyRewrites in internal/autoscan/rewrite.go), so one rule for
 * `/mnt/media` covers `/mnt/media/movies/80s` and everything else beneath it —
 * but only when the arr-side tree mirrors the Silo-side tree below that point.
 *
 * That holds when a single mount is involved, which is the common case: a real
 * install here has 96 library paths across 2 mounts, so per-path rows would be
 * 96 lines of near-identical noise. It does NOT hold when the operator has to
 * supply different arr-side roots per branch (`/downloads/films` for movies,
 * `/downloads/series` for TV) — collapsing those to one row would silently
 * mis-map one of them.
 *
 * The seeded rows are a starting point, not a constraint: an operator whose arr
 * exposes each branch under a different root can delete the collapsed row and
 * add one per branch. `expandedRootsFor` supplies that breakdown so the editor
 * can offer it rather than making them retype paths.
 */
export function collapseToRoots(paths: readonly string[]): string[] {
  const cleaned = paths
    .map((path) => path.trim().replace(/\/+$/, ""))
    .filter((path) => path.startsWith("/"));

  const unique = [...new Set(cleaned)];
  if (unique.length === 0) return [];

  // Group by mount point — the first two segments, e.g. "/mnt/sharedrives".
  // Paths under different mounts are separate storage and must never merge,
  // but one mount's tree is almost always exposed to the arr as a single
  // remapped volume, so it needs one rule rather than one per leaf.
  const groups = new Map<string, string[]>();
  for (const path of unique) {
    const segments = path.split("/").filter(Boolean);
    const mount = "/" + segments.slice(0, 2).join("/");
    const existing = groups.get(mount);
    if (existing) existing.push(path);
    else groups.set(mount, [path]);
  }

  const roots: string[] = [];
  for (const [mount, members] of groups) {
    // A lone member is left exactly as supplied — widening it would claim more
    // of the filesystem than the operator actually configured.
    roots.push(members.length === 1 ? members[0]! : commonAncestor(members) || mount);
  }
  return roots;
}

/**
 * The next level down from a collapsed root: the distinct child directories the
 * supplied paths actually live under.
 *
 * A rewrite keeps the suffix after the matched prefix, so one row per mount is
 * only correct while the arr sees the same tree below that point. When it does
 * not — `/downloads/films` for movies but `/downloads/series` for TV — the
 * operator needs a row per branch. Offering these lets the editor expand a
 * collapsed row instead of asking them to retype the Silo side.
 *
 * Returns [] when there is nothing more specific to offer.
 */
export function expandedRootsFor(root: string, paths: readonly string[]): string[] {
  const prefix = root.replace(/\/+$/, "");
  const children = new Set<string>();

  for (const path of paths) {
    const cleaned = path.trim().replace(/\/+$/, "");
    if (!cleaned.startsWith(`${prefix}/`)) continue;
    const rest = cleaned
      .slice(prefix.length + 1)
      .split("/")
      .filter(Boolean);
    const next = rest[0];
    if (next) children.add(`${prefix}/${next}`);
  }

  // One child is the same rule as the root, so it is not an alternative.
  return children.size > 1 ? [...children] : [];
}

/**
 * Longest path prefix shared by every input, truncated at a segment boundary.
 * Returns "" when the inputs share nothing below the root.
 */
function commonAncestor(paths: readonly string[]): string {
  const split = paths.map((path) => path.split("/").filter(Boolean));
  const first = split[0];
  if (!first) return "";

  const shared: string[] = [];
  for (let i = 0; i < first.length; i++) {
    const segment = first[i];
    if (!split.every((segments) => segments[i] === segment)) break;
    shared.push(segment!);
  }
  return shared.length > 0 ? "/" + shared.join("/") : "";
}

/**
 * Seed mapping rows from the libraries a provider could plausibly feed: Sonarr
 * fills TV (and mixed) libraries, Radarr fills movie (and mixed) ones. Each row
 * starts with a real Silo path on the `to` side and a blank `from` for the
 * operator to complete.
 *
 * Rows are the collapsed roots rather than every library path, so an operator
 * fills in a handful of mappings instead of dozens of near-identical ones.
 */
export function seedMappings(
  provider: AutoscanWebhookProvider | "auto",
  libraries: readonly Library[],
): MappingDraft[] {
  const want = provider === "sonarr" ? "tv" : provider === "radarr" ? "movie" : null;

  const paths: string[] = [];
  for (const library of libraries) {
    if (!library.enabled) continue;
    const kind = libraryKind(library.type);
    if (!kind) continue;
    // A null `want` (auto) accepts everything; otherwise mixed always counts.
    if (want !== null && kind !== want && kind !== "mixed") continue;
    for (const path of library.paths ?? []) {
      paths.push(path);
    }
  }

  return collapseToRoots(paths).map((to) => newMapping(to));
}

/**
 * Drop incomplete rows and trim. A half-filled row is treated as "not yet
 * configured" rather than an error, so an operator can leave one blank and
 * still save the rest.
 */
export function usableMappings(drafts: readonly MappingDraft[]): AutoscanPathRewrite[] {
  return drafts
    .map((draft) => ({ from: draft.from.trim(), to: draft.to.trim() }))
    .filter((draft) => draft.from.length > 0 && draft.to.length > 0);
}

/**
 * Whether the operator has supplied at least one complete mapping. Without one,
 * a webhook source accepts deliveries and resolves nothing — the silent failure
 * this whole flow exists to prevent.
 */
export function hasUsableMapping(drafts: readonly MappingDraft[]): boolean {
  return usableMappings(drafts).length > 0;
}
