import { useId, useMemo, useState, type ReactNode } from "react";
import { Trash2 } from "lucide-react";
import { toast } from "sonner";
import type {
  AdminDownloadStorageFile,
  AdminDownloadStorageFilesQuery,
} from "@/api/v2/adminDownloadStorage";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useAdminDownloadStorage,
  useAdminDownloadStorageFiles,
  useDeleteAdminDownloadStorageFiles,
} from "@/hooks/queries/admin/downloadStorage";
import { useAppLanguage, useT, type MessageKey, type Translate } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import {
  countValues,
  formatExpiresIn,
  formatStorageBytes,
  storageFileRecipe,
  storageFileSubtitle,
  storageFileTitle,
  storageFileUsage,
} from "./downloadStoragePresentation";
import { CHECKBOX, LoadMoreButton, SearchField } from "./controls";
import { formatLocale } from "@/lib/appLocale";

const ALL = "all";

type Sort = NonNullable<AdminDownloadStorageFilesQuery["sort"]>;
type State = "" | "in_use" | "cached" | "expired";

/** Shown where a value does not apply, such as an expired file's size. */
const NONE = "—";

const STATE_OPTIONS: { value: State | typeof ALL; label: MessageKey }[] = [
  { value: ALL, label: "web.admin.downloads.preparedFiles.state.all" },
  { value: "in_use", label: "web.admin.downloads.preparedFiles.state.inUse" },
  { value: "cached", label: "web.admin.downloads.preparedFiles.state.cached" },
  { value: "expired", label: "web.admin.downloads.preparedFiles.state.expired" },
];
const SORT_OPTIONS: { value: Sort; label: MessageKey }[] = [
  { value: "size", label: "web.admin.downloads.preparedFiles.sort.size" },
  { value: "last_used", label: "web.admin.downloads.preparedFiles.sort.lastUsed" },
  { value: "created", label: "web.admin.downloads.preparedFiles.sort.created" },
];

