import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
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
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import { showWatchlistAutoRequestControl } from "@/lib/watchlistTitles";

const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };
const AUTO_REQUEST_KEY = SETTING_KEYS.REQUESTS_WATCHLIST_AUTO_REQUEST;
const AUTO_REQUEST_DEFINITION = SETTING_DEFINITIONS[AUTO_REQUEST_KEY];

/** The profile's request preferences: whether adding to the watchlist also requests. */
export default function RequestsSettings() {
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
    return <div className="text-muted-foreground pt-4">Loading request settings...</div>;
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
      toast.success("Request preference saved");
    } catch {
      toast.error("Failed to save request preference");
    } finally {
      // The status endpoint folds this preference into watchlist_requests.
      void queryClient.invalidateQueries({ queryKey: requestKeys.status() });
    }
  }

  return (
    <div className="space-y-6">
      <SettingsGroup
        title="Watchlist"
        description="What happens when you add a title the library doesn't have yet."
      >
        {showControl ? (
          <SettingRow
            label={AUTO_REQUEST_DEFINITION.label}
            description={AUTO_REQUEST_DEFINITION.description}
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
              ? "Requests are turned off on this server."
              : canRequest
                ? "Adding a title to your watchlist doesn’t request it on this server. You can still request titles from Discover."
                : "Adding a title to your watchlist doesn’t request it on this server."}
          </p>
        )}
      </SettingsGroup>
    </div>
  );
}
