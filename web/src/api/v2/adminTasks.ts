import { v2 } from "@/api/v2/request";
import { adminJobFromV2 } from "@/api/v2/libraries";
import type { components } from "@/api/v2/schema";
import type { AdminJob } from "@/api/types";

export function adminTaskJobFromV2(job: components["schemas"]["AdminTaskJob"]): AdminJob {
  return {
    ...adminJobFromV2(job),
    request_payload: {
      library_ids: job.library_ids,
      source_label: job.source_label,
      library_id: job.library_id,
      library_name: job.library_name,
    },
    result_payload:
      job.library_result ??
      job.catalog_result ??
      (job.item_result
        ? {
            ...job.item_result,
            scan_result: { new: job.item_result.new_files },
            artwork_cache_warning: job.item_result.artwork_cache_incomplete
              ? "Artwork caching did not finish. Inspect administrator diagnostics for details."
              : undefined,
          }
        : undefined) ??
      job.template_result ??
      job.refresh_result ??
      job.deletion_result ??
      {},
    artifact_size_bytes: job.artifact_size_bytes,
    download_url: job.download_url,
    download_expires_at: job.download_expires_at,
    public_url: job.public_url,
    public_link_supported: job.public_link_supported,
  };
}
export async function fetchAdminTaskJob(id: string) {
  return adminTaskJobFromV2(await v2("GET /api/v2/admin/jobs/{id}", { path: { id } }));
}
