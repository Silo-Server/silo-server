import type { RequestIntegration } from "@/api/types";

const SERVICE_MEDIA_TYPES: Record<string, string[]> = {
  radarr: ["movie"],
  sonarr: ["series"],
};

export function supportedMediaTypesForConfig(
  pluginConfig: Record<string, unknown>,
  source?: RequestIntegration | null,
): string[] {
  const serviceKind =
    typeof pluginConfig.service_kind === "string"
      ? pluginConfig.service_kind.trim().toLowerCase()
      : "";
  const derived = SERVICE_MEDIA_TYPES[serviceKind];
  if (derived) return [...derived];
  return [...(source?.supported_media_types ?? [])];
}
