import { useId, type ComponentProps, type ReactNode } from "react";
import { Link } from "react-router";
import { ArrowUpRight } from "lucide-react";

import { CollectionRowSummary } from "@/components/collections/CollectionRowSummary";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  myRowsOnceCreated,
  noMyRowsYet,
  noRowsYet,
  rowsFailed,
  rowsNotListed,
  rowsOnceCreated,
  rowsThatShowIt,
  rowCountLabel,
} from "@/lib/collections/copy";
import { openRowPath, rowMeta, type CollectionRow, type RowsState } from "@/lib/collections/rows";
import { cn } from "@/lib/utils";

import { SettingRow } from "../fields/SettingRow";
import { AddAsRowMenu } from "./AddAsRowMenu";
import { useAppLanguage, useT } from "@/i18n";

type AddAsRowProps = Pick<ComponentProps<typeof AddAsRowMenu>, "bound" | "others" | "onPick"> & {
  /** Why the collection can't be added as a row yet, shown under the row's name. */
  disabledReason?: string | null;
};

/** The rows, each a link that opens it where rows are edited. */
function RowsList({
  rows,
  libraryNames,
  highlightId,
}: {
  rows: readonly CollectionRow[];
  libraryNames: ReadonlyMap<number, string>;
  highlightId?: string | null;
}) {
  const t = useT();
  return (
    <ul className="border-border/80 divide-border/70 grid divide-y overflow-hidden rounded-xl border">
      {rows.map((row) => {
        const meta = rowMeta(row, libraryNames);
        const fresh = row.id === highlightId;
        return (
          <li key={row.id} data-highlighted={fresh || undefined}>
            <Link
              to={openRowPath(row)}
              aria-label={t("web.collections.editor.rowLinkLabel", { title: row.title, meta })}
              className={cn(
                "hover:bg-accent/40 focus-visible:ring-ring/50 flex items-center gap-3 px-3 py-2.5 outline-none focus-visible:ring-[3px] focus-visible:ring-inset",
                fresh && "bg-emerald-500/10 ring-1 ring-emerald-500/50 ring-inset",
                !row.enabled && "opacity-70",
              )}
            >
              <CollectionRowSummary row={row} libraryNames={libraryNames} />
              <ArrowUpRight aria-hidden className="text-muted-foreground size-4 shrink-0" />
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

/** "Rows that show it" before the collection is created: one line, nothing to add yet. */
export function RowsOnceCreated({ mine = false }: { mine?: boolean }) {
  useAppLanguage();
  return (
    <SettingRow label={rowsThatShowIt()} value={mine ? myRowsOnceCreated() : rowsOnceCreated()} />
  );
}

/**
 * Where it shows, "Rows that show it": the Home and library page rows that
 * show a collection, styled like Home rows, with Add as a row on the right.
 * A server collection lists the administrator's rows; a personal one (`mine`)
 * the viewer's own. `rows` is null when the server doesn't report them; Add as
 * a row shows either way.
 */
export function RowsThatShowIt({
  rows,
  libraryNames,
  highlightId,
  mine = false,
  addAsRow,
}: {
  rows: RowsState | null;
  libraryNames: ReadonlyMap<number, string>;
  /** A row just added from here, highlighted for a moment. */
  highlightId?: string | null;
  /** The viewer's own rows: no note about rows profiles add. */
  mine?: boolean;
  addAsRow: AddAsRowProps;
}) {
  const t = useT();
  const reasonId = useId();
  const { disabledReason, ...menu } = addAsRow;
  const listed = rows?.status === "ready" ? rows.rows : null;
  let line: ReactNode = null;
  if (rows?.status === "error") {
    line = (
      <span role="alert" className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {rowsFailed()}
        <Button variant="outline" size="sm" className="h-7" onClick={rows.onRetry}>
          {t("common.retry")}
        </Button>
      </span>
    );
  } else if (listed && listed.length > 0) {
    line = rowCountLabel(listed.length);
  } else if (listed) {
    line = mine ? noMyRowsYet() : noRowsYet();
  }

  return (
    <SettingRow
      label={rowsThatShowIt()}
      value={
        line || disabledReason ? (
          <>
            {line}
            {disabledReason ? (
              <p id={reasonId} className={cn(line && "mt-1")}>
                {disabledReason}
              </p>
            ) : null}
          </>
        ) : null
      }
      action={
        <AddAsRowMenu
          {...menu}
          mine={mine}
          disabled={Boolean(disabledReason)}
          describedBy={disabledReason ? reasonId : undefined}
        />
      }
    >
      {rows?.status === "loading" ? (
        <div className="grid gap-1.5" aria-hidden>
          <Skeleton className="h-12 rounded-xl" />
          <Skeleton className="h-12 rounded-xl" />
        </div>
      ) : null}
      {listed && listed.length > 0 ? (
        <>
          <RowsList rows={listed} libraryNames={libraryNames} highlightId={highlightId} />
          {mine ? null : <p className="text-muted-foreground text-[12.5px]">{rowsNotListed()}</p>}
        </>
      ) : null}
    </SettingRow>
  );
}
