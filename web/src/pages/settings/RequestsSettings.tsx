import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { PageHeader } from "@/components/page/PageHeader";
import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Switch } from "@/components/ui/switch";
import { requestKeys } from "@/hooks/queries/keys";
import {
  isSettingValueMissing,
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
  type SettingIdentity,
} from "@/hooks/queries/settingValues";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useT } from "@/i18n";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import { settingDescription, settingLabel } from "@/lib/settingsText";
import { showWatchlistAutoRequestControl } from "@/lib/watchlistTitles";

const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };
const AUTO_REQUEST_KEY = SETTING_KEYS.REQUESTS_WATCHLIST_AUTO_REQUEST;
const AUTO_REQUEST_DEFINITION = SETTING_DEFINITIONS[AUTO_REQUEST_KEY];

/** The profile's request preferences: whether adding to the watchlist also requests. */
export default function RequestsSettings() {
  const t = useT();
  const queryClient = useQueryClient();
  const featureStatus = useRequestFeatureStatus();
  const { data: effective, isLoading: settingsLoading } = useEffectiveSettings({
    keys: [AUTO_REQUEST_KEY],
  });
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  // The contract default is on; only an explicit false opts the profile out.
  const autoRequest = effective?.[AUTO_REQUEST_KEY]?.value !== false;
  const pending = setValue.isPending || clearValue.isPending;

  if (featureStatus.isLoading || settingsLoading) {
    return <div className="text-muted-foreground pt-4">{t("web.settings.requests.loading")}</div>;
  }

  const status = featureStatus.data;
  const showControl = showWatchlistAutoRequestControl(status, autoRequest);
  const canRequest = status?.requests_enabled === true && status.allowed === true;

  async function handleChange(checked: boolean) {
    try {
      if (checked) {
        // Clear the profile value so it inherits the default (on) again.
        try {
          await clearValue.mutateAsync({ key: AUTO_REQUEST_KEY, identity: PROFILE_SCOPE });
        } catch (error) {
          if (!isSettingValueMissing(error)) throw error;
        }
      } else {
        await setValue.mutateAsync({
          key: AUTO_REQUEST_KEY,
          value: false,
          identity: PROFILE_SCOPE,
        });
      }
      toast.success(t("web.settings.requests.saved"));
    } catch {
      toast.error(t("web.settings.requests.saveFailed"));
    } finally {
      // The status endpoint folds this preference into watchlist_requests.
      void queryClient.invalidateQueries({ queryKey: requestKeys.status() });
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.requests.title")}
        subtitle={t("web.settings.requests.subtitle")}
      />
      <SettingsGroup
        title={t("web.settings.requests.watchlistTitle")}
        description={t("web.settings.requests.watchlistDescription")}
      >
        {showControl ? (
          <SettingRow
            label={settingLabel(AUTO_REQUEST_DEFINITION)}
            description={settingDescription(AUTO_REQUEST_DEFINITION)}
            control={(id) => (
              <Switch
                id={id}
                checked={autoRequest}
                disabled={pending}
                onCheckedChange={handleChange}
              />
            )}
          />
        ) : (
          <p className="text-muted-foreground text-sm" data-testid="watchlist-auto-request-off">
            {status?.requests_enabled !== true
              ? t("web.settings.requests.disabledOnServer")
              : canRequest
                ? t("web.settings.requests.autoRequestOffCanRequest")
                : t("web.settings.requests.autoRequestOff")}
          </p>
        )}
      </SettingsGroup>
    </div>
  );
}
