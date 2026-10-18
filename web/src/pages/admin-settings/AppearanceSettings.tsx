import { useMemo, useState } from "react";
import { AlertTriangle, Check, RotateCcw } from "lucide-react";

import { BrandingAssetField } from "@/components/admin/BrandingAssetField";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  OverlayPreviewCard,
  type OverlayPreviewVariant,
} from "@/components/overlays/OverlayPreviewCard";
import { OverlayPreviewVariantToggle } from "@/components/overlays/OverlayPreviewVariantToggle";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useBranding } from "@/hooks/useBranding";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useShownRatingSources } from "@/hooks/queries/ratingsCapability";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import {
  buildDefaultPrefs,
  CATEGORY_META,
  OVERLAY_CATEGORIES,
  OVERLAY_PRESETS,
  OVERLAY_REGISTRY,
  isOverlayOffered,
  POSITION_OPTIONS,
  PRESET_IDS,
  parseOverlayPrefs,
  serializeOverlayPrefs,
  type CardOverlayPrefs,
  type OverlayId,
  type OverlayPosition,
  type PresetId,
} from "@/lib/overlays";
import { cn } from "@/lib/utils";
import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "@/components/SaveBar";
import { SETTINGS_CONTROL_WIDTH, SettingField, SettingFieldRow } from "./SettingField";

const IMAGE_ACCEPT = "image/png,image/jpeg,image/webp";
const FAVICON_ACCEPT =
  "image/png,image/x-icon,image/vnd.microsoft.icon,image/svg+xml,image/webp,.ico";

const ACCENT_PRESETS = [
  "#4f46e5",
  "#0ea5e9",
  "#10b981",
  "#f59e0b",
  "#ef4444",
  "#ec4899",
  "#8b5cf6",
  "#64748b",
];

const ACCENT_KEY = "branding.accent_color";
const OVERLAYS_ENABLED_KEY = "overlays.enabled";
const OVERLAY_DEFAULTS_KEY = "defaults.card_overlays";

/**
 * Every appearance key the page stages, saved as one batch by the shared
 * SaveBar. Asset uploads keep their own upload/delete mutations because a file
 * picker has no draft to batch.
 *
 * Server name and login subtitle deliberately live on the General page: they are
 * server identity, not look and feel.
 */
const OVERLAY_KEYS = [OVERLAYS_ENABLED_KEY, OVERLAY_DEFAULTS_KEY];
const KEYS = [ACCENT_KEY, ...OVERLAY_KEYS];

/**
 * Silo's built-in overlay configuration, derived from the registry's own
 * per-overlay defaults rather than copied out into a literal here, so an
 * overlay added to the registry is covered without touching this page.
 */
const BUILT_IN_OVERLAY_DEFAULTS = serializeOverlayPrefs(buildDefaultPrefs());

