import { useState, useCallback, useEffect, useRef, useMemo } from "react";
import { createPortal } from "react-dom";
import { Captions, CaptionsOff, Languages, Minus, Plus, SlidersHorizontal } from "lucide-react";
import type { PlayerAudioTrack, PlayerSubtitleInfo } from "../types";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { SubtitleSearchModal } from "./SubtitleSearchModal";
import { SubtitleTranslateModal } from "./SubtitleTranslateModal";
import { SubtitleAppearancePanel } from "./SubtitleAppearancePanel";
import { playerFetch } from "../player-fetch";
import { getLanguageName } from "../utils/languageNames";
import { sortSubtitlesBySource } from "../utils/subtitleSort";
import { getSubtitleFormatLabel, isSubtitleFormatLabel } from "../utils/subtitleCodecs";
import { isTranslatableSource } from "./subtitleTranslateRequest";

interface SubtitleMenuProps {
  tracks: PlayerSubtitleInfo[];
  activeIndex: number | null;
  onSelect: (index: number | null) => void;
  delayMs: number;
  onDelayChange: (ms: number) => void;
  mediaFileId?: number;
  playerConfig?: PlayerConfig;
  onRefreshSubtitles?: () => void;
  sessionId?: string;
  getSubtitleStartPosition?: () => number;
  audioTracks?: PlayerAudioTrack[];
}

const DELAY_STEP_MS = 100;
const DELAY_MAX_MS = 10_000;

const SOURCE_LABELS: Record<string, string> = {
  external: "External",
  embedded: "Embedded",
  downloaded: "Downloaded",
};

function formatDelay(ms: number): string {
  if (ms === 0) return "0 ms";
  const sign = ms > 0 ? "+" : "−";
  return `${sign}${Math.abs(ms)} ms`;
}

