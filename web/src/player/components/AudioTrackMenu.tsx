import { useState, useCallback, useRef } from "react";
import { AudioLines, Check } from "lucide-react";
import type { PlayerAudioTrack } from "../types";
import { audioTrackLabel, formatChannels } from "@/lib/mediaFormat";
import { formatLanguageName } from "@/pages/ItemDetail/components/versionFormatUtils";
import { useT, type Translate } from "@/i18n";
import { PlayerMenuSurface } from "./PlayerMenuSurface";

interface AudioTrackMenuProps {
  tracks: PlayerAudioTrack[];
  activeIndex: number;
  onSelect: (index: number, currentPosition: number) => void;
  currentPosition: number;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  hideTrigger?: boolean;
}

/**
 * Per-track descriptor used for the menu rows.
 *  - `title` — human-readable primary label (falls back gracefully).
 *  - `meta`  — "codec · layout · language".
 *
 * This mirrors what the item detail page surfaces so the audio track names
 * stay consistent across the app instead of collapsing to an opaque embedded
 * label like `SyncUP` or `????`.
 */
interface TrackDescriptor {
  title: string;
  meta: string;
  isDefault: boolean;
}

/** A stream title that only restates the format ("DCA (DTS Coherent Acoustics)", "Stereo"). */
const CODEC_TITLE =
  /^(?:[\s()·.,-]|dca|dts(?:-hd)?|ma|coherent|acoustics|ac-?3|e-?ac-?3|truehd|atmos|aac|flac|opus|mp3|pcm|dolby|digital|plus|surround|stereo|mono|[257]\.[01])+$/i;

function describeTrack(t: Translate, track: PlayerAudioTrack, index: number): TrackDescriptor {
  const language = formatLanguageName(track.language ?? "");
  // Only a title the stream carries: without one, the language leads rather
  // than a run-together "en aac 5.1".
  const streamTitle = track.title || track.embedded_title || "";
  const title =
    (streamTitle && !(language && CODEC_TITLE.test(streamTitle)) ? streamTitle : language) ||
    t("web.player.audioMenu.trackFallback", { number: index + 1 });
  // Codec and layout are what tell two tracks in the same language apart; the
  // bitrate and sample detail stay in Playback info.
  const metaParts = [
    audioTrackLabel(track),
    formatChannels(track.channels),
    language && language.toLowerCase() !== title.toLowerCase() ? language : "",
  ].filter(Boolean);
  return {
    title,
    meta: metaParts.join(" \u00B7 "),
    isDefault: Boolean(track.default),
  };
}

export function AudioTrackMenu({
  tracks,
  activeIndex,
  onSelect,
  currentPosition,
  open: controlledOpen,
  onOpenChange,
  hideTrigger = false,
}: AudioTrackMenuProps) {
  const t = useT();
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = useCallback(
    (value: boolean | ((previous: boolean) => boolean)) => {
      const next = typeof value === "function" ? value(open) : value;
      if (controlledOpen === undefined) setUncontrolledOpen(next);
      onOpenChange?.(next);
    },
    [controlledOpen, onOpenChange, open],
  );
  const menuRef = useRef<HTMLDivElement>(null);

  const handleSelect = useCallback(
    (index: number) => {
      onSelect(index, currentPosition);
      setOpen(false);
    },
    [currentPosition, onSelect],
  );

  const handleBlur = useCallback(
    (e: React.FocusEvent) => {
      if (!menuRef.current?.contains(e.relatedTarget as Node)) {
        setOpen(false);
      }
    },
    [setOpen],
  );

  if (tracks.length === 0) return null;

  const disabled = tracks.length <= 1;
  // The rail names the track playing ("English 5.1") where it has room.
  const activeTrack = tracks[activeIndex];
  const valueLabel = activeTrack
    ? [formatLanguageName(activeTrack.language ?? ""), formatChannels(activeTrack.channels)]
        .filter(Boolean)
        .join(" ")
    : "";

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      {!hideTrigger && (
        <button
          type="button"
          className="hud-icon-btn hud-rail-btn"
          onClick={disabled ? undefined : () => setOpen((v) => !v)}
          aria-label={
            valueLabel
              ? t("web.player.audioMenu.triggerWithTrack", { track: valueLabel })
              : t("web.player.audioMenu.trigger")
          }
          title={
            disabled ? t("web.player.audioMenu.onlyOneTrack") : t("web.player.audioMenu.trigger")
          }
          aria-expanded={open}
          aria-disabled={disabled}
          aria-haspopup="menu"
        >
          <AudioLines />
          {valueLabel ? <span className="hud-rail-label">{valueLabel}</span> : null}
        </button>
      )}

      {open && (
        <PlayerMenuSurface
          title={t("web.player.audioMenu.heading")}
          anchorRef={menuRef}
          className="hud-popover w-[27rem] max-w-[calc(100vw-2rem)]"
          onClose={() => setOpen(false)}
        >
          <div className="hud-scroll-list">
            {tracks.map((track, index) => {
              const descriptor = describeTrack(t, track, index);
              const isActive = index === activeIndex;
              return (
                <button
                  key={index}
                  role="menuitemradio"
                  type="button"
                  aria-checked={isActive}
                  className="hud-item items-start"
                  onClick={() => handleSelect(index)}
                >
                  <span aria-hidden="true" className="hud-check pt-0.5">
                    {isActive ? <Check /> : null}
                  </span>
                  <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="line-clamp-2" title={descriptor.title}>
                      {descriptor.title}
                    </span>
                    {descriptor.meta || descriptor.isDefault ? (
                      <span className="hud-item-secondary flex flex-wrap items-center gap-x-2 gap-y-1">
                        {descriptor.meta ? <span>{descriptor.meta}</span> : null}
                        {descriptor.isDefault ? (
                          <span className="hud-chip">{t("web.player.audioMenu.defaultBadge")}</span>
                        ) : null}
                      </span>
                    ) : null}
                  </span>
                </button>
              );
            })}
          </div>
        </PlayerMenuSurface>
      )}
    </div>
  );
}
