import { useState } from "react";
import { ChevronDown, FolderOpen, RefreshCw, Square } from "lucide-react";

import type { LibraryRealtimeMonitoringEntry } from "@/api/types";
import {
  LibraryActionMenu,
  LibraryLastScanned,
  LibraryScanWarning,
  LibraryStatusBadges,
} from "@/components/admin/libraries/LibraryRowParts";
import { findLibraryType, libraryTypeLabel } from "@/components/admin/libraries/libraryTypes";
import type { LibraryRow } from "@/components/admin/libraries/useLibraryRows";
import { PathList } from "@/components/PathList";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

// Not translated: a separator glyph.
const SEPARATOR = "·";

/**
 * The admin libraries list below the `lg` breakpoint: one card per library in
 * list order, with reordering through each card's menu instead of dragging.
 */
export function LibraryCardList({
  rows,
  realtimeMonitoringByLibraryId,
  trickplayByLibraryId,
  onMove,
  onViewActivity,
  moving,
}: {
  rows: LibraryRow[];
  realtimeMonitoringByLibraryId: Map<number, LibraryRealtimeMonitoringEntry>;
  trickplayByLibraryId: Map<number, AdminTrickplayLibrary>;
  onMove: (libraryId: number, direction: -1 | 1) => void;
  onViewActivity: (libraryId: number) => void;
  /** A reorder is saving or the list is reloading: Move up and Move down wait. */
  moving: boolean;
}) {
  const t = useT();
  if (rows.length === 0) {
    return (
      <div className="surface-panel text-muted-foreground rounded-2xl border-0 px-6 py-10 text-center text-sm">
        {t("web.admin.libraries.list.empty")}
      </div>
    );
  }
  return (
    <ul className="space-y-3">
      {rows.map((row, index) => (
        <li key={row.library.id}>
          <LibraryCard
            row={row}
            realtimeMonitoring={realtimeMonitoringByLibraryId.get(row.library.id)}
            trickplay={trickplayByLibraryId.get(row.library.id)}
            canMoveUp={!moving && index > 0}
            canMoveDown={!moving && index < rows.length - 1}
            onMove={(direction) => onMove(row.library.id, direction)}
            onViewActivity={onViewActivity}
          />
        </li>
      ))}
    </ul>
  );
}

function LibraryCard({
  row,
  realtimeMonitoring,
  trickplay,
  canMoveUp,
  canMoveDown,
  onMove,
  onViewActivity,
}: {
  row: LibraryRow;
  realtimeMonitoring: LibraryRealtimeMonitoringEntry | undefined;
  trickplay: AdminTrickplayLibrary | undefined;
  canMoveUp: boolean;
  canMoveDown: boolean;
  onMove: (direction: -1 | 1) => void;
  onViewActivity: (libraryId: number) => void;
}) {
  const t = useT();
  const { library, scan } = row;
  const TypeIcon = findLibraryType(library.type)?.icon ?? FolderOpen;

  return (
    <article className="surface-panel space-y-3 rounded-2xl border-0 p-4">
      <div className="flex items-start gap-3">
        <div className="bg-secondary flex h-9 w-9 shrink-0 items-center justify-center rounded-xl">
          <TypeIcon className="text-muted-foreground h-4 w-4" aria-hidden="true" />
        </div>
        <div className="min-w-0 flex-1 space-y-0.5">
          <h2 className="line-clamp-2 font-medium break-words">{library.name}</h2>
          <div className="text-muted-foreground flex flex-wrap items-center gap-x-1.5 text-xs">
            <span>{libraryTypeLabel(library.type)}</span>
            <span aria-hidden="true">{SEPARATOR}</span>
            <button
              type="button"
              className="hover:text-foreground transition-colors"
              onClick={row.editFolders}
            >
              {t("web.admin.libraries.list.folderCount", { count: library.paths.length })}
            </button>
          </div>
        </div>
        <LibraryActionMenu
          row={row}
          showEdit
          canMoveUp={canMoveUp}
          canMoveDown={canMoveDown}
          onMove={onMove}
          touch
        />
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        <LibraryStatusBadges
          row={row}
          realtimeMonitoring={realtimeMonitoring}
          trickplay={trickplay}
          onViewActivity={onViewActivity}
          showIdle
          className="contents"
        />
        {library.paths.length === 0 ? (
          <Badge variant="outline">{t("web.admin.libraries.list.folderCount", { count: 0 })}</Badge>
        ) : null}
      </div>

      <LibraryCardPaths paths={library.paths} />

      <div className="flex flex-wrap gap-x-1.5 text-xs">
        <div className="text-muted-foreground font-medium">
          {t("web.admin.libraries.column.lastScanned")}
        </div>
        <div className="text-muted-foreground">
          <LibraryLastScanned row={row} />
        </div>
      </div>

      {row.hasScanWarning ? (
        <div className="bg-destructive/5 rounded-xl px-3 py-2 text-sm break-words">
          <LibraryScanWarning row={row} touch />
        </div>
      ) : null}

      <Button
        variant={scan.stop ? "destructive" : "outline"}
        className="h-11 w-full"
        disabled={scan.disabled}
        onClick={scan.run}
      >
        {scan.stop ? (
          <Square className="h-3.5 w-3.5 fill-current" aria-hidden="true" />
        ) : (
          <RefreshCw
            className={cn("h-3.5 w-3.5", scan.pending && "animate-spin")}
            aria-hidden="true"
          />
        )}
        {scan.label}
      </Button>
    </article>
  );
}

/** One path inline, or several behind an expander; paths wrap instead of scrolling. */
function LibraryCardPaths({ paths }: { paths: string[] }) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const pathList = <PathList paths={paths} className="text-muted-foreground space-y-1 text-xs" />;
  if (paths.length <= 1) return pathList;
  return (
    <div className="space-y-1">
      <button
        type="button"
        className="text-muted-foreground hover:text-foreground -my-2 flex min-h-11 items-center gap-1 text-xs transition-colors"
        aria-expanded={expanded}
        onClick={() => setExpanded((current) => !current)}
      >
        {expanded
          ? t("web.admin.libraries.list.hidePaths")
          : t("web.admin.libraries.list.showPaths")}
        <ChevronDown
          className={cn("h-3.5 w-3.5 transition-transform", expanded && "rotate-180")}
          aria-hidden="true"
        />
      </button>
      {expanded ? pathList : null}
    </div>
  );
}
