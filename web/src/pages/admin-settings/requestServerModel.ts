import type {
  PluginAdminForm,
  PluginCapability,
  PluginInstallation,
  RequestIntegration,
} from "@/api/types";
import type { RequestRoute, RequestRouteMediaType, RequestRouting } from "@/api/v2/adminRequests";
import type { Tier } from "./requestRoutingModel";

/** Every request server is fulfilled by a plugin exposing this capability. */
export const REQUEST_ROUTER_CAPABILITY = "request_router.v1";

/**
 * Plugin config keys that request routing sets itself (the server's
 * `routingOwnedConfigKeys`). The Sonarr/Radarr plugin form still declares
 * them, but once a media type has routes they decide nothing, so the server
 * editor hides them and a route cannot override them.
 */
export const ROUTING_OWNED_CONFIG_KEYS: readonly string[] = [
  "is_default",
  "is_default_4k",
  "is_4k",
  "anime_enabled",
  "anime_root_folder",
  "anime_quality_profile_id",
  "anime_tags",
];

/** The config key naming which *arr a Sonarr/Radarr server is. */
export const SERVICE_KIND_KEY = "service_kind";

export interface RequestRouterInstallation {
  installationID: number;
  pluginID: string;
  capability: PluginCapability;
}

/**
 * One entry per request_router.v1 capability across the installed plugins, so
 * one installation exposing two capabilities offers two server types.
 */
export function requestRouterInstallations(
  installations: PluginInstallation[],
): RequestRouterInstallation[] {
  const out: RequestRouterInstallation[] = [];
  for (const installation of installations) {
    for (const capability of installation.capabilities ?? []) {
      if (
        capability.type === REQUEST_ROUTER_CAPABILITY ||
        capability.id === REQUEST_ROUTER_CAPABILITY
      ) {
        out.push({ installationID: installation.id, pluginID: installation.plugin_id, capability });
      }
    }
  }
  return out;
}

export function installationOptionLabel(entry: RequestRouterInstallation): string {
  return entry.capability.display_name || entry.pluginID;
}

/**
 * The select value for a server type: installation and capability together,
 * since the installation alone collides when it exposes two capabilities.
 */
export function installationOptionValue(entry: RequestRouterInstallation): string {
  return `${entry.installationID}:${entry.capability.id}`;
}

/**
 * The installed capability a server is bound to. Matches the capability too
 * when one is recorded, and otherwise adopts the installation's capability.
 */
export function serverInstallation(
  installations: RequestRouterInstallation[],
  installationId: number | undefined,
  capabilityId: string | undefined,
): RequestRouterInstallation | undefined {
  if (!installationId) return undefined;
  return (
    installations.find(
      (entry) => entry.installationID === installationId && entry.capability.id === capabilityId,
    ) ?? installations.find((entry) => entry.installationID === installationId)
  );
}

export function serverConfigSchema(entry: RequestRouterInstallation | undefined): {
  descriptor?: PluginAdminForm;
  jsonSchema?: string;
} {
  const schema = entry?.capability.config_schema?.[0];
  return { descriptor: schema?.admin_form, jsonSchema: schema?.json_schema };
}

/** `radarr`, `sonarr`, or "" for a server whose plugin does not say. */
export function serverKind(server: Pick<RequestIntegration, "plugin_config">): string {
  const kind = server.plugin_config?.[SERVICE_KIND_KEY];
  return typeof kind === "string" ? kind.trim().toLowerCase() : "";
}

const KIND_LABELS: Record<string, string> = { radarr: "Radarr", sonarr: "Sonarr" };

/** "Radarr" or "Sonarr" for those service kinds, otherwise "". */
export function serviceKindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? "";
}
const KIND_MEDIA_TYPE: Record<string, RequestRouteMediaType> = {
  radarr: "movie",
  sonarr: "series",
};

/** "Radarr", "Sonarr", or the plugin's own name for other request servers. */
export function serverTypeLabel(
  server: RequestIntegration,
  installations: RequestRouterInstallation[],
): string {
  const kind = serverKind(server);
  if (KIND_LABELS[kind]) return KIND_LABELS[kind];
  const entry = serverInstallation(installations, server.installation_id, server.capability_id);
  return entry ? installationOptionLabel(entry) : "Request server";
}

/**
 * Whether routing may send a media type to this server: Radarr takes movies
 * and Sonarr series, as the server enforces; a server of another plugin says
 * what it takes through its supported media types.
 */
