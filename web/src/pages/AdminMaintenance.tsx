import AdminCatalogMaintenance from "@/components/AdminCatalogMaintenance";
import AdminJobHistory from "@/components/AdminJobHistory";
import { PageHeader } from "@/components/page/PageHeader";

export default function AdminMaintenance() {
  return (
    <div className="space-y-6 pb-4 sm:pb-6">
      <PageHeader
        title="Maintenance"
        subtitle="Operational tools that affect the whole catalog live here. Use this page for bulk import/export workflows and other future maintenance actions."
      />

      <AdminCatalogMaintenance />
      <AdminJobHistory />
    </div>
  );
}
