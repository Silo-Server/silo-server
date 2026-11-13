import type { AdminUser } from "@/api/types";
import { useT } from "@/i18n";

import { formatAccountDate } from "../format";
import { DetailCard, KeyValueRow } from "../ui";

export function DetailsCard({ user }: { user: AdminUser }) {
  const t = useT();
  return (
    <DetailCard title={t("web.admin.users.detail.overview.details.title")}>
      <KeyValueRow
        label={t("web.admin.users.detail.overview.details.userId")}
        value={<span className="font-mono">{user.id}</span>}
      />
      <KeyValueRow
        label={t("web.admin.users.detail.overview.details.created")}
        value={formatAccountDate(user.created_at)}
      />
      <KeyValueRow
        label={t("web.admin.users.detail.overview.details.lastChanged")}
        value={formatAccountDate(user.updated_at)}
      />
    </DetailCard>
  );
}
