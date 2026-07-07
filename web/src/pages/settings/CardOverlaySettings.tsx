import { useId, useState, type ReactNode } from "react";
import { Check, ImageIcon, ImageOff, X } from "lucide-react";
import { toast } from "sonner";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { OverlayPreviewCard } from "@/components/overlays/OverlayPreviewCard";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import {
  ACCENT_PALETTE,
  buildDefaultPrefs,
  CATEGORY_GROUPS,
  getOverlayDef,
  isOverlaySuppressed,
  OVERLAY_REGISTRY,
  OVERLAY_PRESETS,
  POSITION_OPTIONS,
  PRESET_IDS,
  getPreset,
  type CardOverlayPrefs,
  type OverlayId,
  type OverlayPosition,
  type PresetId,
} from "@/lib/overlays";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

interface SettingRowProps {
  label: string;
  description: string;
  hint?: string;
  control: (props: { id: string }) => ReactNode;
}

function SettingRow({ label, description, hint, control }: SettingRowProps) {
  const controlId = useId();
  return (
    <div className="border-border/50 flex flex-col gap-3 border-t pt-4 first:border-t-0 first:pt-0 sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0 space-y-0.5">
        <Label htmlFor={controlId} className="text-sm font-medium">
          {label}
        </Label>
        <p className="text-muted-foreground text-[13px] leading-relaxed">{description}</p>
        {hint && <p className="text-muted-foreground/70 text-xs italic">{hint}</p>}
      </div>
      <div className="w-full sm:w-auto">{control({ id: controlId })}</div>
    </div>
  );
}

interface AccentSwatchProps {
  value: string | undefined;
  defaultValue: string | undefined;
  disabled?: boolean;
  onChange: (next: string | undefined) => void;
}

