import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useRestoreJob, useRestoreJobs } from "./backupRestore";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", () => ({ v2: request }));

beforeEach(() => {
  request.mockReset();
  setAccessToken("account-a");
  setProfileId("profile-a");
  setProfileToken(null);
});

const running = { id: "job-1", kind: "backup_restore", state: "running", terminal: false };
const ended = { ...running, state: "succeeded", terminal: true };

// The restore page opens a running job from the list when the address names
// none, so a list that still says running sends Choose again back to the
// finished run's report.
it.each([
  ["the list was read while the run went on", false],
  ["a list read from before the run ended lands after the job read", true],
])("lists a job as ended once its own read says so: %s", async (_, listLandsLast) => {
  let answerList: (page: { items: unknown[] }) => void = () => {};
  let listReads = 0;
  request.mockImplementation((operation: string) => {
    if (operation === "GET /api/v2/admin/jobs") {
      // The first read is sent while the run goes on; later ones see it ended.
      if (listReads++ > 0) return Promise.resolve({ items: [ended] });
      return listLandsLast
        ? new Promise((resolve) => (answerList = resolve))
        : Promise.resolve({ items: [running] });
    }
    if (operation === "GET /api/v2/admin/jobs/{id}") return Promise.resolve(ended);
    return Promise.reject(new Error(`unexpected ${operation}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );

  // Both read together, as on the restore page.
  const { result, rerender } = renderHook(
    ({ jobId }: { jobId: string | null }) => ({
      jobs: useRestoreJobs(),
      job: useRestoreJob(jobId),
    }),
    { wrapper, initialProps: { jobId: listLandsLast ? "job-1" : null } },
  );
  if (listLandsLast) {
    await waitFor(() => expect(result.current.job.data?.terminal).toBe(true));
    answerList({ items: [running] });
  } else {
    await waitFor(() => expect(result.current.jobs.data).toEqual([running]));
    rerender({ jobId: "job-1" });
    await waitFor(() => expect(result.current.job.data?.terminal).toBe(true));
  }

  await waitFor(() => expect(result.current.jobs.data?.[0]?.terminal).toBe(true));
  // Choose again drops the job from the address; the list still says ended,
  // also once the job's own read has left the cache.
  rerender({ jobId: null });
  expect(result.current.jobs.data?.[0]?.terminal).toBe(true);
  client.removeQueries({ predicate: (query) => query.queryKey.includes("job") });
  rerender({ jobId: null });
  await waitFor(() => expect(result.current.jobs.data?.[0]?.terminal).toBe(true));
});
