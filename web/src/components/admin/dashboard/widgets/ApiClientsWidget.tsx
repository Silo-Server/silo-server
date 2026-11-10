import { useRef, useState } from "react";
import { Link } from "react-router";
import { ChevronRight } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  type AdminApiClient,
  MAX_ENDPOINT_HOURS,
  useAdminApiClientEndpoints,
  useAdminApiClients,
  useRevokeApiClientSession,
} from "@/hooks/queries/admin/apiClients";
import { cn } from "@/lib/utils";
import { userDetailTabSearch } from "@/pages/admin-users/detail/userDetailTabs";
import { BarListSkeleton } from "../charts";
import { chartSeriesColor } from "../charts/chartMath";
import { SectionError } from "../feedback";
import { rangeHours, rangePhrase, rangeTitle } from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetRangePicker } from "../WidgetRangePicker";
import { formatLocale } from "@/lib/appLocale";
import { useT, type Translate } from "@/i18n";

const ROW_LIMIT = 8;
const SEPARATOR = " · ";

function clientLabel(t: Translate, client: AdminApiClient): string {
  return (
    client.device_name ||
    client.user_agent ||
    t("web.admin.dashboard.widgets.apiClients.unknownClient")
  );
}

function sharePercent(requests: number, total: number): string {
  if (total <= 0) return "";
  const share = (requests / total) * 100;
  return share >= 10 ? `${Math.round(share)}%` : `${share.toFixed(1)}%`;
}

/**
 * The sign-in sessions that asked the most of the API over the chosen window.
 *
 * Leaves out what the session quota leaves out: playback, downloads, admins
 * and impersonation, so a household watching a film does not outrank a script
 * polling the catalog. A row opens to the routes the session called and a way to sign
 * just that session out; the account's other devices stay signed in.
 */
export function ApiClientsWidget() {
  const t = useT();
  const { range } = useWidgetRange();
  const hours = rangeHours(range);
  const query = useAdminApiClients(hours);
  const [openSession, setOpenSession] = useState<string | null>(null);
  const data = query.data;
  const clients = (data?.clients ?? []).slice(0, ROW_LIMIT);
  const max = clients.reduce((peak, client) => Math.max(peak, client.requests), 0);
  const color = chartSeriesColor(0);

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between gap-2 space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">
          {rangeTitle(t("web.admin.dashboard.widgets.apiClients.title"), range)}
        </CardTitle>
        <div className="flex min-w-0 items-center gap-2">
          {data && data.total_requests > 0 ? (
            <span className="text-muted-foreground hidden text-[11px] whitespace-nowrap tabular-nums sm:inline">
              {t("web.admin.dashboard.widgets.apiClients.totalRequests", {
                count: data.total_requests,
                count_text: data.total_requests.toLocaleString(formatLocale()),
              })}
            </span>
          ) : null}
          <WidgetRangePicker />
        </div>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {query.isLoading ? (
          <BarListSkeleton />
        ) : query.error ? (
          <SectionError message={t("web.admin.dashboard.widgets.apiClients.loadFailed")} />
        ) : clients.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            {t("web.admin.dashboard.widgets.apiClients.empty", { range: rangePhrase(range) })}
          </div>
        ) : (
          <ol className="space-y-1">
            {clients.map((client, index) => {
              const open = openSession === client.session_id;
              const ratio = max > 0 ? client.requests / max : 0;
              return (
                <li key={client.session_id}>
                  <button
                    type="button"
                    aria-expanded={open}
                    onClick={() => setOpenSession(open ? null : client.session_id)}
                    className="hover:bg-muted/50 relative flex w-full items-center gap-2 overflow-hidden rounded-sm px-2 py-1.5 text-left text-sm transition-colors"
                  >
                    <span
                      aria-hidden="true"
                      className="absolute inset-y-0 left-0 rounded-r-[4px]"
                      style={{
                        width: `${ratio * 100}%`,
                        minWidth: client.requests > 0 ? 6 : 0,
                        backgroundColor: color,
                        opacity: 0.18,
                      }}
                    />
                    <span className="text-muted-foreground relative w-4 shrink-0 text-[11px] tabular-nums">
                      {index + 1}
                    </span>
                    <ChevronRight
                      aria-hidden="true"
                      className={cn(
                        "text-muted-foreground relative h-3 w-3 shrink-0 transition-transform",
                        open && "rotate-90",
                      )}
                    />
                    <span
                      className="relative min-w-0 flex-1 truncate"
                      title={clientLabel(t, client)}
                    >
                      {clientLabel(t, client)}
                      <span className="text-muted-foreground">
                        {SEPARATOR}
                        {client.username ||
                          t("web.admin.dashboard.widgets.apiClients.userFallback", {
                            id: String(client.user_id),
                          })}
                      </span>
                    </span>
                    {client.refused > 0 ? (
                      <span className="text-destructive relative shrink-0 text-[11px] tabular-nums">
                        {t("web.admin.dashboard.widgets.apiClients.refused", {
                          count: client.refused,
                          count_text: client.refused.toLocaleString(formatLocale()),
                        })}
                      </span>
                    ) : null}
                    {!client.session_active ? (
                      <span className="text-muted-foreground relative hidden shrink-0 text-[11px] sm:inline">
                        {t("web.admin.dashboard.widgets.apiClients.signedOut")}
                      </span>
                    ) : null}
                    <span className="relative shrink-0 text-[13px] font-semibold tabular-nums">
                      {client.requests.toLocaleString(formatLocale())}
                    </span>
                    <span className="text-muted-foreground relative w-10 shrink-0 text-right text-[11px] tabular-nums">
                      {sharePercent(client.requests, data?.total_requests ?? 0)}
                    </span>
                  </button>
                  {open ? <ApiClientDetail client={client} hours={hours} /> : null}
                </li>
              );
            })}
          </ol>
        )}
      </CardContent>
    </Card>
  );
}

