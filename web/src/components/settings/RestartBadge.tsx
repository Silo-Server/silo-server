import { RotateCw } from "lucide-react";

import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

/**
 * Small amber chip marking a setting whose value is only read at startup.
 * Driven by the compiled restart-required key list (see `useRestartKeys`) so
 * the fact never has to be hand-copied into a field hint.
 */
export function RestartBadge({ className }: { className?: string }) {
  const t = useT();
  const restartTitle = t("web.settings.restartBadge.title");
  return (
    <span
      title={restartTitle}
      aria-label={restartTitle}
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-md border border-amber-500/25",
        "bg-amber-500/10 px-1.5 py-0.5 text-xs font-medium tracking-wide",
        "text-amber-600 dark:text-amber-400",
        className,
      )}
    >
      <RotateCw className="size-2.5" aria-hidden="true" />
      {t("web.settings.restartBadge.label")}
    </span>
  );
}
