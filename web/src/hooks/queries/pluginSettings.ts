import { useQuery } from "@tanstack/react-query";

import { v2, type V2Result } from "@/api/v2/request";
import { settingsKeys } from "./keys";

type PluginSettingsInstallationV2 = V2Result<"GET /api/v2/settings/plugins">["items"][number];

/**
 * One installation exposing user settings, as the screens model it. The v2
 * contract renders the installation id as a string; the plugin route helpers
 * and the settings page address installations by number, so the id is
 * converted once here.
 */
export type PluginSettingsInstallation = Omit<PluginSettingsInstallationV2, "id"> & {
  id: number;
};

export interface PluginSettingsList {
  installations: PluginSettingsInstallation[];
}

function installationFromV2(
  installation: PluginSettingsInstallationV2,
): PluginSettingsInstallation {
  return { ...installation, id: Number(installation.id) };
}

export function usePluginSettingsList() {
  return useQuery({
    queryKey: settingsKeys.plugins(),
    queryFn: async (): Promise<PluginSettingsList> => {
      const list = await v2("GET /api/v2/settings/plugins");
      return { installations: list.items.map(installationFromV2) };
    },
    staleTime: 30_000,
  });
}
