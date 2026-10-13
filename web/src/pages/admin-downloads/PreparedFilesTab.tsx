import { useId, useMemo, useState } from "react";
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
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import {
  formatExpiresIn,
  formatStorageBytes,
  storageFileRecipe,
  storageFileSubtitle,
  storageFileTitle,
  storageFileUsage,
} from "./downloadStoragePresentation";
import { CHECKBOX, LoadMoreButton, SearchField } from "./controls";

const ALL = "all";

type Sort = NonNullable<AdminDownloadStorageFilesQuery["sort"]>;
type State = "" | "in_use" | "cached" | "expired";

const STATE_OPTIONS: { value: State | typeof ALL; label: string }[] = [
  { value: ALL, label: "In use and cached" },
  { value: "in_use", label: "In use" },
  { value: "cached", label: "Cached" },
  { value: "expired", label: "Expired" },
];
const SORT_OPTIONS: { value: Sort; label: string }[] = [
  { value: "size", label: "Largest first" },
  { value: "last_used", label: "Least recently used" },
  { value: "created", label: "Newest first" },
];

export default function PreparedFilesTab({
  location,
  onLocationChange,
}: {
  location: string;
  onLocationChange: (location: string) => void;
}) {
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
        <SearchField label="Search titles" className="w-48" onSearch={setQuery} />
        <Select
          value={location || ALL}
          onValueChange={(value) => onLocationChange(value === ALL ? "" : value)}
        >
          <SelectTrigger size="sm" className="w-44" aria-label="Location">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>All locations</SelectItem>
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
          <SelectTrigger size="sm" className="w-44" aria-label="State">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {STATE_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
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
          <SelectTrigger size="sm" className="w-36" aria-label="Format">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>All formats</SelectItem>
            <SelectItem value="transcode">Transcode</SelectItem>
            <SelectItem value="remux">Remux</SelectItem>
          </SelectContent>
        </Select>
        <Select value={sort} onValueChange={(value) => setSort(value as Sort)}>
          <SelectTrigger size="sm" className="w-48" aria-label="Sort">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SORT_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {selectedRows.length > 0 ? (
        <div
          role="toolbar"
          aria-label="Selected prepared files"
          className="bg-surface/60 border-border flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2"
        >
          <span className="text-[12px] font-medium tabular-nums">
            {selectedRows.length.toLocaleString()} selected ·{" "}
            {formatStorageBytes(selectedRows.reduce((sum, row) => sum + row.bytes, 0))}
            {selectedInUse > 0 ? ` · ${selectedInUse} in use` : ""}
          </span>
          <div className="ml-auto flex gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-[11px]"
              onClick={() => setSelected(new Set())}
            >
              Clear
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="text-destructive hover:text-destructive h-7 gap-1.5 px-2 text-[11px]"
              onClick={() => setConfirming(selectedRows)}
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
              Delete…
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
                  aria-label="Select every listed file"
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
              <TableHead>Title</TableHead>
              <TableHead className="hidden md:table-cell">Prepared as</TableHead>
              <TableHead>Location</TableHead>
              <TableHead className="text-right">On disk</TableHead>
              <TableHead className="hidden sm:table-cell">Last used</TableHead>
              <TableHead className="hidden lg:table-cell">Used by</TableHead>
              <TableHead>State</TableHead>
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
                  {files.isError ? "Prepared files could not be read." : "No prepared files match."}
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

const STATE_PILLS: Record<AdminDownloadStorageFile["state"], { label: string; className: string }> =
  {
    in_use: { label: "In use", className: "text-foreground bg-chart-1/30" },
    cached: { label: "Cached", className: "text-foreground bg-chart-2/30" },
    expired: { label: "Expired", className: "text-muted-foreground bg-muted" },
  };

function StatePill({ row }: { row: AdminDownloadStorageFile }) {
  const pill = STATE_PILLS[row.state];
  return (
    <div className="space-y-0.5">
      <span
        className={cn(
          "inline-flex rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap",
          pill.className,
        )}
      >
        {pill.label}
      </span>
      {row.state === "cached" && row.expires_at ? (
        <div className="text-muted-foreground text-xs whitespace-nowrap">
          expires {formatExpiresIn(row.expires_at)}
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
  const recipe = storageFileRecipe(row);
  const subtitle = storageFileSubtitle(row);
  const stale = row.oldest_waiting;
  return (
    <TableRow data-state={selected ? "selected" : undefined}>
      <TableCell>
        <input
          type="checkbox"
          className={CHECKBOX}
          aria-label={`Select ${storageFileTitle(row)}`}
          checked={selected}
          disabled={row.state === "expired"}
          onChange={onToggle}
        />
      </TableCell>
      <TableCell>
        <div className="font-medium">{storageFileTitle(row)}</div>
        {subtitle ? <div className="text-muted-foreground text-xs">{subtitle}</div> : null}
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <div>{recipe.main}</div>
        {recipe.detail ? (
          <div className="text-muted-foreground text-xs">{recipe.detail}</div>
        ) : null}
      </TableCell>
      <TableCell className="whitespace-nowrap">{row.location_name}</TableCell>
      <TableCell className="text-right whitespace-nowrap tabular-nums">
        {row.state === "expired" ? "—" : formatStorageBytes(row.bytes)}
      </TableCell>
      <TableCell className="hidden whitespace-nowrap sm:table-cell">
        {formatRelativeTime(row.last_used_at) ?? "—"}
      </TableCell>
      <TableCell className="hidden lg:table-cell">
        <div>{storageFileUsage(row)}</div>
        {stale ? (
          <div className="text-warning text-xs">
            {stale.device_name || "A device"}
            {stale.last_seen_at
              ? ` · seen ${formatRelativeTime(stale.last_seen_at)}`
              : " · never seen"}
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
              `${failed} ${failed === 1 ? "file" : "files"} could not be deleted` +
                (deleted.length > 0 ? `; ${deleted.length} deleted` : "") +
                ". Check the server log.",
            );
          } else if (deleted.length > 0) {
            toast.success(
              `Deleted ${deleted.length} prepared ${deleted.length === 1 ? "file" : "files"} (${formatStorageBytes(freed)})` +
                (refused > 0 ? `; ${refused} skipped` : ""),
            );
          } else {
            toast.info("Nothing was deleted: the files are in use or already gone.");
          }
          onClose(true);
        },
        onError: () => toast.error("The files could not be deleted."),
      },
    );
  }

  const names = (rows: AdminDownloadStorageFile[]) =>
    rows.slice(0, 3).map(storageFileTitle).join(", ") +
    (rows.length > 3 ? ` and ${rows.length - 3} more` : "");

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
            Delete {list.length} prepared {list.length === 1 ? "file" : "files"}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            Frees up to {formatStorageBytes(bytes)}. Devices that already finished keep their
            copies.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {cached.length > 0 ? (
          <div className="bg-card rounded-xl border p-3 text-sm">
            <div>
              <strong>{cached.length} cached</strong> · {names(cached)}
            </div>
            <div className="text-muted-foreground text-xs">
              No device is waiting on {cached.length === 1 ? "it" : "these"}.{" "}
              {cached.length === 1 ? "It goes" : "They go"} now instead of when{" "}
              {cached.length === 1 ? "it expires" : "they expire"}.
            </div>
          </div>
        ) : null}
        {inUse.length > 0 ? (
          <div className="border-warning/30 bg-warning/8 rounded-xl border p-3 text-sm">
            <div>
              <strong>{inUse.length} in use</strong> · {names(inUse)}
            </div>
            <div className="text-xs">
              {inUse.length === 1 ? "A device is" : "Devices are"} still waiting on{" "}
              {inUse.length === 1 ? "it" : "them"}. If you delete{" "}
              {inUse.length === 1 ? "it" : "them"}, Silo prepares{" "}
              {inUse.length === 1 ? "it" : "them"} again for those devices. To free the space for
              good, revoke the downloads on the Device copies tab instead.
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
            Also delete the {inUse.length === 1 ? "file" : `${inUse.length} files`} in use
          </label>
        ) : null}
        <AlertDialogFooter>
          <Button variant="outline" onClick={() => onClose(false)}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            disabled={targets.length === 0 || remove.isPending}
            onClick={confirm}
          >
            {remove.isPending
              ? "Deleting…"
              : includeInUse || inUse.length === 0
                ? `Delete ${targets.length} ${targets.length === 1 ? "file" : "files"}`
                : `Delete ${targets.length} cached ${targets.length === 1 ? "file" : "files"}`}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