export function serverServesMediaType(
  server: RequestIntegration,
  mediaType: RequestRouteMediaType,
): boolean {
  const kind = serverKind(server);
  if (KIND_MEDIA_TYPE[kind]) return KIND_MEDIA_TYPE[kind] === mediaType;
  const types = server.supported_media_types ?? [];
  return types.length === 0 || types.includes(mediaType);
}

/**
 * Whether the server is marked 4K ("4K server" on, or the plugin's older 4K
 * default switch). Routing sends 4K versions only to these, and HD versions
 * only to the others.
 */
export function serverIs4K(server: Pick<RequestIntegration, "plugin_config">): boolean {
  return ["is_4k", "is_default_4k"].some((key) => {
    const flag = server.plugin_config?.[key];
    return flag === true || flag === "true";
  });
}

/**
 * Whether routing may send the HD or 4K version to the server, as the server
 * enforces: a Radarr or Sonarr takes 4K versions only when marked 4K and HD
 * versions only when not. A server of another plugin (Seerr) takes either.
 */
export function serverFitsTier(
  server: Pick<RequestIntegration, "plugin_config">,
  tier: Tier,
): boolean {
  return serverKind(server) === "" || serverIs4K(server) === (tier === "uhd");
}

/**
 * Whether the server could take a request right now: switched on, bound to a
 * plugin, and holding an API key. Anything less reads "Needs setup".
 */
export function serverReady(server: RequestIntegration): boolean {
  return Boolean(server.enabled && server.installation_id && server.has_api_key);
}

export function mediaTypePlural(mediaType: RequestRouteMediaType): string {
  return mediaType === "series" ? "series" : "movies";
}

function routeUsage(route: RequestRoute, serverId: string): string | null {
  const hd = route.hd.integration_id === serverId;
  const uhd = route.uhd.integration_id === serverId;
  if (!hd && !uhd) return null;
  const name = route.is_fallback ? "Everything else" : route.name;
  return `${hd ? name : `${name} 4K`} (${mediaTypePlural(route.media_type)})`;
}

/**
 * Where a server is used, for its tile: "Everything else (movies) · Anime
 * (series)", with "4K" when only a route's 4K copies go there. Empty when no
 * route points at it.
 */
export function serverRouteUsage(serverId: string, routes: RequestRoute[]): string {
  return routes
    .map((route) => routeUsage(route, serverId))
    .filter(Boolean)
    .join(" · ");
}

/**
 * Where Standard routing uses a server, for its tile: "Movies", "4K series".
 * Empty when Standard does not send there.
 */
export function standardServerUsage(serverId: string, routing: RequestRouting): string {
  return routing.standard
    .flatMap((destination) => {
      const plural = mediaTypePlural(destination.media_type);
      const label = plural.charAt(0).toUpperCase() + plural.slice(1);
      return [
        destination.hd_integration_id === serverId ? label : null,
        destination.uhd_integration_id === serverId ? `4K ${plural}` : null,
      ];
    })
    .filter(Boolean)
    .join(" · ");
}

const MEDIA_TYPE_KIND: Record<RequestRouteMediaType, string> = {
  movie: "radarr",
  series: "sonarr",
};

/**
 * The routes that stop a server from being deleted, as the server decides:
 * every route sending to it, except an Everything else that only this server
 * serves when it is the last server of its kind and its media type has no
 * rules. The server removes that one with it. Under Standard, Everything else
 * is hidden and never keeps a server: the server clears it instead.
 */
export function serverDeleteBlockers(
  serverId: string,
  routes: RequestRoute[],
  servers: readonly RequestIntegration[],
  standard: boolean,
): string {
  return routes
    .filter((route) => {
      if (route.hd.integration_id !== serverId && route.uhd.integration_id !== serverId) {
        return false;
      }
      if (!route.is_fallback) return true;
      if (standard) return false;
      const onlyThis = [route.hd.integration_id, route.uhd.integration_id].every(
        (id) => !id || id === serverId,
      );
      const noRules = !routes.some((r) => r.media_type === route.media_type && !r.is_fallback);
      const lastOfKind = !servers.some(
        (server) =>
          server.id !== serverId && serverKind(server) === MEDIA_TYPE_KIND[route.media_type],
      );
      return !(onlyThis && noRules && lastOfKind);
    })
    .map((route) => routeUsage(route, serverId))
    .join(" · ");
}
