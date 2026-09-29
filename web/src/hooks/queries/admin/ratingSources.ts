import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { adminKeys } from "@/hooks/queries/keys";

/**
 * The external rating sources an administrator can show on title pages:
 * Silo's own and those the enabled metadata plugins declare.
 */
export function useAdminRatingSources() {
  return useQuery({
    queryKey: adminKeys.ratingSources(),
    queryFn: ({ signal }) => v2("GET /api/v2/admin/rating-sources", { signal }),
    staleTime: 30_000,
  });
}
