import { useCallback, useState } from "react";
import {
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
  useSetSettingValue,
  useClearSettingValue,
} from "./settingValues";
import {
  legacyAudiobookIntervals,
  SEEK_CHOICES,
  SEEK_KEYS,
  seekDefault,
  validSeekInterval,
  type SeekDirection,
  type SeekMedia,
} from "@/lib/seekIntervals";

// One query for all four keys: every consumer (the always-mounted watch host,
// the audiobook player, both settings groups) shares a single cache entry and
// a single request instead of one per media kind per surface.
const ALL_SEEK_KEYS = [
  SEEK_KEYS.video.back,
  SEEK_KEYS.video.forward,
  SEEK_KEYS.audiobook.back,
  SEEK_KEYS.audiobook.forward,
];

/** Shared by playback and settings surfaces; the settings query is the only remote value store. */
export function useSeekPreferences(media: SeekMedia) {
  const capabilities = useSettingsCapabilities();
  const keys = SEEK_KEYS[media];
  const supported = ALL_SEEK_KEYS.every((key) =>
    settingsCapabilitiesSupportKey(capabilities.data, key),
  );
  const effective = useEffectiveSettings({ keys: ALL_SEEK_KEYS, enabled: supported });
  const writer = useSetSettingValue();
  const clearer = useClearSettingValue();
  const [importing, setImporting] = useState(false);
  // Read once per mount: legacy values are only ever written on servers
  // without shared settings, where nothing on this hook consumes them.
  const [legacy] = useState(() => (media === "audiobook" ? legacyAudiobookIntervals() : {}));
  const read = (direction: SeekDirection) => {
    const value = supported ? effective.data?.[keys[direction]]?.value : undefined;
    return validSeekInterval(media, direction, value) ? value : seekDefault(media, direction);
  };
  const { mutateAsync: write } = writer;
  const { mutateAsync: clear } = clearer;
  const save = useCallback(
    async (direction: SeekDirection, value: number) => {
      if (!supported) throw new Error("Shared seek settings are unavailable on this server.");
      if (!validSeekInterval(media, direction, value))
        throw new Error("Choose a supported seek interval.");
      await write({ key: keys[direction], value, identity: { scope: "profile" } });
    },
    [keys, media, supported, write],
  );
  const reset = useCallback(
    async (direction: SeekDirection) => {
      if (!supported) throw new Error("Shared seek settings are unavailable on this server.");
      await clear({ key: keys[direction], identity: { scope: "profile" } });
    },
    [clear, keys, supported],
  );
  // Both requests start in the same profile context. Settled results name each
  // outcome, so a failed second write is never presented as an atomic failure.
  const importLegacy = useCallback(async () => {
    if (media !== "audiobook" || !supported) throw new Error("Audiobook import is unavailable.");
    const entries = Object.entries(legacy) as [SeekDirection, number][];
    setImporting(true);
    try {
      const results = await Promise.allSettled(
        entries.map(([direction, value]) => save(direction, value)),
      );
      return entries.map(([direction], index) => ({ direction, ...results[index]! }));
    } finally {
      setImporting(false);
    }
  }, [legacy, media, save, supported]);
  return {
    supported,
    // Browser-local values may be edited once discovery has settled without
    // shared support — an old server or an unreachable one. Only a pending
    // check withholds them, so a failed request cannot lock a working player.
    legacyAvailable: !supported && !capabilities.isPending,
    isLoading: capabilities.isPending || (supported && effective.isPending),
    error: capabilities.error ?? effective.error,
    isSaving: importing || writer.isPending || clearer.isPending,
    skipBack: read("back"),
    skipForward: read("forward"),
    choices: SEEK_CHOICES[media],
    legacy,
    save,
    reset,
    importLegacy,
  };
}
