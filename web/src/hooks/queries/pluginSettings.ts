import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { v2, type V2Body, type V2Result } from "@/api/v2/request";
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

export interface PluginSettingsDetail {
  installation: PluginSettingsInstallation;
  values: V2Result<"GET /api/v2/settings/plugins/{installation_id}">["values"];
}

export type UpdatePluginSettingsBody = V2Body<"PUT /api/v2/settings/plugins/{installation_id}">;

function installationFromV2(
  installation: PluginSettingsInstallationV2,
): PluginSettingsInstallation {
  return { ...installation, id: Number(installation.id) };
}

function detailFromV2(
  detail: V2Result<"GET /api/v2/settings/plugins/{installation_id}">,
): PluginSettingsDetail {
  return { installation: installationFromV2(detail.installation), values: detail.values };
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

export function usePluginSettingsDetail(installationId: number, enabled = true) {
  return useQuery({
    queryKey: settingsKeys.pluginDetail(installationId),
    queryFn: async () =>
      detailFromV2(
        await v2("GET /api/v2/settings/plugins/{installation_id}", {
          path: { installation_id: String(installationId) },
        }),
      ),
    enabled,
    staleTime: 30_000,
  });
}

export function useUpdatePluginSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, body }: { id: number; body: UpdatePluginSettingsBody }) =>
      detailFromV2(
        await v2("PUT /api/v2/settings/plugins/{installation_id}", {
          path: { installation_id: String(id) },
          body,
        }),
      ),
    onSuccess: (_, { id }) => {
      queryClient.invalidateQueries({ queryKey: settingsKeys.plugins() });
      queryClient.invalidateQueries({ queryKey: settingsKeys.pluginDetail(id) });
    },
  });
}
