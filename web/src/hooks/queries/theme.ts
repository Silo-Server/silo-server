import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { themeKeys } from "./keys";

interface AdminCssResponse {
  vars: string; // JSON-encoded Record<string, string>
  raw_css: string;
}

/** Fetch the admin's server-wide custom CSS config. Public endpoint (no auth needed). */
export function useAdminPublicCss() {
  return useQuery({
    queryKey: themeKeys.adminCss(),
    queryFn: async () => {
      try {
        const result = await api<AdminCssResponse>("/theme/admin-css");
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

export interface ThemeCatalogEntry {
  id: string;
  name: string;
  description: string;
  author: string;
  previewAccent: string;
  previewBg: string;
  tags: string[];
  downloadUrl: string;
  version: string;
}

interface ThemeCatalogIndex {
  version: number;
  updatedAt?: string;
  themes: ThemeCatalogEntry[];
}

/** Fetch the theme catalog from the server proxy. */
export function useThemeCatalog(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: themeKeys.catalogIndex(),
    queryFn: async () => {
      const result = await api<ThemeCatalogIndex>("/theme/catalog");
      return result.themes ?? [];
    },
    enabled: options?.enabled ?? true,
    staleTime: 10 * 60_000,
  });
}

/** Force-refresh the theme catalog cache on the server and refetch. */
export function useRefreshThemeCatalog() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api<ThemeCatalogIndex>("/theme/catalog/refresh", { method: "POST" }),
    onSuccess: (data) => {
      queryClient.setQueryData(themeKeys.catalogIndex(), data.themes ?? []);
    },
  });
}
