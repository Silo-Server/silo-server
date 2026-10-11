/**
 * The Synced list step of the collection editor: what a new synced list
 * follows, the ready-made picks the step offers, and how a pick fills the
 * draft without overwriting what the person typed.
 */
import type { MDBListListSummary } from "@/api/types";
import { isValidTMDBListURL } from "@/lib/tmdbList";
import type {
  CollectionTemplate,
  CollectionTemplateGroup,
  CollectionTemplateSource,
} from "@/lib/collectionTemplates";

import {
  FRANCHISE_ID_INVALID,
  MDBLIST_LINK_INVALID,
  PICK_A_CHART,
  TMDB_LIST_INVALID,
} from "./copy";
import type { ListDraft } from "./scope";
import {
  chartMediaKind,
  chartName,
  normalizeChart,
  sameChart,
  type TMDBChart,
} from "./tmdbSources";

/** The sources a scope can create a synced list from (`import_sources`). */
export type ImportSource = "mdblist" | "tmdb" | "tmdb_list";
/** The tabs of the Synced list step; TMDB imports are charts. */
export type SyncedTab = "mdblist" | "tmdb_chart" | "tmdb_list";
export type ListMediaKind = "movie" | "tv" | "mixed";

export const TAB_OF_SOURCE: Readonly<Record<ImportSource, SyncedTab>> = {
  mdblist: "mdblist",
  tmdb: "tmdb_chart",
  tmdb_list: "tmdb_list",
};

/** The list a new synced collection follows. `pickId` names the ready-made pick it came from. */
export type SyncedList = { mediaKind: ListMediaKind; pickId?: string } & (
  | { source: "mdblist"; url: string }
  | { source: "tmdb_chart"; chart: TMDBChart }
  | { source: "tmdb_list"; url: string }
);

/** The fields a pick fills in. */
interface PickFields {
  name: string;
  description: string;
  limit?: number;
  schedule: string;
}

export interface SyncedDraft {
  /** Null until a list is picked or a valid link is pasted. */
  list: SyncedList | null;
  /** The MDBList and TMDB list links, as typed. */
  mdblistLink: string;
  tmdbListLink: string;
  /** Blank takes the whole list (up to 500). */
  limit?: number;
  /** Server: a cron expression. Personal: a schedule name. "" = no scheduled sync. */
  schedule: string;
  /** The pick's poster, sent as `poster_url` unless the artwork slot replaces or removes it. */
  posterUrl?: string;
  /** What the last pick filled in: a field still holding it counts as untouched. */
  filled: PickFields;
  /** Fields the last pick left alone because they had been changed by hand. */
  kept: Array<"name" | "description">;
}

/** What picking a list puts into the draft. */
export interface SyncedPick extends PickFields {
  list: SyncedList | null;
  posterUrl?: string;
}

export function emptySyncedDraft(): SyncedDraft {
  return {
    list: null,
    mdblistLink: "",
    tmdbListLink: "",
    schedule: "",
    filled: { name: "", description: "", schedule: "" },
    kept: [],
  };
}

// --- Links ------------------------------------------------------------------

const MDBLIST_HOSTS = new Set(["mdblist.com", "www.mdblist.com"]);
/** `URL.port` is "" for the scheme's default port. */
const MDBLIST_PORTS = new Set(["", "80", "443"]);

