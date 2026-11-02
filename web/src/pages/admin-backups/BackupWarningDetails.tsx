import { useId, useState, type ReactNode } from "react";
import { Copy, TriangleAlert } from "lucide-react";
import { toast } from "sonner";

import type { AdminBackup } from "@/api/v2/adminBackups";
import { Button } from "@/components/ui/button";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";

import { Disclosure } from "./Disclosure";
import {
  groupMissingBlobs,
  missingGroupTitle,
  missingHeadline,
  warningDetailsReport,
  type MissingFileGroup,
} from "./warningDetails";

/** A list long enough to need its own scroll: names or keys, selectable to copy. */
const LONG_LIST =
  "bg-background/60 max-h-48 overflow-y-auto rounded-md border px-3 py-2 text-xs select-text";

/** One section's headline: the only amber line, with the warning icon. */
function Headline({ children }: { children: ReactNode }) {
  return (
    <p className="flex items-start gap-1.5 text-[13px] text-amber-600 dark:text-amber-400">
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
      <span className="min-w-0">{children}</span>
    </p>
  );
}

function MissingGroupRow({ group }: { group: MissingFileGroup }) {
  const listId = useId();
  const [open, setOpen] = useState(false);
  return (
    <li className="min-w-0 space-y-1">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-[13px]">{missingGroupTitle(group)}</span>
        <Disclosure open={open} onOpenChange={setOpen} controls={listId}>
          {open ? "Hide files" : "Show files"}
        </Disclosure>
      </div>
      {open ? (
        <ul id={listId} className={cn(LONG_LIST, "font-mono")}>
          {group.keys.map((key, position) => (
            <li key={`${key}-${position}`} className="break-all">
              {key}
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  );
}

/** Files missing when the backup copied them, grouped by what referenced them. */
function MissingFiles({ blobs }: { blobs: AdminBackup["missing_blobs"] }) {
  const bodyId = useId();
  const [open, setOpen] = useState(false);
  if (blobs.length === 0) return null;
  return (
    <div className="min-w-0 space-y-1.5">
      <Headline>{missingHeadline(blobs.length)}</Headline>
      <Disclosure open={open} onOpenChange={setOpen} controls={bodyId}>
        {open ? "Hide the files" : "Show the files"}
      </Disclosure>
      {open ? (
        <div id={bodyId} className="space-y-2 pt-1">
          <p className="text-muted-foreground text-xs">
            They&apos;re referenced in the database but weren&apos;t found in storage; restores
            leave them out.
          </p>
          <ul className="space-y-2">
            {groupMissingBlobs(blobs).map((group) => (
              <MissingGroupRow key={group.source} group={group} />
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

/** A backup's warnings as headlines with grouped details, and a plain-text copy for an issue. */
export function BackupWarningDetails({ backup }: { backup: AdminBackup }) {
  const copy = () =>
    copyTextToClipboard(
      warningDetailsReport({ backupId: backup.id, missing: backup.missing_blobs }),
    ).then(
      () => toast.success("Details copied"),
      () => toast.error("Couldn't copy the details. Open each list and copy it by hand."),
    );
  return (
    <div className="min-w-0 space-y-3">
      <MissingFiles blobs={backup.missing_blobs} />
      <Button variant="outline" size="sm" onClick={() => void copy()}>
        <Copy data-icon="inline-start" aria-hidden="true" />
        Copy details
      </Button>
    </div>
  );
}
