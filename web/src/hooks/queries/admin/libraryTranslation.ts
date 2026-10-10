import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { v2, type V2Result } from "@/api/v2/request";

export type LibraryTranslationJob =
  V2Result<"GET /api/v2/admin/libraries/{library_id}/metadata-translation/jobs">["jobs"][number];

const libraryTranslationJobsKey = (libraryId: number) =>
  ["admin", "library-metadata-translation", libraryId] as const;

export function isActiveTranslationJob(job: LibraryTranslationJob): boolean {
  return job.status === "pending" || job.status === "running";
}

/** A library's recent prewarm jobs, polled while one is active. */
export function useLibraryTranslationJobs(libraryId: number, enabled: boolean) {
  return useQuery({
    queryKey: libraryTranslationJobsKey(libraryId),
    queryFn: () =>
      v2("GET /api/v2/admin/libraries/{library_id}/metadata-translation/jobs", {
        path: { library_id: String(libraryId) },
      }),
    enabled,
    refetchInterval: (query) =>
      (query.state.data?.jobs ?? []).some(isActiveTranslationJob) ? 2000 : false,
  });
}

/** Starts (or joins) the prewarm of a library's missing translations. */
export function useTranslateLibraryMetadata(libraryId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (targetLanguage: string) =>
      v2("POST /api/v2/admin/libraries/{library_id}/metadata-translation", {
        path: { library_id: String(libraryId) },
        body: { target_language: targetLanguage },
        retryAuthentication: false,
      }),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: libraryTranslationJobsKey(libraryId) }),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to start translation");
    },
  });
}

export function useCancelLibraryTranslation(libraryId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (jobId: string) =>
      v2("POST /api/v2/admin/libraries/{library_id}/metadata-translation/jobs/{job_id}/cancel", {
        path: { library_id: String(libraryId), job_id: jobId },
        retryAuthentication: false,
      }),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: libraryTranslationJobsKey(libraryId) }),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to cancel translation");
    },
  });
}
