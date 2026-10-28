import { entryBoolean, readPluginEntryObject } from "@/lib/pluginEntryValues";
import { usePluginInstallation, usePluginSettingsValues } from "./pluginSettings";

/** The marker plugin whose per-account settings drive the Report control. */
const THEINTRODB_PLUGIN_ID = "silo.theintrodb";

/** The per-account settings entry the report control reads. */
const REPORT_SETTING_KEY = "report";

/** The global config entry whose presence makes reporting possible at all. */
const ACCOUNT_GLOBAL_KEY = "account";

/**
 * The installation half of the report gate, shared by the control and the settings
 * screen so the two cannot disagree about when reporting is on offer.
 */
export interface ReportGate {
  /** The installation carrying the setting, or null when the plugin is absent. */
  installationId: number | null;
  /** The installed plugin declares the `report` entry, so a value can be stored. */
  declaresReport: boolean;
  /** The server holds the TheIntroDB key a report is sent with. */
  serverConfigured: boolean;
}

/**
 * Resolve the report feature's installation and what it offers.
 *
 * `declaresReport` is false for an installation predating the entry, and reading
 * values for one would claim an integration that cannot store the setting.
 * `serverConfigured` comes from `configured_global_keys`, which reports WHICH global
 * config entries are set and never their values.
 */
export function useReportGate(): ReportGate {
  const { installation } = usePluginInstallation(THEINTRODB_PLUGIN_ID);

  return {
    installationId: installation?.id ?? null,
    declaresReport: (installation?.user_config_schema ?? []).some(
      (entry) => entry.key === REPORT_SETTING_KEY,
    ),
    serverConfigured: (installation?.configured_global_keys ?? []).includes(ACCOUNT_GLOBAL_KEY),
  };
}

/** Whether a report can be sent at all: an installation that can store the setting, on a server holding a key. */
function gateOffersReporting(gate: ReportGate): boolean {
  return gate.installationId !== null && gate.declaresReport && gate.serverConfigured;
}

export interface MarkerReportSettings {
  /**
   * Whether the integration offers reporting at all: the plugin is installed,
   * declares the per-account setting, and the server holds a TheIntroDB key.
   * False means the control is never drawn, which is the honest answer on a
   * server that cannot report.
   */
  available: boolean;
  /** The viewer turned the control on. */
  enabled: boolean;
  /**
   * The installation serving the report route, for the control to call. Null
   * unless `enabled`: the route is only ever handed to a viewer who asked for the
   * control, so the switch alone decides whether a button is drawn.
   */
  installationId: number | null;
}

/**
 * The Report control's settings for the player.
 *
 * The gate has two halves belonging to different people: the viewer's own toggle,
 * and whether an administrator has configured the server's TheIntroDB key that
 * reports are sent with. Without the key there is nothing to report with, so
 * offering the control would only fail after the viewer had already asked.
 */
export function useMarkerReportSettings(options: { enabled: boolean }): MarkerReportSettings {
  const gate = useReportGate();
  const available = gateOffersReporting(gate);

  const { data: values } = usePluginSettingsValues(gate.installationId, {
    enabled: options.enabled && available,
  });

  if (!available) {
    return { available: false, enabled: false, installationId: null };
  }

  const shown = entryBoolean(readPluginEntryObject(values, REPORT_SETTING_KEY).show_report);

  return {
    available: true,
    enabled: shown,
    installationId: shown ? gate.installationId : null,
  };
}
