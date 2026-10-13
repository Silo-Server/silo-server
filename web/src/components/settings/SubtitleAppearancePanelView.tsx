import { useEffect, useMemo, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { RotateCcw, X } from "lucide-react";
import { usePercentDraft } from "@/hooks/usePercentDraft";
import {
  BACKGROUND_STYLE_OPTIONS,
  BG_COLOR_PALETTE,
  FONT_COLOR_PALETTE,
  FONT_FAMILY_OPTIONS,
  FONT_SIZE_OPTIONS,
  POSITION_OPTIONS,
  computeSubtitleStyles,
  type SubtitleAppearance,
} from "@/lib/subtitleAppearance";

export interface SubtitleAppearancePanelViewProps {
  /** Whether the panel is rendered. Mounting the portal is gated on this. */
  open: boolean;
  /** Current appearance value. Parent owns parsing and serialization. */
  value: SubtitleAppearance;
  /**
   * Apply a partial patch to the current value. The parent decides how to
   * persist (auto-save in the player; mutation-bound in admin), and is
   * expected to feed the next value back via `value` (optimistic OK).
   */
  onChange: (patch: Partial<SubtitleAppearance>) => void;
  /** Close affordance (Escape key + backdrop click + chrome × button). */
  onClose: () => void;
  /** Whether to render the footer reset action. */
  canReset?: boolean;
  /** Reset action — clears the override / restores the fallback style. */
  onReset?: () => void;
  /** Reset button label. Defaults to "Use fallback style". */
  resetLabel?: string;
  /**
   * Small contextual eyebrow above the title — "Captions" in player,
   * something like "admin · Apple TV 4K" in admin. Defaults to "Captions".
   */
  eyebrow?: string;
  /** Footer status message on the right. Defaults to auto-save copy. */
  status?: string;
}

/**
 * Pure view of the subtitle appearance panel — preview, sectioned form,
 * and chrome — with no data-fetching of its own. Both the in-player panel
 * and the admin override dialog wrap this with their own hook layer so the
 * UI is identical wherever subtitles are tuned.
 */
export function SubtitleAppearancePanelView({
  open,
  value,
  onChange,
  onClose,
  canReset = true,
  onReset,
  resetLabel = "Use fallback style",
  eyebrow = "Captions",
  status = "Changes saved automatically",
}: SubtitleAppearancePanelViewProps) {
  const previewStyles = useMemo(() => computeSubtitleStyles(value), [value]);

  // Close on Escape. Blur first: the panel unmounts on close, and React does
  // not dispatch blur for a removed input, so a typed opacity would be lost.
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
      onClose();
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [open, onClose]);

  if (!open) return null;

  const portalHost = document.fullscreenElement ?? document.body;

  return createPortal(
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Subtitle appearance"
      className="player-subtitle-panel fixed inset-0 z-[70] flex items-center justify-center p-4"
      onClick={onClose}
    >
      {/* Backdrop — dims whatever's behind the panel. */}
      <div aria-hidden="true" className="absolute inset-0 bg-black/70 backdrop-blur-sm" />

      <div
        role="document"
        className="relative z-10 w-full max-w-[640px] overflow-hidden rounded-2xl border border-white/10 bg-[#111114]/95 text-white shadow-2xl ring-1 ring-black/40 backdrop-blur-xl"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-white/[0.08] px-5 py-3.5">
          <div className="flex flex-col">
            <div className="text-[10px] font-semibold tracking-[0.22em] text-white/50 uppercase">
              {eyebrow}
            </div>
            <div className="text-[17px] font-semibold tracking-tight text-white">
              Subtitle appearance
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="flex h-9 w-9 items-center justify-center rounded-full text-white/70 transition-colors hover:bg-white/10 hover:text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
          >
            <X className="h-[18px] w-[18px]" />
          </button>
        </div>

        {/* Live preview */}
        <div className="relative mx-5 mt-5 flex h-28 items-center justify-center overflow-hidden rounded-lg border border-white/[0.06] bg-[linear-gradient(135deg,#1f2937_0%,#0b0f1a_60%,#1e293b_100%)]">
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-0 opacity-[0.12] mix-blend-overlay"
            style={{
              backgroundImage: "radial-gradient(rgba(255,255,255,0.7) 1px, transparent 1px)",
              backgroundSize: "3px 3px",
            }}
          />
          <div
            className="absolute inset-x-0 flex flex-col items-center gap-1 px-4 text-center"
            style={previewStyles.containerStyle}
          >
            <span
              className="inline-block rounded px-3 py-1 leading-snug"
              style={{ ...previewStyles.cueStyle, whiteSpace: "pre-line" }}
            >
              The quick brown fox
            </span>
            <span
              className="inline-block rounded px-3 py-1 leading-snug"
              style={{ ...previewStyles.cueStyle, whiteSpace: "pre-line" }}
            >
              jumps over the lazy dog.
            </span>
          </div>
        </div>

        {/* Scrollable body */}
        <div className="overlay-scroll max-h-[52vh] space-y-6 overflow-y-auto px-5 py-5">
          {/* Text */}
          <Section label="Text">
            <Row label="Size">
              <PillGroup
                options={FONT_SIZE_OPTIONS}
                value={value.fontSize}
                onChange={(v) => onChange({ fontSize: v })}
              />
            </Row>
            <Row label="Font">
              <PillGroup
                options={FONT_FAMILY_OPTIONS}
                value={value.fontFamily}
                onChange={(v) => onChange({ fontFamily: v })}
              />
            </Row>
            <Row label="Color">
              <ColorSwatchRow
                colors={FONT_COLOR_PALETTE}
                value={value.fontColor}
                onChange={(v) => onChange({ fontColor: v })}
              />
            </Row>
            <Row label="Opacity">
              <PercentField
                value={value.textOpacity}
                onChange={(v) => onChange({ textOpacity: v })}
                ariaLabel="Text opacity"
              />
            </Row>
            <Row label="Outline">
              <ToggleSwitch
                checked={value.textOutline}
                onChange={(v) => onChange({ textOutline: v })}
                label="Text outline"
              />
            </Row>
          </Section>

          <Divider />

          {/* Background */}
          <Section label="Background">
            <Row label="Style">
              <PillGroup
                options={BACKGROUND_STYLE_OPTIONS}
                value={value.backgroundStyle}
                onChange={(v) => onChange({ backgroundStyle: v })}
              />
            </Row>
            {value.backgroundStyle === "box" && (
              <>
                <Row label="Opacity">
                  <PercentField
                    min={0}
                    value={value.backgroundOpacity}
                    onChange={(v) => onChange({ backgroundOpacity: v })}
                    ariaLabel="Background opacity"
                  />
                </Row>
                <Row label="Color">
                  <ColorSwatchRow
                    colors={BG_COLOR_PALETTE}
                    value={value.backgroundColor}
                    onChange={(v) => onChange({ backgroundColor: v })}
                  />
                </Row>
              </>
            )}
          </Section>

          <Divider />

          {/* Position */}
          <Section label="Position">
            <Row label="Vertical">
              <PillGroup
                options={POSITION_OPTIONS}
                value={value.position}
                onChange={(v) => onChange({ position: v })}
              />
            </Row>
          </Section>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between gap-3 border-t border-white/[0.08] bg-black/30 px-5 py-3">
          {canReset && onReset ? (
            <button
              type="button"
              onClick={onReset}
              className="inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 text-[13px] text-white/70 transition-colors hover:bg-white/10 hover:text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
            >
              <RotateCcw className="h-3.5 w-3.5" />
              {resetLabel}
            </button>
          ) : (
            <span aria-hidden="true" />
          )}
          <div className="text-[11px] tracking-wide text-white/40 uppercase">{status}</div>
        </div>
      </div>
    </div>,
    portalHost,
  );
}

