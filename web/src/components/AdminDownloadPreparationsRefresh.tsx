import { useAdminDownloadPreparationsRefresh } from "@/hooks/queries/admin/downloadPreparations";

/**
 * Mounts the preparation-list refresh for an acting admin. App loads it lazily
 * so the list query and its timers stay out of the launch bundle.
 */
export default function AdminDownloadPreparationsRefresh() {
  useAdminDownloadPreparationsRefresh();
  return null;
}
