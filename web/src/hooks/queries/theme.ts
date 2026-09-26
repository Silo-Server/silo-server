import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { v2, type V2Result } from "@/api/v2/request";
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

export type ThemeCatalogEntry = V2Result<"GET /api/v2/theme/catalog">["document"]["themes"][number];

/** Fetch the theme catalog from the server proxy. */
export function useThemeCatalog(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: themeKeys.catalogIndex(),
    queryFn: async () => {
      const result = await v2("GET /api/v2/theme/catalog");
      return result.document.themes;
    },
    enabled: options?.enabled ?? true,
    staleTime: 10 * 60_000,
  });
}

/** Force-refresh the theme catalog cache on the server and refetch. */
export function useRefreshThemeCatalog() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: () => v2("POST /api/v2/theme/catalog/refresh", { retryAuthentication: false }),
    onSuccess: (data) => {
      queryClient.setQueryData(themeKeys.catalogIndex(), data.document.themes);
    },
  });
}
