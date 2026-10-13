import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";

/**
 * The rating sources the server shows on title pages and cards
 * (getRatingsCapability): IMDb and TMDB, plus the sources an administrator
 * turned on.
 */
export async function fetchShownRatingSources(
  options?: Pick<RequestInit, "signal">,
): Promise<string[]> {
  const capability = await v2("GET /api/v2/capabilities/ratings", {
    signal: options?.signal ?? undefined,
  });
  return capability.state === "available" ? capability.sources.map((entry) => entry.source) : [];
}

/**
 * The shown rating sources as a set. Empty until the capability loads, so a
 * rating the server may hide is not offered before the answer arrives.
 */
export function useShownRatingSources(): ReadonlySet<string> {
  const { data } = useQuery({
    queryKey: ["ratings", "capability"],
    queryFn: ({ signal }) => fetchShownRatingSources({ signal }),
    staleTime: 60 * 1000,
  });
  return useMemo(() => new Set(data ?? []), [data]);
}