export function SubtitleMenu({
  tracks,
  activeIndex,
  onSelect,
  delayMs,
  onDelayChange,
  mediaFileId,
  playerConfig,
  onRefreshSubtitles,
  sessionId,
  getSubtitleStartPosition,
  audioTracks,
}: SubtitleMenuProps) {
  const [open, setOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [translateOpen, setTranslateOpen] = useState(false);
  const [aiEnabled, setAiEnabled] = useState(false);
  const [aiTranscribeEnabled, setAiTranscribeEnabled] = useState(false);
  const [appearanceOpen, setAppearanceOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  const sortedTracks = useMemo(() => sortSubtitlesBySource(tracks), [tracks]);

  // Discover whether the server has AI subtitle translation configured, so we
  // only surface the entry point when it can actually do something. This is a
  // server-wide capability, so we fetch it once per session (keyed on the stable
  // playerConfig) rather than re-checking on every file change.
  useEffect(() => {
    if (!playerConfig) return;
    let cancelled = false;
    playerFetch<{ enabled: boolean; transcribe_enabled?: boolean }>(
      playerConfig,
      "/subtitles/ai/status",
    )
      .then((res) => {
        if (cancelled) return;
        setAiEnabled(Boolean(res?.enabled));
        setAiTranscribeEnabled(Boolean(res?.transcribe_enabled));
      })
      .catch(() => {
        if (cancelled) return;
        setAiEnabled(false);
        setAiTranscribeEnabled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [playerConfig]);

  const clampedDelay = useCallback(
    (ms: number) => Math.max(-DELAY_MAX_MS, Math.min(DELAY_MAX_MS, ms)),
    [],
  );
  const nudgeDelay = useCallback(
    (deltaMs: number) => onDelayChange(clampedDelay(delayMs + deltaMs)),
    [delayMs, onDelayChange, clampedDelay],
  );
  const resetDelay = useCallback(() => onDelayChange(0), [onDelayChange]);

  const delayDisabled = activeIndex === null;

  const handleSelect = useCallback(
    (index: number | null) => {
      onSelect(index);
      setOpen(false);
    },
    [onSelect],
  );

  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) {
      setOpen(false);
    }
  }, []);

  // Close on Escape.
  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [open]);

  const menuItemsRef = useRef<(HTMLButtonElement | null)[]>([]);

  const handleMenuKeyDown = useCallback((e: React.KeyboardEvent) => {
    const items = menuItemsRef.current.filter(Boolean) as HTMLButtonElement[];
    if (items.length === 0) return;
    const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement);
    let nextIndex: number | null = null;

    switch (e.key) {
      case "ArrowDown":
        nextIndex = currentIndex < items.length - 1 ? currentIndex + 1 : 0;
        break;
      case "ArrowUp":
        nextIndex = currentIndex > 0 ? currentIndex - 1 : items.length - 1;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = items.length - 1;
        break;
      case "Escape":
        setOpen(false);
        return;
      default:
        return;
    }
    e.preventDefault();
    items[nextIndex]?.focus();
  }, []);

  if (tracks.length === 0 && !mediaFileId) return null;

  let menuItemIndex = 0;

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="player-utility-btn"
        data-active={activeIndex !== null ? "true" : "false"}
        onClick={() => setOpen((v) => !v)}
        aria-label={activeIndex !== null ? "Disable captions" : "Enable captions"}
        aria-expanded={open}
        aria-haspopup="menu"
      >
        {activeIndex !== null ? (
          <Captions className="h-[18px] w-[18px]" />
        ) : (
          <CaptionsOff className="h-[18px] w-[18px]" />
        )}
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 bottom-full mb-2 flex w-max max-w-[min(420px,calc(100vw-1rem))] min-w-[220px] flex-col rounded-lg bg-black/90 shadow-lg backdrop-blur"
          onKeyDown={handleMenuKeyDown}
        >
          <div className="shrink-0 py-1">
            <button
              ref={(el) => {
                menuItemsRef.current[0] = el;
              }}
              role="menuitem"
              type="button"
              className={`flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                activeIndex === null ? "bg-white/5 text-white" : "text-white/70"
              }`}
              onClick={() => handleSelect(null)}
            >
              <span className="w-4 shrink-0 text-center text-xs">
                {activeIndex === null ? "✓" : ""}
              </span>
              Off
            </button>
          </div>
          <div className="max-h-[60vh] overflow-y-auto py-1">
            {sortedTracks.map((track) => {
              const isActive = track.index === activeIndex;
              const languageName = getLanguageName(track.language);
              const sourceLabel = SOURCE_LABELS[track.source ?? "embedded"] ?? "Embedded";
              const formatLabel = getSubtitleFormatLabel(track.codec);
              const hasDetail =
                track.label &&
                track.label !== track.language &&
                track.label !== languageName &&
                !isSubtitleFormatLabel(track.label, track.codec);
              const itemIdx = ++menuItemIndex;

              return (
                <button
                  key={track.index}
                  ref={(el) => {
                    menuItemsRef.current[itemIdx] = el;
                  }}
                  role="menuitem"
                  type="button"
                  className={`flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                    isActive ? "bg-white/5 text-white" : "text-white/70"
                  }`}
                  onClick={() => handleSelect(track.index)}
                >
                  <span className="mt-0.5 w-4 shrink-0 text-center text-xs">
                    {isActive ? "✓" : ""}
                  </span>
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="flex w-full items-center justify-between gap-3">
                      <span className="text-sm">{languageName}</span>
                      <span className="flex shrink-0 items-center gap-1">
                        {formatLabel && (
                          <span className="rounded bg-white/10 px-1.5 py-0.5 text-[10px] tracking-wide text-white/50 uppercase">
                            {formatLabel}
                          </span>
                        )}
                        <span className="rounded bg-white/10 px-1.5 py-0.5 text-[10px] tracking-wide text-white/50 uppercase">
                          {sourceLabel}
                        </span>
                      </span>
                    </span>
                    {hasDetail && (
                      <span
                        className="mt-0.5 block w-full truncate text-xs text-white/40"
                        title={track.label}
                      >
                        {track.label}
                      </span>
                    )}
                  </span>
                </button>
              );
            })}
          </div>
          <div className="shrink-0 border-t border-white/10 px-3 py-2">
            <div className="flex items-center justify-between gap-3">
              <span className="text-xs tracking-wide text-white/50 uppercase">Delay</span>
              <div className="flex items-center gap-1">
                <button
                  type="button"
                  className="flex h-7 w-7 items-center justify-center rounded text-white/80 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-40"
                  onClick={() => nudgeDelay(-DELAY_STEP_MS)}
                  disabled={delayDisabled || delayMs <= -DELAY_MAX_MS}
                  aria-label={`Subtitle delay ${DELAY_STEP_MS}ms earlier`}
                >
                  <Minus className="h-3.5 w-3.5" />
                </button>
                <span className="min-w-[4.5rem] text-center font-mono text-xs text-white/80 tabular-nums">
                  {formatDelay(delayMs)}
                </span>
                <button
                  type="button"
                  className="flex h-7 w-7 items-center justify-center rounded text-white/80 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-40"
                  onClick={() => nudgeDelay(DELAY_STEP_MS)}
                  disabled={delayDisabled || delayMs >= DELAY_MAX_MS}
                  aria-label={`Subtitle delay ${DELAY_STEP_MS}ms later`}
                >
                  <Plus className="h-3.5 w-3.5" />
                </button>
                <button
                  type="button"
                  className="ml-1 rounded px-2 py-1 text-xs text-white/60 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-40"
                  onClick={resetDelay}
                  disabled={delayDisabled || delayMs === 0}
                  aria-label="Reset subtitle delay"
                >
                  Reset
                </button>
              </div>
            </div>
          </div>
          <div className="shrink-0 border-t border-white/10 py-1">
            {mediaFileId && playerConfig && (
              <button
                ref={(el) => {
                  menuItemsRef.current[menuItemIndex + 1] = el;
                }}
                role="menuitem"
                type="button"
                className="flex w-full px-3 py-2 text-left text-sm text-white/70 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
                onClick={() => {
                  setSearchOpen(true);
                  setOpen(false);
                }}
              >
                Search Online…
              </button>
            )}
            {mediaFileId &&
              playerConfig &&
              ((aiEnabled && tracks.some(isTranslatableSource)) ||
                (aiTranscribeEnabled && (audioTracks?.length ?? 0) > 0)) && (
                <button
                  ref={(el) => {
                    menuItemsRef.current[menuItemIndex + 2] = el;
                  }}
                  role="menuitem"
                  type="button"
                  className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-white/70 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
                  onClick={() => {
                    setTranslateOpen(true);
                    setOpen(false);
                  }}
                >
                  <Languages className="h-3.5 w-3.5 text-white/50" />
                  Translate with AI…
                </button>
              )}
            <button
              ref={(el) => {
                menuItemsRef.current[menuItemIndex + 3] = el;
              }}
              role="menuitem"
              type="button"
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-white/70 hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
              onClick={() => {
                setAppearanceOpen(true);
                setOpen(false);
              }}
            >
              <SlidersHorizontal className="h-3.5 w-3.5 text-white/50" />
              Appearance…
            </button>
          </div>
        </div>
      )}

      <SubtitleAppearancePanel open={appearanceOpen} onClose={() => setAppearanceOpen(false)} />

      {searchOpen &&
        mediaFileId &&
        playerConfig &&
        createPortal(
          <SubtitleSearchModal
            mediaFileId={mediaFileId}
            playerConfig={playerConfig}
            isOpen={searchOpen}
            onClose={() => setSearchOpen(false)}
            onSubtitleDownloaded={() => {
              setSearchOpen(false);
              onRefreshSubtitles?.();
            }}
          />,
          document.body,
        )}

      {translateOpen && mediaFileId && playerConfig && (
        <SubtitleTranslateModal
          mediaFileId={mediaFileId}
          playerConfig={playerConfig}
          tracks={tracks}
          audioTracks={audioTracks}
          translateEnabled={aiEnabled}
          transcribeEnabled={aiTranscribeEnabled}
          isOpen={translateOpen}
          sessionId={sessionId}
          getStartPosition={getSubtitleStartPosition}
          onClose={() => setTranslateOpen(false)}
        />
      )}
    </div>
  );
}
