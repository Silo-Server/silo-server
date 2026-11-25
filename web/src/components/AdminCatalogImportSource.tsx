import type { ReactNode } from "react";
import type { AdminJob } from "@/api/types";
import { PathText } from "@/components/PathText";
import { useT } from "@/i18n";

/** Where a catalog import job reads from, as both admin job lists name it. */
export function AdminCatalogImportSource({
  job,
  fallback,
}: {
  job: AdminJob;
  fallback: ReactNode;
}) {
  const t = useT();
  const { source_label: label, source_export_job_id: exportJobId } = job.request_payload as {
    source_label?: string;
    source_export_job_id?: string;
  };
  if (exportJobId) {
    return t("web.admin.common.catalogImportSource.exportJob", { id: exportJobId });
  }
  // The server's label: the remote URL or a file name, untranslated. Imports queued before
  // the server reported source_export_job_id still carry its English "Export job <id>".
  if (!label) return fallback;
  return (
    <bdi dir="ltr">
      <PathText path={label} />
    </bdi>
  );
}
