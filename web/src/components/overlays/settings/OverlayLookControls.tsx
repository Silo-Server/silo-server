import { useState } from "react";

import { ChoiceButtons } from "@/components/settings/ChoiceButtons";
import { Button } from "@/components/ui/button";
import { Slider } from "@/components/ui/slider";
import {
  OVERLAY_PRESETS,
  PRESET_IDS,
  STYLE_SIZE_MAX,
  STYLE_SIZE_MIN,
  type CardOverlayPrefs,
  type OverlayFontWeight,
  type OverlayShape,
  type OverlayStyle,
  type PresetId,
} from "@/lib/overlays";
import { cn } from "@/lib/utils";

// The presets an older server (below settings revision 23) can store.
const LEGACY_PRESET_IDS: readonly PresetId[] = ["minimal", "classic", "vibrant", "pill", "square"];

export function BadgeStylePicker({
  value,
  onChange,
  supportsStyles,
}: {
  value: PresetId;
  onChange: (next: PresetId) => void;
  supportsStyles: boolean;
}) {
  const ids = supportsStyles ? PRESET_IDS : LEGACY_PRESET_IDS;
  return (
    <div
      role="radiogroup"
      aria-label="Badge style"
      className="grid grid-cols-3 gap-2 sm:grid-cols-4 lg:grid-cols-7"
    >
      {ids.map((id) => {
        const preset = OVERLAY_PRESETS[id];
        const active = value === id;
        return (
          <button
            key={id}
            type="button"
            role="radio"
            aria-checked={active}
            title={preset.description}
            onClick={() => onChange(id)}
            className={cn(
              "focus-visible:ring-ring flex flex-col items-stretch gap-2 rounded-xl border p-2 text-left transition-colors focus-visible:ring-2 focus-visible:outline-none",
              active ? "border-primary bg-primary/8" : "border-border/60 hover:border-border",
            )}
          >
            <span className="flex h-10 items-center justify-center rounded-md bg-gradient-to-br from-slate-700 to-slate-900">
              <span
                className={preset.badgeClass}
                style={preset.badgeStyle(preset.id === "vibrant" ? "#f5c518" : undefined)}
              >
                4K
              </span>
            </span>
            <span className="text-center text-xs font-medium">{preset.label}</span>
          </button>
        );
      })}
    </div>
  );
}

// "preset" stands for an absent style field: the preset's own value.
type Inherit = "preset";
const PRESET_CHOICE = { value: "preset" as const, label: "Style default" };

const SHAPE_CHOICES: readonly { value: OverlayShape | Inherit; label: string }[] = [
  PRESET_CHOICE,
  { value: "pill", label: "Pill" },
  { value: "rounded", label: "Rounded" },
  { value: "square", label: "Square" },
];

const WEIGHT_CHOICES: readonly { value: OverlayFontWeight | Inherit; label: string }[] = [
  PRESET_CHOICE,
  { value: "regular", label: "Regular" },
  { value: "semibold", label: "Semibold" },
  { value: "extrabold", label: "Extra bold" },
];

type Toggle = "on" | "off" | Inherit;

const CASE_CHOICES: readonly { value: Toggle; label: string }[] = [
  PRESET_CHOICE,
  { value: "on", label: "Capitals" },
  { value: "off", label: "As written" },
];

const SHADOW_CHOICES: readonly { value: Toggle; label: string }[] = [
  PRESET_CHOICE,
  { value: "on", label: "Shadow" },
  { value: "off", label: "No shadow" },
];

// Swatches as #RRGGBBAA (fill, border) or #RRGGBB (text).
const FILL_SWATCHES = [
  { value: "#000000B3", label: "Dark" },
  { value: "#0F172ACC", label: "Slate" },
  { value: "#000000FF", label: "Black" },
  { value: "#FFFFFFE6", label: "Light" },
  { value: "#00000000", label: "None" },
] as const;

const TEXT_SWATCHES = [
  { value: "#FFFFFF", label: "White" },
  { value: "#000000", label: "Black" },
  { value: "#F5C518", label: "Gold" },
  { value: "#CBD5E1", label: "Silver" },
] as const;

const BORDER_CHOICES = [
  PRESET_CHOICE,
  { value: "none", label: "None" },
  { value: "subtle", label: "Subtle" },
  { value: "strong", label: "Strong" },
] as const;
type BorderChoice = (typeof BORDER_CHOICES)[number]["value"];

const BORDER_STYLES: Record<
  Exclude<BorderChoice, Inherit>,
  Pick<OverlayStyle, "borderColor" | "borderWidth">
> = {
  none: { borderWidth: 0 },
  subtle: { borderColor: "#FFFFFF26", borderWidth: 1 },
  strong: { borderColor: "#FFFFFFB3", borderWidth: 2 },
};

function borderChoiceOf(style: OverlayStyle): BorderChoice {
  if (style.borderWidth === undefined && style.borderColor === undefined) return "preset";
  if (style.borderWidth === 0) return "none";
  return style.borderWidth !== undefined && style.borderWidth >= 2 ? "strong" : "subtle";
}

function toggleOf(value: boolean | undefined): Toggle {
  return value === undefined ? "preset" : value ? "on" : "off";
}

function fromToggle(value: Toggle): boolean | undefined {
  return value === "preset" ? undefined : value === "on";
}

