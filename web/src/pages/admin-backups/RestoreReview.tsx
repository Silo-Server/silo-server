import type { RestoreDataTypeId, RestoreSet } from "@/api/v2/adminBackupRestore";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { useT } from "@/i18n";
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
  dataTypes: ReadonlySet<RestoreDataTypeId>;
  onBack: () => void;
  onRestore: () => void;
  /** The admin's confirmation for a backup this server cannot authenticate. */
  acceptUnauthenticated: boolean;
  onAcceptUnauthenticated: (accept: boolean) => void;
  pending: boolean;
  refusal: RestoreRefusal | null;
  onOpenRunning: (jobId: string) => void;
  onReloadPreview: () => void;
}

/** Step 2: the whole plan in sentences, what it leaves out, and the button. */
export function RestoreReview({
  set,
  selection,
  dataTypes,
  onBack,
  onRestore,
  acceptUnauthenticated,
  onAcceptUnauthenticated,
  pending,
  refusal,
  onOpenRunning,
  onReloadPreview,
}: RestoreReviewProps) {
  const t = useT();
  const plan = restorePlan(set, selection, dataTypes);
  const caveats = restoreCaveats(set, selection);
  return (
    <div className="max-w-[70rem] space-y-5">
      {plan.map((group) => (
        <FieldGroup key={group.section} label={group.title}>
          <ul className="list-disc space-y-1.5 py-3.5 ps-5 text-sm">
            {group.sentences.map((sentence, position) => (
              <li key={position}>{t("web.admin.backups.restore.plan.sentence", { sentence })}</li>
            ))}
          </ul>
        </FieldGroup>
      ))}
      {caveats.length > 0 ? (
        <FieldGroup
          label={t("web.admin.backups.restore.review.leftOut")}
          description={t("web.admin.backups.restore.review.leftOutDescription")}
        >
          <ul className="space-y-2 py-3.5 text-sm">
            {caveats.map((caveat) => (
              <li key={caveat.key}>
                {t("web.admin.backups.restore.review.caveat", {
                  label: caveat.label,
                  text: caveat.text,
                  b: (chunks) => <span className="font-medium">{chunks}</span>,
                  muted: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
                })}
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
          <p className="min-w-0 flex-1">
            {t("web.admin.backups.restore.review.refused", { reason: refusal.message })}
          </p>
          {refusal.runningJobId ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => onOpenRunning(refusal.runningJobId!)}
            >
              {t("web.admin.backups.restore.review.openRunning")}
            </Button>
          ) : refusal.previewChanged ? (
            <Button variant="outline" size="sm" onClick={onReloadPreview}>
              {t("web.admin.backups.restore.review.reloadPreview")}
            </Button>
          ) : null}
        </div>
      ) : null}

      {!set.authenticated ? (
        <div className="space-y-3 rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm">
          <p>{t("web.admin.backups.restore.review.unauthenticated")}</p>
          <div className="flex items-center gap-2">
            <Checkbox
              id="restore-accept-unauthenticated"
              checked={acceptUnauthenticated}
              onCheckedChange={(value) => onAcceptUnauthenticated(value === true)}
            />
            <Label htmlFor="restore-accept-unauthenticated">
              {t("web.admin.backups.restore.review.trust")}
            </Label>
          </div>
        </div>
      ) : null}

      <div className="border-border/60 flex flex-wrap items-center gap-3 border-t pt-4">
        <p className="text-muted-foreground min-w-0 flex-1 text-sm">
          {t("web.admin.backups.restore.review.duration")}
        </p>
        <Button variant="outline" onClick={onBack} disabled={pending}>
          {t("common.back")}
        </Button>
        <Button
          onClick={onRestore}
          disabled={
            pending || selection.size === 0 || (!set.authenticated && !acceptUnauthenticated)
          }
        >
          {t("web.admin.backups.restore.review.restore")}
        </Button>
      </div>
    </div>
  );
}
