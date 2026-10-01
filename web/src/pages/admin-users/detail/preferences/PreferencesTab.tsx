import { useSearchParams } from "react-router";

import type { AdminUser } from "@/api/types";
import { useAdminUserSettingCounts } from "@/hooks/queries/admin/users";

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
  // Counts come from this account's values alone; the device list that names
  // each device loads only when Per device is open.
  const counts = useAdminUserSettingCounts(user.id);
  const count = (n: number) => (counts.isLoading ? "" : ` · ${n}`);

  const tabs = (
    <SubTabs<PreferencesView>
      ariaLabel="Preferences view"
      value={view}
      onValueChange={(next) => setSearchParams(userDetailTabSearch("preferences", { view: next }))}
      items={[
        {
          value: "account",
          label: `Account & profiles${count(counts.account)}`,
        },
        {
          value: "devices",
          label: `Per device${count(counts.device)}`,
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