export default function PreparedFilesTab({
  location,
  onLocationChange,
}: {
  location: string;
  onLocationChange: (location: string) => void;
}) {
  const t = useT();
  // Titles, recipes and sizes come from plain helpers; re-render them on a language change.
  useAppLanguage();
  const storage = useAdminDownloadStorage();
  const [state, setState] = useState<State>("");
  const [format, setFormat] = useState<"" | "remux" | "transcode">("");
  const [sort, setSort] = useState<Sort>("size");
  const [query, setQuery] = useState("");
  const filters = useMemo(
    () => ({
      location: location || undefined,
      state: state || undefined,
      format: format || undefined,
      sort,
      q: query || undefined,
    }),
    [location, state, format, sort, query],
  );
  const files = useAdminDownloadStorageFiles(filters);
  const rows = useMemo(() => files.data?.pages.flatMap((page) => page.items) ?? [], [files.data]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  // A filter change clears the selection, so files it hid cannot come back
  // selected, and deleted, when the administrator returns to that filter.
  const [selectionFilters, setSelectionFilters] = useState(filters);
  if (selectionFilters !== filters) {
    setSelectionFilters(filters);
    setSelected(new Set());
  }
  const [confirming, setConfirming] = useState<AdminDownloadStorageFile[] | null>(null);
  const selectedRows = rows.filter((row) => selected.has(row.id));
  const selectedInUse = selectedRows.filter((row) => row.state === "in_use").length;
  const selectable = rows.filter((row) => row.state !== "expired");
  const allSelected = selectable.length > 0 && selectable.every((row) => selected.has(row.id));
  const someSelected = !allSelected && selectable.some((row) => selected.has(row.id));

  function toggle(id: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <SearchField
          label={t("web.admin.downloads.preparedFiles.search")}
          className="w-48"
          onSearch={setQuery}
        />
        <Select
          value={location || ALL}
          onValueChange={(value) => onLocationChange(value === ALL ? "" : value)}
        >
          <SelectTrigger
            size="sm"
            className="w-44"
            aria-label={t("web.admin.downloads.preparedFiles.locationFilter")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>
              {t("web.admin.downloads.preparedFiles.allLocations")}
            </SelectItem>
            {(storage.data?.locations ?? []).map((l) => (
              <SelectItem key={l.key} value={l.key}>
                {l.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={state || ALL}
          onValueChange={(value) => setState(value === ALL ? "" : (value as State))}
        >
          <SelectTrigger
            size="sm"
            className="w-44"
            aria-label={t("web.admin.downloads.preparedFiles.stateFilter")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {STATE_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {t(option.label)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={format || ALL}
          onValueChange={(value) =>
            setFormat(value === ALL ? "" : (value as "remux" | "transcode"))
          }
        >
          <SelectTrigger
            size="sm"
            className="w-36"
            aria-label={t("web.admin.downloads.preparedFiles.formatFilter")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t("web.admin.downloads.preparedFiles.allFormats")}</SelectItem>
            <SelectItem value="transcode">
              {t("web.admin.downloads.preparedFiles.formatTranscode")}
            </SelectItem>
            <SelectItem value="remux">
              {t("web.admin.downloads.preparedFiles.formatRemux")}
            </SelectItem>
          </SelectContent>
        </Select>
        <Select value={sort} onValueChange={(value) => setSort(value as Sort)}>
          <SelectTrigger
            size="sm"
            className="w-48"
            aria-label={t("web.admin.downloads.preparedFiles.sortFilter")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SORT_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {t(option.label)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {selectedRows.length > 0 ? (
        <div
          role="toolbar"
          aria-label={t("web.admin.downloads.preparedFiles.selectionToolbar")}
          className="bg-surface/60 border-border flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2"
        >
          <span className="text-[12px] font-medium tabular-nums">
            {[
              t(
                "web.admin.downloads.preparedFiles.selectedCount",
                countValues(
                  selectedRows.length,
                  selectedRows.length.toLocaleString(formatLocale()),
                ),
              ),
              formatStorageBytes(selectedRows.reduce((sum, row) => sum + row.bytes, 0)),
              selectedInUse > 0 &&
                t("web.admin.downloads.preparedFiles.selectedInUse", countValues(selectedInUse)),
            ]
              .filter(Boolean)
              .join(" · ")}
          </span>
          <div className="ms-auto flex gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-[11px]"
              onClick={() => setSelected(new Set())}
            >
              {t("web.admin.downloads.preparedFiles.clear")}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="text-destructive hover:text-destructive h-7 gap-1.5 px-2 text-[11px]"
              onClick={() => setConfirming(selectedRows)}
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
              {t("web.admin.downloads.preparedFiles.delete")}
            </Button>
          </div>
        </div>
      ) : null}

      <div className="surface-panel overflow-hidden rounded-2xl">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8">
                <input
                  type="checkbox"
                  className={CHECKBOX}
                  aria-label={t("web.admin.downloads.preparedFiles.selectAll")}
                  ref={(input) => {
                    if (input) input.indeterminate = someSelected;
                  }}
                  checked={allSelected}
                  disabled={selectable.length === 0}
                  onChange={() =>
                    setSelected(allSelected ? new Set() : new Set(selectable.map((row) => row.id)))
                  }
                />
              </TableHead>
              <TableHead>{t("web.admin.downloads.preparedFiles.column.title")}</TableHead>
              <TableHead className="hidden md:table-cell">
                {t("web.admin.downloads.preparedFiles.column.preparedAs")}
              </TableHead>
              <TableHead>{t("web.admin.downloads.preparedFiles.column.location")}</TableHead>
              <TableHead className="text-end">
                {t("web.admin.downloads.preparedFiles.column.onDisk")}
              </TableHead>
              <TableHead className="hidden sm:table-cell">
                {t("web.admin.downloads.preparedFiles.column.lastUsed")}
              </TableHead>
              <TableHead className="hidden lg:table-cell">
                {t("web.admin.downloads.preparedFiles.column.usedBy")}
              </TableHead>
              <TableHead>{t("web.admin.downloads.preparedFiles.column.state")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {files.isLoading ? (
              Array.from({ length: 4 }, (_, i) => (
                <TableRow key={i}>
                  <TableCell colSpan={8}>
                    <Skeleton className="h-8" />
                  </TableCell>
                </TableRow>
              ))
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} className="text-muted-foreground py-10 text-center">
                  {files.isError
                    ? t("web.admin.downloads.preparedFiles.loadError")
                    : t("web.admin.downloads.preparedFiles.empty")}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((row) => (
                <FileRow
                  key={row.id}
                  row={row}
                  selected={selected.has(row.id)}
                  onToggle={() => toggle(row.id)}
                />
              ))
            )}
          </TableBody>
        </Table>
      </div>
      <LoadMoreButton query={files} />

      <DeleteFilesDialog
        files={confirming}
        onClose={(deleted) => {
          setConfirming(null);
          if (deleted) setSelected(new Set());
        }}
      />
    </div>
  );
}

const STATE_PILLS: Record<
  AdminDownloadStorageFile["state"],
  { label: MessageKey; className: string }
> = {
  in_use: {
    label: "web.admin.downloads.preparedFiles.state.inUse",
    className: "text-foreground bg-chart-1/30",
  },
  cached: {
    label: "web.admin.downloads.preparedFiles.state.cached",
    className: "text-foreground bg-chart-2/30",
  },
  expired: {
    label: "web.admin.downloads.preparedFiles.state.expired",
    className: "text-muted-foreground bg-muted",
  },
};

function StatePill({ row }: { row: AdminDownloadStorageFile }) {
  const t = useT();
  const pill = STATE_PILLS[row.state];
  return (
    <div className="space-y-0.5">
      <span
        className={cn(
          "inline-flex rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap",
          pill.className,
        )}
      >
        {t(pill.label)}
      </span>
      {row.state === "cached" && row.expires_at ? (
        <div className="text-muted-foreground text-xs whitespace-nowrap">
          {t("web.admin.downloads.preparedFiles.expires", {
            when: formatExpiresIn(row.expires_at),
          })}
        </div>
      ) : null}
    </div>
  );
}

function FileRow({
  row,
  selected,
  onToggle,
}: {
  row: AdminDownloadStorageFile;
  selected: boolean;
  onToggle: () => void;
}) {
  const t = useT();
  const title = storageFileTitle(row);
  const recipe = storageFileRecipe(row);
  const subtitle = storageFileSubtitle(row);
  const stale = row.oldest_waiting;
  return (
    <TableRow data-state={selected ? "selected" : undefined}>
      <TableCell>
        <input
          type="checkbox"
          className={CHECKBOX}
          aria-label={t("web.admin.downloads.preparedFiles.selectFile", { title })}
          checked={selected}
          disabled={row.state === "expired"}
          onChange={onToggle}
        />
      </TableCell>
      <TableCell>
        <div className="font-medium">{title}</div>
        {subtitle ? <div className="text-muted-foreground text-xs">{subtitle}</div> : null}
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <div>{recipe.main}</div>
        {recipe.detail ? (
          <div className="text-muted-foreground text-xs">{recipe.detail}</div>
        ) : null}
      </TableCell>
      <TableCell className="whitespace-nowrap">{row.location_name}</TableCell>
      <TableCell className="text-end whitespace-nowrap tabular-nums">
        {row.state === "expired" ? NONE : formatStorageBytes(row.bytes)}
      </TableCell>
      <TableCell className="hidden whitespace-nowrap sm:table-cell">
        {formatRelativeTime(row.last_used_at) ?? NONE}
      </TableCell>
      <TableCell className="hidden lg:table-cell">
        <div>{storageFileUsage(row)}</div>
        {stale ? (
          <div className="text-warning text-xs">
            {stale.last_seen_at
              ? t("web.admin.downloads.preparedFiles.staleSeen", {
                  device: stale.device_name || t("web.admin.downloads.preparedFiles.aDevice"),
                  when: formatRelativeTime(stale.last_seen_at) ?? "",
                })
              : t("web.admin.downloads.preparedFiles.staleNever", {
                  device: stale.device_name || t("web.admin.downloads.preparedFiles.aDevice"),
                })}
          </div>
        ) : null}
      </TableCell>
      <TableCell>
        <StatePill row={row} />
      </TableCell>
    </TableRow>
  );
}

function DeleteFilesDialog({
  files,
  onClose,
}: {
  files: AdminDownloadStorageFile[] | null;
  onClose: (deleted: boolean) => void;
}) {
  const t = useT();
  const remove = useDeleteAdminDownloadStorageFiles();
  const [includeInUse, setIncludeInUse] = useState(false);
  const checkboxId = useId();
  // The last files stay on screen while the dialog animates closed.
  const [shown, setShown] = useState(files);
  if (files !== null && files !== shown) {
    setShown(files);
    setIncludeInUse(false);
  }
  const list = shown ?? [];
  const cached = list.filter((f) => f.state !== "in_use");
  const inUse = list.filter((f) => f.state === "in_use");
  const targets = includeInUse ? list : cached;
  const bytes = targets.reduce((sum, f) => sum + f.bytes, 0);

  function confirm() {
    if (targets.length === 0) return;
    remove.mutate(
      { ids: targets.map((f) => f.id), includeInUse },
      {
        onSuccess: (results) => {
          const deleted = results.filter(
            (r) => r.outcome === "deleted" || r.outcome === "requeued",
          );
          const failed = results.filter((r) => r.outcome === "failed").length;
          const refused = results.length - deleted.length - failed;
          const freed = deleted.reduce((sum, r) => sum + r.bytes, 0);
          if (failed > 0) {
            toast.error(
              deleted.length > 0
                ? t("web.admin.downloads.preparedFiles.deleteFailedSome", {
                    failed: t(
                      "web.admin.downloads.preparedFiles.deleteFailedPart",
                      countValues(failed),
                    ),
                    deleted: t(
                      "web.admin.downloads.preparedFiles.deleteDeletedPart",
                      countValues(deleted.length),
                    ),
                  })
                : t("web.admin.downloads.preparedFiles.deleteFailed", countValues(failed)),
            );
          } else if (deleted.length > 0) {
            const message = t("web.admin.downloads.preparedFiles.deleted", {
              ...countValues(deleted.length),
              size: formatStorageBytes(freed),
            });
            toast.success(
              refused > 0
                ? t("web.admin.downloads.preparedFiles.deletedSkipped", {
                    deleted: message,
                    skipped: t(
                      "web.admin.downloads.preparedFiles.skippedPart",
                      countValues(refused),
                    ),
                  })
                : message,
            );
          } else {
            toast.info(t("web.admin.downloads.preparedFiles.nothingDeleted"));
          }
          onClose(true);
        },
        onError: () => toast.error(t("web.admin.downloads.preparedFiles.deleteError")),
      },
    );
  }

  const names = (rows: AdminDownloadStorageFile[]) => titleList(t, rows);

  return (
    <AlertDialog
      open={files !== null}
      onOpenChange={(open) => {
        if (!open) onClose(false);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("web.admin.downloads.preparedFiles.dialog.title", countValues(list.length))}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.admin.downloads.preparedFiles.dialog.description", {
              size: formatStorageBytes(bytes),
            })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {cached.length > 0 ? (
          <div className="bg-card rounded-xl border p-3 text-sm">
            <div>
              {t("web.admin.downloads.preparedFiles.dialog.cachedList", {
                ...countValues(cached.length),
                names: names(cached),
                b: strong,
              })}
            </div>
            <div className="text-muted-foreground text-xs">
              {t("web.admin.downloads.preparedFiles.dialog.cachedNote", { count: cached.length })}
            </div>
          </div>
        ) : null}
        {inUse.length > 0 ? (
          <div className="border-warning/30 bg-warning/8 rounded-xl border p-3 text-sm">
            <div>
              {t("web.admin.downloads.preparedFiles.dialog.inUseList", {
                ...countValues(inUse.length),
                names: names(inUse),
                b: strong,
              })}
            </div>
            <div className="text-xs">
              {t("web.admin.downloads.preparedFiles.dialog.inUseNote", { count: inUse.length })}
            </div>
          </div>
        ) : null}
        {inUse.length > 0 ? (
          <label htmlFor={checkboxId} className="flex items-start gap-2 text-sm">
            <input
              id={checkboxId}
              type="checkbox"
              className={cn(CHECKBOX, "mt-0.5")}
              checked={includeInUse}
              onChange={(event) => setIncludeInUse(event.target.checked)}
            />
            {t("web.admin.downloads.preparedFiles.dialog.includeInUse", countValues(inUse.length))}
          </label>
        ) : null}
        <AlertDialogFooter>
          <Button variant="outline" onClick={() => onClose(false)}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="destructive"
            disabled={targets.length === 0 || remove.isPending}
            onClick={confirm}
          >
            {remove.isPending
              ? t("web.admin.downloads.preparedFiles.dialog.deleting")
              : includeInUse || inUse.length === 0
                ? t(
                    "web.admin.downloads.preparedFiles.dialog.deleteFiles",
                    countValues(targets.length),
                  )
                : t(
                    "web.admin.downloads.preparedFiles.dialog.deleteCached",
                    countValues(targets.length),
                  )}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

const strong = (chunks: ReactNode[]) => <strong>{chunks}</strong>;

/** The first three titles, then how many more. */
function titleList(t: Translate, rows: AdminDownloadStorageFile[]): string {
  const shown = rows.slice(0, 3).map(storageFileTitle).join(", ");
  if (rows.length <= 3) return shown;
  return t("web.admin.downloads.preparedFiles.namesMore", {
    ...countValues(rows.length - 3),
    names: shown,
  });
}
