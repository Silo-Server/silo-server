import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import { Navigate, useLocation } from "react-router";
import { Play } from "lucide-react";
import { PageHeader } from "@/components/page/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  useAutoscanSettings,
  useAutoscanSources,
  useTriggerAutoscan,
  useUpdateAutoscanSettings,
} from "@/hooks/queries/useAutoscan";
import SourcesPanel from "@/pages/admin/autoscan/SourcesPanel";
import { showRunNow } from "@/pages/admin/autoscan/runNow";
import { legacyAutoscanPageTarget } from "@/pages/autoscanSearchParams";
import { useT } from "@/i18n";

/**
 * Autoscan › Sources: what Autoscan watches, with the switch that turns it on
 * and Run now. Activity and the polling defaults are their own pages under the
 * Autoscan topic.
 */
export default function AdminAutoscan() {
  const { search } = useLocation();
  const legacyTarget = legacyAutoscanPageTarget(search);
  if (legacyTarget) return <Navigate to={legacyTarget} replace />;
  return <AutoscanSourcesPage />;
}

function AutoscanSourcesPage() {
  const t = useT();
  const trigger = useTriggerAutoscan();
  const settings = useAutoscanSettings();
  const readAuthority = captureProfileRequestContext();
  const updateSettings = useUpdateAutoscanSettings();

  const enabled = settings.data?.enabled ?? false;

  // Run now polls every enabled polling source at once, so hide it when
  // Autoscan is off or no loaded source polls.
  const sources = useAutoscanSources();
  const runNowVisible = showRunNow(sources.data, settings.data);

  function toggleEnabled(checked: boolean) {
    if (!settings.data || !readAuthority || !isCapturedProfileAuthorityActive(readAuthority))
      return;
    updateSettings.mutate({ ...settings.data, enabled: checked });
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.admin.autoscan.page.title")}
        subtitle={t("web.admin.autoscan.page.subtitle")}
        actions={
          <>
            {settings.data &&
              (enabled ? (
                <Badge variant="secondary">{t("web.admin.autoscan.page.enabled")}</Badge>
              ) : (
                <Badge variant="outline" className="text-muted-foreground">
                  {t("web.admin.autoscan.page.disabled")}
                </Badge>
              ))}
            <div className="flex items-center gap-2">
              <Label htmlFor="autoscan-enabled" className="text-muted-foreground text-sm">
                {t("web.admin.autoscan.page.switchLabel")}
              </Label>
              <Switch
                id="autoscan-enabled"
                checked={enabled}
                onCheckedChange={toggleEnabled}
                disabled={!settings.data || updateSettings.isPending}
                aria-label={t("web.admin.autoscan.page.enableAria")}
              />
            </div>
            {runNowVisible ? (
              <Button
                variant="outline"
                size="sm"
                disabled={trigger.isPending}
                onClick={() => trigger.mutate()}
              >
                <Play />
                {trigger.isPending
                  ? t("web.admin.autoscan.page.triggering")
                  : t("web.admin.autoscan.page.runNow")}
              </Button>
            ) : null}
          </>
        }
      />

      <SourcesPanel />
    </div>
  );
}
