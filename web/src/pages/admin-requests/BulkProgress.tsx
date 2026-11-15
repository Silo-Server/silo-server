import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import type { BulkRequestProgress } from "@/hooks/queries/admin/requests";
import { useT } from "@/i18n";

export function BulkProgress({
  progress,
  running,
  onDismiss,
}: {
  progress: BulkRequestProgress;
  running: boolean;
  onDismiss: () => void;
}) {
  const t = useT();
  const { action, total, done, failures } = progress;
  const approving = action === "approve";
  if (running) {
    const values = { count: total, count_text: total, done };
    return (
      <div role="status" className="border-border bg-card space-y-2 rounded-lg border p-3 text-sm">
        <p>
          {approving
            ? t("web.admin.requests.bulk.approvingProgress", values)
            : t("web.admin.requests.bulk.decliningProgress", values)}
        </p>
        <Progress
          value={(done / total) * 100}
          aria-label={
            approving
              ? t("web.admin.requests.bulk.approvingLabel")
              : t("web.admin.requests.bulk.decliningLabel")
          }
        />
      </div>
    );
  }
  if (failures.length === 0) return null;
  const resultValues = {
    count: failures.length,
    count_text: failures.length,
    succeeded: total - failures.length,
    total,
  };
  return (
    <div
      role="alert"
      className="border-destructive/40 bg-destructive/5 space-y-2 rounded-lg border p-3 text-sm"
    >
      <div className="flex items-start justify-between gap-3">
        <p className="flex items-center gap-2 font-medium">
          <AlertTriangle className="text-destructive h-4 w-4 shrink-0" aria-hidden="true" />
          {approving
            ? t("web.admin.requests.bulk.approveResult", resultValues)
            : t("web.admin.requests.bulk.declineResult", resultValues)}
        </p>
        <Button size="sm" variant="ghost" onClick={onDismiss}>
          {t("web.admin.requests.bulk.dismiss")}
        </Button>
      </div>
      <ul className="space-y-1">
        {failures.map((failure) => (
          <li key={failure.id}>
            {t("web.admin.requests.bulk.failure", {
              title: failure.title,
              message: failure.message,
              name: (chunks) => <span className="font-medium">{chunks}</span>,
              detail: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
            })}
          </li>
        ))}
      </ul>
    </div>
  );
}
