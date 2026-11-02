import type { RestoreSet } from "@/api/v2/adminBackupRestore";
import { Button } from "@/components/ui/button";
import { FieldGroup } from "@/pages/admin-settings/FieldGroup";

import { restoreCaveats, restorePlan, type SelectionKeys } from "./restoreModel";

export interface RestoreRefusal {
  message: string;
  /** A restore that is already running, to open instead. */
  runningJobId: string | null;
  /** The backup changed since its preview; reading the preview again helps. */
  previewChanged: boolean;
}

export interface RestoreReviewProps {
  set: RestoreSet;
  selection: SelectionKeys;
  onBack: () => void;
  onRestore: () => void;
  pending: boolean;
  refusal: RestoreRefusal | null;
  onOpenRunning: (jobId: string) => void;
  onReloadPreview: () => void;
}

/** Step 2: the whole plan in sentences, what it leaves out, and the button. */
export function RestoreReview({
  set,
  selection,
  onBack,
  onRestore,
  pending,
  refusal,
  onOpenRunning,
  onReloadPreview,
}: RestoreReviewProps) {
  const plan = restorePlan(set, selection);
  const caveats = restoreCaveats(set, selection);
  return (
    <div className="max-w-[70rem] space-y-5">
      {plan.map((group) => (
        <FieldGroup key={group.section} label={group.title}>
          <ul className="list-disc space-y-1.5 py-3.5 pl-5 text-sm">
            {group.sentences.map((sentence, position) => (
              <li key={position}>{sentence}.</li>
            ))}
          </ul>
        </FieldGroup>
      ))}
      {caveats.length > 0 ? (
        <FieldGroup label="Left out" description="Parts of what you chose that won't be restored.">
          <ul className="space-y-2 py-3.5 text-sm">
            {caveats.map((caveat) => (
              <li key={caveat.key}>
                <span className="font-medium">{caveat.label}:</span>{" "}
                <span className="text-muted-foreground">{caveat.text}</span>
              </li>
            ))}
          </ul>
        </FieldGroup>
      ) : null}

      {refusal ? (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-3 rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm"
        >
          <p className="min-w-0 flex-1">Silo didn&apos;t start the restore. {refusal.message}</p>
          {refusal.runningJobId ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => onOpenRunning(refusal.runningJobId!)}
            >
              Open the running restore
            </Button>
          ) : refusal.previewChanged ? (
            <Button variant="outline" size="sm" onClick={onReloadPreview}>
              Reload the preview
            </Button>
          ) : null}
        </div>
      ) : null}

      <div className="border-border/60 flex flex-wrap items-center gap-3 border-t pt-4">
        <p className="text-muted-foreground min-w-0 flex-1 text-sm">
          Takes about a minute. Silo backs up this server first.
        </p>
        <Button variant="outline" onClick={onBack} disabled={pending}>
          Back
        </Button>
        <Button onClick={onRestore} disabled={pending || selection.size === 0}>
          Restore
        </Button>
      </div>
    </div>
  );
}