export default function AppearanceSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const branding = useBranding();
  const restartKeys = useRestartKeys();
  const shownRatingSources = useShownRatingSources();

  const [confirmRestoreOverlaysOpen, setConfirmRestoreOverlaysOpen] = useState(false);
  // Which sample the badge preview stands in for. View state only — show-only
  // overlays (network, show status) are otherwise impossible to see here.
  const [previewVariant, setPreviewVariant] = useState<OverlayPreviewVariant>("movie");

  const accentColor = form.getValue(ACCENT_KEY);
  const customAccentActive =
    Boolean(accentColor) &&
    !ACCENT_PRESETS.some((hex) => hex.toLowerCase() === accentColor.toLowerCase());

  // s3.public_bucket is not staged here, but getValue falls back to the full
  // settings response so the uploads can still be gated on it.
  const s3Configured = Boolean(form.getValue("s3.public_bucket"));
  const assetStorageAvailable = branding.storageAvailable;

  const overlaysEnabled = form.getValue(OVERLAYS_ENABLED_KEY) !== "false";
  const overlayPrefs = parseOverlayPrefs(
    form.getValue(OVERLAY_DEFAULTS_KEY) || BUILT_IN_OVERLAY_DEFAULTS,
  );
  const overlayDefaultsAreBuiltIn =
    serializeOverlayPrefs(overlayPrefs) === BUILT_IN_OVERLAY_DEFAULTS;

  const setOverlayPrefs = (next: CardOverlayPrefs) =>
    form.setValue(OVERLAY_DEFAULTS_KEY, serializeOverlayPrefs(next));

  const updateOverlayItem = (
    id: OverlayId,
    patch: Partial<CardOverlayPrefs["items"][OverlayId]>,
  ) => {
    setOverlayPrefs({
      ...overlayPrefs,
      items: { ...overlayPrefs.items, [id]: { ...overlayPrefs.items[id], ...patch } },
    });
  };

  // Restoring stages the built-in document like any other edit rather than
  // writing it: the admin still confirms the whole batch through the SaveBar,
  // and Discard puts the previous defaults back.
  const restoreOverlayDefaults = () => {
    setConfirmRestoreOverlaysOpen(false);
    form.setValue(OVERLAY_DEFAULTS_KEY, BUILT_IN_OVERLAY_DEFAULTS);
  };

  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));

  if (form.isLoading) return <div>Loading...</div>;

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Appearance" />

      <div className="flex-1 space-y-5">
        <FieldGroup label="Logos and icons">
          {!assetStorageAvailable && (
            <div className="mt-3 flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
              <p className="text-muted-foreground text-[13px] leading-relaxed">
                {s3Configured ? (
                  <>Restart the server to finish enabling image uploads.</>
                ) : (
                  <>
                    Image uploads need artwork storage (local disk or S3), set in{" "}
                    <span className="text-foreground font-medium">Storage</span> settings.
                  </>
                )}
              </p>
            </div>
          )}

          <div className="space-y-2 py-3.5">
            <BrandingAssetField
              label="Logo (wordmark)"
              description="Shown in the expanded sidebar."
              kind="wordmark"
              currentUrl={branding.wordmarkUrl}
              accept={IMAGE_ACCEPT}
              enabled={assetStorageAvailable}
              preview="wide"
            />
            <BrandingAssetField
              label="Logo (icon)"
              description="Shown in the collapsed sidebar and the installed app."
              kind="mark"
              currentUrl={branding.markUrl}
              accept={IMAGE_ACCEPT}
              enabled={assetStorageAvailable}
              preview="square"
            />
            <BrandingAssetField
              label="Favicon"
              description="Shown in the browser tab."
              kind="favicon"
              currentUrl={branding.faviconUrl}
              accept={FAVICON_ACCEPT}
              enabled={assetStorageAvailable}
              preview="square"
            />
            <BrandingAssetField
              label="Login background"
              description="Shown on the login and signup pages."
              kind="login_bg"
              currentUrl={branding.loginBgUrl}
              accept={IMAGE_ACCEPT}
              enabled={assetStorageAvailable}
              preview="wide"
            />
          </div>
        </FieldGroup>

        <FieldGroup label="Colors" restartAll={restartKeys.has(ACCENT_KEY)}>
          <SettingFieldRow
            label="Accent color"
            description="Recolors buttons, focus outlines, and the sidebar."
          >
            <div className="flex flex-wrap items-center justify-end gap-2 sm:max-w-[300px]">
              {/* Exactly one control in this row is always marked selected:
                  the Default swatch when no accent is stored, the matching
                  preset when one is, or the Custom chip when the stored hex
                  matches no preset. */}
              <button
                type="button"
                onClick={() => form.setValue(ACCENT_KEY, "")}
                aria-label="Use theme default accent"
                aria-pressed={!accentColor}
                title="Theme default"
                className={cn(
                  "bg-muted text-muted-foreground relative grid h-8 w-8 place-items-center rounded-full border transition-transform hover:scale-110",
                  !accentColor
                    ? "border-foreground ring-foreground ring-offset-background ring-2 ring-offset-2"
                    : "border-border",
                )}
              >
                <RotateCcw className="h-3.5 w-3.5" />
              </button>
              {ACCENT_PRESETS.map((hex) => {
                const selected = accentColor.toLowerCase() === hex.toLowerCase();
                return (
                  <button
                    key={hex}
                    type="button"
                    onClick={() => form.setValue(ACCENT_KEY, hex)}
                    aria-label={`Use accent ${hex}`}
                    aria-pressed={selected}
                    className={cn(
                      "relative h-8 w-8 rounded-full border transition-transform hover:scale-110",
                      selected
                        ? "border-foreground ring-foreground ring-offset-background ring-2 ring-offset-2"
                        : "border-border",
                    )}
                    style={{ backgroundColor: hex }}
                  >
                    {selected && (
                      <Check className="absolute inset-0 m-auto h-4 w-4 text-white drop-shadow" />
                    )}
                  </button>
                );
              })}
              <label
                className={cn(
                  "inline-flex h-8 cursor-pointer items-center gap-2 rounded-lg border px-2.5 text-xs font-medium",
                  customAccentActive
                    ? "border-foreground ring-foreground ring-offset-background ring-1 ring-offset-1"
                    : "border-border",
                )}
              >
                <input
                  type="color"
                  aria-label="Custom accent color"
                  value={accentColor || "#4f46e5"}
                  onChange={(e) => form.setValue(ACCENT_KEY, e.target.value)}
                  className="h-5 w-5 cursor-pointer border-0 bg-transparent p-0"
                />
                Custom
                {customAccentActive && <Check className="h-3.5 w-3.5" />}
              </label>
            </div>
          </SettingFieldRow>
        </FieldGroup>

        <FieldGroup
          label="Card overlays"
          restartAll={allRestart(OVERLAY_KEYS)}
          actions={
            <button
              type="button"
              onClick={() => setConfirmRestoreOverlaysOpen(true)}
              disabled={overlayDefaultsAreBuiltIn}
              className="text-muted-foreground hover:text-destructive inline-flex items-center gap-1.5 text-xs font-medium transition-colors disabled:pointer-events-none disabled:opacity-40"
            >
              <RotateCcw className="h-3 w-3" aria-hidden="true" />
              Restore defaults
            </button>
          }
        >
          <SettingField
            label="Show badges on poster art"
            description="The default for every profile. A profile that turns badges on or off for itself keeps its own choice."
            type="toggle"
            value={form.getValue(OVERLAYS_ENABLED_KEY) || "true"}
            onChange={(v) => form.setValue(OVERLAYS_ENABLED_KEY, v)}
            restartRequired={restartKeys.has(OVERLAYS_ENABLED_KEY)}
          />

          {/* Row and preview are one list child so the hairline falls after
              the pair, not between them. The preview gets a full-width framed
              strip instead of squatting in the row's control column. */}
          <div
            inert={!overlaysEnabled}
            className={overlaysEnabled ? undefined : "pointer-events-none opacity-50"}
          >
            <SettingFieldRow
              label="Badge style"
              description="Default for people who have not chosen their own."
            >
              <Select
                value={overlayPrefs.preset}
                onValueChange={(v) => setOverlayPrefs({ ...overlayPrefs, preset: v as PresetId })}
              >
                <SelectTrigger className={SETTINGS_CONTROL_WIDTH} aria-label="Badge style">
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
            </SettingFieldRow>
            <div className="border-border/70 bg-foreground/[0.02] mb-4 rounded-xl border p-3">
              <div className="flex items-center justify-between gap-3 px-1">
                <span className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
                  Preview
                </span>
                <OverlayPreviewVariantToggle value={previewVariant} onChange={setPreviewVariant} />
              </div>
              <div className="mt-3 flex justify-center pb-1">
                <OverlayPreviewCard prefs={overlayPrefs} size="sm" variant={previewVariant} />
              </div>
            </div>
          </div>

          <div
            inert={!overlaysEnabled}
            className={cn(overlaysEnabled ? "" : "pointer-events-none opacity-50")}
          >
            {OVERLAY_CATEGORIES.map((category) => {
              const overlays = OVERLAY_REGISTRY.filter(
                (d) => d.category === category && isOverlayOffered(d, shownRatingSources),
              );
              if (overlays.length === 0) return null;
              return (
                <div key={category} className="min-w-0">
                  <SettingsSubheading>{CATEGORY_META[category].title}</SettingsSubheading>
                  <div className="settings-field-list">
                    {overlays.map((def) => {
                      const config = overlayPrefs.items[def.id];
                      return (
                        <SettingFieldRow
                          key={def.id}
                          label={def.label}
                          description={def.description}
                        >
                          <Select
                            value={config.position}
                            disabled={!config.enabled}
                            onValueChange={(pos) =>
                              updateOverlayItem(def.id, { position: pos as OverlayPosition })
                            }
                          >
                            <SelectTrigger className="w-[130px]" aria-label={`${def.label} corner`}>
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
                            aria-label={`Show ${def.label}`}
                            onCheckedChange={(checked) =>
                              updateOverlayItem(def.id, { enabled: checked })
                            }
                          />
                        </SettingFieldRow>
                      );
                    })}
                  </div>
                </div>
              );
            })}
          </div>
        </FieldGroup>
      </div>

      <ConfirmDialog
        open={confirmRestoreOverlaysOpen}
        onOpenChange={setConfirmRestoreOverlaysOpen}
        title="Restore badge defaults"
        description="Replace the server-wide badge defaults with Silo's built-in configuration. The badge on/off switch is left alone, and nothing is written until you save."
        confirmLabel="Restore"
        variant="destructive"
        onConfirm={restoreOverlayDefaults}
      />

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
