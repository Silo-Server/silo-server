import { useSyncExternalStore } from "react";
import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { onMeteredConnection } from "@/player/bandwidth-probe";
import { networkInformation } from "@/player/network-information";
import { normalizeQualityPreferenceV3 } from "@/player/protocol-v3";
import { playbackQualitySettings } from "@/playback/playbackQualitySettings";

/**
 * The resolution cap playback should apply, read from the contract.
 *
 * The settings screen writes `playback.preferred_quality` (and its bitrate
 * companion) as canonical rows, while playback historically took the cap from
 * `currentProfile.quality_preference` — a legacy compound column the canonical
 * write deliberately does not mirror, because one string cannot losslessly
 * carry two axes. Left alone, choosing a quality would appear to save and then
 * change nothing about what plays.
 *
 * The canonical vocabulary is "auto", "480p", "720p", "1080p" and "2160p".
 * "original" is a deprecated member that a migration rewrites to "auto"; it,
 * and the older "source" and "max" spellings, read as "auto" here, because
 * Auto already plays the original file whenever the device and connection
 * can carry it.
 *
 * `fallback` is the profile column, used until the settings read resolves (and
 * if it fails). Playback must never block on a preferences fetch, and the
 * column still holds the pre-cutover choice for anyone who has not re-picked.
 */
export function useQualityPreference(fallback?: string | null): string | null {
  return useQualityPreferenceState(fallback).preference;
}

function subscribeToNetworkChanges(onChange: () => void): () => void {
  const connection = networkInformation() as (EventTarget & object) | undefined;
  connection?.addEventListener?.("change", onChange);
  return () => connection?.removeEventListener?.("change", onChange);
}

/**
 * Whether the current link is metered, as the player decides it at start,
 * updated when the browser reports a network or Data Saver change.
 */
function useMeteredConnection(): boolean {
  return useSyncExternalStore(subscribeToNetworkChanges, onMeteredConnection, () => false);
}

/**
 * The resolution cap the player starts with on the current connection: the
 * cellular quality on a metered link (with its contract default until the read
 * resolves), the main quality otherwise. Detail pages initialize their edition
 * after the first successful settings read.
 */
export function useQualityPreferenceState(fallback?: string | null) {
  const metered = useMeteredConnection();
  const { data } = useEffectiveSettings({
    keys: [
      SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY,
      SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY,
    ],
  });
  const resolved = playbackQualitySettings(data, metered).qualityPreference;
  return {
    preference:
      typeof resolved === "string"
        ? normalizeQualityPreferenceV3(resolved)
        : fallback
          ? normalizeQualityPreferenceV3(fallback)
          : null,
    settled: data != null,
  };
}
