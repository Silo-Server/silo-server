import { CircleCheck } from "lucide-react";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

export function WatchedCheckIndicator({ className }: { className?: string }) {
  const t = useT();
  return (
    <span
      role="img"
      aria-label={t("web.item.card.watched")}
      data-watched-indicator="icon-only"
      className={cn("text-muted-foreground inline-flex shrink-0 items-center", className)}
    >
      <CircleCheck aria-hidden="true" className="size-4" />
    </span>
  );
}
