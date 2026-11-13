import { toast } from "sonner";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { SettingRow } from "@/components/settings/SettingRow";
import { Switch } from "@/components/ui/switch";
import { useReportGate } from "@/hooks/queries/markerReportSettings";
import { usePluginSettingsValues, useUpdatePluginEntry } from "@/hooks/queries/pluginSettings";
import { useT } from "@/i18n";
import { entryBoolean, readPluginEntryObject } from "@/lib/pluginEntryValues";

/** The per-account settings entry the switch writes. */
const REPORT_SETTING_KEY = "report";

/**
 * Per-account settings for TheIntroDB's reporting feature.
 *
 * There is no key field on purpose: a report is sent with the server's key, which
 * the plugin holds and never hands to a browser. What the viewer owns is the
 * switch.
 *
 * Nothing renders when the plugin is not installed, or when it predates the
 * `report` entry. When the plugin is there but the server holds no key, the row
 * stays visible and off, because that is something the viewer can act on.
 */
export function TheIntroDbReportSettings() {
  const t = useT();
  const gate = useReportGate();
  const settings = usePluginSettingsValues(gate.installationId, {
    enabled: gate.declaresReport && gate.serverConfigured,
  });
  const update = useUpdatePluginEntry(gate.installationId);

  if (!gate.installationId || !gate.declaresReport) return null;

  const values = settings.data;
  // The write replaces the whole stored set for this account and plugin, so the control
  // is only offered once the current set is known; the write itself then re-reads and
  // merges onto that read, so a change another tab made in the meantime survives.
  const loaded = settings.isSuccess && values !== undefined;

  const entry = readPluginEntryObject(values, REPORT_SETTING_KEY);
  const showReport = entryBoolean(entry.show_report);

  const setShowReport = (checked: boolean) => {
    if (!loaded) return;
    update.setEntryValue(
      REPORT_SETTING_KEY,
      { show_report: checked },
      {
        onSuccess: () =>
          toast.success(
            t(
              checked
                ? "web.settings.playback.theintrodb.enabled"
                : "web.settings.playback.theintrodb.hidden",
            ),
          ),
        onError: () => toast.error(t("web.settings.playback.theintrodb.saveFailed")),
      },
    );
  };

  return (
    <SettingsGroup
      title={t("web.settings.playback.theintrodb.title")}
      description={t("web.settings.playback.theintrodb.description")}
    >
      <SettingRow
        label={t("web.settings.playback.theintrodb.label")}
        description={
          settings.isError
            ? t("web.settings.playback.theintrodb.loadFailed")
            : gate.serverConfigured
              ? t("web.settings.playback.theintrodb.hint")
              : t("web.settings.playback.theintrodb.unavailable")
        }
        control={(id) => (
          <Switch
            id={id}
            checked={showReport}
            disabled={!gate.serverConfigured || !loaded || update.isPending}
            onCheckedChange={setShowReport}
          />
        )}
      />
    </SettingsGroup>
  );
}
