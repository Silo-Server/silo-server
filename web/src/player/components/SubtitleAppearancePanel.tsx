import { useCallback } from "react";
import { useSubtitleAppearanceSetting } from "@/hooks/queries/subtitleAppearance";
import type { SubtitleAppearance } from "@/lib/subtitleAppearance";
import { SubtitleAppearancePanelView } from "@/components/settings/SubtitleAppearancePanelView";

interface SubtitleAppearancePanelProps {
  open: boolean;
  onClose: () => void;
}

/**
 * In-player subtitle styling panel modelled on Plex's "Playback Settings"
 * sheet — lets a viewer tune font size, colour, position, background, and
 * outline without leaving the player. Every change writes
 * playback.subtitle_appearance at profile_device scope, so the rendered
 * subtitles update live through the shared effective-settings cache and the
 * choice stays on this device.
 *
 * The visual chrome lives in {@link SubtitleAppearancePanelView}; this
 * wrapper only binds the panel to the canonical setting.
 */
export function SubtitleAppearancePanel({ open, onClose }: SubtitleAppearancePanelProps) {
  const { appearance, save, reset } = useSubtitleAppearanceSetting();

  const update = useCallback(
    (patch: Partial<SubtitleAppearance>) => {
      // Best effort: a failed tweak in the player leaves the previous value in
      // place and the next change retries, which beats interrupting playback
      // with an error dialog.
      void save({ ...appearance, ...patch }).catch(() => undefined);
    },
    [appearance, save],
  );

  const resetDefaults = useCallback(() => {
    void reset().catch(() => undefined);
  }, [reset]);

  return (
    <SubtitleAppearancePanelView
      open={open}
      value={appearance}
      onChange={update}
      onClose={onClose}
      onReset={resetDefaults}
    />
  );
}
