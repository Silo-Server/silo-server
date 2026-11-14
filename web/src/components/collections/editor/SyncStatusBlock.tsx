import { useId, useState, type ReactNode } from "react";
import { AlertCircle, Info, Loader2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  lastSync,
  nextSync,
  notCountedYet,
  notInYourLibraries,
  notScheduled,
  notSyncedYet,
  saveBeforeSync,
  syncNow,
  syncingNow,
  whySkipped,
  keepsTitles,
  skippedExplanation,
  syncFailedLead,
  syncedAgo,
  titlesSkipped,
} from "@/lib/collections/copy";
import type { CollectionView } from "@/lib/collections/scope";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

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
  const t = useT();
  const id = useId();
  const [explaining, setExplaining] = useState(false);
  let last: ReactNode = <b className="font-semibold">{notSyncedYet()}</b>;
  if (syncing) {
    last = (
      <b className="flex items-center gap-1.5 font-semibold">
        <Loader2 aria-hidden className="size-3.5 animate-spin" />
        {syncingNow()}
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
          {t("web.collections.metaLine.titleCount", {
            count: itemCount,
            shown: itemCount.toLocaleString(formatLocale()),
          })}
        </span>
      </>
    );
  }
  return (
    <div className="grid gap-2">
      <div
        role="group"
        aria-label={t("web.collections.syncStatus.label")}
        className="border-border divide-border grid overflow-hidden rounded-[14px] border max-sm:divide-y sm:grid-cols-3 sm:divide-x"
      >
        <Cell label={lastSync()}>{last}</Cell>
        <Cell label={nextSync()}>
          <b className="font-semibold">{sync.nextAt ? when(sync.nextAt) : notScheduled()}</b>
        </Cell>
        <Cell label={notInYourLibraries()}>
          <b className="font-semibold">
            {skipped === undefined ? notCountedYet() : titlesSkipped(skipped)}
          </b>
          <button
            type="button"
            aria-label={whySkipped()}
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
  const t = useT();
  const retry = sync.nextAt
    ? ` ${t("web.collections.syncStatus.triesAgain", { when: when(sync.nextAt) })}`
    : "";
  // After the lead: the server's message, if any, what the list kept, and when it tries again.
  const detail = [sync.message, keepsTitles(itemCount)].filter(Boolean).join(" ") + retry;
  return (
    <div
      role="alert"
      className="border-destructive/50 bg-destructive/10 flex flex-wrap items-start gap-3 rounded-xl border px-3.5 py-3 text-[13.5px]"
    >
      <AlertCircle aria-hidden className="text-destructive mt-0.5 size-4 shrink-0" />
      <p className="min-w-0 flex-1">
        <b className="font-semibold">{syncFailedLead(sync.lastAt)}</b> {detail}
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
          {syncNow()}
        </Button>
      ) : null}
      {onSyncNow && saveFirst ? (
        <p className="text-muted-foreground w-full text-[12.5px]">{saveBeforeSync()}</p>
      ) : null}
    </div>
  );
}
