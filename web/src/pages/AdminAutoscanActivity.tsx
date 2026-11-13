import { PageHeader } from "@/components/page/PageHeader";
import { useT } from "@/i18n";
import ActivityPanel from "@/pages/admin/autoscan/ActivityPanel";

/** Autoscan › Activity: the changes Autoscan received and the scans they started. */
export default function AdminAutoscanActivity() {
  const t = useT();
  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.admin.autoscan.activity.page.title")}
        subtitle={t("web.admin.autoscan.activity.page.subtitle")}
      />
      <ActivityPanel />
    </div>
  );
}
