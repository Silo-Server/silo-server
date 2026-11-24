import { Fragment, useEffect, useMemo, useState } from "react";
import { AlertTriangle, ChevronRight } from "lucide-react";

import type { LibrarySkippedRoot } from "@/api/types";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import {
  CardField,
  CardFields,
  CardPath,
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
  useWideDiagnosticsTableLayout,
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
import { numberText, useT } from "@/i18n";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

type SkippedSortField = "root_path" | "library" | "reason" | "first_seen" | "last_seen";

/** The detail paths keep the body text color, and one click selects a whole path. */
const SKIPPED_PATH_CLASS = "text-foreground min-w-0 select-all";

const REASON_BADGE_CLASS = "border-warning/30 bg-warning/5 text-warning";

/** The "Troubleshooting" section: roots the scanner skipped, and why. */
export function SkippedRootsSection({
  embedded = false,
  onStatusChange,
}: {
  embedded?: boolean;
  onStatusChange?: (status: { count: number | undefined; isError?: boolean }) => void;
} = {}) {
  const t = useT();
  const showTable = useWideDiagnosticsTableLayout();
  const [open, setOpen] = useState(false);
  const isOpen = embedded || open;
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 250);
  const { data, isError, isLoading, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useSkippedLibraryRoots({
      enabled: isOpen,
      search: debouncedSearch,
    });
  const skippedRoots = useMemo(() => data?.pages.flatMap((page) => page.roots) ?? [], [data]);
  const totalCount = data?.pages[0]?.total;

  useEffect(() => {
    if (!onStatusChange) return;
    if (debouncedSearch.trim() !== "") return;
    onStatusChange({ count: totalCount, isError: data === undefined && isError });
  }, [data, debouncedSearch, isError, onStatusChange, totalCount]);
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

  const body = (
    <>
      <DiagnosticsSearch
        className="mb-2"
        placeholder={t("web.admin.libraries.skippedRoots.filterPlaceholder")}
        value={search}
        onValueChange={(value) => {
          setSearch(value);
          pag.setPage(0);
        }}
      />
      {data === undefined && isError ? (
        <div
          role="alert"
          className="border-border/40 bg-background/40 text-destructive rounded-xl border px-4 py-8 text-center text-sm"
        >
          {t("web.admin.common.diagnosticsSection.countError")}
        </div>
      ) : data === undefined && isLoading ? (
        <div className="border-border/40 bg-background/40 text-muted-foreground rounded-xl border px-4 py-8 text-center text-sm">
          {t("web.admin.libraries.skippedRoots.loading")}
        </div>
      ) : data !== undefined && pag.rows.length === 0 ? (
        <div className="border-border/40 bg-background/40 text-muted-foreground rounded-xl border px-4 py-8 text-center text-sm">
          {t("web.admin.libraries.health.skippedEmpty")}
        </div>
      ) : showTable ? (
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
                          <span dir="auto" className="bidi-text truncate text-sm font-medium">
                            {pathLeaf(root.root_path)}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className="text-sm whitespace-normal">
                        {/* A long library name wraps, so it can't push the table past
                            its section. */}
                        <span
                          dir="auto"
                          className="align-with-page block w-fit max-w-full wrap-anywhere"
                        >
                          {root.library_name}
                        </span>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={REASON_BADGE_CLASS}>
                          {root.reason}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-end text-xs tabular-nums">
                        {numberText(root.file_count)}
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
                        <TableCell
                          colSpan={6}
                          className="bg-muted/30 border-b px-4 py-3 whitespace-normal"
                        >
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
                  <h3 dir="auto" className="bidi-text font-medium break-words">
                    {pathLeaf(root.root_path)}
                  </h3>
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
    </>
  );

  if (embedded) {
    return (
      <section className="surface-panel space-y-3 rounded-2xl border-0 p-4 sm:p-5">
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold">{t("web.admin.libraries.skippedRoots.title")}</h2>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.libraries.skippedRoots.description")}
          </p>
        </div>
        {body}
      </section>
    );
  }

  return (
    <CollapsibleDiagnosticsSection
      title={t("web.admin.libraries.skippedRoots.title")}
      description={t("web.admin.libraries.skippedRoots.description")}
      count={totalCount}
      icon={<AlertTriangle className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={setOpen}
    >
      {body}
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
    <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs">
      <span className="text-muted-foreground font-medium">
        {t("web.admin.libraries.skippedRoots.rootPath")}
      </span>
      <CardPath path={root.root_path} className={SKIPPED_PATH_CLASS} />
      {root.sample_file_path && (
        <>
          <span className="text-muted-foreground font-medium">
            {t("web.admin.libraries.skippedRoots.sampleFile")}
          </span>
          <CardPath path={root.sample_file_path} className={SKIPPED_PATH_CLASS} />
        </>
      )}
      <span className="text-muted-foreground font-medium">
        {t("web.admin.libraries.skippedRoots.filesAffected")}
      </span>
      <span>{numberText(root.file_count)}</span>
    </div>
  );
}
