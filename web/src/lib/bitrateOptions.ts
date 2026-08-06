import type { SettingDefinition } from "@/lib/settingsContract";

/**
 * Bandwidth choices for `playback.max_bitrate_kbps`, shared by every surface
 * that edits it — the profile Defaults screen and the per-device screen — so
 * the same stored value always reads back as the same choice.
 *
 * The low end matches the server-owned playback ladder, and the player sends
 * the stored value as `bandwidth_cap_kbps` on start and replan so the same cap
 * governs every route. Above 20 Mbps the rungs widen —
 * nobody is fine-tuning between 30 and 40 Mbps, they are picking a rough
 * ceiling for a remote box — up to the definition's own ceiling of 200 Mbps,
 * which is what remuxed 4K HDR and untouched Blu-ray rips actually need. The
 * rung above 200 is "No limit", which clears the value rather than storing a
 * bigger number.
 *
 * Entries outside a definition's declared range are filtered out, so this list
 * can cover more ground than any single setting allows.
 */
export const BITRATE_CHOICES_KBPS = [
  1500, 2000, 4000, 6000, 10000, 15000, 20000, 40000, 60000, 100000, 200000,
];

export function formatBitrateKbps(kbps: number): string {
  if (kbps >= 1000) {
    const mbps = kbps / 1000;
    return mbps % 1 === 0 ? `${mbps} Mbps` : `${mbps.toFixed(1)} Mbps`;
  }
  return `${kbps} kbps`;
}

/**
 * The select entries for a bandwidth cap: an unset entry, the ladder bounded
 * by the definition's own range, and — when the stored value came from
 * somewhere else (an API call, another client) — that value itself, so it
 * stays selectable rather than silently reading as unset.
 */
export function bitrateSelectChoices(
  definition: SettingDefinition,
  currentValue: string,
  unsetLabel: string,
): { value: string; label: string }[] {
  const min = definition.minimum ?? 0;
  const max = definition.maximum ?? Number.MAX_SAFE_INTEGER;
  const choices = BITRATE_CHOICES_KBPS.filter((kbps) => kbps >= min && kbps <= max).map((kbps) => ({
    value: String(kbps),
    label: formatBitrateKbps(kbps),
  }));

  if (currentValue !== "" && !choices.some((choice) => choice.value === currentValue)) {
    const parsed = Number(currentValue);
    if (Number.isFinite(parsed)) {
      choices.push({ value: currentValue, label: formatBitrateKbps(parsed) });
      choices.sort((a, b) => Number(a.value) - Number(b.value));
    }
  }

  return [{ value: "", label: unsetLabel }, ...choices];
}
