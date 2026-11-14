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
import { useT, type MessageKey } from "@/i18n";
import { useHomeRowTitle } from "@/lib/sectionTitle";
import type { HomeRow } from "@/lib/homeRows/types";
import { EmptyPanel, RowsSkeleton } from "@/pages/admin-requests/queueParts";
import { DiscoverRowDialog } from "@/pages/admin-requests/DiscoverRowDialog";
import {
  describeDiscoverRow,
  isBrandRow,
  isBuiltInRow,
  ROW_KINDS,
  toHomeRow,
} from "@/pages/admin-requests/discoverLayoutModel";

/**
 * Admin → Requests → Discover Page: the rows everyone sees on the Requests
 * page's Discover tab, in order. It is the Sections page's row list and row
 * dialog over the Discover layout, and every change saves right away.
 */
export default function AdminRequestDiscover() {
  const t = useT();
  useDocumentTitle(t("web.admin.requests.discoverPage.documentTitle"));
  const capabilities = useAdminRequestCapabilities();
  if (capabilities.isLoading) return <RowsSkeleton />;
  if (!capabilities.data?.discover_layout) {
    return (
      <EmptyPanel
        title={t("web.admin.requests.discoverPage.unavailable.title")}
        detail={t("web.admin.requests.discoverPage.unavailable.detail")}
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
  const t = useT();
  const bullets: Array<[LucideIcon, MessageKey]> = [
    [Rows3, "web.admin.requests.discoverPage.restore.builtInsBack"],
    [Trash2, "web.admin.requests.discoverPage.restore.addedDeleted"],
    [RotateCcw, "web.admin.requests.discoverPage.restore.followsDefaults"],
  ];
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent className="gap-5 sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>{t("web.admin.requests.discoverPage.restore.title")}</DialogTitle>
          <DialogDescription>
            {t("web.admin.requests.discoverPage.restore.description")}
          </DialogDescription>
        </DialogHeader>
        <ul
          aria-label={t("web.admin.requests.discoverPage.restore.changesLabel")}
          className="text-foreground/85 m-0 grid list-none gap-2.5 p-0 text-sm leading-normal"
        >
          {bullets.map(([Icon, text]) => (
            <li key={text} className="grid grid-cols-[18px_1fr] gap-2.5">
              <Icon aria-hidden className="text-muted-foreground mt-[3px] size-4" />
              <span>{t(text)}</span>
            </li>
          ))}
        </ul>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-[13px]">
            {t("web.admin.requests.discoverPage.restore.cannotUndo")}
          </p>
          <div className="flex gap-2">
            <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" disabled={busy} onClick={onConfirm}>
              <RotateCcw />
              {t("web.admin.requests.discoverPage.restore.confirm")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DiscoverLayoutEditor() {
  const t = useT();
  const rowTitle = useHomeRowTitle();
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
      label: t("web.admin.requests.discoverPage.more.open"),
      help: t("web.admin.requests.discoverPage.more.openHelp"),
      icon: ExternalLink,
      onSelect: () => void navigate("/requests"),
    },
    {
      key: "restore",
      label: t("web.admin.requests.discoverPage.more.restore"),
      help: t("web.admin.requests.discoverPage.more.restoreHelp"),
      icon: RotateCcw,
      disabled: !adapter.canEdit || adapter.pending || !layout?.customized,
      onSelect: () => setRestoring(true),
    },
  ];

  function rowMenuItems(row: HomeRow, shared: SharedRowMenuItems): ActionMenuItem[] {
    return [
      {
        key: "edit",
        label: t("web.admin.requests.discoverPage.rowMenu.edit"),
        icon: Pencil,
        disabled: !adapter.canEdit,
        onSelect: () => openRow(row),
      },
      shared.moveToTop,
      shared.moveToBottom,
      {
        key: "delete",
        label: t("web.admin.requests.discoverPage.rowMenu.delete"),
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
      title={t("web.admin.requests.discoverPage.title")}
      subtitle={t("web.admin.requests.discoverPage.subtitle")}
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
            {[
              layout.customized
                ? t("web.admin.requests.discoverPage.notice.customized")
                : t("web.admin.requests.discoverPage.notice.default"),
              t("web.admin.requests.discoverPage.notice.olderApps"),
              full
                ? t("web.admin.requests.discoverPage.notice.full", { count: layout.max_rows })
                : null,
            ]
              .filter(Boolean)
              .join(" ")}
          </p>
        ) : null
      }
    >
      <DeleteRowDialog
        rowTitle={deleting ? rowTitle(toHomeRow(deleting)) : ""}
        pageLabel={t("web.admin.requests.discover.pageName")}
        description={
          stockRow
            ? t("web.admin.requests.discoverPage.delete.stockDescription")
            : t("web.admin.requests.discoverPage.delete.description")
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
