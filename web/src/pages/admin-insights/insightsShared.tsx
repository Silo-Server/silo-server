import type { ReactNode } from "react";
import { AlertCircle, Info } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

export function InsightKpiCard({
  label,
  value,
  subtext,
  accent,
}: {
  label: string;
  value: string;
  subtext?: ReactNode;
  accent?: "default" | "success" | "warning" | "info";
}) {
  return (
    <div className="surface-panel rounded-2xl border-0 p-4">
      <div className="text-muted-foreground text-[0.6875rem] font-medium">{label}</div>
      <div
        className={cn(
          "mt-1 text-2xl font-extrabold tracking-tight tabular-nums",
          accent === "success" && "text-success-label",
          accent === "warning" && "text-warning-label",
          accent === "info" && "text-info-label",
        )}
      >
        {value}
      </div>
      {subtext ? (
        <div className="text-muted-foreground mt-1 text-xs tabular-nums">{subtext}</div>
      ) : null}
    </div>
  );
}

export function InsightNoticeBanner({
  message,
  className,
}: {
  message: string;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "border-border/70 bg-muted/35 text-muted-foreground flex items-start gap-2.5 rounded-xl border px-3.5 py-2.5 text-xs leading-relaxed",
        className,
      )}
    >
      <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      <span>{message}</span>
    </div>
  );
}

export function InsightSectionError({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const t = useT();
  const detail = error instanceof Error ? error.message : t("web.admin.insights.loadError");
  return (
    <div className="surface-panel flex flex-col items-center justify-center gap-3 rounded-2xl border-0 px-4 py-10 text-center">
      <AlertCircle className="text-destructive h-7 w-7" />
      <div className="space-y-1">
        <p className="text-sm font-semibold">{t("web.admin.insights.loadError")}</p>
        <p className="text-muted-foreground max-w-md text-xs">{detail}</p>
      </div>
      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          {t("web.admin.insights.retry")}
        </Button>
      ) : null}
    </div>
  );
}
