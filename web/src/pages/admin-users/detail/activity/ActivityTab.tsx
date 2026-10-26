import type { AdminUser } from "@/api/types";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";

import { DevicesCard } from "./DevicesCard";
import { IPAddressesCard } from "./IPAddressesCard";
import { WatchHistoryCard } from "./WatchHistoryCard";

/** When and where this account watched: history, devices, and addresses. */
export function ActivityTab({ user }: { user: AdminUser }) {
  const capabilities = useAdminUserCapabilities().data;
  return (
    <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
      <WatchHistoryCard userId={user.id} summaryAvailable={capabilities?.watch_summary === true} />
      <div className="flex min-w-0 flex-col gap-4">
        {capabilities?.account_devices === true && <DevicesCard userId={user.id} />}
        <IPAddressesCard userId={user.id} />
      </div>
    </div>
  );
}
