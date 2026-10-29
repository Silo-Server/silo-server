import { useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { ExternalLink, Pencil, RotateCcw, Rows3, Trash2, type LucideIcon } from "lucide-react";

import type { DiscoverRow, DiscoverRowType } from "@/api/v2/adminRequests";
import type { ActionMenuItem } from "@/components/calm/ActionMenu";
import type { PageMoreMenuItem } from "@/components/calm/PageMoreMenu";
import { DeleteRowDialog } from "@/components/homeRows/DeleteRowDialog";
import { HomeRowsPage, type SharedRowMenuItems } from "@/components/homeRows/HomeRowsPage";
import { useNewRowHighlight } from "@/components/homeRows/useNewRowHighlight";
import { useRowFocus } from "@/components/homeRows/useRowFocus";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useAdminRequestCapabilities } from "@/hooks/queries/admin/requests";
import { useDiscoverRows } from "@/hooks/queries/admin/useDiscoverRows";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import type { HomeRow } from "@/lib/homeRows/types";
import { EmptyPanel, RowsSkeleton } from "@/pages/admin-requests/queueParts";
import { DiscoverRowDialog } from "@/pages/admin-requests/DiscoverRowDialog";
import {
  describeDiscoverRow,
  isBrandRow,
  isBuiltInRow,
  ROW_KINDS,
} from "@/pages/admin-requests/discoverLayoutModel";

/**
 * Admin → Requests → Discover Page: the rows everyone sees on the Requests
 * page's Discover tab, in order. It is the Sections page's row list and row
 * dialog over the Discover layout, and every change saves right away.
 */
export default function AdminRequestDiscover() {
  useDocumentTitle("Discover Page - Admin");
  const capabilities = useAdminRequestCapabilities();
  if (capabilities.isLoading) return <RowsSkeleton />;
  if (!capabilities.data?.discover_layout) {
    return (
      <EmptyPanel
        title="Discover page editing unavailable"
        detail="This server can't change the Requests Discover page."
      />
    );
  }
  return <DiscoverLayoutEditor />;
}

