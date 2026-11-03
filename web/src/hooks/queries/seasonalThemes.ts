import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

export type SeasonalThemeCatalog = components["schemas"]["SeasonalThemeCatalog"];
export type SeasonalTheme = components["schemas"]["SeasonalTheme"];
export type SeasonalVibe = components["schemas"]["SeasonalVibe"];

/** The holidays a Seasonal Picks row can follow, with their vibes. Static per server build. */
export function useSeasonalThemes() {
  return useQuery({
    queryKey: ["sections", "seasonal-themes"],
    queryFn: () => v2("GET /api/v2/sections/seasonal-themes"),
    staleTime: Infinity,
  });
}
