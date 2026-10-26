import { useId, type ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import "@/styles/admin-settings.css";

export type ProviderTileState = "connected" | "not_connected" | "error" | "editing";

export interface ProviderTileAction {
  label: string;
  onClick: () => void;
  disabled?: boolean;
}

export interface ProviderTileProps {
  name: string;
  /** One short line under the name, e.g. "Community subtitles". */
  tagline?: ReactNode;
  /** Two letters for the logo square; ignored when `logo` is given. */
  monogram?: string;
  /** Background/foreground classes for the logo square. */
  monogramClass?: string;
  /** Replaces the monogram, e.g. with an icon. */
  logo?: ReactNode;
  state: ProviderTileState;
  /** Overrides the default word for the state. */
  statePill?: string;
  /** Small line at the foot. Only for what the state does not already say. */
  meta?: ReactNode;
  /** The tile's own button — Connect, Manage, Fix. */
  primaryAction?: ProviderTileAction;
  /** Chips beside the name, e.g. a `RestartBadge`. */
  badge?: ReactNode;
  /** Controls level with the state, e.g. an enable switch. */
  headerActions?: ReactNode;
  /** Spans the tile across the grid and reveals `children` as an inline panel. */
  expanded?: boolean;
  /** Disables every control inside while a request is in flight. */
  busy?: boolean;
  className?: string;
  /** The inline connect panel: credential fields and their action row. */
  children?: ReactNode;
}

const STATE_LABELS: Record<ProviderTileState, string> = {
  connected: "Connected",
  not_connected: "Not connected",
  error: "Error",
  editing: "Editing",
};

const STATE_DOT_CLASSES: Record<ProviderTileState, string> = {
  connected: "bg-emerald-500",
  not_connected: "bg-muted-foreground/40",
  error: "bg-amber-500",
  editing: "bg-[var(--settings-accent)]",
};

/** Dot plus word: the tile's one status signal, reused wherever it is listed. */
export function ProviderState({
  state,
  label,
  className,
}: {
  state: ProviderTileState;
  label?: string;
  className?: string;
}) {
  return (
    <span
      data-state={state}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 text-xs whitespace-nowrap",
        state === "error" ? "text-amber-600 dark:text-amber-400" : "text-muted-foreground",
        className,
      )}
    >
      <span aria-hidden="true" className={cn("size-1.5 rounded-full", STATE_DOT_CLASSES[state])} />
      {label ?? STATE_LABELS[state]}
    </span>
  );
}

/**
 * The list providers sit in: one row each, divided by hairlines like every
 * other settings row. An expanded row opens its panel in place.
 */
export function ProviderTileGrid({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  return <div className={cn("provider-list flex min-w-0 flex-col", className)}>{children}</div>;
}

/** Result of the last Test on a tile, kept in memory for the tile itself. */
export interface ProviderTestState {
  ok: boolean;
  message: string;
  /** `Date.now()` when the result came back. */
  at: number;
  durationMs: number;
}

function testedLabel(test: ProviderTestState): string {
  const seconds = Math.max(0, Math.round((Date.now() - test.at) / 1000));
  const ago = seconds < 60 ? `${seconds}s ago` : `${Math.round(seconds / 60)}m ago`;
  return `Tested ${ago} · ${test.durationMs} ms`;
}

/**
 * The tile's state, from the three things every provider page knows: whether
 * its panel is open, the last test, and whether the provider is actually usable
 * right now. Shared so "connected" means the same thing on every page.
 */
export function resolveProviderTileState({
  expanded,
  test,
  connected,
}: {
  expanded: boolean;
  test: ProviderTestState | undefined;
  connected: boolean;
}): ProviderTileState {
  if (expanded) return "editing";
  if (test && !test.ok) return "error";
  return connected ? "connected" : "not_connected";
}

/**
 * The action row every connect panel ends with. Every control in it carries a
 * border or a fill at rest: a `ghost` button reads as plain text until it is
 * hovered, which hid the secondary actions from admins who never hovered them.
 */
export function ProviderPanelActions({
  test,
  children,
}: {
  test?: ProviderTestState;
  children: ReactNode;
}) {
  return (
    // Buttons sit at the right edge — the same corner as the collapsed tile's
    // Manage button — so the eye finds the actions in one place in both
    // states. The test status takes the leftover left side.
    <div className="mt-3.5 flex flex-wrap items-center justify-end gap-2">
      {test ? (
        <span
          role="status"
          className={
            test.ok
              ? "text-muted-foreground mr-auto text-xs"
              : "mr-auto text-xs text-amber-600 dark:text-amber-400"
          }
        >
          {test.ok ? testedLabel(test) : test.message}
        </span>
      ) : null}
      {children}
    </div>
  );
}

/**
 * One third-party provider as a settings row: identity, connection state and
 * its button, and — once expanded — its credential panel inline instead of in
 * a dialog, so the admin never loses sight of the list they are working
 * through.
 */
export function ProviderTile({
  name,
  tagline,
  monogram,
  monogramClass,
  logo,
  state,
  statePill,
  meta,
  primaryAction,
  badge,
  headerActions,
  expanded = false,
  busy = false,
  className,
  children,
}: ProviderTileProps) {
  const headingId = useId();
  const mark = logo ?? (monogram ?? name.slice(0, 2)).toUpperCase();

  return (
    <fieldset
      disabled={busy}
      aria-labelledby={headingId}
      data-state={state}
      data-expanded={expanded ? "true" : undefined}
      className={cn("provider-row flex min-w-0 flex-col py-3.5", className)}
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span
          aria-hidden="true"
          className={cn(
            "grid size-9 shrink-0 place-items-center rounded-[10px] text-sm font-bold tracking-tight",
            monogramClass ?? "bg-muted text-muted-foreground",
          )}
        >
          {mark}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 id={headingId} className="truncate text-sm font-medium">
              {name}
            </h3>
            {badge}
          </div>
          {tagline ? (
            <p className="text-muted-foreground mt-0.5 text-xs leading-snug">{tagline}</p>
          ) : null}
          {meta ? (
            <p
              className={cn(
                "mt-1 text-xs",
                state === "error" ? "text-amber-600 dark:text-amber-400" : "text-muted-foreground",
              )}
            >
              {meta}
            </p>
          ) : null}
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-2.5">
          <ProviderState state={state} label={statePill} />
          {headerActions}
          {!expanded && primaryAction ? (
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={primaryAction.onClick}
              disabled={primaryAction.disabled}
            >
              {primaryAction.label}
            </Button>
          ) : null}
        </div>
      </div>

      {expanded && children ? <div className="mt-3.5 pl-12">{children}</div> : null}
    </fieldset>
  );
}
