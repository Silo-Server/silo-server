import { CircleCheck } from "lucide-react";
import { cn } from "@/lib/utils";

export function WatchedCheckIndicator({ className }: { className?: string }) {
  return (
    <span
      role="img"
      aria-label="Watched"
      data-watched-indicator="icon-only"
      className={cn("text-muted-foreground inline-flex shrink-0 items-center", className)}
    >
      <CircleCheck aria-hidden="true" className="size-4" />
    </span>
  );
}
