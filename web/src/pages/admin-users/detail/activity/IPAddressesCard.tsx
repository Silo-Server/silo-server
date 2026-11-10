import { useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useUserIPs } from "@/hooks/queries/admin/ips";
import { useT } from "@/i18n";

import { formatLastSeen, formatShortDate } from "../format";
import { DetailCard } from "../ui";
import { formatLocale } from "@/lib/appLocale";

const WINDOW_DAYS = 30;
const NEW_ADDRESS_DAYS = 7;
const COLLAPSED_ROWS = 4;
const DAY = 86_400_000;

/**
 * A remote address first seen in the last week is worth a look, unless it
 * was already there when the 30-day window opened (the log can't tell when
 * it first appeared then).
 */
function isNewRemoteAddress(
  entry: { first_seen: string; location?: string },
  now = Date.now(),
): boolean {
  if (entry.location !== "remote") return false;
  const first = Date.parse(entry.first_seen);
  if (!Number.isFinite(first)) return false;
  const windowStart = now - WINDOW_DAYS * DAY;
  return first >= now - NEW_ADDRESS_DAYS * DAY && first >= windowStart + DAY;
}

export function IPAddressesCard({ userId }: { userId: number }) {
  const t = useT();
  const history = useUserIPs(userId, WINDOW_DAYS);
  const [expanded, setExpanded] = useState(false);
  const ips = history.data ?? [];
  const shown = expanded ? ips : ips.slice(0, COLLAPSED_ROWS);
  const canExpand = !expanded && (ips.length > COLLAPSED_ROWS || history.hasNextPage);

  return (
    <DetailCard
      title={t("web.admin.users.detail.activity.ips.title")}
      description={t("web.admin.users.detail.activity.ips.description", { days: WINDOW_DAYS })}
      actions={
        canExpand ? (
          <Button variant="ghost" size="sm" onClick={() => setExpanded(true)}>
            {history.hasNextPage
              ? t("web.admin.users.detail.activity.ips.showAll")
              : t("web.admin.users.detail.activity.showAllCount", { count: String(ips.length) })}
          </Button>
        ) : undefined
      }
      footer={
        expanded && history.hasNextPage ? (
          <Button
            variant="outline"
            size="sm"
            className="ml-auto"
            disabled={history.isFetchingNextPage}
            onClick={() => void history.fetchNextPage()}
          >
            {t("web.admin.users.detail.common.loadMore")}
          </Button>
        ) : undefined
      }
    >
      {history.isError && (
        <div role="alert" className="flex items-center gap-3 px-5 py-4 text-sm">
          <span className="text-destructive">
            {t("web.admin.users.detail.activity.ips.failed")}
          </span>
          <Button variant="outline" size="sm" onClick={() => void history.restart()}>
            {t("web.admin.users.detail.activity.ips.reload")}
          </Button>
        </div>
      )}
      {history.isLoading ? (
        <p className="text-muted-foreground px-5 py-6 text-center text-sm">
          {t("web.admin.users.detail.activity.ips.loading")}
        </p>
      ) : ips.length === 0 && !history.isError ? (
        <p className="text-muted-foreground px-5 py-6 text-center text-sm">
          {t("web.admin.users.detail.activity.ips.empty")}
        </p>
      ) : (
        <ul>
          {shown.map((entry) => (
            <li
              key={entry.client_ip}
              className="border-border/50 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 border-t px-4 py-2.5 first:border-t-0 sm:grid-cols-[minmax(0,1fr)_auto_auto] sm:px-5"
            >
              <span className="truncate font-mono text-[13px]">{entry.client_ip}</span>
              <span className="text-xs">
                {isNewRemoteAddress(entry) ? (
                  <Badge
                    variant="outline"
                    className="rounded-full border-amber-500/40 bg-amber-500/10 text-amber-200"
                  >
                    {t("web.admin.users.detail.activity.ips.firstSeen", {
                      date: formatShortDate(entry.first_seen),
                    })}
                  </Badge>
                ) : entry.location === "local" ? (
                  <span className="text-muted-foreground">
                    {t("web.admin.users.detail.activity.ips.local")}
                  </span>
                ) : entry.location === "remote" ? (
                  <span className="text-muted-foreground">
                    {t("web.admin.users.detail.activity.ips.remote")}
                  </span>
                ) : null}
              </span>
              <span className="text-muted-foreground col-span-2 text-xs tabular-nums sm:col-span-1 sm:text-right">
                {t("web.admin.users.detail.activity.ips.requests", {
                  count: entry.request_count,
                  count_text: entry.request_count.toLocaleString(formatLocale()),
                  last_seen: formatLastSeen(entry.last_seen),
                })}
              </span>
            </li>
          ))}
        </ul>
      )}
    </DetailCard>
  );
}
