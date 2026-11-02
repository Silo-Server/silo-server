import { Link } from "react-router";
import { ChevronDown, ChevronRight } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useEffect, useId, useRef, type MouseEvent, type ReactNode } from "react";
import { cn } from "@/lib/utils";

// Shared building blocks for the grouped vertical navigation rails used by the
// admin sidebar and the user settings sidebar. Keeping the markup here means
// active states, spacing, and section headers stay consistent across both.

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
   * react-router <Link>. Used for plugin routes mounted at /api/v2/plugin-content/plugins/...
   */
  external?: boolean;
  /** Without href, the item renders as a <button>. */
  onClick?: (event: MouseEvent<HTMLElement>) => void;
  /** Hover or focus: the reader is likely to open this, so warm what it needs. */
  onIntent?: () => void;
}

export function SideNavItem({
  label,
  icon: Icon,
  active = false,
  badge,
  href,
  external,
  onClick,
  onIntent,
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
          <Link
            to={href}
            onClick={onClick}
            onPointerEnter={onIntent}
            onFocus={onIntent}
            aria-current={ariaCurrent}
            className={className}
          >
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

interface SideNavTopicProps {
  label: string;
  icon: LucideIcon;
  open: boolean;
  onToggle: () => void;
  /** Shown on the heading while it is closed, standing in for its pages' own. */
  badge?: ReactNode;
  /** `SideNavSubItem`s. */
  children: ReactNode;
}

/**
 * A heading that opens to list a topic's pages. It never navigates: a click
 * only opens or closes it, so reaching a page is always the page's own row.
 */
/** Scroll an element just far enough to show it, without motion when asked. */
function revealInNav(element: HTMLElement | null) {
  const reduceMotion =
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  // jsdom has no scrollIntoView.
  element?.scrollIntoView?.({ block: "nearest", behavior: reduceMotion ? "auto" : "smooth" });
}

export function SideNavTopic({
  label,
  icon: Icon,
  open,
  onToggle,
  badge,
  children,
}: SideNavTopicProps) {
  const listId = useId();
  const topicRef = useRef<HTMLLIElement>(null);
  const wasOpen = useRef(open);
  const Chevron = open ? ChevronDown : ChevronRight;

  // A topic opened near the bottom of the sidebar would list its pages below
  // the fold, and the click would seem to do nothing.
  useEffect(() => {
    if (open && !wasOpen.current) revealInNav(topicRef.current);
    wasOpen.current = open;
  }, [open]);

  return (
    <li ref={topicRef}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={listId}
        onClick={onToggle}
        className={cn(
          "hover:text-foreground hover:bg-accent/70 flex w-full items-center gap-2.5 rounded-xl px-3 py-2.5 text-left text-[13px] font-medium transition-colors duration-150",
          open ? "text-foreground" : "text-muted-foreground",
        )}
      >
        <span className="flex w-[18px] flex-shrink-0 items-center justify-center">
          <Icon className="h-[18px] w-[18px]" />
        </span>
        <span className="min-w-0 truncate">{label}</span>
        <span className="ml-auto flex items-center gap-2">
          {!open && badge ? badge : null}
          <Chevron className="text-muted-foreground h-3.5 w-3.5" aria-hidden="true" />
        </span>
      </button>
      {open ? (
        <ul
          id={listId}
          aria-label={label}
          className="border-border/70 mt-0.5 mb-1.5 ml-[21px] list-none space-y-px border-l pl-3"
        >
          {children}
        </ul>
      ) : null}
    </li>
  );
}

interface SideNavSubItemProps {
  label: string;
  href: string;
  active?: boolean;
  badge?: ReactNode;
  /** See `SideNavItem`. */
  external?: boolean;
  onClick?: (event: MouseEvent<HTMLElement>) => void;
  onIntent?: () => void;
}

/** One page under a `SideNavTopic`: no icon, indented under its heading. */
export function SideNavSubItem({
  label,
  href,
  active = false,
  badge,
  external,
  onClick,
  onIntent,
}: SideNavSubItemProps) {
  const className = cn(
    "relative flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-[12.5px] transition-colors duration-150",
    active
      ? "text-primary bg-accent font-medium"
      : "text-muted-foreground hover:text-foreground hover:bg-accent/70",
  );
  const inner = (
    <>
      {active && (
        <span
          className="absolute top-1/2 left-[-13px] h-4 w-[2px] -translate-y-1/2 rounded-full"
          style={{ background: "var(--primary)" }}
        />
      )}
      <span className="min-w-0 truncate">{label}</span>
      {badge && <span className="ml-auto">{badge}</span>}
    </>
  );
  const ariaCurrent = active ? "page" : undefined;
  const itemRef = useRef<HTMLLIElement>(null);

  // A deep link can open on a page far down the sidebar; keep it in sight.
  useEffect(() => {
    if (active) revealInNav(itemRef.current);
  }, [active]);

  return (
    <li ref={itemRef}>
      {external ? (
        <a href={href} onClick={onClick} aria-current={ariaCurrent} className={className}>
          {inner}
        </a>
      ) : (
        <Link
          to={href}
          onClick={onClick}
          onPointerEnter={onIntent}
          onFocus={onIntent}
          aria-current={ariaCurrent}
          className={className}
        >
          {inner}
        </Link>
      )}
    </li>
  );
}

/** A nav badge for a page holding something broken; the label is for screen readers. */
export function SideNavAttentionDot({ label }: { label: string }) {
  return (
    <span className="flex items-center">
      <span aria-hidden="true" className="block h-2 w-2 rounded-full bg-amber-400" />
      <span className="sr-only">, {label}</span>
    </span>
  );
}
