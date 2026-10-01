import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { themeKeys } from "./keys";

/** Fetch the admin's server-wide custom CSS config. Public endpoint (no auth needed). */
export function useAdminPublicCss() {
  return useQuery({
    queryKey: themeKeys.adminCss(),
    queryFn: async () => {
      try {
        const result = await v2("GET /api/v2/theme/admin-css");
        let vars: Record<string, string> = {};
        if (result.vars) {
          try {
            vars = JSON.parse(result.vars) as Record<string, string>;
          } catch {
            // Keep valid raw CSS active even if a legacy vars row is corrupt.
          }
        }
        return {
          vars,
          rawCss: result.raw_css ?? "",
        };
      } catch {
        return { vars: {} as Record<string, string>, rawCss: "" };
      }
    },
    staleTime: 60_000,
  });
}