// Drops undefined fields so an emptied style leaves the document entirely.
function withStyle(prefs: CardOverlayPrefs, patch: Partial<OverlayStyle>): CardOverlayPrefs {
  const merged: Record<string, unknown> = { ...prefs.style, ...patch };
  for (const key of Object.keys(merged)) {
    if (merged[key] === undefined) delete merged[key];
  }
  const { style: _style, ...rest } = prefs;
  return Object.keys(merged).length > 0 ? { ...rest, style: merged as OverlayStyle } : rest;
}

function Swatches({
  label,
  value,
  swatches,
  onChange,
}: {
  label: string;
  value: string | undefined;
  swatches: readonly { value: string; label: string }[];
  onChange: (next: string | undefined) => void;
}) {
  return (
    <div className="space-y-2">
      <p className="text-sm font-medium">{label}</p>
      <div className="flex flex-wrap items-center gap-1.5" role="radiogroup" aria-label={label}>
        <button
          type="button"
          role="radio"
          aria-checked={value === undefined}
          onClick={() => onChange(undefined)}
          className={cn(
            "border-border text-muted-foreground hover:text-foreground focus-visible:ring-ring h-7 rounded-full border px-2.5 text-xs font-medium focus-visible:ring-2 focus-visible:outline-none",
            value === undefined && "border-primary text-foreground",
          )}
        >
          {PRESET_CHOICE.label}
        </button>
        {swatches.map((swatch) => {
          const active = value?.toLowerCase() === swatch.value.toLowerCase();
          return (
            <button
              key={swatch.value}
              type="button"
              role="radio"
              aria-checked={active}
              aria-label={swatch.label}
              title={swatch.label}
              onClick={() => onChange(swatch.value)}
              className={cn(
                "focus-visible:ring-ring h-7 w-7 rounded-full border bg-[conic-gradient(#ccc_25%,#fff_0_50%,#ccc_0_75%,#fff_0)] bg-[length:8px_8px] focus-visible:ring-2 focus-visible:outline-none",
                active ? "ring-primary ring-2 ring-offset-1" : "border-border/60",
              )}
            >
              <span
                className="block h-full w-full rounded-full"
                style={{ background: swatch.value }}
              />
            </button>
          );
        })}
      </div>
    </div>
  );
}

/**
 * The badges' look: a preset, then overrides on top of it. Each override has a
 * "Style default" choice that removes it, so a viewer can always get back to
 * the preset as designed.
 */
export function OverlayLookControls({
  prefs,
  onChange,
  supportsStyles,
}: {
  prefs: CardOverlayPrefs;
  onChange: (next: CardOverlayPrefs) => void;
  supportsStyles: boolean;
}) {
  const style = prefs.style ?? {};
  // The slider moves freely and saves once, when it is let go.
  const [draftSize, setDraftSize] = useState<number | null>(null);
  const size = draftSize ?? style.size ?? 1;
  const patch = (next: Partial<OverlayStyle>) => onChange(withStyle(prefs, next));

  return (
    <div className="space-y-5">
      <BadgeStylePicker
        value={prefs.preset}
        onChange={(preset) => onChange({ ...prefs, preset })}
        supportsStyles={supportsStyles}
      />
      {supportsStyles ? (
        <div className="space-y-5">
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <p className="text-sm font-medium">Size</p>
              <span className="text-muted-foreground text-xs tabular-nums">
                {Math.round(size * 100)}%
              </span>
            </div>
            <Slider
              min={STYLE_SIZE_MIN}
              max={STYLE_SIZE_MAX}
              step={0.05}
              value={[size]}
              thumbLabels={["Badge size"]}
              onValueChange={([next]) => {
                if (next !== undefined) setDraftSize(next);
              }}
              onValueCommit={([next]) => {
                setDraftSize(null);
                if (next === undefined) return;
                patch({ size: Math.abs(next - 1) < 0.001 ? undefined : Number(next.toFixed(2)) });
              }}
            />
          </div>
          <ChoiceButtons
            label="Shape"
            value={style.shape ?? "preset"}
            options={SHAPE_CHOICES}
            onChange={(next) => patch({ shape: next === "preset" ? undefined : next })}
          />
          <ChoiceButtons
            label="Weight"
            value={style.fontWeight ?? "preset"}
            options={WEIGHT_CHOICES}
            onChange={(next) => patch({ fontWeight: next === "preset" ? undefined : next })}
          />
          <ChoiceButtons
            label="Letters"
            value={toggleOf(style.uppercase)}
            options={CASE_CHOICES}
            onChange={(next) => patch({ uppercase: fromToggle(next) })}
          />
          <ChoiceButtons
            label="Shadow"
            value={toggleOf(style.shadow)}
            options={SHADOW_CHOICES}
            onChange={(next) => patch({ shadow: fromToggle(next) })}
          />
          <Swatches
            label="Fill"
            value={style.background}
            swatches={FILL_SWATCHES}
            onChange={(background) => patch({ background })}
          />
          <Swatches
            label="Text"
            value={style.textColor}
            swatches={TEXT_SWATCHES}
            onChange={(textColor) => patch({ textColor })}
          />
          <ChoiceButtons
            label="Border"
            value={borderChoiceOf(style)}
            options={BORDER_CHOICES}
            onChange={(next) =>
              patch(
                next === "preset"
                  ? { borderColor: undefined, borderWidth: undefined }
                  : { borderColor: undefined, ...BORDER_STYLES[next] },
              )
            }
          />
          {prefs.style ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                const { style: _style, ...rest } = prefs;
                onChange(rest);
              }}
            >
              Use the style as designed
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
