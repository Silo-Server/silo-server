import AdminCatalogMaintenance from "@/components/AdminCatalogMaintenance";
import AdminJobHistory from "@/components/AdminJobHistory";
import AdminArtworkStorage from "@/components/AdminArtworkStorage";

export default function AdminMaintenance() {
  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Maintenance</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Operational tools that affect the whole catalog live here. Use this page for bulk
            import/export workflows and storage maintenance.
          </p>
        </div>
      </div>

      <AdminCatalogMaintenance />
      <AdminArtworkStorage />
      <AdminJobHistory />
    </div>
  );
}
