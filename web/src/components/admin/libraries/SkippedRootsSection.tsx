import { Fragment, useMemo, useState } from "react";
import { AlertTriangle, ChevronRight } from "lucide-react";

import type { LibrarySkippedRoot } from "@/api/types";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import {
  CardField,
  CardFields,
  DiagnosticsCard,
  DiagnosticsCardList,
  DiagnosticsSearch,
  PaginationBar,
  SortableHead,
  SortControl,
  TOUCH_CONTROL_CLASS,
} from "@/components/admin/libraries/DiagnosticsListParts";
import {
  pathLeaf,
  useLibrariesTableLayout,
  usePagination,
  useSort,
} from "@/components/admin/libraries/diagnosticsListState";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useSkippedLibraryRoots } from "@/hooks/queries/admin/libraries";
import { useDebounce } from "@/hooks/useDebounce";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

type SkippedSortField = "root_path" | "library" | "reason" | "first_seen" | "last_seen";

const REASON_BADGE_CLASS = "border-warning/30 bg-warning/5 text-warning";

/** The "Troubleshooting" section: roots the scanner skipped, and why. */
export function SkippedRootsSection() {
  const t = useT();
  const showTable = useLibrariesTableLayout();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 250);
  const { data, hasNextPage, fetchNextPage, isFetchingNextPage } = useSkippedLibraryRoots({
    enabled: open,
    search: debouncedSearch,
  });
  const skippedRoots = useMemo(() => data?.pages.flatMap((page) => page.roots) ?? [], [data]);
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const { sortField, sortDir, toggle, setSortField, setSortDir } = useSort<SkippedSortField>(
    "last_seen",
    "desc",
  );

  const filtered = skippedRoots;

  const sorted = useMemo(() => {
    const cmp = sortDir === "asc" ? 1 : -1;
    return [...filtered].sort((a, b) => {
      switch (sortField) {
        case "root_path":
          return cmp * a.root_path.localeCompare(b.root_path);
        case "library":
          return cmp * a.library_name.localeCompare(b.library_name);
        case "reason":
          return cmp * a.reason.localeCompare(b.reason);
        case "first_seen":
          return cmp * a.first_seen_at.localeCompare(b.first_seen_at);
        case "last_seen":
          return cmp * a.last_seen_at.localeCompare(b.last_seen_at);
        default:
          return 0;
      }
    });
  }, [filtered, sortField, sortDir]);

  const pag = usePagination(sorted);

  // The sortable columns, as table headers and as the card layout's sort options.
  const sortColumns: { value: SkippedSortField; label: string }[] = [
    { value: "root_path", label: t("web.admin.libraries.column.item") },
    { value: "library", label: t("web.admin.libraries.column.library") },
    { value: "reason", label: t("web.admin.libraries.column.reason") },
    { value: "first_seen", label: t("web.admin.libraries.column.firstSeen") },
    { value: "last_seen", label: t("web.admin.libraries.column.lastSeen") },
  ];
  const sortHead = (field: SkippedSortField) => (
    <SortableHead field={field} activeField={sortField} activeDir={sortDir} onSort={toggle}>
      {sortColumns.find((column) => column.value === field)?.label}
    </SortableHead>
  );

  return (
    <CollapsibleDiagnosticsSection
      title={t("web.admin.libraries.skippedRoots.title")}
      description={t("web.admin.libraries.skippedRoots.description")}
      count={data?.pages[0]?.total}
      icon={<AlertTriangle className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={setOpen}
    >
      <DiagnosticsSearch
        className="mb-2"
        placeholder={t("web.admin.libraries.skippedRoots.filterPlaceholder")}
        value={search}
        onValueChange={(value) => {
          setSearch(value);
          pag.setPage(0);
        }}
      />
      {showTable ? (
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                {sortHead("root_path")}
                {sortHead("library")}
                {sortHead("reason")}
                <TableHead className="text-end">{t("web.admin.libraries.column.files")}</TableHead>
                {sortHead("first_seen")}
                {sortHead("last_seen")}
              </TableRow>
            </TableHeader>
            <TableBody>
              {pag.rows.map((root) => {
                const rowKey = skippedRootKey(root);
                const isExpanded = expandedKey === rowKey;
                return (
                  <Fragment key={rowKey}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() => setExpandedKey(isExpanded ? null : rowKey)}
                    >
                      <TableCell className="max-w-[20rem]">
                        <div className="flex items-center gap-2">
                          <ChevronRight
                            className={cn(
                              "text-muted-foreground h-3.5 w-3.5 shrink-0 transition-transform",
                              isExpanded && "rotate-90 rtl:-rotate-90",
                            )}
                          />
                          <span className="truncate text-sm font-medium">
                            {pathLeaf(root.root_path)}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className="text-sm">{root.library_name}</TableCell>
                      <TableCell>
                        <Badge variant="outline" className={REASON_BADGE_CLASS}>
                          {root.reason}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-end text-xs tabular-nums">
                        {root.file_count}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {formatDateTime(root.first_seen_at)}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {formatDateTime(root.last_seen_at)}
                      </TableCell>
                    </TableRow>
                    {isExpanded && (
                      <TableRow className="hover:bg-transparent">
                        <TableCell colSpan={6} className="bg-muted/30 border-b px-4 py-3">
                          <SkippedRootDetails root={root} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
      ) : (
        <>
          <SortControl
            options={sortColumns}
            sortField={sortField}
            sortDir={sortDir}
            onFieldChange={setSortField}
            onDirChange={setSortDir}
          />
          <DiagnosticsCardList>
            {pag.rows.map((root) => (
              <DiagnosticsCard key={skippedRootKey(root)}>
                <div className="space-y-1.5">
                  <h3 className="font-medium break-words">{pathLeaf(root.root_path)}</h3>
                  <Badge
                    variant="outline"
                    className={cn(REASON_BADGE_CLASS, "max-w-full whitespace-normal")}
                  >
                    {root.reason}
                  </Badge>
                </div>
                <CardFields>
                  <CardField label={t("web.admin.libraries.column.library")}>
                    {root.library_name}
                  </CardField>
                  <CardField label={t("web.admin.libraries.column.firstSeen")}>
                    <span className="tabular-nums">{formatDateTime(root.first_seen_at)}</span>
                  </CardField>
                  <CardField label={t("web.admin.libraries.column.lastSeen")}>
                    <span className="tabular-nums">{formatDateTime(root.last_seen_at)}</span>
                  </CardField>
                </CardFields>
                <div className="bg-muted/30 rounded-xl px-3 py-2">
                  <SkippedRootDetails root={root} />
                </div>
              </DiagnosticsCard>
            ))}
          </DiagnosticsCardList>
        </>
      )}
      <PaginationBar {...pag} />
      {hasNextPage ? (
        <Button
          variant="outline"
          size="sm"
          className={cn(TOUCH_CONTROL_CLASS, "max-lg:mt-2 max-lg:w-full")}
          disabled={isFetchingNextPage}
          onClick={() => void fetchNextPage()}
        >
          {isFetchingNextPage
            ? t("web.admin.libraries.skippedRoots.loading")
            : t("web.admin.libraries.loadMore")}
        </Button>
      ) : null}
    </CollapsibleDiagnosticsSection>
  );
}

function skippedRootKey(root: LibrarySkippedRoot): string {
  return `${root.library_id}:${root.root_path}`;
}

/** The full paths and file count: a table row's expansion, and part of every card. */
function SkippedRootDetails({ root }: { root: LibrarySkippedRoot }) {
  const t = useT();
  return (
    <div className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-xs">
      <span className="text-muted-foreground font-medium">
        {t("web.admin.libraries.skippedRoots.rootPath")}
      </span>
      <code className="min-w-0 font-mono break-all select-all">{root.root_path}</code>
      {root.sample_file_path && (
        <>
          <span className="text-muted-foreground font-medium">
            {t("web.admin.libraries.skippedRoots.sampleFile")}
          </span>
          <code className="min-w-0 font-mono break-all select-all">{root.sample_file_path}</code>
        </>
      )}
      <span className="text-muted-foreground font-medium">
        {t("web.admin.libraries.skippedRoots.filesAffected")}
      </span>
      <span>{root.file_count}</span>
    </div>
  );
}
