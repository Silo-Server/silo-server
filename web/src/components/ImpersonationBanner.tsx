import { useState } from "react";
import { X } from "lucide-react";

import { useT } from "@/i18n";

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

  async function handleEnd() {
    setIsEnding(true);
    try {
      await onEnd();
    } finally {
      setIsEnding(false);
    }
  }

  const initial = userName.charAt(0).toUpperCase() || "?";

  return (
    <div role="status" aria-live="polite" className="impersonation-banner sticky top-0 z-50">
      <div className="impersonation-banner-inner flex items-center gap-3 px-4 py-2 sm:gap-4 sm:px-6 lg:pr-8 lg:pl-8">
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
          <span className="text-foreground truncate text-sm font-semibold tracking-tight">
            {userName}
          </span>
        </div>

        <span className="text-muted-foreground hidden truncate text-xs sm:inline">
          {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- decorative separator */}
          <span className="text-muted-foreground/60 mr-2">·</span>
          {t("web.shell.impersonation.authorizedBy", {
            name: impersonatorName,
            strong: (chunks) => <span className="text-foreground/80 font-medium">{chunks}</span>,
          })}
        </span>

        <button
          type="button"
          onClick={() => void handleEnd()}
          disabled={isEnding}
          aria-label={t("web.shell.impersonation.endSessionLabel")}
          className="impersonation-end-btn group ml-auto inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md px-3 text-xs font-semibold whitespace-nowrap transition-all duration-150 outline-none disabled:pointer-events-none disabled:opacity-50"
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
