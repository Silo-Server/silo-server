import { useSearchParams } from "react-router";

import type { AdminUser } from "@/api/types";
import { useAdminUserDeviceSettings, useAdminUserSettings } from "@/hooks/queries/admin/users";

import { SubTabs } from "../ui";
import { userDetailTabSearch } from "../userDetailTabs";
import { AccountPreferences } from "./AccountPreferences";
import { DevicePreferences } from "./DevicePreferences";

type PreferencesView = "account" | "devices";

function parseView(value: string | null): PreferencesView {
  return value === "devices" ? "devices" : "account";
}

/** Settings the user saved in their apps: account and profiles, or per device. */
export function PreferencesTab({ user }: { user: AdminUser }) {
  const [searchParams, setSearchParams] = useSearchParams();
  const view = parseView(searchParams.get("view"));
  const settings = useAdminUserSettings(user.id);
  const deviceSettings = useAdminUserDeviceSettings(user.id);
  const count = (loading: boolean, n: number) => (loading ? "" : ` · ${n}`);

  const tabs = (
    <SubTabs<PreferencesView>
      ariaLabel="Preferences view"
      value={view}
      onValueChange={(next) => setSearchParams(userDetailTabSearch("preferences", { view: next }))}
      items={[
        {
          value: "account",
          label: `Account & profiles${count(settings.isLoading, settings.data.length)}`,
        },
        {
          value: "devices",
          label: `Per device${count(deviceSettings.isLoading, deviceSettings.data.length)}`,
        },
      ]}
    />
  );

  return view === "devices" ? (
    <DevicePreferences userId={user.id} tabs={tabs} />
  ) : (
    <AccountPreferences userId={user.id} tabs={tabs} />
  );
}
