import { Fragment } from "react";

import { MetaDot } from "@/components/calm/ListRow";
import { cn } from "@/lib/utils";

/** A sync state worth showing: a list that failed, or one syncing now. */
export interface SyncAttention {
  label: string;
  tone: "failed" | "syncing";
  /** The server's message, as a tooltip. */
  message?: string;
}

/**
 * "Movies, Kids · 23 titles" under a collection's name, or "Manual · 23
 * titles" on a card. A Smart collection adds "Updates itself as titles are
 * added" after its count, and the editor adds where rows show it. Sync status is added at the end only when it needs
 * attention.
 */
export function CollectionMetaLine({
  typeLabel,
  libraryNames,
  itemCount,
  extra,
  attention,
  className,
}: {
  typeLabel?: string;
  libraryNames?: readonly string[];
  itemCount?: number;
  /** What follows the count: one part, or several. */
  extra?: string | readonly string[];
  attention?: SyncAttention;
  className?: string;
}) {
  const counted = [
    typeLabel,
    itemCount === undefined ? undefined : `${itemCount} title${itemCount === 1 ? "" : "s"}`,
  ]
    .filter(Boolean)
    .join(" · ");
  const parts = [counted, ...(typeof extra === "string" ? [extra] : (extra ?? []))].filter(
    (part): part is string => Boolean(part),
  );
  const names = libraryNames && libraryNames.length > 0 ? libraryNames.join(", ") : null;
  return (
    <p className={cn("text-muted-foreground text-[14px]", className)}>
      {names ? <b className="text-foreground font-semibold">{names}</b> : null}
      {parts.map((part, index) => (
        <Fragment key={part}>
          {names || index > 0 ? <MetaDot /> : null}
          {part}
        </Fragment>
      ))}
      {attention ? (
        <>
          {" · "}
          <span
            title={attention.message || undefined}
            className={cn(attention.tone === "failed" && "text-destructive font-medium")}
          >
            {attention.label}
          </span>
        </>
      ) : null}
    </p>
  );
}
