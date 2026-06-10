import { Link } from "react-router";
import type { LucideIcon } from "lucide-react";
import type { MouseEvent, ReactNode } from "react";
import { cn } from "@/lib/utils";

// Shared building blocks for the grouped vertical navigation rails used by the
// admin sidebar, the admin settings page, and the user settings page. Keeping
// the markup here means active states, spacing, and section headers stay
// consistent across all three.

interface SideNavSectionProps {
  label: string;
  /** Prefix for the section heading id, e.g. "admin-nav". */
  idPrefix: string;
  children: ReactNode;
}

export function SideNavSection({ label, idPrefix, children }: SideNavSectionProps) {
  const headingId = `${idPrefix}-${label.toLowerCase().replace(/\s+/g, "-")}`;
  return (
    <div role="group" aria-labelledby={headingId}>
      <h3
        id={headingId}
        className="text-muted-foreground mb-2 px-2 text-[10px] font-semibold tracking-[0.18em] uppercase"
      >
        {label}
      </h3>
      <ul className="list-none space-y-0.5">{children}</ul>
    </div>
  );
}

interface SideNavItemProps {
  label: string;
  icon: LucideIcon;
  active?: boolean;
  badge?: ReactNode;
  /** Internal route rendered as a react-router <Link>. */
  href?: string;
  /**
   * With href, render a plain <a> (full page navigation) instead of a
   * react-router <Link>. Used for plugin routes mounted at /api/v1/plugins/...
   */
  external?: boolean;
  /** Without href, the item renders as a <button>. */
  onClick?: (event: MouseEvent<HTMLElement>) => void;
}

export function SideNavItem({
  label,
  icon: Icon,
  active = false,
  badge,
  href,
  external,
  onClick,
}: SideNavItemProps) {
  const className = cn(
    "relative flex w-full items-center gap-2.5 rounded-xl px-3 py-2.5 text-left text-[13px] font-medium transition-colors duration-150",
    active
      ? "text-primary bg-accent"
      : "text-muted-foreground hover:text-foreground hover:bg-accent/70",
  );
  const inner = (
    <>
      {active && (
        <span
          className="absolute top-1/2 left-[-12px] h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
          style={{ background: "var(--primary)" }}
        />
      )}
      <span className="flex w-[18px] flex-shrink-0 items-center justify-center">
        <Icon className="h-[18px] w-[18px]" />
      </span>
      <span className="min-w-0 truncate">{label}</span>
      {badge && <span className="ml-auto">{badge}</span>}
    </>
  );
  const ariaCurrent = active ? "page" : undefined;

  return (
    <li>
      {href ? (
        external ? (
          <a href={href} onClick={onClick} aria-current={ariaCurrent} className={className}>
            {inner}
          </a>
        ) : (
          <Link to={href} onClick={onClick} aria-current={ariaCurrent} className={className}>
            {inner}
          </Link>
        )
      ) : (
        <button type="button" onClick={onClick} aria-current={ariaCurrent} className={className}>
          {inner}
        </button>
      )}
    </li>
  );
}
