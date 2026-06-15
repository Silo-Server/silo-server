import { useState } from "react";
import { Download, RefreshCw, AlertCircle } from "lucide-react";
import { useThemeCatalog, useRefreshThemeCatalog } from "@/hooks/queries/theme";
import type { ThemeCatalogEntry } from "@/hooks/queries/theme";
import { parseThemeFile } from "@/lib/themeExport";
import { sanitizeCss } from "@/lib/cssSanitizer";
import { api } from "@/api/client";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";

interface CatalogBrowserProps {
  onInstall: (entry: {
    baseTheme: string;
    vars: Record<string, string>;
    customCss: string;
  }) => void;
}

export function CatalogBrowser({ onInstall }: CatalogBrowserProps) {
  const { data: themes, isLoading, isError, refetch } = useThemeCatalog();
  const refreshCatalog = useRefreshThemeCatalog();
  const isAdmin = useIsActingAdmin();
  const [installing, setInstalling] = useState<string | null>(null);

  function handleRefresh() {
    refreshCatalog.mutate(undefined, {
      onSuccess: () => toast.success("Theme catalog refreshed"),
      onError: () => toast.error("Failed to refresh catalog"),
    });
  }

  async function handleInstall(entry: ThemeCatalogEntry) {
    setInstalling(entry.id);
    try {
      // Proxy the download through the backend to prevent browser SSRF
      const json = await api(`/theme/download?url=${encodeURIComponent(entry.downloadUrl)}`);
      const themeFile = parseThemeFile(json);
      onInstall({
        baseTheme: themeFile.baseTheme,
        vars: themeFile.vars as Record<string, string>,
        customCss: sanitizeCss(themeFile.customCss),
      });
      toast.success(`Installed "${entry.name}"`);
    } catch (err) {
      toast.error(
        `Failed to install theme: ${err instanceof Error ? err.message : "Unknown error"}`,
      );
    } finally {
      setInstalling(null);
    }
  }

  if (isLoading) {
    return (
      <div className="grid gap-3 sm:grid-cols-2">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="bg-muted/30 h-32 animate-pulse rounded-2xl" />
        ))}
      </div>
    );
  }

  if (isError || !themes) {
    return (
      <div className="flex flex-col items-center gap-3 py-8 text-center">
        <AlertCircle className="text-muted-foreground h-8 w-8" />
        <p className="text-muted-foreground text-sm">
          Could not load the theme catalog. The catalog may be unavailable or the server is
          unreachable.
        </p>
        <button
          type="button"
          onClick={() => refetch()}
          className="text-primary hover:text-primary/80 inline-flex items-center gap-1.5 text-sm font-medium"
        >
          <RefreshCw className="h-3.5 w-3.5" />
          Try again
        </button>
      </div>
    );
  }

  if (themes.length === 0) {
    return (
      <p className="text-muted-foreground py-8 text-center text-sm">
        No community themes available yet.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      {isAdmin && (
        <div className="flex justify-end">
          <button
            type="button"
            onClick={handleRefresh}
            disabled={refreshCatalog.isPending}
            className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
          >
            <RefreshCw className={cn("h-3.5 w-3.5", refreshCatalog.isPending && "animate-spin")} />
            Refresh catalog
          </button>
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {themes.map((entry) => (
          <div
            key={entry.id}
            className="surface-panel-subtle flex flex-col rounded-2xl border px-4 py-4"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-semibold tracking-tight">{entry.name}</span>
                </div>
                <p className="text-muted-foreground mt-0.5 text-[11px]">by {entry.author}</p>
                {entry.description && (
                  <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                    {entry.description}
                  </p>
                )}
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <div
                  className="h-3.5 w-3.5 rounded-full border border-white/15"
                  style={{ backgroundColor: entry.previewAccent }}
                />
                <div
                  className="h-3.5 w-3.5 rounded-full border border-white/15"
                  style={{ backgroundColor: entry.previewBg }}
                />
              </div>
            </div>

            {entry.tags.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {entry.tags.map((tag) => (
                  <span
                    key={tag}
                    className="bg-muted text-muted-foreground rounded-full px-2 py-0.5 text-[10px] font-medium"
                  >
                    {tag}
                  </span>
                ))}
              </div>
            )}

            {/* Theme preview strip */}
            <div
              className="mt-3 overflow-hidden rounded-xl border"
              style={{
                backgroundColor: entry.previewBg,
                borderColor: `color-mix(in srgb, ${entry.previewBg} 40%, white 15%)`,
              }}
            >
              <div className="px-3 py-2.5">
                <div className="flex items-center gap-2">
                  <div
                    className="h-2 w-2 rounded-full"
                    style={{ backgroundColor: entry.previewAccent }}
                  />
                  <div className="h-1.5 w-14 rounded-full bg-white/60" />
                </div>
                <div className="mt-2 flex gap-1.5">
                  <div className="h-6 flex-1 rounded-lg bg-white/8" />
                  <div className="h-6 w-10 rounded-lg bg-white/12" />
                </div>
              </div>
            </div>

            <button
              type="button"
              onClick={() => handleInstall(entry)}
              disabled={installing === entry.id}
              className={cn(
                "mt-3 inline-flex items-center justify-center gap-1.5 rounded-xl px-3 py-2 text-xs font-medium transition-colors",
                "bg-primary/10 text-primary hover:bg-primary/20",
                installing === entry.id && "pointer-events-none opacity-50",
              )}
            >
              <Download className="h-3.5 w-3.5" />
              {installing === entry.id ? "Installing..." : "Install"}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
