import { useCallback, useState } from "react";
import { AlertTriangle, FolderOpen, Search, Unlink, Wrench } from "lucide-react";

import type { Library } from "@/api/types";
import { AmbiguousRootsSection } from "@/components/admin/libraries/AmbiguousRootsSection";
import { MetadataMatcherQueuesSection } from "@/components/admin/libraries/MetadataMatcherQueuesSection";
import { SkippedRootsSection } from "@/components/admin/libraries/SkippedRootsSection";
import { StaleIDsSection } from "@/components/admin/libraries/StaleIDsSection";
import { UnmatchedItemsSection } from "@/components/admin/libraries/UnmatchedItemsSection";
import { numberText, useT } from "@/i18n";
import { cn } from "@/lib/utils";

// Not translated: glyph standing in for a failed count load.
const ERROR_MARK = "!";

type HealthSectionId = "unmatched" | "matcher" | "ambiguous" | "skipped" | "stale";

interface LazySectionStatus {
  count: number | undefined;
  isError?: boolean;
}

/**
 * The Catalog Health tab: a 5-tile summary strip selecting between Unmatched
 * Items, Metadata Matcher, Ambiguous Roots, Troubleshooting (Skipped Roots),
 * and Stale External IDs, with lazy loading preserved for the three heavy
 * root/stale endpoints.
 */
export function LibraryCatalogHealthPanel({
  libraries,
  unmatchedCount,
  matcherCount,
}: {
  libraries: Library[];
  unmatchedCount: number | undefined;
  matcherCount: number | undefined;
}) {
  const t = useT();
  const [selected, setSelected] = useState<HealthSectionId>("unmatched");
  const [ambiguousStatus, setAmbiguousStatus] = useState<LazySectionStatus>({ count: undefined });
  const [skippedStatus, setSkippedStatus] = useState<LazySectionStatus>({ count: undefined });
  const [staleStatus, setStaleStatus] = useState<LazySectionStatus>({ count: undefined });

  const handleAmbiguousStatus = useCallback((status: LazySectionStatus) => {
    setAmbiguousStatus((prev) =>
      prev.count === status.count && prev.isError === status.isError ? prev : status,
    );
  }, []);
  const handleSkippedStatus = useCallback((status: LazySectionStatus) => {
    setSkippedStatus((prev) =>
      prev.count === status.count && prev.isError === status.isError ? prev : status,
    );
  }, []);
  const handleStaleStatus = useCallback((status: LazySectionStatus) => {
    setStaleStatus((prev) =>
      prev.count === status.count && prev.isError === status.isError ? prev : status,
    );
  }, []);

  const tiles: Array<{
    id: HealthSectionId;
    label: string;
    count: number | undefined;
    isError?: boolean;
    icon: React.ReactNode;
    accentClass: string;
  }> = [
    {
      id: "unmatched",
      label: t("web.admin.libraries.unmatched.title"),
      count: unmatchedCount,
      icon: <Search className="h-4 w-4 text-amber-500" aria-hidden="true" />,
      accentClass: "bg-amber-500/10",
    },
    {
      id: "matcher",
      label: t("web.admin.common.libraryEditor.matcher.title"),
      count: matcherCount,
      icon: <Wrench className="h-4 w-4 text-amber-500" aria-hidden="true" />,
      accentClass: "bg-amber-500/10",
    },
    {
      id: "ambiguous",
      label: t("web.admin.libraries.ambiguousRoots.title"),
      count: ambiguousStatus.count,
      isError: ambiguousStatus.isError,
      icon: <FolderOpen className="h-4 w-4 text-amber-500" aria-hidden="true" />,
      accentClass: "bg-amber-500/10",
    },
    {
      id: "skipped",
      label: t("web.admin.libraries.skippedRoots.title"),
      count: skippedStatus.count,
      isError: skippedStatus.isError,
      icon: <AlertTriangle className="h-4 w-4 text-amber-500" aria-hidden="true" />,
      accentClass: "bg-amber-500/10",
    },
    {
      id: "stale",
      label: t("web.admin.libraries.staleIds.title"),
      count: staleStatus.count,
      isError: staleStatus.isError,
      icon: <Unlink className="h-4 w-4 text-red-400" aria-hidden="true" />,
      accentClass: "bg-red-500/10",
    },
  ];

  return (
    <div className="space-y-4">
      <div
        role="tablist"
        aria-label={t("web.admin.libraries.tabs.health")}
        className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 lg:grid-cols-5"
      >
        {tiles.map((tile) => {
          const isActive = selected === tile.id;
          return (
            <button
              key={tile.id}
              type="button"
              role="tab"
              aria-selected={isActive}
              className={cn(
                "surface-panel flex min-h-20 flex-col items-start justify-between gap-2 rounded-2xl border p-3.5 text-start transition-colors",
                isActive
                  ? "border-foreground/60 bg-secondary/50"
                  : "hover:bg-surface-hover/50 border-border/50",
              )}
              onClick={() => setSelected(tile.id)}
            >
              <div className="flex w-full items-center justify-between gap-2">
                {tile.isError ? (
                  <span className="text-destructive text-lg leading-none font-bold tabular-nums">
                    <span className="sr-only">
                      {t("web.admin.common.diagnosticsSection.countError")}
                    </span>
                    <span aria-hidden="true">{ERROR_MARK}</span>
                  </span>
                ) : tile.count !== undefined ? (
                  <span
                    className={cn(
                      "text-xl leading-none font-bold tracking-tight tabular-nums",
                      tile.count === 0 && "text-muted-foreground",
                    )}
                  >
                    {numberText(tile.count)}
                  </span>
                ) : (
                  <span className="text-muted-foreground text-xs font-medium">
                    {t("web.admin.libraries.health.inspect")}
                  </span>
                )}
                <span
                  className={cn(
                    "flex h-7 w-7 shrink-0 items-center justify-center rounded-lg",
                    tile.accentClass,
                  )}
                >
                  {tile.icon}
                </span>
              </div>
              <span className="text-xs leading-snug font-semibold">{tile.label}</span>
            </button>
          );
        })}
      </div>

      {selected === "unmatched" ? <UnmatchedItemsSection embedded /> : null}
      {selected === "matcher" ? (
        <MetadataMatcherQueuesSection libraries={libraries} embedded />
      ) : null}
      {selected === "ambiguous" ? (
        <AmbiguousRootsSection
          libraries={libraries}
          embedded
          onStatusChange={handleAmbiguousStatus}
        />
      ) : null}
      {selected === "skipped" ? (
        <SkippedRootsSection embedded onStatusChange={handleSkippedStatus} />
      ) : null}
      {selected === "stale" ? (
        <StaleIDsSection embedded onStatusChange={handleStaleStatus} />
      ) : null}
    </div>
  );
}
