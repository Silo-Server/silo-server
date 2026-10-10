import { useId, useState, type ReactNode } from "react";
import { AlertCircle, Info, Loader2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  LAST_SYNC,
  NEXT_SYNC,
  NOT_COUNTED_YET,
  NOT_IN_YOUR_LIBRARIES,
  NOT_SCHEDULED,
  NOT_SYNCED_YET,
  SAVE_BEFORE_SYNC,
  SYNC_NOW,
  SYNCING_NOW,
  WHY_SKIPPED,
  keepsTitles,
  skippedExplanation,
  syncFailedLead,
  syncedAgo,
  titlesSkipped,
} from "@/lib/collections/copy";
import type { CollectionView } from "@/lib/collections/scope";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

type SyncState = NonNullable<CollectionView["sync"]>;

const STATUS_DOT: Record<string, string> = {
  success: "bg-emerald-400",
  warning: "bg-amber-400",
  failed: "bg-red-400",
};

function when(iso: string) {
  return formatDateTime(iso, { dateStyle: "medium", seconds: false });
}

function Cell({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid content-start gap-1 px-4 py-3">
      <span className="text-muted-foreground text-[12.5px]">{label}</span>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[14px]">{children}</div>
    </div>
  );
}

/**
 * The strip at the top of a saved Synced list: when it last synced and how
 * many titles that left, when it syncs next, and how many of the list's
 * titles it skipped because they're in none of its libraries. The skipped
 * count comes from a sync run here; the collection itself doesn't carry it.
 */
export function SyncStatusBlock({
  sync,
  itemCount,
  syncing,
  skipped,
  libraryNames,
  canSync = true,
}: {
  sync: SyncState;
  itemCount: number;
  syncing: boolean;
  /** Titles the last sync run here skipped; unknown until one runs. */
  skipped?: number;
  libraryNames: readonly string[];
  /** Whether this page offers Sync now, which counts the skipped titles. */
  canSync?: boolean;
}) {
  const id = useId();
  const [explaining, setExplaining] = useState(false);
  let last: ReactNode = <b className="font-semibold">{NOT_SYNCED_YET}</b>;
  if (syncing) {
    last = (
      <b className="flex items-center gap-1.5 font-semibold">
        <Loader2 aria-hidden className="size-3.5 animate-spin" />
        {SYNCING_NOW}
      </b>
    );
  } else if (sync.lastAt) {
    last = (
      <>
        <span
          aria-hidden
          className={cn("size-2 rounded-full", STATUS_DOT[sync.status] ?? "bg-muted-foreground")}
        />
        <b className="font-semibold">{syncedAgo(sync.lastAt)}</b>
        <span className="text-muted-foreground text-[12.5px]">
          {itemCount} title{itemCount === 1 ? "" : "s"}
        </span>
      </>
    );
  }
  return (
    <div className="grid gap-2">
      <div
        role="group"
        aria-label="Sync status"
        className="border-border divide-border grid overflow-hidden rounded-[14px] border max-sm:divide-y sm:grid-cols-3 sm:divide-x"
      >
        <Cell label={LAST_SYNC}>{last}</Cell>
        <Cell label={NEXT_SYNC}>
          <b className="font-semibold">{sync.nextAt ? when(sync.nextAt) : NOT_SCHEDULED}</b>
        </Cell>
        <Cell label={NOT_IN_YOUR_LIBRARIES}>
          <b className="font-semibold">
            {skipped === undefined ? NOT_COUNTED_YET : titlesSkipped(skipped)}
          </b>
          <button
            type="button"
            aria-label={WHY_SKIPPED}
            aria-expanded={explaining}
            aria-controls={`${id}-why`}
            onClick={() => setExplaining((open) => !open)}
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 grid size-6 place-items-center rounded-full outline-none focus-visible:ring-[3px]"
          >
            <Info aria-hidden className="size-4" />
          </button>
        </Cell>
      </div>
      <p
        id={`${id}-why`}
        hidden={!explaining}
        className="bg-muted/50 rounded-xl px-3 py-2.5 text-[13px]"
      >
        {skippedExplanation(skipped, libraryNames, canSync)}
      </p>
    </div>
  );
}

/** At the top of a list whose last sync failed: why, what it kept, and Sync now. */
export function SyncFailedCallout({
  sync,
  itemCount,
  syncing,
  saveFirst = false,
  onSyncNow,
}: {
  sync: SyncState;
  itemCount: number;
  syncing: boolean;
  /** Unsaved changes to what the sync reads: Sync now waits for Save. */
  saveFirst?: boolean;
  onSyncNow?: () => void;
}) {
  const retry = sync.nextAt ? ` It tries again ${when(sync.nextAt)}.` : "";
  return (
    <div
      role="alert"
      className="border-destructive/50 bg-destructive/10 flex flex-wrap items-start gap-3 rounded-xl border px-3.5 py-3 text-[13.5px]"
    >
      <AlertCircle aria-hidden className="text-destructive mt-0.5 size-4 shrink-0" />
      <p className="min-w-0 flex-1">
        <b className="font-semibold">{syncFailedLead(sync.lastAt)}</b>
        {sync.message ? ` ${sync.message}` : ""} {keepsTitles(itemCount)}
        {retry}
      </p>
      {onSyncNow ? (
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={syncing || saveFirst}
          onClick={onSyncNow}
        >
          {syncing ? <Loader2 aria-hidden className="animate-spin" /> : null}
          {SYNC_NOW}
        </Button>
      ) : null}
      {onSyncNow && saveFirst ? (
        <p className="text-muted-foreground w-full text-[12.5px]">{SAVE_BEFORE_SYNC}</p>
      ) : null}
    </div>
  );
}
