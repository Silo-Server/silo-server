import { useCallback, useRef, useState } from "react";
import { Check, Settings } from "lucide-react";
import { useT } from "@/i18n";
import {
  buildVersionStatusLabels,
  plainResolutionLabel,
  resolveActiveQualityOptionId,
} from "../playback-info";
import type { QualityOption } from "../types";
import { formatPlaybackSpeed } from "../utils/playbackSpeed";
import { PlaybackSpeedPicker } from "./PlaybackSpeedPicker";
import { PlayerMenuSurface } from "./PlayerMenuSurface";

export interface VersionInfo {
  fileId: number;
  label: string;
  isCurrentSource: boolean;
  isRequestedSource: boolean;
}

interface QualityMenuProps {
  options: QualityOption[];
  activeId: string;
  /** The plan's `active_quality`: the entry it delivers, lit unless the preference is Auto. */
  activeQuality?: string;
  isTranscoding: boolean;
  error: string | null;
  onSelect: (id: string) => void;
  versions?: VersionInfo[];
  versionLocked?: boolean;
  onSwitchVersion?: (fileId: number) => void;
  /** Omitted while something else owns the rate (a Watch Party room). */
  playbackSpeed?: number;
  onPlaybackSpeedChange?: (rate: number) => void;
}

export function QualityMenu({
  options,
  activeId,
  activeQuality,
  isTranscoding,
  error,
  onSelect,
  versions,
  versionLocked,
  onSwitchVersion,
  playbackSpeed = 1,
  onPlaybackSpeedChange,
}: QualityMenuProps) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  const handleSelect = useCallback(
    (id: string) => {
      onSelect(id);
      setOpen(false);
    },
    [onSelect],
  );

  // Close when focus leaves the menu and its trigger.
  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) {
      setOpen(false);
    }
  }, []);

  const showVersions = !versionLocked && versions && versions.length > 1 && onSwitchVersion;
  const showQuality = options.length > 1;
  const showSpeed = onPlaybackSpeedChange !== undefined;
  if (!showVersions && !showQuality && !showSpeed) return null;

  let menuLabel = t("web.player.quality.version");
  if (showSpeed) menuLabel = t("web.player.quality.playbackSettings");
  else if (showQuality) menuLabel = t("web.player.quality.title");

  const resolvedActiveId = resolveActiveQualityOptionId(options, activeId, activeQuality);
  const activeOption = options.find((option) => option.id === resolvedActiveId);
  let sourceLabel: string | null = null;
  if (showQuality) {
    sourceLabel = isTranscoding
      ? "…"
      : (activeOption?.label ?? plainResolutionLabel(activeId) ?? t("web.player.quality.title"));
  } else if (showVersions) sourceLabel = t("web.player.quality.version");
  // A speed other than 1x stays on the gear in every layout: inline after the
  // quality label, or on the gear's corner where the label is hidden.
  const changedSpeed = showSpeed && playbackSpeed !== 1 ? formatPlaybackSpeed(playbackSpeed) : null;

  // The visible value is part of the name, so speech users can say what they see.
  const valueLabel = sourceLabel !== menuLabel ? sourceLabel : null;
  let accessibleName = menuLabel;
  if (valueLabel && changedSpeed) {
    accessibleName = t("web.player.quality.triggerWithValueAndSpeed", {
      menu: menuLabel,
      value: valueLabel,
      speed: changedSpeed,
    });
  } else if (valueLabel) {
    accessibleName = t("web.player.quality.triggerWithValue", {
      menu: menuLabel,
      value: valueLabel,
    });
  } else if (changedSpeed) {
    accessibleName = t("web.player.quality.triggerWithSpeed", {
      menu: menuLabel,
      speed: changedSpeed,
    });
  }

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="hud-icon-btn hud-icon-btn--labeled"
        onClick={() => setOpen((v) => !v)}
        aria-label={accessibleName}
        title={menuLabel}
        aria-expanded={open}
        aria-haspopup="menu"
      >
        <Settings />
        {sourceLabel && <span className="player-quality-label">{sourceLabel}</span>}
        {changedSpeed && (
          <span aria-hidden="true" className="hud-speed-badge">
            {changedSpeed}
          </span>
        )}
      </button>

      {open && (
        <PlayerMenuSurface
          title={menuLabel}
          anchorRef={menuRef}
          className="hud-popover z-30 w-[22rem] max-w-[calc(100vw-2rem)]"
          onClose={() => setOpen(false)}
        >
          <div className="hud-scroll-list">
            {error && (
              <p role="alert" className="px-4 pb-1 text-sm text-red-300">
                {error}
              </p>
            )}
            {showSpeed && (
              <>
                <PlaybackSpeedPicker value={playbackSpeed} onChange={onPlaybackSpeedChange} />
                {(showVersions || showQuality) && <div className="hud-panel-divider" />}
              </>
            )}
            {versionLocked && showQuality && (
              <p className="px-4 py-1.5 text-xs leading-relaxed text-[var(--hud-ash)]">
                {t("web.player.quality.watchPartyVersionLocked")}
              </p>
            )}
            {/* Version switching (multiple file versions) */}
            {showVersions && (
              <>
                <div className="hud-panel-section">{t("web.player.quality.sourceFile")}</div>
                {versions.map((v) => {
                  const statusLabels = buildVersionStatusLabels(v);
                  return (
                    <button
                      key={v.fileId}
                      role="menuitem"
                      type="button"
                      aria-current={v.isCurrentSource ? "true" : undefined}
                      className="hud-item"
                      onClick={() => {
                        onSwitchVersion(v.fileId);
                        setOpen(false);
                      }}
                    >
                      <span aria-hidden="true" className="hud-check">
                        {v.isCurrentSource ? <Check /> : null}
                      </span>
                      <span className="min-w-0 flex-1 truncate" title={v.label}>
                        {v.label}
                      </span>
                      {statusLabels.length > 0 && (
                        <span className="flex shrink-0 gap-1">
                          {statusLabels.map((status) => (
                            <span key={status} className="hud-chip">
                              {status}
                            </span>
                          ))}
                        </span>
                      )}
                    </button>
                  );
                })}
              </>
            )}
            {showQuality && (
              <>
                {showVersions && <div className="hud-panel-divider" />}
                <div className="hud-panel-section">{t("web.player.quality.streamingQuality")}</div>
                {options.map((opt) => {
                  const selected = opt.id === resolvedActiveId;
                  return (
                    <button
                      key={opt.id}
                      role="menuitem"
                      type="button"
                      className="hud-item"
                      aria-current={selected ? "true" : undefined}
                      onClick={() => handleSelect(opt.id)}
                    >
                      <span aria-hidden="true" className="hud-check">
                        {selected ? <Check /> : null}
                      </span>
                      <span className="flex-1">{opt.label}</span>
                      {opt.sublabel ? (
                        <span className="hud-item-secondary tabular-nums">{opt.sublabel}</span>
                      ) : null}
                      {selected && (
                        <span className="sr-only">{t("web.player.quality.selected")}</span>
                      )}
                    </button>
                  );
                })}
              </>
            )}
          </div>
        </PlayerMenuSurface>
      )}
    </div>
  );
}
