import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import type { V2Body, V2Result } from "@/api/v2/request";

export type EpisodeOrderState = V2Result<"GET /api/v2/admin/items/{id}/episode-order">;
export type EpisodeOrderChoice = EpisodeOrderState["choice"];
export type EpisodeOrderPreview = V2Result<"POST /api/v2/admin/items/{id}/episode-order/preview">;
export type EpisodeOrderOption = EpisodeOrderState["providers"][number]["orders"][number];
type EpisodeOrderSave = V2Body<"PUT /api/v2/admin/items/{id}/episode-order">;

const episodeOrderKey = (contentId: string, withOrders: boolean) =>
  ["episode-order", contentId, withOrders ? "orders" : "choice"] as const;

/**
 * A series' episode order. With `withOrders`, the server also asks each
 * provider which orders it offers, which the dialog needs and the series page
 * note does not.
 */
export function useEpisodeOrder(contentId: string, enabled: boolean, withOrders: boolean) {
  return useQuery({
    queryKey: episodeOrderKey(contentId, withOrders),
    queryFn: () =>
      v2("GET /api/v2/admin/items/{id}/episode-order", {
        path: { id: contentId },
        query: { orders: withOrders },
      }),
    enabled,
    staleTime: withOrders ? 5 * 60_000 : 30_000,
  });
}

/** How the series would look under a choice; refetched as the choice changes. */
export function useEpisodeOrderPreview(
  contentId: string,
  choice: EpisodeOrderChoice | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["episode-order-preview", contentId, choice] as const,
    queryFn: ({ signal }) =>
      v2("POST /api/v2/admin/items/{id}/episode-order/preview", {
        path: { id: contentId },
        body: choice!,
        signal,
        // Each preview asks the providers for every season of the show.
        timeoutMs: 60_000,
      }),
    enabled: enabled && !!choice,
    placeholderData: keepPreviousData,
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useSaveEpisodeOrder(contentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: EpisodeOrderSave) =>
      v2("PUT /api/v2/admin/items/{id}/episode-order", { path: { id: contentId }, body }),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["episode-order", contentId] });
    },
  });
}

export type CustomSeasons = V2Result<"GET /api/v2/admin/items/{id}/custom-seasons">;
export type CustomSeason = CustomSeasons["seasons"][number];
export type CustomSeasonEpisode = CustomSeason["episodes"][number];
type CustomSeasonsSave = V2Body<"PUT /api/v2/admin/items/{id}/custom-seasons">;

/**
 * A series' arranged seasons. With `startFrom`, a fresh arrangement from that
 * order's seasons instead of the stored one.
 */
export function useCustomSeasons(
  contentId: string,
  enabled: boolean,
  startFrom: EpisodeOrderChoice | null,
) {
  return useQuery({
    queryKey: ["custom-seasons", contentId, startFrom] as const,
    queryFn: ({ signal }) =>
      v2("GET /api/v2/admin/items/{id}/custom-seasons", {
        path: { id: contentId },
        query: startFrom
          ? {
              start_over: true,
              provider: startFrom.provider,
              order_id: startFrom.order_id,
              file_order: startFrom.file_order,
            }
          : {},
        signal,
        timeoutMs: 60_000,
      }),
    enabled,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  });
}

export function useSaveCustomSeasons(contentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: CustomSeasonsSave) =>
      v2("PUT /api/v2/admin/items/{id}/custom-seasons", { path: { id: contentId }, body }),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["episode-order", contentId] });
      void queryClient.invalidateQueries({ queryKey: ["custom-seasons", contentId] });
    },
  });
}
