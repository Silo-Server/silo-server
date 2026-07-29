import { useMemo } from "react";
import { AlertTriangle, Check, RotateCcw } from "lucide-react";

import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useBranding } from "@/hooks/useBranding";
import { BrandingAssetField } from "@/components/admin/BrandingAssetField";
import { parseVarsJson } from "@/lib/themeExport";
import { ACCENT_TOKENS, accentColorToTokens } from "@/lib/accentMapping";
import { THEME_IDS, THEMES } from "@/lib/themes";
import { cn } from "@/lib/utils";
import { SaveBar } from "./SaveBar";

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

// Text identity, accent color, and default theme are staged locally and persist
// together on Save. Accent writes both the manifest/meta color
// (branding.accent_color) and the theme token overrides (ui.admin_theme_vars),
// so both keys are tracked here. None of these require a restart — the server
// reads them live, and useUpdateServerSetting refreshes the public theme caches
// on save so changes apply immediately. Asset uploads (below) keep their own
// immediate upload/delete mutations: a file picker has no draft to batch.
const KEYS = [
  "branding.server_name",
  "branding.login_subtitle",
  "branding.accent_color",
  "branding.default_theme",
  "ui.admin_theme_vars",
];

export default function BrandingSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const branding = useBranding();

  const accentColor = form.getValue("branding.accent_color");
  const defaultTheme = form.getValue("branding.default_theme");
  // s3.public_bucket is not managed here, but getValue falls back to the full
  // settings response so we can still gate the asset uploads on it.
  const s3Configured = Boolean(form.getValue("s3.public_bucket"));
  const assetStorageAvailable = branding.storageAvailable;

  // Accent recolors the primary action color, focus ring, and sidebar accent
  // (ACCENT_TOKENS). It merges into any overrides set via the Theming tab so
  // they are not clobbered, reading the staged value so repeated picks compound
  // correctly.
  const applyAccent = (hex: string) => {
    const existing = parseVarsJson(form.getValue("ui.admin_theme_vars"));
    const merged = { ...existing, ...accentColorToTokens(hex) };
    form.setValue("ui.admin_theme_vars", JSON.stringify(merged));
    form.setValue("branding.accent_color", hex);
  };

  const clearAccent = () => {
    const existing = parseVarsJson(form.getValue("ui.admin_theme_vars"));
    for (const token of ACCENT_TOKENS) {
      delete existing[token];
    }
    form.setValue("ui.admin_theme_vars", JSON.stringify(existing));
    form.setValue("branding.accent_color", "");
  };

  const selectDefaultTheme = (id: string) => form.setValue("branding.default_theme", id);

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 space-y-8">
        {/* Identity */}
        <section className="space-y-3">
          <div>
            <h4 className="text-sm font-medium">Identity</h4>
            <p className="text-muted-foreground mt-1 text-[13px]">
              Your server name appears in the browser tab, on the login page, in the sidebar, and in
              the installed app. Leave blank for defaults.
            </p>
          </div>
          <div className="space-y-3">
            <div>
              <label className="text-muted-foreground mb-1 block text-xs font-medium">
                Server Name
              </label>
              <input
                type="text"
                value={form.getValue("branding.server_name")}
                onChange={(e) => form.setValue("branding.server_name", e.target.value)}
                className="border-border bg-background text-foreground focus:ring-ring w-full rounded-xl border px-3 py-2 text-sm focus:ring-2 focus:outline-none"
                placeholder="Silo"
              />
            </div>
            <div>
              <label className="text-muted-foreground mb-1 block text-xs font-medium">
                Login Page Subtitle
              </label>
              <input
                type="text"
                value={form.getValue("branding.login_subtitle")}
                onChange={(e) => form.setValue("branding.login_subtitle", e.target.value)}
                className="border-border bg-background text-foreground focus:ring-ring w-full rounded-xl border px-3 py-2 text-sm focus:ring-2 focus:outline-none"
                placeholder="Sign in with an existing account."
              />
            </div>
          </div>
        </section>

        {/* Logos & icons */}
        <section className="space-y-3">
          <div>
            <h4 className="text-sm font-medium">Logos &amp; Icons</h4>
            <p className="text-muted-foreground mt-1 text-[13px]">
              Upload custom images to replace the Silo logo, browser favicon, and login background.
              Each falls back to the Silo default when not set.
            </p>
          </div>

          {!assetStorageAvailable && (
            <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
              <p className="text-muted-foreground text-[13px] leading-relaxed">
                {s3Configured ? (
                  <>
                    The public bucket is saved, but object storage is not active in this process
                    yet. Restart the server to enable image uploads.
                  </>
                ) : (
                  <>
                    Image uploads require S3 object storage. Configure a public bucket in{" "}
                    <span className="text-foreground font-medium">Storage</span> settings, then
                    restart the server.
                  </>
                )}
              </p>
            </div>
          )}

          <div className="space-y-2">
            <BrandingAssetField
              label="Logo (wordmark)"
              description="Wide logo shown in the expanded sidebar."
              kind="wordmark"
              currentUrl={branding.wordmarkUrl}
              accept={IMAGE_ACCEPT}
              enabled={assetStorageAvailable}
              preview="wide"
            />
            <BrandingAssetField
              label="Logo (icon)"
              description="Square mark shown in the collapsed sidebar and installed app."
              kind="mark"
              currentUrl={branding.markUrl}
              accept={IMAGE_ACCEPT}
              enabled={assetStorageAvailable}
              preview="square"
            />
            <BrandingAssetField
              label="Favicon"
              description="Browser tab icon. PNG, ICO, or SVG."
              kind="favicon"
              currentUrl={branding.faviconUrl}
              accept={FAVICON_ACCEPT}
              enabled={assetStorageAvailable}
              preview="square"
            />
            <BrandingAssetField
              label="Login Background"
              description="Full-bleed background image for the login and signup pages."
              kind="login_bg"
              currentUrl={branding.loginBgUrl}
              accept={IMAGE_ACCEPT}
              enabled={assetStorageAvailable}
              preview="wide"
            />
          </div>
        </section>

        {/* Accent color */}
        <section className="space-y-3">
          <div>
            <h4 className="text-sm font-medium">Brand Accent Color</h4>
            <p className="text-muted-foreground mt-1 text-[13px]">
              A quick way to recolor the primary buttons, focus rings, and sidebar accent. For full
              control, use the Theming tab. Also used as the installed app theme color.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {ACCENT_PRESETS.map((hex) => (
              <button
                key={hex}
                type="button"
                onClick={() => applyAccent(hex)}
                aria-label={`Use accent ${hex}`}
                className={cn(
                  "relative h-8 w-8 rounded-full border transition-transform hover:scale-110",
                  accentColor.toLowerCase() === hex.toLowerCase()
                    ? "border-foreground"
                    : "border-border",
                )}
                style={{ backgroundColor: hex }}
              >
                {accentColor.toLowerCase() === hex.toLowerCase() && (
                  <Check className="absolute inset-0 m-auto h-4 w-4 text-white drop-shadow" />
                )}
              </button>
            ))}
            <label className="border-border ml-1 inline-flex h-8 cursor-pointer items-center gap-2 rounded-lg border px-2.5 text-xs font-medium">
              <input
                type="color"
                value={accentColor || "#4f46e5"}
                onChange={(e) => applyAccent(e.target.value)}
                className="h-5 w-5 cursor-pointer border-0 bg-transparent p-0"
              />
              Custom
            </label>
            {accentColor && (
              <button
                type="button"
                onClick={clearAccent}
                className="text-muted-foreground hover:text-destructive ml-1 inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
              >
                <RotateCcw className="h-3 w-3" />
                Reset
              </button>
            )}
          </div>
        </section>

        {/* Default theme */}
        <section className="space-y-3">
          <div>
            <h4 className="text-sm font-medium">Default Theme</h4>
            <p className="text-muted-foreground mt-1 text-[13px]">
              The base theme new users see until they choose their own. Users can always pick a
              different theme for themselves.
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => selectDefaultTheme("")}
              className={cn(
                "rounded-xl border px-3 py-2 text-xs font-medium transition-colors",
                defaultTheme === ""
                  ? "border-foreground bg-muted/50"
                  : "border-border hover:bg-muted/30",
              )}
            >
              No default
            </button>
            {THEME_IDS.map((id) => (
              <button
                key={id}
                type="button"
                onClick={() => selectDefaultTheme(id)}
                className={cn(
                  "inline-flex items-center gap-2 rounded-xl border px-3 py-2 text-xs font-medium transition-colors",
                  defaultTheme === id
                    ? "border-foreground bg-muted/50"
                    : "border-border hover:bg-muted/30",
                )}
              >
                <span
                  className="h-3.5 w-3.5 rounded-full border border-black/10"
                  style={{ backgroundColor: THEMES[id].previewBg }}
                >
                  <span
                    className="block h-full w-full scale-50 rounded-full"
                    style={{ backgroundColor: THEMES[id].previewAccent }}
                  />
                </span>
                {THEMES[id].label}
              </button>
            ))}
          </div>
        </section>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
      />
    </div>
  );
}
