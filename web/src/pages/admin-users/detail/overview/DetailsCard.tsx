import type { AdminUser } from "@/api/types";

import { formatAccountDate } from "../format";
import { DetailCard, KeyValueRow } from "../ui";

export function DetailsCard({ user }: { user: AdminUser }) {
  return (
    <DetailCard title="Details">
      <KeyValueRow label="User ID" value={<span className="font-mono">{user.id}</span>} />
      <KeyValueRow label="Created" value={formatAccountDate(user.created_at)} />
      <KeyValueRow label="Last changed" value={formatAccountDate(user.updated_at)} />
    </DetailCard>
  );
}
