import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

export type RowOrigin = "server" | "yours";

/**
 * Tag telling a profile whether a Home row comes from the server or was added
 * by this profile itself. Never shown on the admin surface, where every row
 * belongs to the server.
 */
export function RowOriginBadge({ origin, className }: { origin: RowOrigin; className?: string }) {
  const t = useT();
  if (origin === "yours") {
    return (
      <span
        className={cn(
          "bg-info/15 text-info ring-info/30 inline-flex h-[22px] shrink-0 items-center rounded-full px-2 text-xs font-semibold ring-1 ring-inset",
          className,
        )}
      >
        {t("web.home.rows.line.yours")}
      </span>
    );
  }
  return (
    <span
      className={cn(
        "bg-muted/70 text-muted-foreground ring-border inline-flex h-[22px] shrink-0 items-center rounded-full px-2 text-xs font-semibold ring-1 ring-inset",
        className,
      )}
    >
      {t("web.home.rows.line.server")}
    </span>
  );
}
