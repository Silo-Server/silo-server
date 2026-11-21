import { useEffect, useRef } from "react";

import type { RestoreDataTypeId, RestoreSet } from "@/api/v2/adminBackupRestore";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";
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

/**
 * A refusal's action is never wider than the alert: a label too long for the
 * alert wraps inside the button, in lines of even length, instead of running
 * past the alert's edge.
 */
const REFUSAL_ACTION = "h-auto min-h-8 max-w-full py-1 text-balance whitespace-normal";

/**
 * The plan's sentences as a bulleted list, here and in the Choose step's summary. The part
 * of a name that can't wrap, such as one long run of joined emoji, is clipped sideways at
 * the list's edge; the words around it still wrap. The clip has 0.25em of padding, offset by
 * a negative margin, so letters whose ink reaches past their edge stay whole. It sits on the
 * end side only: the start padding already gives that room and holds the bullets, which
 * clipping each item would cut off.
 */
export function PlanSentences({
  sentences,
  className,
}: {
  sentences: readonly string[];
  className?: string;
}) {
  const t = useT();
  return (
    <ul
      className={cn(
        "-me-[0.25em] list-disc space-y-1.5 overflow-x-clip ps-5 pe-[0.25em] text-sm wrap-anywhere",
        className,
      )}
    >
      {sentences.map((sentence, position) => (
        <li key={position}>{t("web.admin.backups.restore.plan.sentence", { sentence })}</li>
      ))}
    </ul>
  );
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
  // Back and Restore are disabled while the request runs, which drops a focused one to the
  // page. When the server refuses, focus moves to the refusal's action, or to the alert when
  // it has none, unless the admin has put focus somewhere else in the meantime.
  const refusalRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const alert = refusalRef.current;
    if (alert && (document.activeElement === document.body || !document.activeElement))
      (alert.querySelector("button") ?? alert).focus();
  }, [refusal]);
  return (
    <div className="max-w-[70rem] space-y-5">
      {plan.map((group) => (
        <FieldGroup key={group.section} label={group.title}>
          <PlanSentences sentences={group.sentences} className="py-3.5" />
        </FieldGroup>
      ))}
      {caveats.length > 0 ? (
        <FieldGroup
          label={t("web.admin.backups.restore.review.leftOut")}
          description={t("web.admin.backups.restore.review.leftOutDescription")}
        >
          {/* Clips a name that can't wrap like PlanSentences, but on both sides: this list
              has no start padding or bullets. The name's title shows it in full. */}
          <ul className="-mx-[0.25em] space-y-2 overflow-x-clip px-[0.25em] py-3.5 text-sm wrap-anywhere">
            {caveats.map((caveat) => (
              <li key={caveat.key}>
                {t("web.admin.backups.restore.review.caveat", {
                  // Each name is isolated in every language, so a right-to-left name or one
                  // with a direction override can't reorder the ":" and the text after it.
                  // The colon stays outside the isolate, in the page's direction.
                  label: caveat.profile ? (
                    t("web.admin.backups.restore.caveat.profileLabel", {
                      account: <bdi>{caveat.profile.account}</bdi>,
                      profile: <bdi>{caveat.profile.profile}</bdi>,
                    })
                  ) : (
                    <bdi>{caveat.label}</bdi>
                  ),
                  text: caveat.text,
                  b: (chunks) => (
                    <span title={caveat.label} className="font-medium">
                      {chunks}
                    </span>
                  ),
                  muted: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
                })}
              </li>
            ))}
          </ul>
        </FieldGroup>
      ) : null}

      {refusal ? (
        <div
          ref={refusalRef}
          role="alert"
          tabIndex={-1}
          className="flex flex-wrap items-center gap-3 rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm outline-none"
        >
          <p className="min-w-0 flex-1 basis-64 text-balance">
            {t("web.admin.backups.restore.review.refused", { reason: refusal.message })}
          </p>
          {refusal.runningJobId ? (
            <Button
              variant="outline"
              size="sm"
              className={REFUSAL_ACTION}
              onClick={() => onOpenRunning(refusal.runningJobId!)}
            >
              {t("web.admin.backups.restore.review.openRunning")}
            </Button>
          ) : refusal.previewChanged ? (
            <Button
              variant="outline"
              size="sm"
              className={REFUSAL_ACTION}
              onClick={onReloadPreview}
            >
              {t("web.admin.backups.restore.review.reloadPreview")}
            </Button>
          ) : null}
        </div>
      ) : null}

      {!set.authenticated ? (
        <div className="space-y-3 rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm">
          <p>{t("web.admin.backups.restore.review.unauthenticated")}</p>
          {/* The label's text-sm replaces Label's leading-none with the box's own unitless
              line height, which the row inherits too, so the checkbox's one-line box stays
              on the label's first line when it wraps. */}
          <div className="flex items-start gap-2">
            <span className="flex min-h-lh shrink-0 items-center">
              <Checkbox
                id="restore-accept-unauthenticated"
                checked={acceptUnauthenticated}
                onCheckedChange={(value) => onAcceptUnauthenticated(value === true)}
              />
            </span>
            <Label htmlFor="restore-accept-unauthenticated" className="text-sm">
              {t("web.admin.backups.restore.review.trust")}
            </Label>
          </div>
        </div>
      ) : null}

      {/* The 16rem basis wraps the buttons below the note on a narrow row
          instead of squeezing the note to a word per line. The buttons stay
          at the row's end, even when they stack. */}
      <div className="border-border/60 flex flex-wrap items-center gap-3 border-t pt-4">
        <p className="text-muted-foreground min-w-0 flex-1 basis-64 text-sm text-balance">
          {t("web.admin.backups.restore.review.duration")}
        </p>
        <div className="ms-auto flex flex-wrap justify-end gap-3">
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
    </div>
  );
}
