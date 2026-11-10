import { type ComponentType, useId } from "react";
import { ListEnd, Loader2, RefreshCw, SkipForward } from "lucide-react";
import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { type MarkerDetectionKinds, useMarkerDetectionKinds } from "@/hooks/queries/admin/markers";
import type { RedetectMarkersKind } from "@/hooks/queries/items";
import { useT, type MessageKey, type Translate } from "@/i18n";

interface RedetectMarkersDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (kind: RedetectMarkersKind) => void;
  isPending?: boolean;
}

const options: {
  kind: RedetectMarkersKind;
  label: MessageKey;
  description: MessageKey;
  Icon: ComponentType<{ className?: string }>;
}[] = [
  {
    kind: "intro",
    label: "web.markers.redetect.intro",
    description: "web.markers.redetect.introDescription",
    Icon: SkipForward,
  },
  {
    kind: "credits",
    label: "web.markers.redetect.credits",
    description: "web.markers.redetect.creditsDescription",
    Icon: ListEnd,
  },
  {
    kind: "all",
    label: "web.markers.redetect.all",
    description: "web.markers.redetect.allDescription",
    Icon: RefreshCw,
  },
];

// offReason explains why the server would not run kind, or returns null
// when it would. An "all" request with one kind off would quietly run only the
// other, which its own option already offers.
function offReason(
  t: Translate,
  kind: RedetectMarkersKind,
  enabled: MarkerDetectionKinds | undefined,
) {
  if (!enabled) return null;
  const introOff = !enabled.intro && kind !== "credits";
  const creditsOff = !enabled.credits && kind !== "intro";
  if (introOff && creditsOff) return t("web.markers.redetect.bothOff");
  if (introOff) return t("web.markers.redetect.introOff");
  if (creditsOff) return t("web.markers.redetect.creditsOff");
  return null;
}

/**
 * Asks which markers an episode re-detection runs for. Kinds turned off in
 * marker settings are shown but cannot be picked.
 */
export default function RedetectMarkersDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending = false,
}: RedetectMarkersDialogProps) {
  const t = useT();
  const id = useId();
  // Read on open, not with the page: most visits never re-detect.
  const enabled = useMarkerDetectionKinds(open);
  const anyOff = enabled !== undefined && (!enabled.intro || !enabled.credits);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("web.markers.redetect.title")}</DialogTitle>
          <DialogDescription>{t("web.markers.redetect.description")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {options.map(({ kind, label, description, Icon }) => {
            const reason = offReason(t, kind, enabled);
            return (
              <button
                key={kind}
                type="button"
                aria-labelledby={`${id}-${kind}-label`}
                aria-describedby={`${id}-${kind}-description`}
                disabled={isPending || reason !== null}
                onClick={() => onConfirm(kind)}
                className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-start transition-colors disabled:cursor-not-allowed disabled:opacity-60"
              >
                {isPending ? (
                  <Loader2 className="text-muted-foreground mt-0.5 size-5 animate-spin" />
                ) : (
                  <Icon className="text-muted-foreground mt-0.5 size-5" />
                )}
                <div className="space-y-1">
                  <div id={`${id}-${kind}-label`} className="text-sm font-semibold">
                    {t(label)}
                  </div>
                  <div id={`${id}-${kind}-description`} className="text-muted-foreground text-sm">
                    {reason ?? t(description)}
                  </div>
                </div>
              </button>
            );
          })}
        </div>

        <div className="flex items-center justify-end gap-3">
          {anyOff && (
            <Link
              to="/admin/settings/playback"
              className="text-muted-foreground hover:text-foreground me-auto text-sm underline underline-offset-4 transition-colors"
            >
              {t("web.markers.redetect.settingsLink")}
            </Link>
          )}
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={isPending}>
            {t("common.cancel")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