function AccentSwatch({ value, defaultValue, disabled, onChange }: AccentSwatchProps) {
  const [open, setOpen] = useState(false);
  const display = value ?? defaultValue ?? "#94a3b8";
  const hasOverride = !!value;
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          disabled={disabled}
          className="border-border/60 hover:border-border focus:border-border focus-visible:ring-ring relative h-6 w-6 rounded-full border transition-colors focus:outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-40"
          style={{ background: display }}
          aria-label={hasOverride ? "Change accent color" : "Set accent color"}
          title={hasOverride ? `Accent: ${display}` : "Default accent"}
        />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[200px] p-3">
        <div className="space-y-2">
          <div className="text-xs font-medium tracking-wide uppercase">Accent color</div>
          <div className="grid grid-cols-6 gap-1.5">
            {ACCENT_PALETTE.map((color) => (
              <button
                key={color.value}
                type="button"
                onClick={() => {
                  onChange(color.value);
                  setOpen(false);
                }}
                className="border-border/40 hover:border-border focus-visible:ring-ring relative h-7 w-7 rounded-full border focus:outline-none focus-visible:ring-2"
                style={{ background: color.value }}
                title={color.label}
                aria-label={color.label}
              >
                {value === color.value && (
                  <Check
                    size={12}
                    className="absolute inset-0 m-auto"
                    style={{
                      color: color.value === "#ffffff" ? "black" : "white",
                    }}
                  />
                )}
              </button>
            ))}
          </div>
          <button
            type="button"
            onClick={() => {
              onChange(undefined);
              setOpen(false);
            }}
            className="text-muted-foreground hover:text-foreground flex w-full items-center gap-1.5 text-xs"
          >
            <X size={12} /> Reset to default
          </button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

interface OverlayToggleProps {
  overlayId: OverlayId;
  prefs: CardOverlayPrefs;
  onUpdate: (next: CardOverlayPrefs) => void;
}

function OverlayToggle({ overlayId, prefs, onUpdate }: OverlayToggleProps) {
  const def = getOverlayDef(overlayId);
  if (!def) return null;
  const config = prefs.items[overlayId];
  const preset = getPreset(prefs.preset);
  const resolvedShowIcon = !!def.iconCapable && (config.showIcon ?? preset.preferIcon);
  const suppressed = config.enabled && isOverlaySuppressed(overlayId, prefs);

  return (
    <SettingRow
      label={def.label}
      description={def.description}
      hint={
        suppressed
          ? "Hidden while the combined Resolution + HDR badge is enabled."
          : def.availabilityNote
      }
      control={({ id }) => (
        <div className="flex items-center gap-2">
          {def.iconCapable && (
            <button
              type="button"
              disabled={!config.enabled}
              onClick={() =>
                onUpdate({
                  ...prefs,
                  items: {
                    ...prefs.items,
                    [overlayId]: { ...config, showIcon: !resolvedShowIcon },
                  },
                })
              }
              className="text-muted-foreground hover:text-foreground disabled:opacity-40"
              title={resolvedShowIcon ? "Hide icon" : "Show icon"}
              aria-label={resolvedShowIcon ? "Hide icon" : "Show icon"}
            >
              {resolvedShowIcon ? <ImageIcon size={16} /> : <ImageOff size={16} />}
            </button>
          )}
          <AccentSwatch
            value={config.accentColor}
            defaultValue={def.defaultAccent}
            disabled={!config.enabled}
            onChange={(next) =>
              onUpdate({
                ...prefs,
                items: {
                  ...prefs.items,
                  [overlayId]: { ...config, accentColor: next },
                },
              })
            }
          />
          <Select
            value={config.position}
            disabled={!config.enabled}
            onValueChange={(value) =>
              onUpdate({
                ...prefs,
                items: {
                  ...prefs.items,
                  [overlayId]: { ...config, position: value as OverlayPosition },
                },
              })
            }
          >
            <SelectTrigger className="w-[140px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {POSITION_OPTIONS.map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Switch
            id={id}
            checked={config.enabled}
            onCheckedChange={(checked) =>
              onUpdate({
                ...prefs,
                items: {
                  ...prefs.items,
                  [overlayId]: { ...config, enabled: checked },
                },
              })
            }
          />
        </div>
      )}
    />
  );
}

interface PresetPickerProps {
  value: PresetId;
  onChange: (next: PresetId) => void;
}

function PresetPicker({ value, onChange }: PresetPickerProps) {
  return (
    <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
      {PRESET_IDS.map((id) => {
        const preset = OVERLAY_PRESETS[id];
        const active = value === id;
        return (
          <button
            key={id}
            type="button"
            onClick={() => onChange(id)}
            className={`flex flex-col items-stretch gap-2 rounded-lg border p-3 text-left transition-colors ${
              active
                ? "border-primary bg-primary/5"
                : "border-border/60 hover:border-border bg-transparent"
            }`}
          >
            <div className="flex h-12 items-center justify-center rounded-md bg-gradient-to-br from-slate-700 to-slate-900">
              <span
                className={preset.badgeClass}
                style={preset.badgeStyle(preset.id === "vibrant" ? "#f5c518" : undefined)}
              >
                Sample
              </span>
            </div>
            <div>
              <div className="text-sm font-medium">{preset.label}</div>
              <div className="text-muted-foreground text-xs leading-snug">{preset.description}</div>
            </div>
          </button>
        );
      })}
    </div>
  );
}

export default function CardOverlaySettings() {
  const { prefs, setPrefs, isLoading, enabled } = useOverlayPrefs();
  const [previewVariant, setPreviewVariant] = useState<"movie" | "show">("movie");

  const handleUpdate = (next: CardOverlayPrefs) => {
    setPrefs(next);
    toast.success("Setting saved");
  };

  if (isLoading) return null;
  const displayPrefs = prefs ?? buildDefaultPrefs();

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Card Overlays</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Choose which badges appear on poster cards, where they sit, and how they look. Inspired by
          Kometa.
        </p>
      </div>

      {!enabled && (
        <div className="surface-panel-subtle rounded-2xl border px-4 py-3 text-sm">
          Card overlays have been disabled by your server administrator.
        </div>
      )}

      <div className={enabled ? "" : "pointer-events-none opacity-50"}>
        <SettingsGroup
          title="Preview"
          description="Live preview of your current overlay configuration."
        >
          <div className="flex flex-col items-center gap-4">
            <OverlayPreviewCard prefs={displayPrefs} variant={previewVariant} size="md" />
            <div className="flex gap-1.5">
              {(["movie", "show"] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  onClick={() => setPreviewVariant(v)}
                  className={`rounded-full border px-3 py-1 text-xs font-medium capitalize transition-colors ${
                    previewVariant === v
                      ? "border-primary bg-primary/10 text-primary"
                      : "border-border/60 hover:border-border text-muted-foreground"
                  }`}
                >
                  {v}
                </button>
              ))}
            </div>
          </div>
        </SettingsGroup>

        <Tabs defaultValue="overlays" className="mt-6">
          <TabsList>
            <TabsTrigger value="overlays">Overlays</TabsTrigger>
            <TabsTrigger value="style">Style</TabsTrigger>
          </TabsList>

          <TabsContent value="overlays" className="mt-4 space-y-6">
            {CATEGORY_GROUPS.map(({ category, title, description }) => {
              const overlays = OVERLAY_REGISTRY.filter((d) => d.category === category);
              if (overlays.length === 0) return null;
              return (
                <SettingsGroup key={category} title={title} description={description}>
                  {overlays.map((def) => (
                    <OverlayToggle
                      key={def.id}
                      overlayId={def.id}
                      prefs={displayPrefs}
                      onUpdate={handleUpdate}
                    />
                  ))}
                </SettingsGroup>
              );
            })}
          </TabsContent>

          <TabsContent value="style" className="mt-4 space-y-6">
            <SettingsGroup
              title="Preset"
              description="A preset controls the base appearance of every badge. You can override the accent color of individual badges in the Overlays tab."
            >
              <PresetPicker
                value={displayPrefs.preset}
                onChange={(next) => handleUpdate({ ...displayPrefs, preset: next })}
              />
            </SettingsGroup>
            <SettingsGroup title="How styling works" description="Where to find what.">
              <ul className="text-muted-foreground list-disc space-y-1 pl-5 text-sm">
                <li>
                  <strong className="text-foreground">Preset</strong> sets the badge shape,
                  background, font, and whether icons show by default.
                </li>
                <li>
                  <strong className="text-foreground">Accent color</strong> (per overlay) tints that
                  badge — gold for IMDb, red for RT, etc.
                </li>
                <li>
                  <strong className="text-foreground">Icon toggle</strong> (per overlay) overrides
                  the preset's icon default for a single overlay.
                </li>
                <li>
                  <strong className="text-foreground">Position</strong> picks which corner the badge
                  sits in. Multiple badges in the same corner stack vertically.
                </li>
              </ul>
            </SettingsGroup>
          </TabsContent>
        </Tabs>
      </div>
    </div>
  );
}
