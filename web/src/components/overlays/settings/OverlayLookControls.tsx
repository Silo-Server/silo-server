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
import { useT, type Translate } from "@/i18n";
import { cn } from "@/lib/utils";

// Badge text in the style picker's samples: a resolution, not interface text.
const SAMPLE_BADGE = "4K";
const PERCENT = "%";

// The presets an older server (below settings revision 25) can store.
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
  const t = useT();
  const ids = supportsStyles ? PRESET_IDS : LEGACY_PRESET_IDS;
  return (
    <div
      role="radiogroup"
      aria-label={t("web.settings.overlays.look.style")}
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
                {SAMPLE_BADGE}
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
const presetChoice = (t: Translate) => ({
  value: "preset" as const,
  label: t("web.settings.overlays.look.styleDefault"),
});

const shapeChoices = (
  t: Translate,
): readonly { value: OverlayShape | Inherit; label: string }[] => [
  presetChoice(t),
  { value: "pill", label: t("web.settings.overlays.look.shape.pill") },
  { value: "rounded", label: t("web.settings.overlays.look.shape.rounded") },
  { value: "square", label: t("web.settings.overlays.look.shape.square") },
];

const weightChoices = (
  t: Translate,
): readonly { value: OverlayFontWeight | Inherit; label: string }[] => [
  presetChoice(t),
  { value: "regular", label: t("web.settings.overlays.look.weight.regular") },
  { value: "semibold", label: t("web.settings.overlays.look.weight.semibold") },
  { value: "extrabold", label: t("web.settings.overlays.look.weight.extrabold") },
];

type Toggle = "on" | "off" | Inherit;

const caseChoices = (t: Translate): readonly { value: Toggle; label: string }[] => [
  presetChoice(t),
  { value: "on", label: t("web.settings.overlays.look.letters.capitals") },
  { value: "off", label: t("web.settings.overlays.look.letters.asWritten") },
];

const shadowChoices = (t: Translate): readonly { value: Toggle; label: string }[] => [
  presetChoice(t),
  { value: "on", label: t("web.settings.overlays.look.shadow.on") },
  { value: "off", label: t("web.settings.overlays.look.shadow.off") },
];

// Swatches as #RRGGBBAA (fill, border) or #RRGGBB (text).
const fillSwatches = (t: Translate) =>
  [
    { value: "#000000B3", label: t("web.settings.overlays.look.fill.dark") },
    { value: "#0F172ACC", label: t("web.settings.overlays.look.fill.slate") },
    { value: "#000000FF", label: t("web.settings.overlays.look.fill.black") },
    { value: "#FFFFFFE6", label: t("web.settings.overlays.look.fill.light") },
    { value: "#00000000", label: t("web.settings.overlays.look.fill.none") },
  ] as const;

const textSwatches = (t: Translate) =>
  [
    { value: "#FFFFFF", label: t("web.settings.overlays.look.textColor.white") },
    { value: "#000000", label: t("web.settings.overlays.look.textColor.black") },
    { value: "#F5C518", label: t("web.settings.overlays.look.textColor.gold") },
    { value: "#CBD5E1", label: t("web.settings.overlays.look.textColor.silver") },
  ] as const;

type BorderChoice = Inherit | "none" | "subtle" | "strong";

const borderChoices = (t: Translate): readonly { value: BorderChoice; label: string }[] => [
  presetChoice(t),
  { value: "none", label: t("web.settings.overlays.look.border.none") },
  { value: "subtle", label: t("web.settings.overlays.look.border.subtle") },
  { value: "strong", label: t("web.settings.overlays.look.border.strong") },
];

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
  const t = useT();
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
          {presetChoice(t).label}
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
  const t = useT();
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
              <p className="text-sm font-medium">{t("web.settings.overlays.look.size")}</p>
              <span className="text-muted-foreground text-xs tabular-nums">
                {Math.round(size * 100)}
                {PERCENT}
              </span>
            </div>
            <Slider
              min={STYLE_SIZE_MIN}
              max={STYLE_SIZE_MAX}
              step={0.05}
              value={[size]}
              thumbLabels={[t("web.settings.overlays.look.sizeLabel")]}
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
            label={t("web.settings.overlays.look.shape")}
            value={style.shape ?? "preset"}
            options={shapeChoices(t)}
            onChange={(next) => patch({ shape: next === "preset" ? undefined : next })}
          />
          <ChoiceButtons
            label={t("web.settings.overlays.look.weight")}
            value={style.fontWeight ?? "preset"}
            options={weightChoices(t)}
            onChange={(next) => patch({ fontWeight: next === "preset" ? undefined : next })}
          />
          <ChoiceButtons
            label={t("web.settings.overlays.look.letters")}
            value={toggleOf(style.uppercase)}
            options={caseChoices(t)}
            onChange={(next) => patch({ uppercase: fromToggle(next) })}
          />
          <ChoiceButtons
            label={t("web.settings.overlays.look.shadow")}
            value={toggleOf(style.shadow)}
            options={shadowChoices(t)}
            onChange={(next) => patch({ shadow: fromToggle(next) })}
          />
          <Swatches
            label={t("web.settings.overlays.look.fill")}
            value={style.background}
            swatches={fillSwatches(t)}
            onChange={(background) => patch({ background })}
          />
          <Swatches
            label={t("web.settings.overlays.look.text")}
            value={style.textColor}
            swatches={textSwatches(t)}
            onChange={(textColor) => patch({ textColor })}
          />
          <ChoiceButtons
            label={t("web.settings.overlays.look.border")}
            value={borderChoiceOf(style)}
            options={borderChoices(t)}
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
              {t("web.settings.overlays.look.reset")}
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
