import type { AutoscanSource } from "@/api/types";
import { pluginDisplayNameKey, type SourceLabelLookups } from "@/lib/autoscanLabels";

import { WEBHOOK_PROVIDER_KEY, isWebhookSource } from "./sourceForm";

/**
 * How a source is named and summarised in the Sources list and the edit dialog.
 * Pure, so the naming rules can be tested without rendering.
 */

export interface SourceDisplay {
  /** The operator's label, else what the source is. */
  title: string;
  /** What the source is and how it hears about changes; never repeats the title. */
  subtitle: string;
  /** Plugin display name, e.g. "Sonarr/Radarr Webhook". */
  typeName: string;
}

const PROVIDER_NAMES: Record<string, string> = { sonarr: "Sonarr", radarr: "Radarr" };

/** "Sonarr" / "Radarr" for a provider setting, else null ("auto" or unset). */
export function providerName(provider: string | undefined): string | null {
  return PROVIDER_NAMES[provider ?? ""] ?? null;
}

/** "Sonarr" / "Radarr" for a webhook source whose provider is set, else null. */
export function webhookProviderName(
  source: Pick<AutoscanSource, "delivery_mode" | "source_config">,
): string | null {
  if (!isWebhookSource(source)) return null;
  return providerName(source.source_config?.[WEBHOOK_PROVIDER_KEY]);
}

const BUILTIN_ARR_WEBHOOK_PLUGIN_ID = "silo.autoscan.arr-webhook";

/** An arr webhook with no provider chosen, which infers it from each payload. */
function detectsArrProvider(source: AutoscanSource): boolean {
  const configured = source.source_config?.[WEBHOOK_PROVIDER_KEY];
  return configured === "auto" || source.plugin_id === BUILTIN_ARR_WEBHOOK_PLUGIN_ID;
}

/** The plugin's display name, else its capability id. */
export function sourceTypeName(
  ref: { plugin_id: string; capability_id: string },
  pluginDisplayNames: Map<string, string>,
): string {
  return (
    pluginDisplayNames.get(pluginDisplayNameKey(ref.plugin_id, ref.capability_id))?.trim() ||
    ref.capability_id
  );
}

/**
 * What a source is called wherever it is named: the operator's label, else the
 * arr a webhook source is set to receive from, else the plugin's display name.
 * The Sources list, the edit dialog and Activity all name sources through this.
 */
export function sourceTitle(
  source: Pick<AutoscanSource, "label" | "delivery_mode" | "source_config">,
  typeName: string,
): string {
  return source.label?.trim() || webhookProviderName(source) || typeName;
}

/**
 * The name Activity shows for an event or scan that references a source. It is
 * the list's title; an unlabelled polling source also names its server, which
 * the list shows on the row's second line and Activity has no room for.
 * A deleted source falls back to its plugin's name; a reference with no plugin
 * resolves to "" so the caller can supply its own fallback.
 */
export function eventSourceName(
  ref: { source_id?: string | null; plugin_id?: string | null; capability_id?: string },
  lookups: SourceLabelLookups,
): string {
  if (!ref.plugin_id || !ref.capability_id) return "";
  const typeName = sourceTypeName(
    { plugin_id: ref.plugin_id, capability_id: ref.capability_id },
    lookups.displayNames,
  );
  const source = ref.source_id ? lookups.sourceByID.get(ref.source_id) : undefined;
  if (!source) return typeName;

  const title = sourceTitle(source, typeName);
  if (isWebhookSource(source) || title !== typeName || !source.connection_id) return title;
  const connection = lookups.connectionByID.get(source.connection_id)?.trim();
  return connection ? `${title} · via ${connection}` : title;
}

/**
 * How often a poll source is actually checked. The scheduler fires at the
 * global default and skips a source until its own interval has elapsed, so a
 * per-source interval only ever lengthens the gap: values below the default
 * have no effect.
 */
export function effectivePollIntervalSeconds(
  sourceSeconds: number | null,
  defaultSeconds: number | null,
): number | null {
  if (defaultSeconds == null) return sourceSeconds;
  if (sourceSeconds == null) return defaultSeconds;
  return Math.max(sourceSeconds, defaultSeconds);
}

/** "45 s", "10 min", "2 h", "1 h 30 min", "1 day". */
export function formatInterval(seconds: number): string {
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const days = Math.floor(minutes / 1440);
  if (days >= 1 && minutes % 1440 === 0) return days === 1 ? "1 day" : `${days} days`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? `${hours} h` : `${hours} h ${rest} min`;
}

export function describeSource({
  source,
  pluginDisplayNames,
  connectionNames,
  defaultPollSeconds,
}: {
  source: AutoscanSource;
  pluginDisplayNames: Map<string, string>;
  connectionNames: Map<string, string>;
  defaultPollSeconds: number | null;
}): SourceDisplay {
  const typeName = sourceTypeName(source, pluginDisplayNames);
  const label = source.label?.trim() ?? "";
  const provider = webhookProviderName(source);
  const title = sourceTitle(source, typeName);

  const parts: string[] = [];
  if (isWebhookSource(source)) {
    parts.push(title === typeName ? "Webhook" : typeName);
    if (label && provider) parts.push(provider);
    else if (!provider && detectsArrProvider(source)) parts.push("auto-detects Sonarr or Radarr");
  } else {
    if (title !== typeName) parts.push(typeName);
    const connection = source.connection_id ? connectionNames.get(source.connection_id) : undefined;
    if (connection) parts.push(`via ${connection}`);
    const seconds = effectivePollIntervalSeconds(source.poll_interval_seconds, defaultPollSeconds);
    parts.push(
      seconds == null ? "polls on the default schedule" : `polls every ${formatInterval(seconds)}`,
    );
  }

  const subtitle = parts.join(" · ");
  return { title, typeName, subtitle: subtitle.charAt(0).toUpperCase() + subtitle.slice(1) };
}

export type SourceHealth =
  | { kind: "off" }
  | { kind: "error"; headline: string; message: string; at: string | null }
  | { kind: "ok"; headline: string; at: string }
  | { kind: "idle"; headline: string }
  | { kind: "missing-endpoint"; headline: string };

/**
 * The status column. Poll sources report last_run_at/last_error; webhook
 * sources report their endpoint's delivery bookkeeping instead (deliveries do
 * not stamp last_run_at).
 */
export function sourceHealth(source: AutoscanSource): SourceHealth {
  if (!source.enabled) return { kind: "off" };

  if (isWebhookSource(source)) {
    const receivedMs = source.webhook_last_received_at
      ? new Date(source.webhook_last_received_at).getTime()
      : 0;
    const errorMs = source.webhook_last_error_at
      ? new Date(source.webhook_last_error_at).getTime()
      : 0;
    if (errorMs > 0 && errorMs >= receivedMs) {
      return {
        kind: "error",
        headline: "Delivery failed",
        message: source.webhook_last_error_message || source.last_error || "Delivery failed",
        at: source.webhook_last_error_at ?? null,
      };
    }
    if (receivedMs > 0) {
      return { kind: "ok", headline: "Last delivery", at: source.webhook_last_received_at! };
    }
    if (!source.webhook_configured) {
      return { kind: "missing-endpoint", headline: "No webhook URL yet" };
    }
    return { kind: "idle", headline: "No deliveries yet" };
  }

  if (source.last_error) {
    return {
      kind: "error",
      headline: "Last poll failed",
      message: source.last_error,
      at: source.last_run_at,
    };
  }
  if (source.last_run_at) return { kind: "ok", headline: "Last checked", at: source.last_run_at };
  return { kind: "idle", headline: "Not run yet" };
}
