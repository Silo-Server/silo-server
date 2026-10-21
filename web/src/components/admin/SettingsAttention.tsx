import { ArrowRight, TriangleAlert } from "lucide-react";
import { Link } from "react-router";

import type { SettingsHealthItem } from "@/hooks/admin/useSettingsHealth";
import { adminSettingsHref } from "@/lib/adminSettingsSearch";
import { cn } from "@/lib/utils";

/**
 * The server setup the admin still owes: storage nobody configured, a restart
 * a saved setting is waiting on, mail that cannot send. Each item links to the
 * settings page that fixes it. Renders nothing while everything is in order.
 */
export function SettingsAttention({
  items,
  className,
}: {
  items: readonly SettingsHealthItem[];
  className?: string;
}) {
  if (items.length === 0) return null;

  return (
    <section
      aria-label="Needs attention"
      className={cn("border-warning/30 bg-warning/8 rounded-2xl border p-4", className)}
    >
      <p className="text-warning flex items-center gap-2 text-sm font-semibold">
        <TriangleAlert className="h-4 w-4" aria-hidden="true" />
        Needs attention
      </p>
      <ul className="mt-3 grid list-none gap-2 sm:grid-cols-2 xl:grid-cols-3">
        {items.map((item) =>
          item.action ? (
            <li key={item.id}>
              <Link
                to={adminSettingsHref(item.action.page)}
                className="hover:bg-warning/10 focus-visible:ring-ring/60 border-warning/20 flex h-full items-start gap-3 rounded-xl border px-3 py-2.5 transition-colors focus-visible:ring-[3px] focus-visible:outline-none"
              >
                <span className="min-w-0 flex-1 text-sm leading-snug">
                  <span className="text-foreground block font-medium">
                    {item.label} · {item.stateText}
                  </span>
                  <span className="text-muted-foreground block text-xs">{item.detail}</span>
                </span>
                <span className="text-foreground/80 inline-flex shrink-0 items-center gap-1 text-xs font-medium">
                  {item.action.label}
                  <ArrowRight className="h-3 w-3" aria-hidden="true" />
                </span>
              </Link>
            </li>
          ) : null,
        )}
      </ul>
    </section>
  );
}
