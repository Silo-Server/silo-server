import { useEffect, useMemo, useState } from "react";
import { Search, Unlink } from "lucide-react";

import type { StaleMediaID } from "@/api/types";
import MatchItemDialog from "@/components/MatchItemDialog";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import {
  CARD_ACTION_CLASS,
  CardActions,
  CardField,
  CardFields,
  DiagnosticsCard,
  DiagnosticsCardList,
  DiagnosticsSearch,
  PaginationBar,
  TOUCH_CONTROL_CLASS,
} from "@/components/admin/libraries/DiagnosticsListParts";
import {
  useWideDiagnosticsTableLayout,
  usePagination,
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
import { flattenStaleMediaIDs, useStaleMediaIDs } from "@/hooks/queries/admin/libraries";
import { useDebounce } from "@/hooks/useDebounce";
import { numberText, useT } from "@/i18n";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

export function StaleIDsSection({
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
  const [matchItem, setMatchItem] = useState<StaleMediaID | null>(null);
  // The listing is paged on demand: the first page loads when the section
  // opens and "Load more" fetches the rest, so a large stale-id table does
  // not make the server walk every row on mount.
  const {
    data: stalePages,
    isError,
    isLoading,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useStaleMediaIDs({ enabled: isOpen, search: debouncedSearch });
  const staleIDs = useMemo(() => flattenStaleMediaIDs(stalePages), [stalePages]);
  const totalCount = stalePages?.pages[0]?.total;

  useEffect(() => {
    if (!onStatusChange) return;
    if (debouncedSearch.trim() !== "") return;
    onStatusChange({ count: totalCount, isError: stalePages === undefined && isError });
  }, [debouncedSearch, isError, onStatusChange, stalePages, totalCount]);

  // Preserve the server's last-seen order across the pages loaded so far.
  const pag = usePagination(staleIDs);

  // The row's one action, sized by the layout showing it.
  const matchButton = (s: StaleMediaID, className: string) => (
    <Button
      variant="outline"
      size="sm"
      className={cn("gap-1", className)}
      onClick={() => setMatchItem(s)}
    >
      <Search className="h-3 w-3" />
      {t("web.admin.libraries.match")}
    </Button>
  );

  const body = (
    <>
      <DiagnosticsSearch
        className="mb-2"
        placeholder={t("web.admin.libraries.staleIds.filterPlaceholder")}
        value={search}
        onValueChange={(value) => {
          setSearch(value);
          pag.setPage(0);
        }}
      />
      {stalePages === undefined && isError ? (
        <div
          role="alert"
          className="border-border/40 bg-background/40 text-destructive rounded-xl border px-4 py-8 text-center text-sm"
        >
          {t("web.admin.common.diagnosticsSection.countError")}
        </div>
      ) : stalePages === undefined && isLoading ? (
        <div className="border-border/40 bg-background/40 text-muted-foreground rounded-xl border px-4 py-8 text-center text-sm">
          {t("common.loading")}
        </div>
      ) : stalePages !== undefined && pag.rows.length === 0 ? (
        <div className="border-border/40 bg-background/40 text-muted-foreground rounded-xl border px-4 py-8 text-center text-sm">
          {t("web.admin.libraries.health.staleEmpty")}
        </div>
      ) : showTable ? (
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table aria-label={t("web.admin.libraries.staleIds.tableLabel")}>
            <TableHeader>
              {/* Headers wrap: in longer languages they are wider than the
                  dates and IDs under them, and would push the table past its
                  section. */}
              <TableRow className="hover:bg-transparent [&>th]:whitespace-normal">
                <TableHead>{t("web.admin.libraries.column.title")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.year")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.library")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.provider")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.providerId")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.firstSeen")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.lastSeen")}</TableHead>
                <TableHead className="w-[100px]">
                  {t("web.admin.libraries.column.actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pag.rows.map((s) => (
                <TableRow key={staleIDKey(s)}>
                  {/* The title and library name wrap, so a long one can't push the
                      table past its section. The title keeps a minimum width, so a
                      long library name can't squeeze it to a few letters a line. */}
                  <TableCell className="font-medium whitespace-normal">
                    <span className="bidi-text block min-w-32 wrap-anywhere">{s.title}</span>
                  </TableCell>
                  <TableCell className="tabular-nums">{numberText(s.year)}</TableCell>
                  <TableCell className="text-sm whitespace-normal">
                    <span
                      dir="auto"
                      className="align-with-page block w-fit max-w-full wrap-anywhere"
                    >
                      {s.library_name}
                    </span>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{s.provider}</Badge>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{s.provider_id}</TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">
                    {formatDateTime(s.first_seen_at)}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">
                    {formatDateTime(s.last_seen_at)}
                  </TableCell>
                  <TableCell>{matchButton(s, "h-7 text-xs")}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <DiagnosticsCardList>
          {pag.rows.map((s) => (
            <DiagnosticsCard key={staleIDKey(s)}>
              <h3 dir="auto" className="bidi-text font-medium break-words">
                {s.title}
              </h3>
              <CardFields>
                <CardField label={t("web.admin.libraries.column.year")}>
                  <span className="tabular-nums">{numberText(s.year)}</span>
                </CardField>
                <CardField label={t("web.admin.libraries.column.library")}>
                  {s.library_name}
                </CardField>
                <CardField label={t("web.admin.libraries.column.provider")}>
                  <Badge variant="outline">{s.provider}</Badge>
                </CardField>
                <CardField label={t("web.admin.libraries.column.providerId")}>
                  <code className="font-mono break-all" dir="ltr">
                    {s.provider_id}
                  </code>
                </CardField>
                <CardField label={t("web.admin.libraries.column.firstSeen")}>
                  <span className="tabular-nums">{formatDateTime(s.first_seen_at)}</span>
                </CardField>
                <CardField label={t("web.admin.libraries.column.lastSeen")}>
                  <span className="tabular-nums">{formatDateTime(s.last_seen_at)}</span>
                </CardField>
              </CardFields>
              <CardActions>{matchButton(s, CARD_ACTION_CLASS)}</CardActions>
            </DiagnosticsCard>
          ))}
        </DiagnosticsCardList>
      )}
      <PaginationBar {...pag} />
      {hasNextPage ? (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-3 text-xs">
          <span className="text-muted-foreground tabular-nums">
            {t("web.admin.libraries.staleIds.showing", {
              count: staleIDs.length,
              count_text: staleIDs.length,
            })}
          </span>
          <Button
            variant="outline"
            size="sm"
            className={cn("h-7 text-xs", TOUCH_CONTROL_CLASS)}
            disabled={isFetchingNextPage}
            onClick={() => {
              void fetchNextPage();
            }}
          >
            {isFetchingNextPage
              ? t("web.admin.libraries.loadingMore")
              : t("web.admin.libraries.loadMore")}
          </Button>
        </div>
      ) : null}
      {matchItem && (
        <MatchItemDialog
          item={{
            content_id: matchItem.content_id,
            library_id: matchItem.library_id,
            title: matchItem.title,
            year: matchItem.year,
            type: matchItem.content_type,
          }}
          open={true}
          onOpenChange={(open) => {
            if (!open) setMatchItem(null);
          }}
        />
      )}
    </>
  );

  if (embedded) {
    return (
      <section className="surface-panel space-y-3 rounded-2xl border-0 p-4 sm:p-5">
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold">{t("web.admin.libraries.staleIds.title")}</h2>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.libraries.staleIds.description")}
          </p>
        </div>
        {body}
      </section>
    );
  }

  return (
    <CollapsibleDiagnosticsSection
      title={t("web.admin.libraries.staleIds.title")}
      description={t("web.admin.libraries.staleIds.description")}
      count={totalCount}
      icon={<Unlink className="h-4 w-4 text-red-400" />}
      iconClassName="bg-red-500/10"
      open={open}
      onOpenChange={setOpen}
    >
      {body}
    </CollapsibleDiagnosticsSection>
  );
}

function staleIDKey(s: StaleMediaID): string {
  return `${s.content_id}:${s.provider}`;
}
