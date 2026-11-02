import { PageHeader } from "@/components/page/PageHeader";
import ActivityPanel from "@/pages/admin/autoscan/ActivityPanel";

/** Autoscan › Activity: the changes Autoscan received and the scans they started. */
export default function AdminAutoscanActivity() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Autoscan Activity"
        subtitle="Changes Autoscan received from its sources, and the scans they started."
      />
      <ActivityPanel />
    </div>
  );
}
