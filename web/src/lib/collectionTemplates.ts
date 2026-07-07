import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type {
  ImportMDBListCollectionRequest,
  ImportTMDBCollectionRequest,
  ImportTraktCollectionRequest,
} from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";

export type CollectionTemplateCategory =
  | "trending"
  | "popular"
  | "streaming"
  | "top_rated"
  | "in_theaters"
  | "upcoming"
  | "airing"
  | "editorial"
  | "custom";

export type CollectionTemplateSource =
  | "tmdb"
  | "trakt"
  | "mdblist"
  | "tmdb_discover"
  | "tmdb_collection";

export type CollectionTemplateMediaKind = "movie" | "tv" | "mixed";

// Each spec is a strict subset of the corresponding import request, so adding
// a new preset to api/types.ts automatically widens the template spec too.
export type CollectionTemplateTMDB = Pick<
  ImportTMDBCollectionRequest,
  "preset" | "media_type" | "time_window"
>;

export type CollectionTemplateTrakt = Pick<ImportTraktCollectionRequest, "preset" | "media_type">;

export type CollectionTemplateMDBList = Pick<ImportMDBListCollectionRequest, "url">;

// Discover and collection templates ship as backend-driven blueprints that
// the admin cannot tweak inline; the form surfaces a read-only summary and
// directs the admin to apply via Template Bundles.
export interface CollectionTemplateTMDBCollection {
  collection_id: number;
}

// CollectionTemplateTMDBDiscover mirrors templates.TMDBDiscoverSpec field by
// field. Discover templates are not editable via the admin import form — the
// gallery renders a read-only summary and routes apply through Template
// Bundles.
export interface CollectionTemplateTMDBDiscover {
  media_type: "movie" | "tv";
  with_genres?: number[];
  without_genres?: number[];
  sort_by: string;
  vote_count_gte?: number;
  vote_average_gte?: number;
  release_date_gte?: string;
  release_date_lte?: string;
  certifications?: string[];
  certification_lte?: string;
  with_runtime_gte?: number;
  with_runtime_lte?: number;
  original_language?: string;
}

export interface CollectionTemplate {
  id: string;
  title: string;
  description: string;
  icon: string;
  category: CollectionTemplateCategory;
  source: CollectionTemplateSource;
  media_kind: CollectionTemplateMediaKind;
  default_limit?: number;
  default_sort_order?: number;
  default_sync_schedule?: string;
  poster_path?: string;
  requires_profile?: boolean;
  featured?: boolean;
  tags?: string[];
  tmdb?: CollectionTemplateTMDB;
  trakt?: CollectionTemplateTrakt;
  mdblist?: CollectionTemplateMDBList;
  tmdb_collection?: CollectionTemplateTMDBCollection;
  tmdb_discover?: CollectionTemplateTMDBDiscover;
}

export interface CollectionTemplateGroup {
  category: CollectionTemplateCategory;
  label: string;
  templates: CollectionTemplate[];
}

export interface CollectionTemplateCatalog {
  categories: CollectionTemplateGroup[];
}

export interface CollectionTemplateBundle {
  id: string;
  title: string;
  description: string;
  template_ids: string[];
}

export interface CollectionTemplateBundleCatalog {
  bundles: CollectionTemplateBundle[];
}

export interface ApplyCollectionTemplateBundleRequest {
  library_ids: number[];
  dry_run?: boolean;
  delete_existing?: boolean;
  featured?: ApplyCollectionTemplateBundleFeaturedRequest;
}

export interface ApplyCollectionTemplateBundleJobRequest {
  library_ids: number[];
  delete_existing?: boolean;
  featured?: ApplyCollectionTemplateBundleFeaturedRequest;
}

export interface ApplyCollectionTemplateBundleFeaturedRequest {
  home?: {
    library_id: number;
    template_id: string;
  };
  libraries?: Record<string, string>;
}

export interface CollectionTemplateBundleApplyEntry {
  template_id: string;
  template_title: string;
  library_id: number;
  library_name: string;
  collection_id?: string;
  reason?: string;
}

export interface CollectionTemplateBundleCollectionEntry {
  library_id: number;
  library_name: string;
  collection_id?: string;
  collection_title?: string;
  reason?: string;
}

export interface CollectionTemplateBundleFeaturedEntry {
  surface: "home" | "library" | string;
  library_id?: number;
  library_name?: string;
  template_id: string;
  template_title: string;
  collection_id?: string;
  section_id?: string;
  reason?: string;
}

export interface ApplyCollectionTemplateBundleResponse {
  bundle_id: string;
  dry_run: boolean;
  delete_existing?: boolean;
  deleted: CollectionTemplateBundleCollectionEntry[];
  delete_skipped: CollectionTemplateBundleCollectionEntry[];
  delete_failed: CollectionTemplateBundleCollectionEntry[];
  created: CollectionTemplateBundleApplyEntry[];
  skipped: CollectionTemplateBundleApplyEntry[];
  failed: CollectionTemplateBundleApplyEntry[];
  sync_queued?: CollectionTemplateBundleApplyEntry[];
  featured: CollectionTemplateBundleFeaturedEntry[];
  featured_failed: CollectionTemplateBundleFeaturedEntry[];
}

export const TEMPLATE_STALE_TIME = 5 * 60_000;

// Largest explicit "Max Items" value the import APIs accept. Mirrors
// MaxExplicitItemLimit in internal/collectionutil — sync never scans more
// than 500 source entries, so larger limits could never be satisfied.
export const COLLECTION_MAX_ITEMS = 500;

export function fetchCollectionTemplates(): Promise<CollectionTemplateCatalog> {
  return api<CollectionTemplateCatalog>("/admin/collections/templates");
}

export function fetchCollectionTemplateBundles(): Promise<CollectionTemplateBundleCatalog> {
  return api<CollectionTemplateBundleCatalog>("/admin/collections/template-bundles");
}

export function useCollectionTemplates(enabled = true) {
  return useQuery({
    queryKey: adminKeys.collectionTemplates(),
    queryFn: fetchCollectionTemplates,
    enabled,
    staleTime: TEMPLATE_STALE_TIME,
  });
}

export function useCollectionTemplateBundles(enabled = true) {
  return useQuery({
    queryKey: adminKeys.collectionTemplateBundles(),
    queryFn: fetchCollectionTemplateBundles,
    enabled,
    staleTime: TEMPLATE_STALE_TIME,
  });
}

export function mediaKindLabel(kind: CollectionTemplateMediaKind): string {
  switch (kind) {
    case "movie":
      return "Movies";
    case "tv":
      return "TV";
    case "mixed":
      return "Movies + TV";
  }
}

export interface LibraryEligibility {
  kinds?: string[];
  hint?: string;
}

export function libraryEligibilityForMediaKind(
  kind: "movie" | "tv" | "all" | "mixed",
): LibraryEligibility {
  if (kind === "movie") {
    return {
      kinds: ["movies"],
      hint: "Movie-only source — TV-only libraries are disabled. Mixed libraries always work.",
    };
  }
  if (kind === "tv") {
    return {
      kinds: ["series"],
      hint: "TV-only source — movie-only libraries are disabled. Mixed libraries always work.",
    };
  }
  return {};
}
