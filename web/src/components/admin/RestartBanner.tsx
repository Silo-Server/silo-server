import { useState, type ReactNode } from "react";
import { TriangleAlert } from "lucide-react";

import { RestartServerButton } from "@/components/admin/RestartServerButton";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export interface RestartBannerProps {
  /**
   * Whether a restart is owed. The admin shell already reads
   * `useAdminServerStatus()` to drive this banner, so the flag is passed in
   * rather than queried again here — one caller, one query, one banner.
   */
  restartRequired?: boolean;
  /**
   * Opaque value that changes whenever a NEW restart-required save lands
   * (`restart_mark_count` from the same status response). The boolean latches
   * true for the life of the process, so this is the only signal that another
   * requirement arrived after a "Later" — a changed signal un-dismisses.
   */
  restartSignal?: string | number;
  /** One-line explanation under the title. */
  description?: ReactNode;
  className?: string;
}

const DEFAULT_RESTART_DESCRIPTION =
  "Saved settings that are only read at startup take effect after a restart. Playback sessions will reconnect.";

/**
 * The single restart prompt for the admin area: an amber banner at the top of
 * the content column, rendered once by `AdminLayout` so it stays with the admin
 * on every admin page rather than only inside settings.
 *
 * It is a normal in-flow block, not a fixed bar. Nothing to stack above the
 * sidebar, no spacer to buy back scroll room, nothing to lift out of the way:
 * a block at the top of the content column cannot clip under the sidebar and
 * cannot cover the page. It is deliberately not sticky — it may scroll away;
 * being present on every page is the requirement, not being permanently
 * on screen.
 */
export function RestartBanner({
  restartRequired = false,
  restartSignal,
  description,
  className,
}: RestartBannerProps) {
  const pending = restartRequired;
  // "Later" records what was pending at the time, not a boolean: the server's
  // restart_required flag never clears while the process lives, so a fresh
  // restart-required save only shows up as a change in the signal. A
  // dismissal therefore holds exactly until the signature it silenced changes.
  const signature = String(restartSignal ?? "");
  const [dismissedSignature, setDismissedSignature] = useState<string | null>(null);
  // Forgotten entirely once nothing is owed (a restart happened), so the next
  // requirement always prompts again even with an identical key list.
  if (!pending && dismissedSignature !== null) {
    setDismissedSignature(null);
  }
  const visible = pending && dismissedSignature !== signature;

  if (!visible) return null;

  return (
    <div
      role="status"
      className={cn(
        "mb-4 flex flex-wrap items-center gap-3 rounded-2xl border border-amber-500/30 px-4 py-3 sm:mb-6 sm:px-5",
        "bg-[linear-gradient(90deg,color-mix(in_srgb,var(--warning)_16%,transparent),color-mix(in_srgb,var(--warning)_6%,transparent))]",
        className,
      )}
    >
      <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-amber-500/15 text-amber-600 dark:text-amber-400">
        <TriangleAlert className="size-3.5" aria-hidden="true" />
      </span>
      {/* A 16rem flex basis with wrapping: on a phone the two actions drop to
          their own row instead of squeezing the message to a few characters,
          and on a wide page everything still shares one line. */}
      <div className="min-w-0 flex-1 basis-64">
        <p className="text-[13px] font-medium">Restart required</p>
        <p className="text-muted-foreground text-[11.5px]">
          {description ?? DEFAULT_RESTART_DESCRIPTION}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Button variant="ghost" size="sm" onClick={() => setDismissedSignature(signature)}>
          Later
        </Button>
        <RestartServerButton label="Restart server" />
      </div>
    </div>
  );
}
