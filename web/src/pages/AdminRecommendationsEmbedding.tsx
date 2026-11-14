import { useState } from "react";
import { AlertTriangle, Loader2, RotateCcw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import type { parseRecommendationEmbeddingLock } from "@/pages/admin-settings/recommendationsSettings";
import { useT } from "@/i18n";

export function RecEmbeddingLockCard({
  lock,
}: {
  lock: ReturnType<typeof parseRecommendationEmbeddingLock>;
}) {
  const t = useT();
  if (!lock) {
    return null;
  }

  return (
    <div className="surface-panel max-w-3xl rounded-2xl border-0 px-5 py-4">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <h2 className="text-sm font-semibold">{t("web.admin.recommendations.lock.title")}</h2>
          <p className="text-muted-foreground text-sm">
            {t("web.admin.recommendations.lock.body")}
          </p>
        </div>
        <Badge variant="outline" className="shrink-0">
          {t("web.admin.recommendations.lock.badge")}
        </Badge>
      </div>

      <dl className="mt-4 grid gap-4 sm:grid-cols-3">
        <div className="space-y-1">
          <dt className="text-muted-foreground text-xs tracking-wide uppercase">
            {t("web.admin.recommendations.lock.model")}
          </dt>
          <dd className="text-sm font-medium">{lock.model}</dd>
        </div>
        <div className="space-y-1">
          <dt className="text-muted-foreground text-xs tracking-wide uppercase">
            {t("web.admin.recommendations.lock.sourceDimensions")}
          </dt>
          <dd className="text-sm font-medium">{lock.sourceDimensions}</dd>
        </div>
        <div className="space-y-1">
          <dt className="text-muted-foreground text-xs tracking-wide uppercase">
            {t("web.admin.recommendations.lock.storageDimensions")}
          </dt>
          <dd className="text-sm font-medium">{lock.storageDimensions}</dd>
        </div>
      </dl>

      <p className="text-muted-foreground mt-4 text-xs">{lock.note}</p>
    </div>
  );
}

export function RecEmbeddingResetCard({
  lockConflict,
  disabled,
  isPending,
  onReset,
}: {
  lockConflict: string;
  disabled: boolean;
  isPending: boolean;
  onReset: () => void;
}) {
  const t = useT();
  const [confirming, setConfirming] = useState(false);
  return (
    <div className="surface-panel max-w-3xl space-y-3 rounded-2xl border-0 px-5 py-4">
      {lockConflict && (
        <div
          role="alert"
          className="border-warning/30 bg-warning/10 text-warning flex items-start gap-3 rounded-xl border px-4 py-3 text-sm"
        >
          <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0" />
          <span>{lockConflict}</span>
        </div>
      )}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1">
          <h2 className="text-sm font-semibold">{t("web.admin.recommendations.reset.title")}</h2>
          <p className="text-muted-foreground text-sm">
            {t("web.admin.recommendations.reset.body")}
          </p>
        </div>
        <Button
          size="sm"
          variant="outline"
          className="text-destructive shrink-0"
          onClick={() => setConfirming(true)}
          disabled={disabled || isPending}
        >
          {isPending ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <RotateCcw className="size-3.5" />
          )}
          {t("web.admin.recommendations.reset.button")}
        </Button>
      </div>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t("web.admin.recommendations.reset.confirmTitle")}
        description={t("web.admin.recommendations.reset.confirmBody")}
        confirmLabel={t("web.admin.recommendations.reset.confirmButton")}
        variant="destructive"
        isPending={isPending}
        onConfirm={() => {
          setConfirming(false);
          onReset();
        }}
      />
    </div>
  );
}
