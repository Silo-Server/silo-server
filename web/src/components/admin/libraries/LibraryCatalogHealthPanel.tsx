import { useCallback, useState } from "react";
import { AlertTriangle, FolderOpen, Search, Unlink, Wrench } from "lucide-react";

import type { Library } from "@/api/types";
import { AmbiguousRootsSection } from "@/components/admin/libraries/AmbiguousRootsSection";
import { MetadataMatcherQueuesSection } from "@/components/admin/libraries/MetadataMatcherQueuesSection";
import { SkippedRootsSection } from "@/components/admin/libraries/SkippedRootsSection";
import { StaleIDsSection } from "@/components/admin/libraries/StaleIDsSection";
import { UnmatchedItemsSection } from "@/components/admin/libraries/UnmatchedItemsSection";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { numberText, useT } from "@/i18n";
import { cn } from "@/lib/utils";

// Not translated: glyph standing in for a failed count load.
const ERROR_MARK = "!";

type HealthSectionId = "unmatched" | "matcher" | "ambiguous" | "skipped" | "stale";

function parseHealthSectionId(value: string): HealthSectionId {
  if (
    value === "unmatched" ||
    value === "matcher" ||
    value === "ambiguous" ||
    value === "skipped" ||
    value === "stale"
  ) {
    return value;
  }
  return "unmatched";
}

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
  unmatchedError = false,
  matcherCount,
  matcherError = false,
}: {
  libraries: Library[];
  unmatchedCount: number | undefined;
  unmatchedError?: boolean;
  matcherCount: number | undefined;
  matcherError?: boolean;
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
      isError: unmatchedError,
      icon: <Search className="h-4 w-4 text-amber-500" aria-hidden="true" />,
      accentClass: "bg-amber-500/10",
    },
    {
      id: "matcher",
      label: t("web.admin.common.libraryEditor.matcher.title"),
      count: matcherCount,
      isError: matcherError,
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
    <Tabs
      value={selected}
      onValueChange={(value) => setSelected(parseHealthSectionId(value))}
      className="gap-4"
    >
      <TabsList
        aria-label={t("web.admin.libraries.tabs.health")}
        className="grid h-auto! w-full grid-cols-2 gap-2.5 bg-transparent p-0 sm:grid-cols-3 lg:grid-cols-5"
      >
        {tiles.map((tile) => (
          <TabsTrigger
            key={tile.id}
            value={tile.id}
            className="surface-panel hover:bg-surface-hover/50 border-border/50 data-[state=active]:border-foreground/60 data-[state=active]:bg-secondary/50 text-foreground flex h-auto min-h-20 w-full flex-col items-start justify-between gap-2 rounded-2xl border p-3.5 text-start whitespace-normal shadow-none transition-colors after:hidden"
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
          </TabsTrigger>
        ))}
      </TabsList>

      <TabsContent value="unmatched" className="mt-0">
        <UnmatchedItemsSection embedded />
      </TabsContent>
      <TabsContent value="matcher" className="mt-0">
        <MetadataMatcherQueuesSection libraries={libraries} embedded />
      </TabsContent>
      <TabsContent value="ambiguous" className="mt-0">
        <AmbiguousRootsSection
          libraries={libraries}
          embedded
          onStatusChange={handleAmbiguousStatus}
        />
      </TabsContent>
      <TabsContent value="skipped" className="mt-0">
        <SkippedRootsSection embedded onStatusChange={handleSkippedStatus} />
      </TabsContent>
      <TabsContent value="stale" className="mt-0">
        <StaleIDsSection embedded onStatusChange={handleStaleStatus} />
      </TabsContent>
    </Tabs>
  );
}
