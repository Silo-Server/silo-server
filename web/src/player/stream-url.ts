import type { PlayMethod } from "./types";

export function buildPlayerStreamUrl(
  apiBaseUrl: string,
  streamPath: string,
  token: string | null,
  playMethod: PlayMethod,
  initialPosition: number,
): string {
  const params = new URLSearchParams();

  if (token) {
    params.set("token", token);
  }

  if (playMethod === "remux" && initialPosition > 0) {
    params.set("seek", initialPosition.toFixed(3));
  }

  const query = params.toString();
  const base =
    streamPath.startsWith("http://") || streamPath.startsWith("https://")
      ? streamPath
      : `${apiBaseUrl}${streamPath}`;
  return `${base}${query ? `?${query}` : ""}`;
}
