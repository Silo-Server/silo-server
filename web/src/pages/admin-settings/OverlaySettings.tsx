import { useMemo } from "react";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";
import { OverlayPreviewCard } from "@/components/overlays/OverlayPreviewCard";
import {
  buildDefaultPrefs,
  CATEGORY_META,
  OVERLAY_CATEGORIES,
  OVERLAY_REGISTRY,
  OVERLAY_PRESETS,
  POSITION_OPTIONS,
  PRESET_IDS,
  parseOverlayPrefs,
  serializeOverlayPrefs,
  type CardOverlayPrefs,
  type OverlayId,
  type OverlayPosition,
  type PresetId,
} from "@/lib/overlays";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const KEYS = ["overlays.enabled", "defaults.card_overlays"];

interface DefaultsEditorProps {
  value: string;
  onChange: (value: string) => void;
}

function DefaultsEditor({ value, onChange }: DefaultsEditorProps) {
  const prefs = parseOverlayPrefs(value || null);

  const updateItem = (id: OverlayId, patch: Partial<CardOverlayPrefs["items"][OverlayId]>) => {
    const next: CardOverlayPrefs = {
      ...prefs,
      items: { ...prefs.items, [id]: { ...prefs.items[id], ...patch } },
    };
    onChange(serializeOverlayPrefs(next));
  };

  const setPreset = (preset: PresetId) => {
    onChange(serializeOverlayPrefs({ ...prefs, preset }));
  };

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <Label className="text-sm font-medium">Default style preset</Label>
        <Select value={prefs.preset} onValueChange={(v) => setPreset(v as PresetId)}>
          <SelectTrigger className="w-[200px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PRESET_IDS.map((id) => (
              <SelectItem key={id} value={id}>
                {OVERLAY_PRESETS[id].label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {OVERLAY_CATEGORIES.map((category) => {
        const overlays = OVERLAY_REGISTRY.filter((d) => d.category === category);
        if (overlays.length === 0) return null;
        return (
          <div key={category} className="space-y-2">
            <div className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
              {CATEGORY_META[category].title}
            </div>
            <div className="space-y-2">
              {overlays.map((def) => {
                const config = prefs.items[def.id];
                return (
                  <div
                    key={def.id}
                    className="flex flex-col justify-between gap-3 py-1.5 sm:flex-row sm:items-center"
                  >
                    <div className="min-w-0 space-y-0.5">
                      <Label className="text-sm font-medium">{def.label}</Label>
                      <p className="text-muted-foreground text-xs">{def.description}</p>
                    </div>
                    <div className="flex items-center gap-2">
                      <Select
                        value={config.position}
                        disabled={!config.enabled}
                        onValueChange={(pos) =>
                          updateItem(def.id, { position: pos as OverlayPosition })
                        }
                      >
                        <SelectTrigger className="w-[130px]">
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
                        checked={config.enabled}
                        onCheckedChange={(checked) => updateItem(def.id, { enabled: checked })}
                      />
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}

export default function OverlaySettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });

  if (form.isLoading) return <div>Loading...</div>;

  const overlaysEnabled = form.getValue("overlays.enabled") !== "false";
  const defaultsValue = form.getValue("defaults.card_overlays");
  const previewPrefs = parseOverlayPrefs(
    defaultsValue || serializeOverlayPrefs(buildDefaultPrefs()),
  );

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Card Overlays</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure the default overlay badges and style preset shown on poster cards. Users can
          override these in their personal settings.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="General">
          <SettingField
            label="Card Overlays Enabled"
            hint="When disabled, no overlay badges appear for any user regardless of their personal settings."
            type="toggle"
            value={form.getValue("overlays.enabled") || "true"}
            onChange={(v) => form.setValue("overlays.enabled", v)}
          />
        </FieldGroup>

        <FieldGroup label="Default Configuration">
          <div className={overlaysEnabled ? "" : "pointer-events-none opacity-50"}>
            <p className="text-muted-foreground mb-4 text-xs">
              These defaults apply to users who have not customized their overlay settings.
            </p>
            <div className="flex flex-col gap-6 lg:flex-row">
              <div className="flex-1">
                <DefaultsEditor
                  value={defaultsValue || serializeOverlayPrefs(buildDefaultPrefs())}
                  onChange={(v) => form.setValue("defaults.card_overlays", v)}
                />
              </div>
              <div className="flex items-start justify-center lg:w-[180px]">
                <OverlayPreviewCard prefs={previewPrefs} size="sm" variant="movie" />
              </div>
            </div>
          </div>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={false}
      />
    </div>
  );
}
