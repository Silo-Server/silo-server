// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { AdminJob } from "@/api/types";
import AdminJobHistory from "./AdminJobHistory";

const jobs = vi.hoisted(() => ({ current: [] as AdminJob[] }));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAllAdminJobs: () => ({
    data: jobs.current,
    isFetching: false,
    isError: false,
    hasNextPage: false,
    isFetchingNextPage: false,
    refetch: vi.fn(),
    fetchNextPage: vi.fn(),
    restart: vi.fn(),
  }),
}));

afterEach(cleanup);

function imageCacheCleanupJob(id: string, status: AdminJob["status"]): AdminJob {
  return {
    id,
    job_type: "image_cache_cleanup",
    status,
    created_by_user_id: 0,
    request_payload: { library_id: 4, library_name: "Movies" },
    result_payload: { deleted_prefixes: 2, deleted_s3_objects: 17 },
    message: "",
    progress_current: 0,
    progress_total: 0,
    artifact_size_bytes: 0,
    requested_at: "2026-10-10T12:00:00Z",
  };
}

it("shows the totals a canceled image cache cleanup deleted before the cancel", () => {
  jobs.current = [imageCacheCleanupJob("canceled", "cancelled")];
  render(<AdminJobHistory />);
  expect(
    screen.getByText("Canceled after deleting 17 cached objects across 2 prefixes"),
  ).toBeTruthy();
});

it("keeps the completed image cache cleanup summary", () => {
  jobs.current = [imageCacheCleanupJob("done", "completed")];
  render(<AdminJobHistory />);
  expect(screen.getByText("Deleted 17 cached objects across 2 prefixes")).toBeTruthy();
});
