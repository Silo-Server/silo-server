import { Children, useState, type ReactNode } from "react";
import { X } from "lucide-react";

import { useRootHeightProperty } from "@/hooks/useRootHeightProperty";
import { useT } from "@/i18n";
import { getInitial } from "@/lib/text";

/**
 * A message part split into its edge spaces and the words between them. A
 * trailing PDF or PDI mark (the `ar-XB` pseudo-locale's) counts as an edge too,
 * so the space before it stays out of the part that is cut; leading marks stay
 * with the words they apply to.
 */
const EDGE_SPACES = /^(\s*)([\s\S]*?)([\s\u202c\u2069]*)$/;

/** Up to this many characters a name is short enough to keep whole. */
const SHORT_NAME = 3;

/**
 * A narrow banner squeezes the "authorized by" sentence. Its words give up space
 * only once the name is down to a letter and "…", and are then cut with "…" too.
 * The spaces at their edges stay, so a cut word never runs into the name.
 */
function wordsYieldLast(message: ReactNode): ReactNode {
  return Children.map(message, (part) => {
    if (typeof part !== "string") return part;
    const [, before = "", words = "", after = ""] = EDGE_SPACES.exec(part) ?? [];
    return (
      <>
        {before ? <span className="shrink-0 whitespace-pre">{before}</span> : null}
        <span className="min-w-0 overflow-hidden text-ellipsis whitespace-pre">{words}</span>
        {after ? <span className="shrink-0 whitespace-pre">{after}</span> : null}
      </>
    );
  });
}

interface ImpersonationBannerProps {
  userName: string;
  impersonatorName: string;
  onEnd: () => void | Promise<void>;
}

export default function ImpersonationBanner({
  userName,
  impersonatorName,
  onEnd,
}: ImpersonationBannerProps) {
  const t = useT();
  const [isEnding, setIsEnding] = useState(false);
  // Pinned chrome and in-page jumps clear the banner by this height; see
  // `--impersonation-banner-height` in app.css.
  const bannerRef = useRootHeightProperty("--impersonation-banner-height");

  async function handleEnd() {
    setIsEnding(true);
    try {
      await onEnd();
    } finally {
      setIsEnding(false);
    }
  }

  const initial = getInitial(userName) || "?";
  // The authorizing name keeps at least a letter and "…" before the words around
  // it give way. A short name is kept whole instead: a fixed floor wider than
  // the name would leave a gap after it.
  const nameFloor = [...impersonatorName].length <= SHORT_NAME ? "min-w-max" : "min-w-[2em]";

  return (
    <div
      ref={bannerRef}
      role="status"
      aria-live="polite"
      data-sticky-header
      className="impersonation-banner sticky top-0 z-50"
    >
      <div className="impersonation-banner-inner flex items-center gap-3 px-4 py-2 sm:gap-4 sm:px-6 lg:ps-8 lg:pe-8">
        <div className="flex shrink-0 items-center gap-2">
          <span className="relative inline-flex h-2 w-2" aria-hidden="true">
            <span className="impersonation-pulse absolute inset-0 rounded-full motion-safe:animate-ping" />
            <span className="impersonation-dot relative inline-flex h-2 w-2 rounded-full" />
          </span>
          <span className="impersonation-label text-[10px] font-semibold uppercase">
            {t("web.shell.impersonation.viewingAs")}
          </span>
        </div>

        <div className="flex min-w-0 items-center gap-2">
          <span className="impersonation-chip flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-[11px] font-bold">
            {initial}
          </span>
          <span
            dir="auto"
            className="text-foreground truncate text-sm font-semibold tracking-tight"
          >
            {userName}
          </span>
        </div>

        <span className="text-muted-foreground hidden overflow-hidden text-xs whitespace-pre sm:flex">
          {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- decorative separator */}
          <span className="text-muted-foreground/60 me-2">·</span>
          {wordsYieldLast(
            t("web.shell.impersonation.authorizedBy", {
              name: (
                <span dir="auto" className="truncate">
                  {impersonatorName}
                </span>
              ),
              strong: (chunks) => (
                <span
                  className={`text-foreground/80 flex max-w-max grow basis-0 overflow-hidden font-medium ${nameFloor}`}
                >
                  {chunks}
                </span>
              ),
            }),
          )}
        </span>

        <button
          type="button"
          onClick={() => void handleEnd()}
          disabled={isEnding}
          aria-label={t("web.shell.impersonation.endSessionLabel")}
          className="impersonation-end-btn group ms-auto inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md px-3 text-xs font-semibold whitespace-nowrap transition-all duration-150 outline-none disabled:pointer-events-none disabled:opacity-50"
        >
          <X
            className="h-3.5 w-3.5 transition-transform duration-200 motion-safe:group-hover:rotate-90"
            aria-hidden="true"
          />
          <span>{t("web.shell.impersonation.endSession")}</span>
        </button>
      </div>
    </div>
  );
}
