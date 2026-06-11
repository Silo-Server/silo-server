import type { LucideIcon } from "lucide-react";
import { X } from "lucide-react";
import { Link } from "react-router";

import { Button } from "@/components/ui/button";
import { notificationTimeAgo } from "@/lib/notifications";
import type { AppNotification } from "@/api/types";

interface NotificationStripBarProps {
  /** The notification to render in the bar. */
  item: AppNotification;
  /** Leading icon for the bar. */
  icon: LucideIcon;
  /** Optional eyebrow label rendered before the title (e.g. "ANNOUNCEMENT"). */
  label?: string;
  /**
   * Visual treatment. "announcement" uses the primary accent styling; "default"
   * uses the plain surface styling.
   */
  variant: "announcement" | "default";
  /**
   * When true, shift the dismiss button left on large screens so it clears the
   * admin-only ServerActivity widget pinned at `fixed top-5 right-5` (which
   * floats over the top-right corner of the page content).
   */
  reserveActivityWidget?: boolean;
  /** Called when the bar is opened (Link click) or dismissed (✕ click). */
  onDismiss: (id: number) => void;
}

export function NotificationStripBar({
  item,
  icon: Icon,
  label,
  variant,
  reserveActivityWidget,
  onDismiss,
}: NotificationStripBarProps) {
  const when =
    notificationTimeAgo(item.created_at) ?? new Date(item.created_at).toLocaleDateString();

  const isAnnouncement = variant === "announcement";
  const containerClass = isAnnouncement
    ? "border-primary/30 bg-primary/10 text-primary"
    : "border-border bg-surface";
  const iconClass = isAnnouncement ? "h-4 w-4 shrink-0" : "text-muted-foreground h-4 w-4 shrink-0";
  const dismissClass = isAnnouncement
    ? `text-primary hover:text-primary ${reserveActivityWidget ? "lg:mr-10" : ""}`
    : reserveActivityWidget
      ? "lg:mr-10"
      : undefined;
  const titleClass = isAnnouncement
    ? "text-foreground shrink-0 font-medium"
    : "shrink-0 font-medium";

  return (
    <div className={`${containerClass} flex items-center gap-3 rounded-lg border px-4 py-2`}>
      <Link
        to={item.link ?? "/notifications"}
        onClick={() => onDismiss(item.id)}
        className="flex min-w-0 flex-1 items-center gap-3"
      >
        <Icon className={iconClass} />
        {label && <span className="shrink-0 text-xs font-semibold tracking-wide">{label}</span>}
        <span className={titleClass}>{item.title}</span>
        {item.body && (
          <span className="text-muted-foreground min-w-0 flex-1 truncate text-xs">{item.body}</span>
        )}
        {when && <span className="text-muted-foreground shrink-0 text-xs">{when}</span>}
      </Link>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Dismiss"
        className={dismissClass}
        onClick={(e) => {
          e.stopPropagation();
          onDismiss(item.id);
        }}
      >
        <X className="h-3 w-3" />
      </Button>
    </div>
  );
}
