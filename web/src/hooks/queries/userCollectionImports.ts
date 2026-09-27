import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { v2 } from "@/api/v2/request";
import {
  discoveryFromV2,
  importBodyToV2,
  importFromV2,
  syncFromV2,
} from "@/api/personalCollections";
import type {
  ImportUserMDBListCollectionRequest,
  ImportUserTMDBCollectionRequest,
  ImportUserTraktCollectionRequest,
} from "@/api/types";
import { TEMPLATE_STALE_TIME, type CollectionTemplateCatalog } from "@/lib/collectionTemplates";
import { invalidateUserCollectionQueries } from "./collectionSurfaceRefresh";
import { collectionKeys } from "./keys";

export function useUserCollectionTemplates(enabled = true) {
  return useQuery({
    queryKey: collectionKeys.templates(),
    queryFn: () =>
      v2("GET /api/v2/collections/templates").then((value) => value as CollectionTemplateCatalog),
    enabled,
    staleTime: TEMPLATE_STALE_TIME,
  });
}

export function useMDBListSearch(query: string, enabled = true) {
  const trimmed = query.trim();
  return useQuery({
    queryKey: collectionKeys.mdblistSearch(trimmed),
    queryFn: () =>
      v2("GET /api/v2/collections/import/mdblist/search", { query: { q: trimmed } }).then(
        discoveryFromV2,
      ),
    enabled: enabled && trimmed.length > 0,
    staleTime: 60_000,
  });
}

export function useMDBListTop(enabled = true) {
  return useQuery({
    queryKey: collectionKeys.mdblistTop(),
    queryFn: () => v2("GET /api/v2/collections/import/mdblist/top").then(discoveryFromV2),
    enabled,
    staleTime: 5 * 60_000,
  });
}

function importToastMessage(label: string, status: string | undefined) {
  if (status === "warning") return `${label} imported with warnings`;
  if (status === "failed") return `${label} imported but sync failed`;
  return `${label} imported`;
}

export function useImportUserMDBListCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: ImportUserMDBListCollectionRequest) =>
      v2("POST /api/v2/collections/import/mdblist", { body: importBodyToV2(body) }).then(
        importFromV2,
      ),
    onSuccess: (result) => {
      toast.success(importToastMessage("MDBList", result.sync?.status));
      void invalidateUserCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Import failed");
    },
  });
}

export function useImportUserTMDBCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: ImportUserTMDBCollectionRequest) =>
      v2("POST /api/v2/collections/import/tmdb", { body: importBodyToV2(body) }).then(importFromV2),
    onSuccess: (result) => {
      toast.success(importToastMessage("TMDB collection", result.sync?.status));
      void invalidateUserCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Import failed");
    },
  });
}

export function useImportUserTraktCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: ImportUserTraktCollectionRequest) =>
      v2("POST /api/v2/collections/import/trakt", {
        body: { ...importBodyToV2(body), preset: body.preset ?? "" },
      }).then(importFromV2),
    onSuccess: (result) => {
      toast.success(importToastMessage("Trakt collection", result.sync?.status));
      void invalidateUserCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Import failed");
    },
  });
}

export function useSyncUserCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (collectionId: string) =>
      v2("POST /api/v2/collections/{id}/sync", { path: { id: collectionId } }).then(syncFromV2),
    onSuccess: (result, collectionId) => {
      const matched = `${result.items_matched} item${result.items_matched === 1 ? "" : "s"}`;
      const message =
        result.status === "warning"
          ? `Synced with warnings — matched ${matched}`
          : `Synced — matched ${matched}`;
      toast.success(message);
      void invalidateUserCollectionQueries(queryClient, collectionId);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Sync failed");
    },
  });
}
