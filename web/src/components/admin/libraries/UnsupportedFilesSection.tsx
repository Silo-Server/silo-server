import { Fragment, useId, useState } from "react";
import { ChevronRight, FileX, Search } from "lucide-react";

import type { Library, LibraryUnsupportedFileGroup } from "@/api/types";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useUnsupportedLibraryFiles } from "@/hooks/queries/admin/libraries";
import { useDebounce } from "@/hooks/useDebounce";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

const REASON_LABELS: Record<LibraryUnsupportedFileGroup["reason"], string> = {
  dvd_vob: "DVD VOB",
  disc_image: "Disc image",
  realmedia: "RealMedia",
  disc_stream: "Blu-ray or AVCHD folder",
};

/**
 * The folder relative to the root of its library that holds it, so a row
 * reads "Ronin (1998)/VIDEO_TS" rather than the whole mount path. Only the
 * row's own library counts, since libraries can share a parent folder. Falls
 * back to the full path when no root holds it, as for files directly in a
 * root.
 */
function unsupportedFolderLabel(group: LibraryUnsupportedFileGroup, libraries: Library[]): string {
  const roots = libraries.find((library) => library.id === group.library_id)?.paths ?? [];
  const path = group.directory_path;
  let longest = "";
  for (const root of roots) {
    const prefix = root.replace(/\/+$/, "") + "/";
    if (path.startsWith(prefix) && prefix.length > longest.length) {
      longest = prefix;
    }
  }
  return longest ? path.slice(longest.length) : path;
}

/**
 * Folders holding video files that scans find but do not catalog because of
 * their type, so an admin can see why a title never appeared.
 */
export function UnsupportedFilesSection({ libraries }: { libraries: Library[] }) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 250);
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const detailsIdPrefix = useId();
  // Loaded while collapsed too, so the header count shows without opening.
  const { data, isError, refetch, isRefetching, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useUnsupportedLibraryFiles({ search: debouncedSearch });
  const groups = data?.pages.flatMap((page) => page.groups) ?? [];
  // A failed refresh or later page keeps the rows already loaded, so only a
  // failed first load replaces the count with the error mark.
  const loadFailed = data === undefined && isError;

  return (
    <CollapsibleDiagnosticsSection
      title="Files Not Cataloged"
      description="Folders holding video files that scans find but skip because of their type."
      count={data?.pages[0]?.total}
      isError={loadFailed}
      icon={<FileX className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={setOpen}
    >
      <div className="relative mb-2">
        <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
        <Input
          placeholder="Filter by folder, file, library, or reason..."
          aria-label="Filter files not cataloged"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="h-8 pl-8 text-xs"
        />
      </div>
      {isError ? (
        <div role="alert" className="text-destructive mb-2 flex items-center gap-2 px-2 text-sm">
          <span>
            {loadFailed
              ? "The list could not be loaded."
              : "The list could not be refreshed. It shows what loaded before."}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={isRefetching}
            onClick={() => void refetch()}
          >
            Retry
          </Button>
        </div>
      ) : null}
      {groups.length === 0 ? (
        loadFailed ? null : (
          <p className="text-muted-foreground px-2 py-3 text-sm">
            {data ? "No skipped files found." : "Loading..."}
          </p>
        )
      ) : (
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Folder</TableHead>
                <TableHead>Library</TableHead>
                <TableHead>Type</TableHead>
                <TableHead className="text-right">Files</TableHead>
                <TableHead>First found</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.map((group, index) => {
                const rowKey = `${group.library_id}:${group.directory_path}:${group.reason}`;
                const isExpanded = expandedKey === rowKey;
                const detailsId = `${detailsIdPrefix}-${index}`;
                const hiddenNames = group.file_count - group.file_names.length;
                return (
                  <Fragment key={rowKey}>
                    <TableRow>
                      <TableCell className="max-w-[20rem]">
                        <button
                          type="button"
                          className="flex max-w-full items-center gap-2 text-left"
                          aria-expanded={isExpanded}
                          aria-controls={isExpanded ? detailsId : undefined}
                          onClick={() => setExpandedKey(isExpanded ? null : rowKey)}
                        >
                          <ChevronRight
                            aria-hidden="true"
                            className={cn(
                              "text-muted-foreground h-3.5 w-3.5 shrink-0 transition-transform",
                              isExpanded && "rotate-90",
                            )}
                          />
                          <span className="truncate text-sm font-medium">
                            {unsupportedFolderLabel(group, libraries)}
                          </span>
                        </button>
                      </TableCell>
                      <TableCell className="text-sm">{group.library_name}</TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className="border-warning/30 bg-warning/5 text-warning"
                        >
                          {REASON_LABELS[group.reason] ?? group.reason}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-right text-xs tabular-nums">
                        {group.file_count}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {formatDateTime(group.first_seen_at)}
                      </TableCell>
                    </TableRow>
                    {isExpanded && (
                      <TableRow id={detailsId} className="hover:bg-transparent">
                        <TableCell
                          colSpan={5}
                          className="bg-muted/30 border-b px-4 py-3 whitespace-normal"
                        >
                          <div className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-xs">
                            <span className="text-muted-foreground font-medium">Folder</span>
                            <code className="font-mono break-all select-all">
                              {group.directory_path}
                            </code>
                            <span className="text-muted-foreground font-medium">Why</span>
                            <span>{group.message}</span>
                            <span className="text-muted-foreground font-medium">Files</span>
                            <div>
                              <ul className="max-h-48 space-y-0.5 overflow-y-auto">
                                {group.file_names.map((name) => (
                                  <li key={name}>
                                    <code className="font-mono break-all select-all">{name}</code>
                                  </li>
                                ))}
                              </ul>
                              {hiddenNames > 0 ? (
                                <p className="text-muted-foreground mt-1">and {hiddenNames} more</p>
                              ) : null}
                            </div>
                            <span className="text-muted-foreground font-medium">Last found</span>
                            <span>{formatDateTime(group.last_seen_at)}</span>
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
      {hasNextPage ? (
        <Button
          variant="outline"
          size="sm"
          className="mt-2"
          disabled={isFetchingNextPage}
          onClick={() => void fetchNextPage()}
        >
          {isFetchingNextPage ? "Loading..." : "Load more"}
        </Button>
      ) : null}
    </CollapsibleDiagnosticsSection>
  );
}
