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
        title="Autoscan"
        subtitle="Silo re-scans a library as soon as something changes, instead of waiting for the next scheduled scan. Add a source for each thing you want watched."
        actions={
          <>
            {settings.data &&
              (enabled ? (
                <Badge variant="secondary">Enabled</Badge>
              ) : (
                <Badge variant="outline" className="text-muted-foreground">
                  Disabled
                </Badge>
              ))}
            <div className="flex items-center gap-2">
              <Label htmlFor="autoscan-enabled" className="text-muted-foreground text-sm">
                Autoscan
              </Label>
              <Switch
                id="autoscan-enabled"
                checked={enabled}
                onCheckedChange={toggleEnabled}
                disabled={!settings.data || updateSettings.isPending}
                aria-label="Enable autoscan"
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
                {trigger.isPending ? "Triggering…" : "Run now"}
              </Button>
            ) : null}
          </>
        }
      />

      <SourcesPanel />
    </div>
  );
}
