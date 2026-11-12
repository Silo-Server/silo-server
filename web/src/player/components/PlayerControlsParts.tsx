import { useRef, type ReactNode } from "react";
import {
  Info,
  MoreHorizontal,
  PictureInPicture2,
  RotateCcw,
  RotateCw,
  Scaling,
  Tags,
} from "lucide-react";
import type { VideoFitMode } from "../types";
import { PlayerMenuSurface } from "./PlayerMenuSurface";
import { numberText, useT } from "@/i18n";

/** Decorative separator between values; not text, so not translated. */
const SEPARATOR = "·";

/* ─────────────────────────────────────────────────────────────────────
   Building blocks of the player controls
   ───────────────────────────────────────────────────────────────────── */

export function TitleBlock({
  title,
  subtitleLabel,
  chapterTitle,
}: {
  title?: string;
  subtitleLabel?: string;
  chapterTitle?: string;
}) {
  const meta = [subtitleLabel, chapterTitle].filter((part): part is string => Boolean(part));
  return (
    <div className="player-compact-metadata flex min-w-0 flex-1 flex-col gap-1">
      {title ? (
        <div className="hud-title truncate" title={title}>
          {title}
        </div>
      ) : null}
      {meta.length > 0 ? (
        <div className="hud-meta" title={meta.join(` ${SEPARATOR} `)}>
          {meta.map((part, index) => (
            <span key={index} className="flex min-w-0 items-center gap-2">
              {index > 0 ? <span aria-hidden="true" className="hud-dot" /> : null}
              <span className={index === 0 ? "shrink-0" : "truncate"}>{part}</span>
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export function TransportButton({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick?: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      className="hud-glass-btn hud-transport-btn"
      aria-label={label}
      title={label}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

/** Curved arrow with the skip-seconds number centered in the loop. */
export function SkipGlyph({
  direction,
  seconds,
}: {
  direction: "back" | "forward";
  seconds: number;
}) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="hud-skip-glyph" aria-hidden="true">
      <Arrow />
      <span>{numberText(seconds)}</span>
    </span>
  );
}

interface MoreMenuItemsProps {
  close: () => void;
  pipAvailable: boolean;
  onTogglePiP?: () => void;
  videoFit: VideoFitMode;
  onVideoFitToggle: () => void;
  showPlaybackInfo: boolean;
  onTogglePlaybackInfo: () => void;
  markerEditAvailable: boolean;
  markerEditActive: boolean;
  onToggleMarkerEdit?: () => void;
}

/** The less frequent controls, shared by the desktop More menu and the compact overflow. */
export function MoreMenuItems({
  close,
  pipAvailable,
  onTogglePiP,
  videoFit,
  onVideoFitToggle,
  showPlaybackInfo,
  onTogglePlaybackInfo,
  markerEditAvailable,
  markerEditActive,
  onToggleMarkerEdit,
}: MoreMenuItemsProps) {
  const t = useT();
  return (
    <>
      <OverflowToggle
        icon={<Scaling />}
        label={t("web.player.controls.fillScreen")}
        checked={videoFit === "cover"}
        onClick={onVideoFitToggle}
      />
      <OverflowToggle
        icon={<Info />}
        label={t("web.player.controls.playbackInfo")}
        checked={showPlaybackInfo}
        onClick={() => {
          onTogglePlaybackInfo();
          close();
        }}
      />
      {markerEditAvailable && onToggleMarkerEdit && (
        <OverflowToggle
          icon={<Tags />}
          label={t("web.player.controls.editMarkers")}
          checked={markerEditActive}
          onClick={() => {
            onToggleMarkerEdit();
            close();
          }}
        />
      )}
      {pipAvailable && onTogglePiP && (
        <OverflowAction
          icon={<PictureInPicture2 />}
          label={t("web.player.controls.pictureInPictureItem")}
          onClick={() => {
            onTogglePiP();
            close();
          }}
        />
      )}
    </>
  );
}

export function MoreMenu({
  open,
  onOpenChange,
  ...items
}: Omit<MoreMenuItemsProps, "close"> & { open: boolean; onOpenChange: (open: boolean) => void }) {
  const t = useT();
  const menuRef = useRef<HTMLDivElement>(null);
  return (
    <div ref={menuRef} className="relative">
      <button
        type="button"
        className="hud-icon-btn"
        aria-label={t("web.player.controls.moreOptions")}
        title={t("web.player.controls.more")}
        aria-expanded={open}
        aria-haspopup="menu"
        onClick={() => onOpenChange(!open)}
      >
        <MoreHorizontal />
      </button>
      {open && (
        <PlayerMenuSurface
          title={t("web.player.controls.more")}
          anchorRef={menuRef}
          className="hud-popover"
          onClose={() => onOpenChange(false)}
        >
          <MoreMenuItems close={() => onOpenChange(false)} {...items} />
        </PlayerMenuSurface>
      )}
    </div>
  );
}

export function OverflowAction({
  icon,
  label,
  onClick,
}: {
  icon: ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button role="menuitem" type="button" className="hud-item" onClick={onClick}>
      <span aria-hidden="true" className="hud-item-icon">
        {icon}
      </span>
      <span>{label}</span>
    </button>
  );
}

function OverflowToggle({
  icon,
  label,
  checked,
  onClick,
}: {
  icon: ReactNode;
  label: string;
  checked: boolean;
  onClick: () => void;
}) {
  return (
    <button
      role="menuitemcheckbox"
      type="button"
      aria-checked={checked}
      className="hud-item"
      onClick={onClick}
    >
      <span aria-hidden="true" className="hud-item-icon">
        {icon}
      </span>
      <span>{label}</span>
      <span aria-hidden="true" className="hud-switch" />
    </button>
  );
}
