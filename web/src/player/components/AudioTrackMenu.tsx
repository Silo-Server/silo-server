import { useState, useCallback, useEffect, useRef } from "react";
import { AudioLines } from "lucide-react";
import type { PlayerAudioTrack } from "../types";
import { formatChannels, mapAudioLabel } from "@/lib/mediaFormat";
import {
  audioTitle,
  compactAudioMeta,
  formatLanguageName,
} from "@/pages/ItemDetail/components/versionFormatUtils";

interface AudioTrackMenuProps {
  tracks: PlayerAudioTrack[];
  activeIndex: number;
  onSelect: (index: number, currentPosition: number) => void;
  currentPosition: number;
}

/**
 * Rich per-track descriptor used for the menu rows.
 *  - `title`   — human-readable primary label (falls back gracefully).
 *  - `meta`    — "Language · layout · bitrate · sample-rate · bit-depth".
 *  - `badges`  — codec / channel / default pills.
 *
 * This mirrors what the item detail page surfaces so the audio track names
 * stay consistent across the app instead of collapsing to an opaque embedded
 * label like `SyncUP` or `????`.
 */
interface TrackDescriptor {
  title: string;
  meta: string;
  codecLabel: string;
  channelsLabel: string;
  isDefault: boolean;
}

function describeTrack(track: PlayerAudioTrack, index: number): TrackDescriptor {
  const title = audioTitle(track) || `Track ${index + 1}`;
  const language = formatLanguageName(track.language ?? "");
  const metaParts = [
    language && language.toLowerCase() !== title.toLowerCase() ? language : "",
    compactAudioMeta(track),
  ].filter(Boolean);
  return {
    title,
    meta: metaParts.join(" \u00B7 "),
    codecLabel: track.codec ? mapAudioLabel(track.codec) : "",
    channelsLabel: formatChannels(track.channels),
    isDefault: Boolean(track.default),
  };
}

export function AudioTrackMenu({
  tracks,
  activeIndex,
  onSelect,
  currentPosition,
}: AudioTrackMenuProps) {
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  const handleSelect = useCallback(
    (index: number) => {
      onSelect(index, currentPosition);
      setOpen(false);
    },
    [currentPosition, onSelect],
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

  if (tracks.length === 0) return null;

  const disabled = tracks.length <= 1;

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className={`player-utility-btn ${disabled ? "cursor-default opacity-40" : ""}`}
        onClick={disabled ? undefined : () => setOpen((v) => !v)}
        aria-label="Audio tracks"
        aria-expanded={open}
        aria-disabled={disabled}
        aria-haspopup="menu"
      >
        <AudioLines className="h-[18px] w-[18px]" />
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 bottom-full mb-2 max-w-[min(360px,calc(100vw-1rem))] min-w-[280px] rounded-lg bg-black/90 py-1.5 shadow-xl backdrop-blur-sm"
          onKeyDown={handleMenuKeyDown}
        >
          <div className="px-3 py-1.5 text-xs font-medium tracking-wide text-white/50 uppercase">
            Audio
          </div>
          {tracks.map((track, index) => {
            const descriptor = describeTrack(track, index);
            const isActive = index === activeIndex;
            return (
              <button
                key={index}
                ref={(el) => {
                  menuItemsRef.current[index] = el;
                }}
                role="menuitem"
                type="button"
                className={`flex w-full items-start gap-2 px-3 py-2 text-left transition-colors hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                  isActive ? "text-blue-400" : "text-white/85"
                }`}
                onClick={() => handleSelect(index)}
              >
                <span className="mt-[3px] w-4 shrink-0 text-center text-sm leading-none">
                  {isActive ? "\u2713" : ""}
                </span>
                <span className="flex min-w-0 flex-1 flex-col gap-1">
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate text-sm font-medium">{descriptor.title}</span>
                    {descriptor.codecLabel && <TrackBadge>{descriptor.codecLabel}</TrackBadge>}
                    {descriptor.channelsLabel && (
                      <TrackBadge>{descriptor.channelsLabel}</TrackBadge>
                    )}
                    {descriptor.isDefault && <TrackBadge variant="outline">Default</TrackBadge>}
                  </span>
                  {descriptor.meta && (
                    <span className="truncate text-[11px] leading-snug text-white/55">
                      {descriptor.meta}
                    </span>
                  )}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** Compact pill used to tag tracks with codec/channel/default metadata. The
 *  `outline` variant is used for the DEFAULT marker so it reads as a
 *  qualifier rather than another content facet. */
function TrackBadge({
  children,
  variant = "solid",
}: {
  children: React.ReactNode;
  variant?: "solid" | "outline";
}) {
  const base =
    "inline-flex items-center rounded px-1.5 py-[1px] text-[9.5px] font-semibold tracking-wide whitespace-nowrap uppercase leading-4";
  const skin =
    variant === "outline" ? "border border-white/25 text-white/70" : "bg-white/10 text-white/75";
  return <span className={`${base} ${skin}`}>{children}</span>;
}
