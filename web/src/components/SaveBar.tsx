import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import "@/styles/admin-settings.css";

interface SaveBarProps {
  dirtyCount: number;
  onSave: () => void;
  onDiscard: () => void;
  isSaving: boolean;
  saveLabel?: string;
  discardLabel?: string;
  /**
   * False while nothing staged can be saved as it stands, e.g. every edit is
   * waiting on a reload after another admin's change. Discard stays available.
   */
  canSave?: boolean;
  /** Replaces the default "N unsaved changes", e.g. to name the staged fields. */
  message?: ReactNode;
  /**
   * `"settings"` is the admin settings pill, centred beside the admin
   * sidebar. `"page"` docks a full-width bar along the bottom of the page
   * container in either shell, above the background playback bar.
   */
  placement?: "settings" | "page";
  /**
   * Page placement only. Whether the bar shows; defaults to whether anything is
   * staged. Create mode shows it before any edit ("Not created yet").
   */
  visible?: boolean;
  /** Page placement only. `"idle"` mutes the dot while nothing is pending. */
  tone?: "pending" | "idle";
  /** Page placement only. The region's accessible name. */
  label?: string;
  /** Page placement only. Classes for the bar, such as a narrower measure. */
  className?: string;
}

function plural(count: number, word: string) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

/**
 * The floating save bar: what is staged and the two actions. Hidden while
 * the page is clean (unless a page bar is shown on purpose, as in create mode),
 * so a page with nothing staged has no permanent furniture at the bottom of
 * the viewport. It says nothing about restarts — the one restart
 * prompt is `RestartBanner`, rendered once by the admin shell at the top of
 * every admin page.
 */
export function SaveBar({
  dirtyCount,
  onSave,
  onDiscard,
  isSaving,
  saveLabel = "Save",
  discardLabel = "Discard",
  canSave = true,
  message,
  placement = "settings",
  visible = dirtyCount > 0,
  tone = "pending",
  label = "Unsaved changes",
  className,
}: SaveBarProps) {
  const text = message ?? plural(dirtyCount, "unsaved change");
  const saveDisabled = isSaving || !canSave;
  const saveText = isSaving ? "Saving..." : saveLabel;

  if (placement === "page") {
    return (
      <>
        {visible && (
          <>
            {/* In-flow scroll room for the bar and the playback bar it rises
                above. The admin shell drops its playback padding at lg, so the
                spacer can't rely on the shell to cover that part. */}
            <div aria-hidden="true" className="h-[calc(7rem+var(--playback-bar-clearance,0px))]" />
            <div
              aria-hidden="true"
              className="pointer-events-none fixed right-0 bottom-(--playback-bar-clearance,0px) left-[var(--app-sidebar-offset,0px)] z-30 h-40 bg-gradient-to-t from-[var(--background)] via-[color-mix(in_srgb,var(--background)_72%,transparent)] to-transparent"
            />
          </>
        )}
        {/* Starts at the sidebar edge the shell publishes in
            `--app-sidebar-offset`, pads by the shell's `--page-gutter` and keeps
            the 1000px page measure (or the page's own, via `className`), so it
            lines up with the content column. It rises by the measured
            `--playback-bar-clearance`. The status stays mounted (empty,
            without the landmark) while the bar is hidden, so the first change
            is announced by a live region already in place. Only the message
            is announced; the buttons stay out of it. */}
        <div
          className={
            visible
              ? "pointer-events-none fixed right-0 bottom-[calc(var(--playback-bar-clearance,0px)+0.75rem)] left-[var(--app-sidebar-offset,0px)] z-40 flex justify-center px-(--page-gutter,1rem) lg:bottom-[calc(var(--playback-bar-clearance,0px)+1.125rem)]"
              : "sr-only"
          }
        >
          <div
            role={visible ? "region" : undefined}
            aria-label={visible ? label : undefined}
            className={
              visible
                ? cn(
                    "bg-popover/95 border-border pointer-events-auto flex w-full max-w-[1000px] items-center justify-between gap-2 rounded-2xl border py-2 pr-2 pl-4 shadow-2xl backdrop-blur-xl",
                    className,
                  )
                : undefined
            }
          >
            <span role="status" className="flex min-w-0 items-center gap-2 text-sm font-medium">
              {visible && (
                <>
                  <span
                    aria-hidden="true"
                    className={cn(
                      "size-[7px] shrink-0 rounded-full",
                      tone === "idle" ? "bg-muted-foreground" : "bg-warning",
                    )}
                  />
                  <span className="line-clamp-2 min-w-0 lg:line-clamp-1">{text}</span>
                </>
              )}
            </span>
            {visible && (
              <span className="flex shrink-0 items-center gap-2">
                <Button variant="ghost" className="h-11 lg:h-8" onClick={onDiscard}>
                  {discardLabel}
                </Button>
                <Button className="h-11 lg:h-8" onClick={() => onSave()} disabled={saveDisabled}>
                  {saveText}
                </Button>
              </span>
            )}
          </div>
        </div>
      </>
    );
  }

  if (dirtyCount <= 0) return null;

  return (
    <>
      {/* In-flow scroll room. The pill is fixed, so without this the last row of
          the page parks under it at full scroll and cannot be reached. Sized to
          clear the pill (bottom 1.5rem + 3rem tall) with margin. */}
      <div aria-hidden="true" className="h-28" />
      {/* Scrim so page content dissolves under the pill instead of colliding.
          Stops at the desktop sidebar so it never tints the nav. */}
      <div
        aria-hidden="true"
        className="pointer-events-none fixed right-0 bottom-0 left-0 z-30 h-40 bg-gradient-to-t from-[var(--background)] via-[color-mix(in_srgb,var(--background)_72%,transparent)] to-transparent lg:left-[240px]"
      />
      {/* `lg:left-[240px]` matches AdminLayout's `lg:ml-[240px]`: the pill
          centers over the content column, not the whole viewport, and stays
          clear of the sidebar it would otherwise paint under. */}
      <div
        role="status"
        className="pointer-events-none fixed right-0 bottom-6 left-0 z-40 flex justify-center px-4 lg:left-[240px]"
      >
        <div className="glass pointer-events-auto flex max-w-full items-center gap-3 rounded-full py-2 pr-2 pl-4 shadow-2xl backdrop-blur-xl sm:gap-4 sm:pl-5">
          <span className="min-w-0 truncate text-[13px] font-medium">{text}</span>
          <span className="flex shrink-0 items-center gap-1.5">
            <Button variant="ghost" size="sm" className="rounded-full" onClick={onDiscard}>
              {discardLabel}
            </Button>
            <Button
              size="sm"
              onClick={() => onSave()}
              disabled={saveDisabled}
              className="rounded-full bg-[var(--settings-accent)] text-[#15151a] hover:bg-[var(--settings-accent)] hover:brightness-110"
            >
              {saveText}
            </Button>
          </span>
        </div>
      </div>
    </>
  );
}