/** The link as sent: no `?query`, `#fragment` or trailing slash; a `/json` ending stays. */
export function cleanMDBListLink(raw: string): string {
  return raw.trim().replace(/#.*$/, "").replace(/\?.*$/, "").replace(/\/+$/, "");
}

/** A franchise list's TMDB collection ID as typed: a positive whole number, or null. */
export function franchiseIdOf(typed: string): number | null {
  const trimmed = typed.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const id = Number(trimmed);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}

/**
 * True for a list page on mdblist.com (`/lists/...`), with or without `/json`.
 * Mirrors the server's `ValidateMDBListURL`: no credentials, and no port but 80 or 443.
 */
export function isMDBListLink(link: string): boolean {
  let parsed: URL;
  try {
    parsed = new URL(link);
  } catch {
    return false;
  }
  return (
    (parsed.protocol === "https:" || parsed.protocol === "http:") &&
    parsed.username === "" &&
    parsed.password === "" &&
    MDBLIST_PORTS.has(parsed.port) &&
    MDBLIST_HOSTS.has(parsed.hostname.toLowerCase().replace(/\.$/, "")) &&
    /^\/lists\/[^/]+/.test(parsed.pathname)
  );
}

// --- Picks ------------------------------------------------------------------

const IMPORT_SOURCE_OF: Partial<Record<CollectionTemplateSource, ImportSource>> = {
  mdblist: "mdblist",
  tmdb: "tmdb",
  tmdb_list: "tmdb_list",
};

/**
 * A template the scope can create on its own: its source is one of the
 * scope's `import_sources` and it needs no profile. Discover and Franchise
 * templates come only with Starter packs.
 */
export function isCreatableTemplate(
  template: CollectionTemplate,
  importSources: readonly string[],
): boolean {
  const source = IMPORT_SOURCE_OF[template.source];
  return Boolean(source && importSources.includes(source) && !template.requires_profile);
}

/** The list a template follows; null for a template that asks for a link instead. */
function templateList(template: CollectionTemplate): SyncedList | null {
  const base = { mediaKind: template.media_kind, pickId: template.id };
  if (template.source === "tmdb" && template.tmdb) {
    const { preset, media_type: mediaType, time_window: timeWindow } = template.tmdb;
    return {
      ...base,
      source: "tmdb_chart",
      chart: normalizeChart({ preset, mediaType, timeWindow }),
    };
  }
  if (template.source === "mdblist" || template.source === "tmdb_list") {
    const url = (
      template.source === "mdblist" ? template.mdblist : template.tmdb_list
    )?.url?.trim();
    return url ? { ...base, source: template.source, url } : null;
  }
  return null;
}

/**
 * The ready-made picks: creatable templates that name their list, grouped by
 * the catalog's categories. "Bring your own list" templates are left out; the
 * step has its own link fields.
 */
export function popularPicks(
  groups: readonly CollectionTemplateGroup[],
  importSources: readonly string[],
): CollectionTemplateGroup[] {
  return groups
    .map((group) => ({
      ...group,
      templates: group.templates.filter(
        (template) => isCreatableTemplate(template, importSources) && templateList(template),
      ),
    }))
    .filter((group) => group.templates.length > 0);
}

/**
 * A personal list syncs on a named schedule; a template's cron maps to the
 * nearest one, never more often than daily.
 */
export function namedSchedule(cron: string | undefined): string {
  if (!cron) return "";
  const fields = cron.trim().split(/\s+/);
  if (fields.length !== 5) return "daily";
  const [, , dom, month, dow] = fields;
  if (dom === "1" && month === "*") return "monthly";
  if (dom === "*" && month === "*" && dow !== "*") return "weekly";
  return "daily";
}

export function templatePick(
  template: CollectionTemplate,
  scope: "server" | "personal",
): SyncedPick {
  return {
    list: templateList(template),
    name: template.title,
    description: template.description,
    posterUrl: template.poster_path || undefined,
    limit: template.default_limit || undefined,
    schedule:
      scope === "server"
        ? (template.default_sync_schedule ?? "")
        : namedSchedule(template.default_sync_schedule),
  };
}

function mdblistMediaKind(mediatype: string): ListMediaKind {
  if (mediatype === "movie") return "movie";
  if (mediatype === "show") return "tv";
  return "mixed";
}

/** A list found in MDBList search: its own name and description, and nothing else. */
export function mdblistPick(list: MDBListListSummary): SyncedPick {
  return {
    list: {
      source: "mdblist",
      url: `${list.url}/json`,
      pickId: mdblistPickId(list),
      mediaKind: mdblistMediaKind(list.mediatype),
    },
    name: list.name,
    description: list.description ?? "",
    schedule: "",
  };
}

export function mdblistPickId(list: Pick<MDBListListSummary, "id">): string {
  return `mdblist:${list.id}`;
}

/** A chart: the ready-made pick for it when there is one, otherwise just its name. */
export function chartPick(
  chart: TMDBChart,
  templates: readonly CollectionTemplate[],
  scope: "server" | "personal",
): SyncedPick {
  const template = templates.find(
    (entry) =>
      entry.source === "tmdb" &&
      entry.tmdb &&
      sameChart(chart, {
        preset: entry.tmdb.preset,
        mediaType: entry.tmdb.media_type,
        timeWindow: entry.tmdb.time_window,
      }),
  );
  if (template) return templatePick(template, scope);
  const normalized = normalizeChart(chart);
  return {
    list: { source: "tmdb_chart", chart: normalized, mediaKind: chartMediaKind(normalized) },
    name: chartName(normalized),
    description: "",
    schedule: "",
  };
}

/** A pasted link: the list it names, once it is a valid one. */
export function linkPick(list: SyncedList | null): SyncedPick {
  return { list, name: "", description: "", schedule: "" };
}

/**
 * The draft with `pick` as its list. Name, description, max titles and
 * schedule take the pick's value only while they still hold what the last
 * pick filled in; a field changed by hand stays, and Name and Description say
 * so through `kept`.
 */
export function applyPick<
  Draft extends { name: string; description: string; synced?: SyncedDraft },
>(draft: Draft, pick: SyncedPick): Draft {
  const synced = draft.synced ?? emptySyncedDraft();
  const { filled } = synced;
  const take = <T>(current: T, previous: T, next: T) => (current === previous ? next : current);
  // A name or description cleared by hand is untouched too.
  const typed = (field: "name" | "description") =>
    draft[field].trim() !== "" && draft[field] !== filled[field];
  const kept = (["name", "description"] as const).filter(
    (field) => typed(field) && draft[field] !== pick[field],
  );
  return {
    ...draft,
    name: typed("name") ? draft.name : pick.name,
    description: typed("description") ? draft.description : pick.description,
    synced: {
      ...synced,
      list: pick.list,
      posterUrl: pick.posterUrl,
      limit: take(synced.limit, filled.limit, pick.limit),
      schedule: take(synced.schedule, filled.schedule, pick.schedule),
      filled: {
        name: pick.name,
        description: pick.description,
        limit: pick.limit,
        schedule: pick.schedule,
      },
      kept,
    },
  };
}

/** The library kinds a list can match into; none when it holds movies and shows. */
export function eligibleLibraryKinds(mediaKind: ListMediaKind | undefined): string[] | undefined {
  if (mediaKind === "movie") return ["movies"];
  if (mediaKind === "tv") return ["series"];
  return undefined;
}

// --- A saved list ---------------------------------------------------------------

/** Why a saved list's changed source can't be saved yet; null when it can. */
export function savedListProblem(list: ListDraft): string | null {
  switch (list.source) {
    case "mdblist":
      return isMDBListLink(cleanMDBListLink(list.link)) ? null : MDBLIST_LINK_INVALID;
    case "tmdb_list":
      return isValidTMDBListURL(list.link) ? null : TMDB_LIST_INVALID;
    case "tmdb_chart":
      return list.chart ? null : PICK_A_CHART;
    case "tmdb_franchise":
      return franchiseIdOf(list.franchiseId) ? null : FRANCHISE_ID_INVALID;
    default:
      return null;
  }
}

/** The kind of titles a saved list holds, as far as its source says. */
export function savedListMediaKind(list: ListDraft): ListMediaKind {
  const media = list.stored.media_type;
  switch (list.source) {
    case "tmdb_chart":
      return list.chart ? chartMediaKind(list.chart) : "mixed";
    case "tmdb_franchise":
      return "movie";
    case "tmdb_discover":
    case "trakt":
      return media === "movie" || media === "tv" ? media : "mixed";
    default:
      return "mixed";
  }
}
