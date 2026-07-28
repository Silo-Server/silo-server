/**
 * The Quality picker's presets.
 *
 * The server stores two orthogonal values — `playback.preferred_quality` (a
 * resolution cap) and `playback.max_bitrate_kbps` (a bandwidth cap, null for
 * uncapped). This file composes them into the single list a user picks from.
 *
 * Presets live here rather than in the contract on purpose. Baking "high" into
 * an enum member would freeze what it means: retuning 1080p High from 10 to 12
 * Mbps would be a contract change every client has to agree to, and every
 * client would still have to decompose the compound value before sending it.
 * As a client-side table it is a one-line edit, and older servers keep working
 * because they only ever see the two axes they already understand.
 *
 * The bitrates match the ladder the in-player switcher already used
 * (web/src/player/hooks/useTranscodeQuality.ts), so a preset chosen here lines
 * up with what the player offers mid-playback.
 */

export interface QualityPreset {
  id: string;
  label: string;
  description: string;
  /** null means "let the server decide", matching the contract's auto member. */
  resolution: "auto" | "original" | "2160p" | "1080p" | "720p" | "480p";
  /** null is uncapped. */
  bitrateKbps: number | null;
}

export const QUALITY_PRESETS: readonly QualityPreset[] = [
  {
    id: "auto",
    label: "Auto",
    description: "Silo picks based on your connection.",
    resolution: "auto",
    bitrateKbps: null,
  },
  {
    id: "original",
    label: "Original",
    description: "Never transcode. Needs bandwidth to match the file.",
    resolution: "original",
    bitrateKbps: null,
  },
  {
    id: "2160p",
    label: "4K",
    description: "Up to 2160p.",
    resolution: "2160p",
    bitrateKbps: null,
  },
  {
    id: "1080p-high",
    label: "1080p High",
    description: "1080p at up to 10 Mbps.",
    resolution: "1080p",
    bitrateKbps: 10000,
  },
  {
    id: "1080p",
    label: "1080p",
    description: "1080p at up to 6 Mbps.",
    resolution: "1080p",
    bitrateKbps: 6000,
  },
  {
    id: "1080p-low",
    label: "1080p Low",
    description: "1080p at up to 3 Mbps, for a slower link.",
    resolution: "1080p",
    bitrateKbps: 3000,
  },
  {
    id: "720p-high",
    label: "720p High",
    description: "720p at up to 4 Mbps.",
    resolution: "720p",
    bitrateKbps: 4000,
  },
  {
    id: "720p",
    label: "720p",
    description: "720p at up to 2 Mbps.",
    resolution: "720p",
    bitrateKbps: 2000,
  },
  {
    id: "480p",
    label: "480p",
    description: "480p at up to 1.5 Mbps, for the tightest connections.",
    resolution: "480p",
    bitrateKbps: 1500,
  },
] as const;

/** The preset id for a stored (resolution, bitrate) pair, or null for a custom combination. */
export function presetIdFor(
  resolution: string | null | undefined,
  bitrateKbps: number | null | undefined,
): string | null {
  const normalizedBitrate = bitrateKbps ?? null;
  const match = QUALITY_PRESETS.find(
    (preset) => preset.resolution === resolution && preset.bitrateKbps === normalizedBitrate,
  );
  return match?.id ?? null;
}

export function presetById(id: string): QualityPreset | undefined {
  return QUALITY_PRESETS.find((preset) => preset.id === id);
}

/**
 * A label for any stored pair, including combinations no preset covers —
 * someone who set the two axes independently through the API, or whose values
 * came from a legacy compound value the migration decomposed.
 */
export function describeQuality(
  resolution: string | null | undefined,
  bitrateKbps: number | null | undefined,
): string {
  const preset = presetIdFor(resolution, bitrateKbps);
  if (preset) return presetById(preset)!.label;

  const resolutionLabel =
    resolution === "auto" || !resolution
      ? "Auto"
      : resolution === "original"
        ? "Original"
        : resolution === "2160p"
          ? "4K"
          : resolution;
  if (bitrateKbps == null) return resolutionLabel;
  const mbps = bitrateKbps / 1000;
  const rounded = Number.isInteger(mbps) ? String(mbps) : mbps.toFixed(1);
  return `${resolutionLabel} at ${rounded} Mbps`;
}