/** Confirms going back to the default rows, and says what that brings back and what goes. */
function RestoreDefaultsDialog({
  open,
  busy,
  onConfirm,
  onOpenChange,
}: {
  open: boolean;
  busy: boolean;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const bullets: Array<[LucideIcon, string]> = [
    [Rows3, "The built-in rows and Studios, Networks and Genres come back, in their first order."],
    [Trash2, "Rows you added are deleted."],
    [RotateCcw, "The page follows Silo's defaults again until you change it."],
  ];
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent className="gap-5 sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>Restore Discover to the default rows?</DialogTitle>
          <DialogDescription>
            Everyone&apos;s Discover page goes back to the rows Silo starts with.
          </DialogDescription>
        </DialogHeader>
        <ul
          aria-label="What changes"
          className="text-foreground/85 m-0 grid list-none gap-2.5 p-0 text-sm leading-normal"
        >
          {bullets.map(([Icon, text]) => (
            <li key={text} className="grid grid-cols-[18px_1fr] gap-2.5">
              <Icon aria-hidden className="text-muted-foreground mt-[3px] size-4" />
              <span>{text}</span>
            </li>
          ))}
        </ul>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-[13px]">This can&apos;t be undone.</p>
          <div className="flex gap-2">
            <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button variant="destructive" disabled={busy} onClick={onConfirm}>
              <RotateCcw />
              Restore
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DiscoverLayoutEditor() {
  const adapter = useDiscoverRows();
  const { layout, discoverRows } = adapter;
  const navigate = useNavigate();
  const focus = useRowFocus(adapter.rows, adapter.pending);
  const [highlightId, setHighlightId] = useNewRowHighlight();
  // The Add row / Edit row dialog: open with no row to add one.
  const [rowDialog, setRowDialog] = useState<{ key: number; row: DiscoverRow | null } | null>(null);
  const dialogCount = useRef(0);
  const [deleting, setDeleting] = useState<DiscoverRow | null>(null);
  const deletedRow = useRef(false);
  const [restoring, setRestoring] = useState(false);
  const [restoreBusy, setRestoreBusy] = useState(false);
  const byId = useMemo(() => new Map(discoverRows.map((row) => [row.id, row])), [discoverRows]);
  const full = layout !== undefined && discoverRows.length >= layout.max_rows;

  function openDialog(row: DiscoverRow | null) {
    setRowDialog({ key: ++dialogCount.current, row });
  }

  function openRow(row: HomeRow) {
    const stored = byId.get(row.id);
    if (stored && adapter.canEdit) openDialog(stored);
  }

  function confirmDelete() {
    if (!deleting?.id) return;
    deletedRow.current = true;
    focus.afterRemoval(deleting.id);
    void adapter.removeRow(deleting.id);
    setDeleting(null);
  }

  async function confirmRestore() {
    setRestoreBusy(true);
    const restored = await adapter.restoreDefaults();
    setRestoreBusy(false);
    if (restored) setRestoring(false);
  }

  const moreItems: PageMoreMenuItem[] = [
    {
      key: "open",
      label: "Open Discover",
      help: "See the page as viewers do.",
      icon: ExternalLink,
      onSelect: () => void navigate("/requests"),
    },
    {
      key: "restore",
      label: "Restore defaults…",
      help: "Put back the rows Discover starts with.",
      icon: RotateCcw,
      disabled: !adapter.canEdit || adapter.pending || !layout?.customized,
      onSelect: () => setRestoring(true),
    },
  ];

  function rowMenuItems(row: HomeRow, shared: SharedRowMenuItems): ActionMenuItem[] {
    return [
      {
        key: "edit",
        label: "Edit row…",
        icon: Pencil,
        disabled: !adapter.canEdit,
        onSelect: () => openRow(row),
      },
      shared.moveToTop,
      shared.moveToBottom,
      {
        key: "delete",
        label: "Delete row…",
        icon: Trash2,
        destructive: true,
        group: true,
        disabled: !adapter.canEdit,
        onSelect: () => {
          deletedRow.current = false;
          setDeleting(byId.get(row.id) ?? null);
        },
      },
    ];
  }

  const stockRow = deleting !== null && (isBuiltInRow(deleting) || isBrandRow(deleting));

  return (
    <HomeRowsPage
      adapter={adapter}
      title="Discover Page"
      subtitle="The rows everyone sees on the Requests page's Discover tab, in order."
      focus={focus}
      pageSwitcher={false}
      describe={(row) => {
        const stored = byId.get(row.id);
        return stored ? describeDiscoverRow(stored) : [];
      }}
      rowIcon={(row) => ROW_KINDS[row.sectionType as DiscoverRowType].icon}
      onOpenRow={openRow}
      highlightRowId={highlightId}
      rowMenuItems={rowMenuItems}
      moreItems={moreItems}
      addRow={{ onClick: () => openDialog(null), disabled: !adapter.canEdit || full }}
      notices={
        layout ? (
          <p className="text-muted-foreground max-w-3xl text-sm">
            {layout.customized
              ? "You've changed this page from the default."
              : "This is the default layout, which follows Silo's defaults until you change it."}{" "}
            Apps released before custom rows show only the trending and popular rows, merged into
            two.
            {full ? ` A page holds at most ${layout.max_rows} rows.` : null}
          </p>
        ) : null
      }
    >
      <DeleteRowDialog
        rowTitle={deleting?.title ?? ""}
        pageLabel="Discover"
        description={
          stockRow
            ? "The row goes away from Discover for everyone. Add row can bring it back."
            : "The row goes away from Discover for everyone."
        }
        open={deleting !== null}
        conflict={false}
        busy={false}
        canConfirm={adapter.canEdit}
        onConfirm={confirmDelete}
        onReload={() => void adapter.reload()}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        onCloseAutoFocus={(event) => {
          // After a delete the row's menu is about to go; focus moves to its
          // neighbour once the write lands.
          if (deletedRow.current) event.preventDefault();
        }}
      />
      <RestoreDefaultsDialog
        open={restoring}
        busy={restoreBusy}
        onConfirm={() => void confirmRestore()}
        onOpenChange={setRestoring}
      />
      {rowDialog ? (
        <DiscoverRowDialog
          key={rowDialog.key}
          adapter={adapter}
          editRow={rowDialog.row}
          onClose={() => setRowDialog(null)}
          onSaved={(newId) => setHighlightId(newId ?? null)}
          onDelete={(row) => {
            setRowDialog(null);
            deletedRow.current = false;
            setDeleting(row);
          }}
        />
      ) : null}
    </HomeRowsPage>
  );
}