function ApiClientDetail({ client, hours }: { client: AdminApiClient; hours: number }) {
  const t = useT();
  const endpoints = useAdminApiClientEndpoints(client.session_id, hours);
  const revoke = useRevokeApiClientSession();
  const [confirming, setConfirming] = useState(false);
  // The dialog's action button closes it on click; while the sign-out is in
  // flight the close is ignored, so a failure stays on screen with its error.
  const signingOut = useRef(false);

  return (
    <div className="border-border/60 text-muted-foreground mx-2 mt-1 mb-2 space-y-2 border-l pl-3 text-[12px]">
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5">
        {client.user_agent ? (
          <>
            <dt>{t("web.admin.dashboard.widgets.apiClients.userAgent")}</dt>
            <dd className="text-foreground truncate" title={client.user_agent}>
              {client.user_agent}
            </dd>
          </>
        ) : null}
        <dt>{t("web.admin.dashboard.widgets.apiClients.busiestHour")}</dt>
        <dd className="text-foreground tabular-nums">
          {t("web.admin.dashboard.widgets.apiClients.peakHourRequests", {
            count: client.peak_hour_requests,
            count_text: client.peak_hour_requests.toLocaleString(formatLocale()),
          })}
        </dd>
      </dl>
      <div>
        {t("web.admin.dashboard.widgets.apiClients.routesHeading", {
          hours: Math.min(hours, MAX_ENDPOINT_HOURS),
        })}
      </div>
      {endpoints.isLoading ? (
        <div>{t("web.admin.dashboard.widgets.apiClients.loadingRoutes")}</div>
      ) : endpoints.error ? (
        <div>{t("web.admin.dashboard.widgets.apiClients.routesFailed")}</div>
      ) : endpoints.data?.endpoints.length === 0 ? (
        <div>{t("web.admin.dashboard.widgets.apiClients.noRoutes")}</div>
      ) : (
        <ol className="space-y-0.5">
          {(endpoints.data?.endpoints ?? []).map((endpoint) => (
            <li key={`${endpoint.method} ${endpoint.route}`} className="flex gap-2">
              <span className="w-12 shrink-0 font-mono text-[11px]">{endpoint.method}</span>
              <span
                className="text-foreground min-w-0 flex-1 truncate font-mono text-[11px]"
                title={endpoint.route}
              >
                {endpoint.route}
              </span>
              <span className="shrink-0 tabular-nums">
                {endpoint.requests.toLocaleString(formatLocale())}
              </span>
            </li>
          ))}
        </ol>
      )}
      <div className="flex flex-wrap items-center gap-2 pt-1">
        {client.session_active ? (
          <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>
            {t("web.admin.dashboard.widgets.apiClients.signOut")}
          </Button>
        ) : (
          <span>{t("web.admin.dashboard.widgets.apiClients.sessionSignedOut")}</span>
        )}
        <Link
          to={`/admin/users/${client.user_id}${userDetailTabSearch("activity")}`}
          className="hover:text-primary transition-colors"
        >
          {t("web.admin.dashboard.widgets.apiClients.openAccount")}
        </Link>
      </div>
      <ConfirmDialog
        open={confirming}
        onOpenChange={(next) => {
          if (!next && signingOut.current) return;
          setConfirming(next);
          if (!next) revoke.reset();
        }}
        title={t("web.admin.dashboard.widgets.apiClients.confirmTitle", {
          client: clientLabel(t, client),
        })}
        description={t("web.admin.dashboard.widgets.apiClients.confirmDescription", {
          user:
            client.username ||
            t("web.admin.dashboard.widgets.apiClients.confirmUserFallback", {
              id: String(client.user_id),
            }),
        })}
        confirmLabel={t("web.admin.dashboard.widgets.apiClients.confirmLabel")}
        variant="destructive"
        isPending={revoke.isPending}
        error={revoke.error ? t("web.admin.dashboard.widgets.apiClients.revokeFailed") : null}
        onConfirm={() => {
          if (signingOut.current) return;
          signingOut.current = true;
          revoke.mutate(
            { userId: client.user_id, sessionId: client.session_id },
            {
              onSuccess: () => setConfirming(false),
              onSettled: () => {
                signingOut.current = false;
              },
            },
          );
        }}
      />
    </div>
  );
}
