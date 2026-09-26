/** Display order for the built-in subtitle providers; unknown ones sort last. */
export const SUBTITLE_PROVIDER_ORDER = ["opensubtitles", "subdl", "subsource"];

export function sortSubtitleProviders<T extends { provider_name: string }>(providers: T[]): T[] {
  return [...providers].sort((a, b) => {
    const ai = SUBTITLE_PROVIDER_ORDER.indexOf(a.provider_name);
    const bi = SUBTITLE_PROVIDER_ORDER.indexOf(b.provider_name);
    if (ai === -1 && bi === -1) return 0;
    if (ai === -1) return 1;
    if (bi === -1) return -1;
    return ai - bi;
  });
}