/* ─────────────────────────────────────────────────────────────────────
   Internal form primitives — bespoke for the dark glass aesthetic.
   Kept local (rather than reaching for shadcn) so the panel matches the
   immersive, always-black chrome wherever it's hosted.
   ───────────────────────────────────────────────────────────────────── */

function Section({ label, children }: { label: string; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <div className="text-[10.5px] font-semibold tracking-[0.2em] text-white/45 uppercase">
        {label}
      </div>
      <div className="space-y-3">{children}</div>
    </section>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-4">
      <div className="text-[13px] text-white/80 sm:w-24 sm:shrink-0">{label}</div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function Divider() {
  return <div className="h-px w-full bg-white/[0.06]" />;
}

interface PillOption<T extends string> {
  value: T;
  label: string;
}

function PillGroup<T extends string>({
  options,
  value,
  onChange,
}: {
  options: readonly PillOption<T>[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div role="radiogroup" className="flex flex-wrap gap-1.5">
      {options.map((option) => {
        const active = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(option.value)}
            className={`inline-flex items-center rounded-full px-3 py-1 text-[12.5px] font-medium transition-colors focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
              active
                ? "bg-white text-black"
                : "bg-white/[0.06] text-white/75 hover:bg-white/10 hover:text-white"
            }`}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}

function ColorSwatchRow({
  colors,
  value,
  onChange,
}: {
  colors: readonly { hex: string; label: string }[];
  value: string;
  onChange: (hex: string) => void;
}) {
  return (
    <div role="radiogroup" className="flex flex-wrap gap-2">
      {colors.map((color) => {
        const active = color.hex.toLowerCase() === value.toLowerCase();
        return (
          <button
            key={color.hex}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={color.label}
            title={color.label}
            onClick={() => onChange(color.hex)}
            className={`relative h-7 w-7 rounded-full transition-transform focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
              active ? "scale-110" : "hover:scale-105"
            }`}
            style={{
              backgroundColor: color.hex,
              boxShadow: active
                ? "0 0 0 2px rgb(255 255 255 / 0.9), 0 0 0 4px rgb(0 0 0 / 0.6)"
                : "inset 0 0 0 1px rgb(255 255 255 / 0.18)",
            }}
          />
        );
      })}
    </div>
  );
}

/** A typed percentage value, for controls where dragging a slider is more
 * fiddly than just typing the number — opacity wants precision at the low
 * end where a few percent is the difference between legible and not. */
function PercentField({
  value,
  onChange,
  min = 1,
  ariaLabel,
}: {
  value: number;
  onChange: (v: number) => void;
  min?: number;
  ariaLabel?: string;
}) {
  const { draft, setDraft, commit } = usePercentDraft(value, min, onChange);

  return (
    <div className="flex items-center gap-1.5">
      <input
        type="number"
        inputMode="numeric"
        min={min}
        max={100}
        value={draft}
        aria-label={ariaLabel}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={(e) => commit(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") e.currentTarget.blur();
        }}
        className="w-14 [appearance:textfield] rounded-md border border-white/15 bg-white/[0.06] px-2 py-1 text-right text-[12.5px] text-white outline-none focus-visible:ring-2 focus-visible:ring-white/70 [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
      />
      <span className="text-[12.5px] text-white/60">%</span>
    </div>
  );
}

function ToggleSwitch({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
        checked ? "bg-white" : "bg-white/15"
      }`}
    >
      <span
        className={`inline-block h-5 w-5 transform rounded-full shadow-md transition-transform ${
          checked ? "translate-x-[22px] bg-black" : "translate-x-[2px] bg-white"
        }`}
      />
    </button>
  );
}
