import type { ReactNode } from "react";
import { ChevronDown } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function CollapsibleDiagnosticsSection({
  title,
  description,
  count,
  icon,
  iconClassName,
  open,
  onOpenChange,
  children,
}: {
  title: string;
  description: string;
  count: number;
  icon: ReactNode;
  iconClassName?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  return (
    <section className="surface-panel-subtle overflow-hidden rounded-2xl">
      <button
        type="button"
        className="hover:bg-surface-hover/40 flex w-full items-center gap-3 px-5 py-4 text-left transition-colors"
        aria-expanded={open}
        onClick={() => onOpenChange(!open)}
      >
        <div
          className={cn(
            "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
            iconClassName ?? "bg-amber-500/10",
          )}
        >
          {icon}
        </div>
        <div className="min-w-0 flex-1 space-y-0.5">
          <h2 className="text-sm font-semibold tracking-wide">{title}</h2>
          <p className="text-muted-foreground text-xs leading-relaxed">{description}</p>
        </div>
        {open ? (
          <Badge variant="secondary" className="text-[11px] tabular-nums">
            {count}
          </Badge>
        ) : (
          <div className="text-2xl leading-none font-bold tabular-nums">{count}</div>
        )}
        <ChevronDown
          className={cn(
            "text-muted-foreground h-4 w-4 shrink-0 transition-transform",
            !open && "-rotate-90",
          )}
        />
      </button>
      {open ? <div className="px-3 pb-3">{children}</div> : null}
    </section>
  );
}
