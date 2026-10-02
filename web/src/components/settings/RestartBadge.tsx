import { RotateCw } from "lucide-react";

import { cn } from "@/lib/utils";

const RESTART_TITLE = "Takes effect after a server restart";

/**
 * Small amber chip marking a setting whose value is only read at startup.
 * Driven by the compiled restart-required key list (see `useRestartKeys`) so
 * the fact never has to be hand-copied into a field hint.
 */
export function RestartBadge({ className }: { className?: string }) {
  return (
    <span
      title={RESTART_TITLE}
      aria-label={RESTART_TITLE}
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-md border border-amber-500/25",
        "bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium tracking-wide",
        "text-amber-600 dark:text-amber-400",
        className,
      )}
    >
      <RotateCw className="size-2.5" aria-hidden="true" />
      Restart
    </span>
  );
}
