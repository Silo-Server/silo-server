export type RequestRouterConnectionDefaults = {
  baseURL: string;
  apiKey: string;
};

export function requestRouterConnectionDefaults(
  metadata: Record<string, unknown> | undefined,
): RequestRouterConnectionDefaults | null {
  const raw = metadata?.connection_defaults;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const defaults = raw as Record<string, unknown>;
  const baseURL = typeof defaults.base_url === "string" ? defaults.base_url.trim() : "";
  const apiKey = typeof defaults.api_key === "string" ? defaults.api_key.trim() : "";
  if (!baseURL || !apiKey) return null;
  return { baseURL, apiKey };
}
