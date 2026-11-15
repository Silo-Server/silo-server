import { useId } from "react";
import { useT } from "@/i18n";
import { formatDecimal } from "@/lib/numberFormat";
import { PLAYBACK_SPEEDS, formatPlaybackSpeed } from "../utils/playbackSpeed";

interface PlaybackSpeedPickerProps {
  value: number;
  onChange: (rate: number) => void;
}

/**
 * The speeds as one evenly spaced segmented row. Each segment is a menu item,
 * so the menu's arrow keys move between speeds.
 */
export function PlaybackSpeedPicker({ value, onChange }: PlaybackSpeedPickerProps) {
  const t = useT();
  const labelId = useId();
  return (
    <div role="group" aria-labelledby={labelId} className="px-4 pt-1 pb-2">
      <div className="flex items-baseline justify-between pb-2">
        <span id={labelId} className="text-sm">
          {t("web.player.speed.heading")}
        </span>
        <span className="text-sm font-semibold tabular-nums" aria-hidden="true">
          {formatPlaybackSpeed(value)}
        </span>
      </div>
      <div
        className="hud-segmented"
        // The speeds run slowest to fastest left to right in every language,
        // as the timeline does, and the menu's arrow keys follow the row.
        dir="ltr"
        // A press in a gap between segments would move focus out of the menu,
        // which closes it.
        onPointerDown={(event) => {
          if (!(event.target instanceof HTMLButtonElement)) event.preventDefault();
        }}
      >
        {PLAYBACK_SPEEDS.map((rate) => (
          <button
            key={rate}
            role="menuitemradio"
            type="button"
            aria-checked={rate === value}
            aria-label={formatPlaybackSpeed(rate)}
            className="hud-segment"
            onClick={() => {
              if (rate !== value) onChange(rate);
            }}
          >
            {formatDecimal(rate, 2, 0)}
          </button>
        ))}
      </div>
    </div>
  );
}
