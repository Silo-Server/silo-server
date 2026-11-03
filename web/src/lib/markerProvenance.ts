import type { PlayerMarkerProvenance } from "@/player/types";

/**
 * The label for where a marker came from, as the skip prompt shows it.
 *
 * The vocabulary is deliberately about the viewer's choices, not the server's:
 * "Detected" is Silo's own analysis (including an authored chapter), a provider
 * is named by who it is, and "Edited" is a person. A provider with no friendly
 * name falls back to its own id rather than being hidden — an odd label is
 * better than a blank one, because the badge exists to answer "where did this
 * come from?".
 */
export function markerSourceLabel(provenance: PlayerMarkerProvenance): string | null {
  const { source, provider } = provenance;

  switch (source) {
    case "scanner":
      return "Detected";
    case "manual":
      return "Edited";
    case "plugin":
    case "online":
      return providerDisplayName(provider);
    default:
      // No source recorded (an older host, or a row written before provenance
      // was stored). A provider name is still worth showing when there is one.
      return provider ? providerDisplayName(provider) : null;
  }
}

/** Marker provider ids that map to a name worth showing a viewer. */
const PROVIDER_NAMES: Record<string, string> = {
  introdb: "TheIntroDB",
  built_in: "Detected",
};

/**
 * The name to show for a provider id.
 *
 * Provider ids are not uniform. A plugin provider arrives qualified as
 * `plugin:<installation id>:<capability id>`, a built-in one as `built_in`, and a
 * provider that is neither may just be its own slug. Match the capability suffix
 * rather than the whole string, or every plugin-sourced marker shows a raw
 * installation id instead of a name.
 */
function providerDisplayName(provider: string | null | undefined): string {
  const id = provider?.trim();
  if (!id) return "Online";

  if (PROVIDER_NAMES[id]) return PROVIDER_NAMES[id];

  // plugin:<installation id>:<capability id> — the capability id is the stable
  // half; the installation id is local to one server and meaningless to a viewer.
  const qualified = id.match(/^plugin:\d+:(.+)$/);
  if (qualified?.[1]) {
    return PROVIDER_NAMES[qualified[1]] ?? qualified[1];
  }

  return id;
}

/**
 * A confidence as a whole percentage, or null when there is nothing to show.
 *
 * Confidence is absent for an unrated marker; it is never inferred from 0,
 * because 0 is what the server stores for "nobody rated this" and printing it
 * would read as a real measurement of nothing.
 */
export function confidencePercent(provenance: PlayerMarkerProvenance): string | null {
  const { confidence } = provenance;
  if (confidence == null || !Number.isFinite(confidence)) return null;
  const clamped = Math.min(1, Math.max(0, confidence));
  return `${Math.round(clamped * 100)}%`;
}

/**
 * The line above the skip prompt: the confidence, the source, or both.
 *
 * Confidence first because it is the number the viewer is judging; the source
 * qualifies it. Empty when there is nothing true to say, so the prompt keeps its
 * original shape rather than carrying an empty row.
 */
export function markerProvenanceLabel(
  provenance: PlayerMarkerProvenance,
  options: { showConfidence: boolean; showSource: boolean },
): string | null {
  const parts: string[] = [];
  if (options.showConfidence) {
    const percent = confidencePercent(provenance);
    if (percent) parts.push(percent);
  }
  if (options.showSource) {
    const source = markerSourceLabel(provenance);
    if (source) parts.push(source);
  }
  return parts.length > 0 ? parts.join(" · ") : null;
}
