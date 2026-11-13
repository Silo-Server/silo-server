import { useMemo, useState } from "react";

import type { DiscoverBrandCard, RequestIntegration } from "@/api/types";
import type { RequestRoute, RequestRouteMediaType } from "@/api/v2/adminRequests";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useDiscoverNetworks, useDiscoverStudios } from "@/hooks/queries/useRequests";
import { useT, type MessageKey } from "@/i18n";
import { useRequestRouting, useUpdateRequestRouting } from "@/hooks/queries/admin/requests";

import { FieldGroup } from "./FieldGroup";
import { RequestRoutePreview } from "./RequestRoutePreview";
import type { RoutingScope } from "./RequestRuleEditor";
import { RequestRoutingList } from "./RequestRoutingList";
import { RoutingModeChoice, StandardRoutingSummary } from "./RequestRoutingMode";
import { adminLanguage } from "./requestRoutingPresets";
import { serverServesMediaType, type RequestRouterInstallation } from "./requestServerModel";

/** TMDB ID → name. Networks and studios get one map each: their IDs overlap. */
function brandMap(brands: readonly DiscoverBrandCard[] | undefined): Map<number, string> {
  return new Map(
    (brands ?? [])
      .filter((brand) => brand.tmdb_id)
      .map((brand) => [brand.tmdb_id!, brand.display_name]),
  );
}

const MEDIA_TYPES: readonly { value: RequestRouteMediaType; label: MessageKey }[] = [
  { value: "movie", label: "web.admin.settings.requests.routing.movies" },
  { value: "series", label: "web.admin.settings.requests.routing.series" },
];

/**
 * "Where requests go": Standard, which sends each request to the server for
 * its type, or Advanced: for movies and for series, the rules Silo checks from
 * the top, Everything else for what no rule takes, and a way to try a title.
 * Every change saves right away.
 */
export function RequestRoutingGroup({
  routes,
  routesLoading,
  routesFetching,
  routesError,
  serversLoading,
  allServers,
  installations,
  requestsEnabled,
  forceDual,
}: {
  routes: RequestRoute[];
  routesLoading: boolean;
  /** The list is being read again; its order and validators may be stale. */
  routesFetching: boolean;
  routesError: boolean;
  /** Until the servers load, no media type can say whether it has one. */
  serversLoading: boolean;
  allServers: RequestIntegration[];
  installations: RequestRouterInstallation[];
  /** The saved request switch; undefined until the settings have loaded. */
  requestsEnabled: boolean | undefined;
  /** "Also request a 4K version of every title", as saved. */
  forceDual: boolean;
}) {
  const t = useT();
  const [tab, setTab] = useState<RequestRouteMediaType>("movie");
  const routing = useRequestRouting();
  const switchRouting = useUpdateRequestRouting();
  const users = useAdminUsers();
  // The curated networks and studios come from the requesters' discover API,
  // which answers only while requests are on (and refuses with a 409 when
  // they are off, which asking again does not change).
  const brandsReadable = requestsEnabled === true;
  const networks = useDiscoverNetworks({ enabled: brandsReadable, retry: false });
  const studios = useDiscoverStudios({ enabled: brandsReadable, retry: false });
  const language = adminLanguage(typeof navigator === "undefined" ? undefined : navigator.language);

  const names = useMemo(
    () => ({
      users: new Map((users.data ?? []).map((user) => [user.id, user.username])),
      networks: brandMap(networks.data),
      studios: brandMap(studios.data),
    }),
    [users.data, networks.data, studios.data],
  );

  function scopeFor(mediaType: RequestRouteMediaType): RoutingScope {
    const ofType = routes.filter((route) => route.media_type === mediaType);
    const brands = mediaType === "series" ? networks : studios;
    return {
      mediaType,
      rules: ofType.filter((route) => !route.is_fallback).sort((a, b) => a.position - b.position),
      fallback: ofType.find((route) => route.is_fallback),
      servers: allServers.filter((server) => serverServesMediaType(server, mediaType)),
      allServers,
      installations,
      lookups: {
        users: users.data ?? [],
        brands: brands.data ?? [],
        brandsHint:
          requestsEnabled === false
            ? t("web.admin.settings.requests.routing.brandsNeedRequests")
            : brands.isError
              ? t("web.admin.settings.requests.routing.brandsFailed", { media_type: mediaType })
              : undefined,
      },
      names,
      forceDual,
      language,
    };
  }

  const advanced = routing.data?.mode === "advanced";
  return (
    <FieldGroup
      label={t("web.admin.settings.requests.routing.label")}
      description={
        advanced
          ? t("web.admin.settings.requests.routing.advancedDescription")
          : t("web.admin.settings.requests.routing.standardDescription")
      }
    >
      {routesLoading || serversLoading || routing.isPending ? (
        <div className="space-y-2 py-3.5">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : routesError || routing.isError ? (
        <p className="text-destructive py-3.5 text-sm">
          {t("web.admin.settings.requests.routing.loadFailed")}
        </p>
      ) : (
        <div className="space-y-3 pt-3">
          <RoutingModeChoice
            routing={routing.data}
            pending={switchRouting.isPending}
            onChange={(mode) => switchRouting.mutate({ mode, current: routing.data })}
          />
          {advanced ? (
            <Tabs value={tab} onValueChange={(value) => setTab(value as RequestRouteMediaType)}>
              <TabsList aria-label={t("web.admin.settings.requests.routing.mediaType")}>
                {MEDIA_TYPES.map((type) => (
                  <TabsTrigger key={type.value} value={type.value} className="px-4">
                    {t(type.label)}
                  </TabsTrigger>
                ))}
              </TabsList>
              {MEDIA_TYPES.map((type) => {
                const scope = scopeFor(type.value);
                return (
                  <TabsContent key={type.value} value={type.value}>
                    <RequestRoutingList scope={scope} routesFetching={routesFetching} />
                    <RequestRoutePreview scope={scope} />
                  </TabsContent>
                );
              })}
            </Tabs>
          ) : (
            <StandardRoutingSummary routing={routing.data} servers={allServers} routes={routes} />
          )}
        </div>
      )}
    </FieldGroup>
  );
}
