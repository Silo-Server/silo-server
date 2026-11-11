import { useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  DatabaseBackup,
  Ellipsis,
  FolderOpen,
  Pencil,
  RefreshCw,
  Square,
  Trash2,
} from "lucide-react";

import type { LibraryRealtimeMonitoringEntry } from "@/api/types";
import {
  LibraryActiveWork,
  LibraryLastScanned,
  LibraryScanWarning,
  LibraryStatusBadges,
  MountCheckButtonIcon,
} from "@/components/admin/libraries/LibraryRowParts";
import { findLibraryType, libraryTypeLabel } from "@/components/admin/libraries/libraryTypes";
import type { LibraryRow } from "@/components/admin/libraries/useLibraryRows";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

// Menu rows tall enough to tap on a phone.
const MENU_ITEM_CLASS = "min-h-11";

/**
 * The admin libraries list below the `lg` breakpoint: one card per library in
 * list order, with reordering through each card's menu instead of dragging.
 */
export function LibraryCardList({
  rows,
  realtimeMonitoringByLibraryId,
  trickplayByLibraryId,
  onMove,
}: {
  rows: LibraryRow[];
  realtimeMonitoringByLibraryId: Map<number, LibraryRealtimeMonitoringEntry>;
  trickplayByLibraryId: Map<number, AdminTrickplayLibrary>;
  onMove: (libraryId: number, direction: -1 | 1) => void;
}) {
  return (
    <ul className="space-y-3">
      {rows.map((row, index) => (
        <li key={row.library.id}>
          <LibraryCard
            row={row}
            realtimeMonitoring={realtimeMonitoringByLibraryId.get(row.library.id)}
            trickplay={trickplayByLibraryId.get(row.library.id)}
            canMoveUp={index > 0}
            canMoveDown={index < rows.length - 1}
            onMove={(direction) => onMove(row.library.id, direction)}
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
}: {
  row: LibraryRow;
  realtimeMonitoring: LibraryRealtimeMonitoringEntry | undefined;
  trickplay: AdminTrickplayLibrary | undefined;
  canMoveUp: boolean;
  canMoveDown: boolean;
  onMove: (direction: -1 | 1) => void;
}) {
  const t = useT();
  const { library, scan } = row;
  const TypeIcon = findLibraryType(library.type)?.icon ?? FolderOpen;

  return (
    <article className="surface-panel space-y-3 rounded-2xl border-0 p-4">
      <div className="flex items-start gap-3">
        <TypeIcon className="text-muted-foreground mt-0.5 h-5 w-5 shrink-0" aria-hidden="true" />
        <h2 className="line-clamp-2 min-w-0 flex-1 font-medium break-words">{library.name}</h2>
        <LibraryCardMenu
          row={row}
          canMoveUp={canMoveUp}
          canMoveDown={canMoveDown}
          onMove={onMove}
        />
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="secondary">{libraryTypeLabel(library.type)}</Badge>
        <LibraryStatusBadges
          row={row}
          realtimeMonitoring={realtimeMonitoring}
          trickplay={trickplay}
          className="contents"
        />
        <span className="text-muted-foreground text-xs">
          {t("web.admin.libraries.list.folderCount", { count: library.paths.length })}
        </span>
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

      {row.hasActiveWork ? (
        <div className="bg-muted/20 rounded-xl px-3 py-2">
          <LibraryActiveWork row={row} touch />
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
  const pathList = (
    <ul className="text-muted-foreground space-y-1 font-mono text-xs break-all" dir="ltr">
      {paths.map((path) => (
        <li key={path}>{path}</li>
      ))}
    </ul>
  );
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

function LibraryCardMenu({
  row,
  canMoveUp,
  canMoveDown,
  onMove,
}: {
  row: LibraryRow;
  canMoveUp: boolean;
  canMoveDown: boolean;
  onMove: (direction: -1 | 1) => void;
}) {
  const t = useT();
  const { library, refresh, mountCheck, cleanup } = row;
  return (
    // Not modal, like the shared ActionMenu, so an item can open a dialog.
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="text-muted-foreground data-[state=open]:bg-accent data-[state=open]:text-foreground -me-2 -mt-2 size-11 shrink-0 rounded-[10px]"
          aria-label={t("web.admin.libraries.list.moreActions", { name: library.name })}
        >
          <Ellipsis className="size-4" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64 max-w-[calc(100vw-2rem)]">
        <DropdownMenuItem
          className={cn(MENU_ITEM_CLASS, refresh.stop && "text-destructive")}
          disabled={refresh.disabled}
          onSelect={refresh.run}
        >
          {refresh.stop ? (
            <Square className="text-destructive fill-current" aria-hidden="true" />
          ) : (
            <DatabaseBackup aria-hidden="true" />
          )}
          {refresh.label}
        </DropdownMenuItem>
        <DropdownMenuItem
          className={cn(MENU_ITEM_CLASS, "items-start")}
          disabled={mountCheck.pending}
          onSelect={(event) => {
            // Stay open so the result shows under the label.
            event.preventDefault();
            mountCheck.run();
          }}
        >
          <MountCheckButtonIcon
            className="mt-0.5 size-4"
            pending={mountCheck.pending}
            result={mountCheck.result}
          />
          <span className="min-w-0 flex-1">
            <span className="block">{t("web.admin.libraries.list.verifyMounts")}</span>
            {mountCheck.message ? (
              <span className="text-muted-foreground block text-xs break-words">
                {mountCheck.message}
              </span>
            ) : null}
          </span>
        </DropdownMenuItem>
        <DropdownMenuItem className={MENU_ITEM_CLASS} onSelect={row.edit}>
          <Pencil aria-hidden="true" />
          {t("web.admin.libraries.list.edit", { name: library.name })}
        </DropdownMenuItem>
        <DropdownMenuItem
          className={MENU_ITEM_CLASS}
          disabled={!canMoveUp}
          onSelect={(event) => {
            // Stay open so a library can move several places in a row.
            event.preventDefault();
            onMove(-1);
          }}
        >
          <ArrowUp aria-hidden="true" />
          {t("web.admin.libraries.list.moveUp")}
        </DropdownMenuItem>
        <DropdownMenuItem
          className={MENU_ITEM_CLASS}
          disabled={!canMoveDown}
          onSelect={(event) => {
            event.preventDefault();
            onMove(1);
          }}
        >
          <ArrowDown aria-hidden="true" />
          {t("web.admin.libraries.list.moveDown")}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem className={MENU_ITEM_CLASS} variant="destructive" onSelect={row.remove}>
          <Trash2 aria-hidden="true" />
          {t("web.admin.libraries.list.delete", { name: library.name })}
        </DropdownMenuItem>
        {cleanup.available ? (
          <DropdownMenuItem
            className={MENU_ITEM_CLASS}
            variant="destructive"
            disabled={cleanup.pending}
            onSelect={cleanup.run}
          >
            <Trash2 aria-hidden="true" />
            {t("web.admin.libraries.warning.confirmCleanup")}
          </DropdownMenuItem>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
