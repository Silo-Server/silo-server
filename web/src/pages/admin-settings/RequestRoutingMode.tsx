import { useId } from "react";

import type { RequestIntegration } from "@/api/types";
import type {
  RequestRoute,
  RequestRouteMediaType,
  RequestRouting,
  RequestRoutingMode,
} from "@/api/v2/adminRequests";
import { useT, type MessageKey } from "@/i18n";
import { ChoiceCard } from "@/pages/admin/autoscan/ChoiceCard";

import { serverKind } from "./requestServerModel";

const MEDIA_TYPES: readonly { value: RequestRouteMediaType; label: MessageKey; kind: string }[] = [
  { value: "movie", label: "web.admin.settings.requests.routingMode.movies", kind: "Radarr" },
  { value: "series", label: "web.admin.settings.requests.routingMode.series", kind: "Sonarr" },
];

/** Standard or Advanced. Standard is offered only while the servers allow it. */
export function RoutingModeChoice({
  routing,
  pending,
  onChange,
}: {
  routing: RequestRouting;
  pending: boolean;
  onChange: (mode: RequestRoutingMode) => void;
}) {
  const t = useT();
  const blocked = Boolean(routing.standard_unavailable_reason);
  const reasonId = useId();
  const showReason = blocked && routing.mode === "advanced";
  return (
    <div className="space-y-2">
      <div
        role="group"
        aria-label={t("web.admin.settings.requests.routingMode.label")}
        className="grid gap-2 sm:grid-cols-2"
      >
        <ChoiceCard
          title={t("web.admin.settings.requests.routingMode.standard")}
          description={t("web.admin.settings.requests.routingMode.standardDescription")}
          selected={routing.mode === "standard"}
          disabled={pending || (blocked && routing.mode !== "standard")}
          describedBy={showReason ? reasonId : undefined}
          onSelect={() => routing.mode !== "standard" && onChange("standard")}
        />
        <ChoiceCard
          title={t("web.admin.settings.requests.routingMode.advanced")}
          description={t("web.admin.settings.requests.routingMode.advancedDescription")}
          selected={routing.mode === "advanced"}
          disabled={pending}
          onSelect={() => routing.mode !== "advanced" && onChange("advanced")}
        />
      </div>
      {showReason ? (
        <p id={reasonId} className="text-muted-foreground text-xs">
          {t("web.admin.settings.requests.routingMode.standardUnavailable", {
            reason: routing.standard_unavailable_reason ?? "",
          })}
        </p>
      ) : null}
    </div>
  );
}

/**
 * What Standard does, one line per media type: its server, and where its 4K
 * copies go.
 */
export function StandardRoutingSummary({
  routing,
  servers,
  routes,
}: {
  routing: RequestRouting;
  servers: readonly RequestIntegration[];
  routes: readonly RequestRoute[];
}) {
  const t = useT();
  const name = (id: string | undefined) => servers.find((server) => server.id === id)?.name;
  const pausedRules = routes.filter((route) => !route.is_fallback && route.enabled).length;
  const isArr = (id: string | undefined) => {
    const server = servers.find((candidate) => candidate.id === id);
    return server !== undefined && serverKind(server) !== "";
  };
  // The 4K hint matters only while a Radarr or Sonarr media type has no 4K
  // server; another plugin (Seerr) handles 4K itself.
  const missing4K = routing.standard.some(
    (d) => isArr(d.hd_integration_id) && !d.uhd_integration_id,
  );
  if (routing.standard_unavailable_reason) {
    // Saved as Standard while a server change was being made: the rules decide
    // until an admin picks.
    return (
      <p className="text-muted-foreground pt-3 text-sm">
        {t("web.admin.settings.requests.routingMode.standardBlocked", {
          reason: routing.standard_unavailable_reason,
        })}
      </p>
    );
  }
  return (
    <div className="space-y-3 pt-3">
      <ul
        aria-label={t("web.admin.settings.requests.routingMode.summaryLabel")}
        className="space-y-2"
      >
        {MEDIA_TYPES.map((type) => {
          const destination = routing.standard.find((d) => d.media_type === type.value);
          const hd = name(destination?.hd_integration_id);
          const uhd = name(destination?.uhd_integration_id);
          const problem = t(
            uhd
              ? "web.admin.settings.requests.routingMode.onlyUhdServer"
              : "web.admin.settings.requests.routingMode.noServer",
            { kind: type.kind },
          );
          return (
            <li key={type.value} className="border-border/70 rounded-lg border px-3 py-2 text-sm">
              <span className="font-medium">{t(type.label)}</span>
              {hd ? (
                <>
                  {" → "}
                  {hd}
                </>
              ) : (
                <span className="text-muted-foreground"> {problem}</span>
              )}
              {hd || uhd ? (
                <span className="text-muted-foreground block text-xs">
                  {uhd
                    ? t("web.admin.settings.requests.routingMode.uhdServer", { server: uhd })
                    : t("web.admin.settings.requests.routingMode.noUhd")}
                </span>
              ) : null}
            </li>
          );
        })}
      </ul>
      {routing.standard.some((d) => d.media_type === "series" && isArr(d.hd_integration_id)) ? (
        <p className="text-muted-foreground text-xs">
          {t("web.admin.settings.requests.routingMode.animeNote")}
        </p>
      ) : null}
      {missing4K ? (
        <p className="text-muted-foreground text-xs">
          {t("web.admin.settings.requests.routingMode.uhdNote")}
        </p>
      ) : null}
      {pausedRules > 0 ? (
        <p className="text-muted-foreground text-xs">
          {t("web.admin.settings.requests.routingMode.pausedRules", {
            count: pausedRules,
            count_text: String(pausedRules),
          })}
        </p>
      ) : null}
    </div>
  );
}
