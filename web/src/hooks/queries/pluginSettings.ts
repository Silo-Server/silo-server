import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

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

/**
 * The installation a given plugin is installed as, or null when it is not
 * installed at all.
 *
 * A screen addresses an installation by its id, and that id is local to one
 * server — it is whichever position the install landed in, not something a
 * caller can know in advance. Resolving it from the stable `plugin_id` is what
 * lets a screen be written against the plugin rather than against one server's
 * numbering.
 */
export function usePluginInstallation(pluginId: string) {
  const query = usePluginSettingsList();
  const installation =
    query.data?.installations.find((candidate) => candidate.plugin_id === pluginId) ?? null;
  return { ...query, installation };
}

/** The acting account's stored values for one installation. */
export function usePluginSettingsValues(
  installationId: number | null,
  options?: { enabled?: boolean },
) {
  return useQuery({
    queryKey: settingsKeys.pluginDetail(installationId ?? -1),
    enabled: (options?.enabled ?? true) && installationId != null,
    queryFn: async (): Promise<Record<string, string>> => {
      const result = await v2("GET /api/v2/settings/plugins/{installation_id}", {
        path: { installation_id: String(installationId) },
      });
      return result.values ?? {};
    },
    staleTime: 30_000,
  });
}

/**
 * Write the acting account's values for one installation.
 *
 * The endpoint REPLACES the stored set, so the caller passes the whole map:
 * sending only the changed key would clear every other value the account has
 * stored for this plugin. Callers merge the current values with their change.
 */
export function useUpdatePluginSettings(installationId: number | null) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (values: Record<string, string>) => {
      if (installationId == null) {
        throw new Error("Plugin settings are unavailable: no installation resolved.");
      }
      return v2("PUT /api/v2/settings/plugins/{installation_id}", {
        path: { installation_id: String(installationId) },
        body: { values },
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: settingsKeys.pluginDetail(installationId ?? -1),
      });
      void queryClient.invalidateQueries({ queryKey: settingsKeys.plugins() });
    },
  });
}
