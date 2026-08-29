import { Button } from "@/components/ui/button";
import "@/styles/admin-settings.css";

interface SaveBarProps {
  dirtyCount: number;
  onSave: () => void;
  onDiscard: () => void;
  isSaving: boolean;
}

function plural(count: number, word: string) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

/**
 * The floating save pill: the staged count and the two actions. Hidden while
 * the page is clean, so a page with nothing staged has no permanent furniture at
 * the bottom of the viewport. It says nothing about restarts — the one restart
 * prompt is `RestartBanner`, rendered once by the admin shell at the top of
 * every admin page.
 */
export function SaveBar({ dirtyCount, onSave, onDiscard, isSaving }: SaveBarProps) {
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
          <span className="min-w-0 truncate text-[13px] font-medium">
            {plural(dirtyCount, "unsaved change")}
          </span>
          <span className="flex shrink-0 items-center gap-1.5">
            <Button variant="ghost" size="sm" className="rounded-full" onClick={onDiscard}>
              Discard
            </Button>
            <Button
              size="sm"
              onClick={onSave}
              disabled={isSaving}
              className="rounded-full bg-[var(--settings-accent)] text-[#15151a] hover:bg-[var(--settings-accent)] hover:brightness-110"
            >
              {isSaving ? "Saving..." : "Save"}
            </Button>
          </span>
        </div>
      </div>
    </>
  );
}
