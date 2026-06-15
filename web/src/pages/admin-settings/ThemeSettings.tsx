import { useCallback, useEffect, useRef, useState } from "react";
import { AlertTriangle, RotateCcw } from "lucide-react";
import { useAdminServerSettings, useUpdateServerSetting } from "@/hooks/queries/admin/settings";
import { TokenEditor } from "@/components/theme/TokenEditor";
import { RawCssEditor } from "@/components/theme/RawCssEditor";
import { ThemePreviewCard } from "@/components/theme/ThemePreviewCard";
import { parseVarsJson } from "@/lib/themeExport";
import { sanitizeCss } from "@/lib/cssSanitizer";
import type { ThemeToken } from "@/lib/themeTokens";
import type { ThemeVarOverrides } from "@/hooks/useCustomTheme";

export default function ThemeSettings() {
  const { data: settings } = useAdminServerSettings();
  const updateSetting = useUpdateServerSetting();

  const [vars, setVars] = useState<ThemeVarOverrides>({});
  const [rawCss, setRawCss] = useState("");
  const [catalogUrl, setCatalogUrl] = useState("");

  // Only seed local state once from the first server response
  const seededRef = useRef(false);
  useEffect(() => {
    if (settings && !seededRef.current) {
      seededRef.current = true;
      setVars(parseVarsJson(settings["ui.admin_theme_vars"]));
      setRawCss(settings["ui.admin_custom_css"] ?? "");
      setCatalogUrl(
        settings["theme.catalog_url"] ??
          "https://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json",
      );
    }
  }, [settings]);

  // Debounce timers
  const varsTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const cssTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const persistVars = useCallback(
    (newVars: ThemeVarOverrides) => {
      updateSetting.mutate({ key: "ui.admin_theme_vars", value: JSON.stringify(newVars) });
    },
    [updateSetting],
  );

  const handleSetVar = useCallback(
    (token: ThemeToken, value: string) => {
      setVars((prev) => {
        const next = { ...prev, [token]: value };
        clearTimeout(varsTimerRef.current);
        varsTimerRef.current = setTimeout(() => persistVars(next), 500);
        return next;
      });
    },
    [persistVars],
  );

  const handleResetVar = useCallback(
    (token: ThemeToken) => {
      setVars((prev) => {
        const next = { ...prev };
        delete next[token];
        persistVars(next);
        return next;
      });
    },
    [persistVars],
  );

  const handleCssChange = useCallback(
    (css: string) => {
      setRawCss(css);
      clearTimeout(cssTimerRef.current);
      cssTimerRef.current = setTimeout(() => {
        updateSetting.mutate({ key: "ui.admin_custom_css", value: sanitizeCss(css) });
      }, 1000);
    },
    [updateSetting],
  );

  const handleCatalogUrlBlur = useCallback(() => {
    updateSetting.mutate({ key: "theme.catalog_url", value: catalogUrl });
  }, [updateSetting, catalogUrl]);

  const handleResetAll = useCallback(() => {
    setVars({});
    setRawCss("");
    persistVars({});
    updateSetting.mutate({ key: "ui.admin_custom_css", value: "" });
  }, [persistVars, updateSetting]);

  const hasOverrides = Object.keys(vars).length > 0 || rawCss.length > 0;

  return (
    <div className="space-y-6">
      <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-4">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
        <div className="text-[13px] leading-relaxed">
          <p className="font-medium text-amber-500">Server-wide theme customization</p>
          <p className="text-muted-foreground mt-1">
            These overrides apply to all users as a base layer. Individual users can further
            customize on top of these settings.
          </p>
        </div>
      </div>

      {/* Preview */}
      <div>
        <h4 className="mb-2 text-sm font-medium">Preview</h4>
        <ThemePreviewCard vars={vars} />
        {hasOverrides && (
          <div className="mt-2 flex justify-end">
            <button
              type="button"
              onClick={handleResetAll}
              className="text-muted-foreground hover:text-destructive inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              <RotateCcw className="h-3 w-3" />
              Reset all
            </button>
          </div>
        )}
      </div>

      {/* Token editor */}
      <div>
        <h4 className="mb-2 text-sm font-medium">Token Overrides</h4>
        <TokenEditor vars={vars} onSetVar={handleSetVar} onResetVar={handleResetVar} />
      </div>

      {/* Raw CSS */}
      <div>
        <h4 className="mb-2 text-sm font-medium">Custom CSS</h4>
        <RawCssEditor value={rawCss} onChange={handleCssChange} />
      </div>

      {/* Catalog URL */}
      <div>
        <h4 className="mb-2 text-sm font-medium">Theme Catalog URL</h4>
        <p className="text-muted-foreground mb-2 text-[13px]">
          URL of the community theme catalog JSON index. Users browse this in their settings.
        </p>
        <input
          type="url"
          value={catalogUrl}
          onChange={(e) => setCatalogUrl(e.target.value)}
          onBlur={handleCatalogUrlBlur}
          className="border-border bg-background text-foreground focus:ring-ring w-full rounded-xl border px-3 py-2 text-sm focus:ring-2 focus:outline-none"
          placeholder="https://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json"
        />
      </div>
    </div>
  );
}
