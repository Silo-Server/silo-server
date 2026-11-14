import { Info } from "lucide-react";
import { Link } from "react-router";

import { useServerDownloadSwitches } from "@/hooks/admin/useServerDownloadSwitches";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

const DOWNLOADS_SETTINGS_PATH = "/admin/settings/downloads";

/**
 * Says, beside an access group's or an account's download permissions, that a
 * server-wide switch overrides them. The permissions stay editable: they take
 * effect once the switch is turned on. With downloads off the conversion
 * switch is moot, so only the downloads note shows.
 */
export function DownloadsServerNotice({ className }: { className?: string }) {
  const t = useT();
  const { downloadsOff, conversionOff } = useServerDownloadSwitches();
  if (!downloadsOff && !conversionOff) return null;
  return (
    <p className={cn("text-muted-foreground flex items-start gap-2 text-xs", className)}>
      <Info aria-hidden="true" className="text-info mt-px size-3.5 shrink-0" />
      <span>
        {t(
          downloadsOff
            ? "web.admin.common.downloadAccess.serverOff"
            : "web.admin.common.downloadAccess.conversionOff",
          {
            link: (chunks) => (
              <Link
                to={DOWNLOADS_SETTINGS_PATH}
                className="text-foreground font-medium underline underline-offset-2"
              >
                {chunks}
              </Link>
            ),
          },
        )}
      </span>
    </p>
  );
}
