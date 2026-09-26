import type {
  DiscoverBrandCard,
  DiscoverBrowseKind,
  DiscoverBrowseResponse,
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  RequestDiscoverySection,
  RequestListParams,
  RequestMediaDetail,
  RequestMediaPage,
  RequestMediaResult,
  RequestMediaType,
  RequestSearchMediaType,
  RequestTarget,
} from "@/api/types";
import { v2, type V2Body } from "@/api/v2/request";
import type { components, paths } from "@/api/v2/schema";

type Schemas = components["schemas"];
type DiscoverSectionKey =
  paths["/api/v2/requests/discover/{section}"]["get"]["parameters"]["path"]["section"];

// The v2 request bodies stay close to v1 (the mobile clients consume them);
// the adapters below only narrow the wire strings to the web's literal unions
// and unwrap the {items} envelopes, so the pages keep their existing types.

function mediaResultFromV2(r: Schemas["RequestMediaResult"]): RequestMediaResult {
  return r as RequestMediaResult;
}

function requestTargetFromV2(t: Schemas["RequestTarget"]): RequestTarget {
  return { ...t, id: Number(t.id) } as RequestTarget;
}

export function mediaRequestFromV2(r: Schemas["MediaRequest"]): MediaRequest {
  const { requested_by_user_id, targets, media_type, status, outcome, ...rest } = r;
  return {
    ...rest,
    media_type: media_type as RequestMediaType,
    status: status as MediaRequestStatus,
    outcome: outcome as MediaRequestOutcome,
    ...(requested_by_user_id !== undefined
      ? { requested_by_user_id: Number(requested_by_user_id) }
      : {}),
    targets: targets.map(requestTargetFromV2),
  };
}

export function createMediaRequestV2(body: V2Body<"POST /api/v2/requests">): Promise<MediaRequest> {
  return v2("POST /api/v2/requests", { body }).then(mediaRequestFromV2);
}

// v2 pages by cursor with a page size of at most 50; callers that asked for a
// larger window (the Requests page shows up to 100) walk the pages.
export async function listMyMediaRequestsV2(
  params: RequestListParams = {},
): Promise<MediaRequest[]> {
  const wanted =
    params.limit != null && Number.isFinite(params.limit) && params.limit > 0
      ? Math.min(100, Math.floor(params.limit))
      : 50;
  const status = params.status && params.status !== "all" ? params.status : undefined;
  const outcome = params.outcome && params.outcome !== "all" ? params.outcome : undefined;
  const out: MediaRequest[] = [];
  let cursor: string | undefined;
  while (out.length < wanted) {
    const page = await v2("GET /api/v2/requests/mine", {
      query: { status, outcome, limit: Math.min(50, wanted - out.length), cursor },
    });
    out.push(...page.items.map(mediaRequestFromV2));
    if (!page.page?.has_more || !page.page.next_cursor) break;
    cursor = page.page.next_cursor;
  }
  return out;
}

export function getMediaRequestV2(id: string): Promise<MediaRequest> {
  return v2("GET /api/v2/requests/{id}", { path: { id } }).then(mediaRequestFromV2);
}

export function searchRequestMediaV2(
  mediaType: RequestSearchMediaType,
  q: string,
  page: number,
  signal?: AbortSignal,
): Promise<RequestMediaPage> {
  return v2("GET /api/v2/requests/search", {
    query: { q, media_type: mediaType, page },
    signal,
  }).then((p) => ({ ...p, results: p.results.map(mediaResultFromV2) }));
}

export function getRequestMediaDetailV2(
  mediaType: RequestMediaType,
  tmdbID: number,
): Promise<RequestMediaDetail> {
  return v2("GET /api/v2/requests/detail/{media_type}/{tmdb_id}", {
    path: { media_type: mediaType, tmdb_id: tmdbID },
  }).then(
    (d) =>
      ({
        ...d,
        recommendations: d.recommendations.map(mediaResultFromV2),
      }) as RequestMediaDetail,
  );
}

function discoverSectionFromV2(s: Schemas["DiscoverSection"]): RequestDiscoverySection {
  return { ...s, results: s.results.map(mediaResultFromV2) };
}

export function listDiscoverSectionsV2(): Promise<RequestDiscoverySection[]> {
  return v2("GET /api/v2/requests/discover").then((c) => c.items.map(discoverSectionFromV2));
}

export function getDiscoverSectionV2(
  section: string,
  page: number,
): Promise<RequestDiscoverySection> {
  return v2("GET /api/v2/requests/discover/{section}", {
    path: { section: section as DiscoverSectionKey },
    query: { page },
  }).then(discoverSectionFromV2);
}

function brandsFromV2(c: Schemas["DiscoverBrandCollection"]): DiscoverBrandCard[] {
  return c.items;
}

export function listDiscoverStudiosV2(): Promise<DiscoverBrandCard[]> {
  return v2("GET /api/v2/requests/discover/studios").then(brandsFromV2);
}

export function listDiscoverNetworksV2(): Promise<DiscoverBrandCard[]> {
  return v2("GET /api/v2/requests/discover/networks").then(brandsFromV2);
}

export function listDiscoverGenresV2(): Promise<DiscoverBrandCard[]> {
  return v2("GET /api/v2/requests/discover/genres").then(brandsFromV2);
}

export function browseDiscoverV2(args: {
  kind: DiscoverBrowseKind;
  slug: string;
  mediaType?: RequestMediaType;
  sort: "popularity" | "vote_average" | "release_date";
  page: number;
}): Promise<DiscoverBrowseResponse> {
  const { kind, slug, mediaType, sort, page } = args;
  const query = { sort, page };
  const request =
    kind === "genre"
      ? v2("GET /api/v2/requests/discover/browse/genre/{slug}", {
          path: { slug },
          query: { ...query, media_type: mediaType ?? "movie" },
        })
      : kind === "network"
        ? v2("GET /api/v2/requests/discover/browse/network/{slug}", { path: { slug }, query })
        : v2("GET /api/v2/requests/discover/browse/studio/{slug}", { path: { slug }, query });
  return request.then(({ kind: k, media_type, sort: s, ...rest }) => ({
    ...rest,
    kind: k as DiscoverBrowseKind,
    media_type: media_type as RequestMediaType,
    sort: s as DiscoverBrowseResponse["sort"],
    results: rest.results.map(mediaResultFromV2),
  }));
}
