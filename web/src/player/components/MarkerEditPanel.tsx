import { useEffect, useRef, useState } from "react";
import { Check, GripHorizontal, RotateCcw, Trash2, X } from "lucide-react";
import type { MarkerEditor } from "../hooks/useMarkerEditor";
import { MARKER_KINDS, MARKER_LABELS } from "../hooks/useMarkerEditor";
import type { MarkerKind } from "../types";
import { formatTime } from "./SeekBar";

interface MarkerEditPanelProps {
  editor: MarkerEditor;
  currentTime: number;
}

/** Solid accent per kind, matching the seek-bar region tints. */
const DOT_COLORS: Record<MarkerKind, string> = {
  intro: "bg-sky-400",
  recap: "bg-violet-400",
  credits: "bg-amber-400",
  preview: "bg-emerald-400",
};

/**
 * Floating panel for editing intro/recap/credits/preview markers from the
 * player. The selected segment expands to reveal its controls (and is the one
 * whose draggable handles show on the seek bar); the others stay collapsed to
 * a single line so the panel reads cleanly.
 */
export function MarkerEditPanel({ editor, currentTime }: MarkerEditPanelProps) {
  const { draft, activeKind, saving, error, dirty } = editor;
  const panelRef = useRef<HTMLDivElement>(null);
  const dragCleanupRef = useRef<(() => void) | null>(null);
  const [offset, setOffset] = useState({ x: 0, y: 0 });

  useEffect(() => {
    return () => {
      dragCleanupRef.current?.();
    };
  }, []);

  // Drag the panel by its header. The anchored position is shifted with a
  // transform and clamped to the player bounds so it can't leave the frame.
  const handleDragStart = (e: React.PointerEvent) => {
    e.preventDefault();
    dragCleanupRef.current?.();
    const startX = e.clientX;
    const startY = e.clientY;
    const base = offset;

    const onMove = (ev: PointerEvent) => {
      const panel = panelRef.current;
      let nx = base.x + (ev.clientX - startX);
      let ny = base.y + (ev.clientY - startY);
      const parent = panel?.offsetParent as HTMLElement | null;
      if (panel && parent) {
        const margin = 8;
        // offsetLeft/offsetTop are the layout (untransformed) position, so the
        // clamp range stays stable across repeated drags.
        nx = Math.min(
          parent.clientWidth - panel.offsetWidth - margin - panel.offsetLeft,
          Math.max(margin - panel.offsetLeft, nx),
        );
        ny = Math.min(
          parent.clientHeight - panel.offsetHeight - margin - panel.offsetTop,
          Math.max(margin - panel.offsetTop, ny),
        );
      }
      setOffset({ x: nx, y: ny });
    };
    const onUp = () => {
      document.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerup", onUp);
      dragCleanupRef.current = null;
    };
    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
    dragCleanupRef.current = onUp;
  };

  return (
    <div
      ref={panelRef}
      className="absolute bottom-40 left-4 z-50 w-[22rem] overflow-hidden rounded-2xl border border-white/10 bg-neutral-900/95 text-white shadow-2xl backdrop-blur-xl sm:bottom-44 sm:left-6"
      style={{ transform: `translate(${offset.x}px, ${offset.y}px)` }}
      onClick={(e) => e.stopPropagation()}
    >
      {/* Header — drag handle */}
      <div
        className="flex cursor-move touch-none items-start justify-between gap-3 px-4 pt-3.5 pb-3 select-none"
        onPointerDown={handleDragStart}
      >
        <div className="flex items-start gap-2">
          <GripHorizontal className="mt-0.5 h-4 w-4 shrink-0 text-white/30" />
          <div className="flex flex-col gap-0.5">
            <span className="text-sm font-semibold tracking-tight">Edit markers</span>
            <span className="text-[11px] leading-tight text-white/40">
              Drag the timeline handles, or set points to the playhead.
            </span>
          </div>
        </div>
        <button
          type="button"
          aria-label="Close marker editor"
          className="-mr-1 rounded-md p-1 text-white/50 transition-colors hover:bg-white/10 hover:text-white"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={editor.cancel}
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      {/* Segments */}
      <div className="flex flex-col gap-1 px-2 pb-2">
        {MARKER_KINDS.map((kind) => {
          const range = draft[kind];
          const isActive = kind === activeKind;
          return (
            <div
              key={kind}
              className={[
                "rounded-xl px-2.5 transition-colors",
                isActive ? "bg-white/[0.06] py-2 ring-1 ring-white/10" : "py-1",
              ].join(" ")}
            >
              <button
                type="button"
                onClick={() => editor.selectKind(kind)}
                className="flex w-full items-center gap-2.5 rounded-md py-1 text-left"
              >
                <span
                  className={[
                    "shrink-0 rounded-full transition-all",
                    DOT_COLORS[kind],
                    isActive ? "h-2.5 w-2.5 ring-2 ring-white/25" : "h-2 w-2 opacity-50",
                  ].join(" ")}
                />
                <span
                  className={[
                    "text-[13px] font-medium transition-colors",
                    isActive ? "text-white" : "text-white/70",
                  ].join(" ")}
                >
                  {MARKER_LABELS[kind]}
                </span>
                <span
                  className={[
                    "ml-auto font-mono text-[11px] tabular-nums",
                    range ? "text-white/75" : "text-white/35",
                  ].join(" ")}
                >
                  {range ? `${formatTime(range.start)} – ${formatTime(range.end)}` : "Not set"}
                </span>
              </button>

              {isActive && (
                <div className="mt-2 flex items-center gap-1.5">
                  <EdgeButton onClick={() => editor.setEdge(kind, "start", currentTime)}>
                    Set start
                  </EdgeButton>
                  <EdgeButton onClick={() => editor.setEdge(kind, "end", currentTime)}>
                    Set end
                  </EdgeButton>
                  <div className="ml-auto flex items-center gap-0.5">
                    <button
                      type="button"
                      aria-label={`Reset ${MARKER_LABELS[kind]} marker`}
                      title="Reset to saved"
                      disabled={!editor.isKindDirty(kind)}
                      onClick={() => editor.resetKind(kind)}
                      className="rounded-md p-1.5 text-white/40 transition-colors hover:bg-white/10 hover:text-white disabled:pointer-events-none disabled:opacity-25"
                    >
                      <RotateCcw className="h-3.5 w-3.5" />
                    </button>
                    <button
                      type="button"
                      aria-label={`Clear ${MARKER_LABELS[kind]} marker`}
                      title="Clear marker"
                      disabled={!range}
                      onClick={() => editor.clearKind(kind)}
                      className="rounded-md p-1.5 text-white/40 transition-colors hover:bg-white/10 hover:text-red-300 disabled:pointer-events-none disabled:opacity-25"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </div>
                </div>
              )}
            </div>
          );
        })}
      </div>

      {error && (
        <div className="mx-4 mb-2 rounded-md bg-red-500/15 px-2.5 py-1.5 text-[11px] text-red-300">
          {error}
        </div>
      )}

      {/* Footer */}
      <div className="flex items-center justify-between gap-3 border-t border-white/[0.06] bg-black/20 px-4 py-3">
        <span className="shrink-0 font-mono text-[11px] whitespace-nowrap text-white/40 tabular-nums">
          {formatTime(currentTime)}
        </span>
        <div className="flex shrink-0 items-center gap-1.5">
          {dirty && (
            <button
              type="button"
              onClick={editor.resetAll}
              title="Reset all markers to saved"
              className="flex items-center gap-1 rounded-lg px-2.5 py-1.5 text-xs font-medium whitespace-nowrap text-white/60 transition-colors hover:bg-white/10 hover:text-white"
            >
              <RotateCcw className="h-3.5 w-3.5" />
              Reset all
            </button>
          )}
          <button
            type="button"
            onClick={editor.cancel}
            className="rounded-lg px-3 py-1.5 text-xs font-medium whitespace-nowrap text-white/70 transition-colors hover:bg-white/10 hover:text-white"
          >
            Cancel
          </button>
          <button
            type="button"
            disabled={!dirty || saving}
            onClick={() => {
              void editor.save();
            }}
            className="flex items-center gap-1.5 rounded-lg bg-white px-3.5 py-1.5 text-xs font-semibold whitespace-nowrap text-neutral-900 transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {saving ? (
              <span className="h-3 w-3 animate-spin rounded-full border-2 border-neutral-900/30 border-t-neutral-900" />
            ) : (
              <Check className="h-3.5 w-3.5" />
            )}
            Save
          </button>
        </div>
      </div>
    </div>
  );
}

function EdgeButton({ onClick, children }: { onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="rounded-md border border-white/10 bg-white/[0.05] px-2.5 py-1 text-[11px] font-medium text-white/85 transition-colors hover:border-white/20 hover:bg-white/10 hover:text-white active:bg-white/[0.16]"
    >
      {children}
    </button>
  );
}
