import { toast } from "sonner";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { SettingRow } from "@/components/settings/SettingRow";
import { Switch } from "@/components/ui/switch";
import { useReportGate } from "@/hooks/queries/markerReportSettings";
import { usePluginSettingsValues, useUpdatePluginSettings } from "@/hooks/queries/pluginSettings";
import {
  entryBoolean,
  readPluginEntryObject,
  updatePluginEntryObject,
} from "@/lib/pluginEntryValues";

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
  const gate = useReportGate();
  const settings = usePluginSettingsValues(gate.installationId, {
    enabled: gate.declaresReport && gate.serverConfigured,
  });
  const update = useUpdatePluginSettings(gate.installationId);

  if (!gate.installationId || !gate.declaresReport) return null;

  const values = settings.data;
  // The write replaces the whole stored set for this account and plugin, so it may only
  // run once the current set is known. Merging onto an unknown one would send this entry
  // alone and delete every other entry the account has stored.
  const loaded = settings.isSuccess && values !== undefined;

  const entry = readPluginEntryObject(values, REPORT_SETTING_KEY);
  const showReport = entryBoolean(entry.show_report);

  const setShowReport = (checked: boolean) => {
    if (!loaded) return;
    update.mutate(updatePluginEntryObject(values, REPORT_SETTING_KEY, { show_report: checked }), {
      onSuccess: () => toast.success(checked ? "Report button enabled." : "Report button hidden."),
      onError: () => toast.error("Could not change the reporting setting."),
    });
  };

  return (
    <SettingsGroup
      title="TheIntroDB"
      description="Contest a segment TheIntroDB got wrong. Reports are sent using this server's TheIntroDB key."
    >
      <SettingRow
        label="Show a Report button under the skip prompt"
        description={
          settings.isError
            ? "Could not load this account's plugin settings, so this switch stays off until they load."
            : gate.serverConfigured
              ? "Draws a button under the Skip Intro prompt. It asks for confirmation before sending, because a report cannot be taken back from here."
              : "Unavailable: this server has no TheIntroDB API key. An administrator can add one under the plugin's settings."
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
