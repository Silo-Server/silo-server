import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { adminKeys } from "@/hooks/queries/keys";

/**
 * Reads the rating source support of this API build. During a rolling deploy
 * the web bundle can be newer than the node answering, so callers gate the
 * rating source list on it and treat a failed read as "not supported".
 */
export function useAdminRatingSourceCapabilities() {
  return useQuery({
    queryKey: adminKeys.ratingSourceCapabilities(),
    queryFn: ({ signal }) => v2("GET /api/v2/admin/rating-sources/capabilities", { signal }),
    staleTime: 30_000,
    retry: false,
  });
}

/**
 * The external rating sources an administrator can show on title pages:
 * Silo's own and those the enabled metadata plugins declare.
 */
export function useAdminRatingSources(enabled = true) {
  return useQuery({
    queryKey: adminKeys.ratingSources(),
    queryFn: ({ signal }) => v2("GET /api/v2/admin/rating-sources", { signal }),
    staleTime: 30_000,
    enabled,
  });
}
