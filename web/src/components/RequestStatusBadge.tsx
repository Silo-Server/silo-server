import type { ComponentProps } from "react";
import { Badge } from "@/components/ui/badge";
import {
  formatRequestDisplayState,
  formatRequestReason,
  type RequestDisplayState,
} from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";
import { useAppLanguage } from "@/i18n";

type BadgeProps = ComponentProps<typeof Badge>;
type BadgeVariant = NonNullable<BadgeProps["variant"]>;

// Theme variants only, so every theme (light ones included) colors them.
// None takes the accent color; Failed is the one destructive badge, and
// closed requests recede behind open ones.
const STATE_STYLES: Record<RequestDisplayState, { variant: BadgeVariant; className?: string }> = {
  pending: { variant: "outline" },
  approved: { variant: "secondary" },
  processing: { variant: "secondary" },
  partially_available: { variant: "secondary" },
  available: { variant: "secondary" },
  declined: { variant: "outline", className: "text-muted-foreground" },
  cancelled: { variant: "outline", className: "text-muted-foreground" },
  failed: { variant: "destructive" },
};

// An outline badge is transparent, so over artwork it needs its own backing.
function overlayClassName(variant: BadgeVariant): string {
  return cn("shadow-sm backdrop-blur-md", variant === "outline" && "bg-background/85");
}

type SharedProps = Omit<BadgeProps, "variant" | "children" | "asChild"> & {
  /** Keeps the badge legible when it sits on top of poster artwork. */
  overlay?: boolean;
};

export type RequestStatusBadgeProps = SharedProps & {
  state: RequestDisplayState;
  /** Leads the label with a count, as in a per-status summary. */
  count?: number;
};

/** The single badge the request pages use for a request's state. */
export function RequestStatusBadge({
  state,
  count,
  overlay = false,
  className,
  ...props
}: RequestStatusBadgeProps) {
  // The label comes from a plain helper; follow the app language.
  useAppLanguage();
  const style = STATE_STYLES[state];
  return (
    <Badge
      variant={style.variant}
      data-request-state={state}
      className={cn(style.className, overlay && overlayClassName(style.variant), className)}
      {...props}
    >
      {count !== undefined ? <span className="tabular-nums">{count}</span> : null}
      <span className="truncate">{formatRequestDisplayState(state)}</span>
    </Badge>
  );
}

/** Explains why a title without a request cannot be requested. */
export function RequestReasonBadge({
  reason,
  overlay = false,
  className,
  ...props
}: SharedProps & { reason?: string }) {
  useAppLanguage();
  return (
    <Badge
      variant="outline"
      className={cn("text-muted-foreground", overlay && overlayClassName("outline"), className)}
      {...props}
    >
      <span className="truncate">{formatRequestReason(reason)}</span>
    </Badge>
  );
}
