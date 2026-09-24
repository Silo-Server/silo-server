// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, cleanup, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import AdminTaskDetail from "./AdminTaskDetail";

beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("access");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("keeps the original schedule guard and draft after412 until explicit revision adoption", async () => {
  let reads = 0;
  const writes: Array<{ etag: string | null; body: unknown }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/triggers")) {
        if (init?.method === "PUT") {
          writes.push({
            etag: new Headers(init.headers).get("If-Match"),
            body: JSON.parse(String(init.body)),
          });
          if (writes.length === 1)
            return jsonResponse(
              {
                type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
                title: "Precondition failed",
                status: 412,
                detail: "Schedule changed",
              },
              412,
            );
          return jsonResponse({ task_key: "fixture", triggers: [] });
        }
        reads++;
        return new Response(
          JSON.stringify({
            task_key: "fixture",
            triggers: [{ type: "interval", interval_ms: reads === 1 ? 1000 : 60_000 }],
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json", ETag: `"revision-${reads}"` },
          },
        );
      }
      if (path.includes("/history")) return jsonResponse({ items: [], page: { has_more: false } });
      return jsonResponse({
        key: "fixture",
        name: "Fixture task",
        description: "Task description",
        category: "system",
        state: "idle",
        progress: 0,
        manual_only: false,
        triggers: [],
        execution_scope: "process",
      });
    }),
  );
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: 2 } } })
      }
    >
      <MemoryRouter initialEntries={["/admin/tasks/fixture"]}>
        <Routes>
          <Route path="/admin/tasks/:key" element={<AdminTaskDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  fireEvent.click(await screen.findByRole("button", { name: "Edit Schedule" }));
  const input = (await screen.findAllByRole("spinbutton"))[0]!;
  fireEvent.change(input, { target: { value: "3" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByRole("button", { name: "Review current schedule" });
  expect(writes).toEqual([
    { etag: '"revision-1"', body: { triggers: [{ type: "interval", interval_ms: 3000 }] } },
  ]);
  expect((input as HTMLInputElement).value).toBe("3");
  expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Review current schedule" }));
  fireEvent.click(
    await screen.findByRole("button", { name: "Use this revision and keep my draft" }),
  );
  expect(writes).toHaveLength(1);
  expect((input as HTMLInputElement).value).toBe("3");
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(writes).toHaveLength(2));
  expect(writes[1]).toEqual({
    etag: '"revision-2"',
    body: { triggers: [{ type: "interval", interval_ms: 3000 }] },
  });
});

it("names failed maintenance steps in execution history", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (url) => {
      const path = String(url);
      if (path.endsWith("/triggers"))
        return new Response(JSON.stringify({ task_key: "database_maintenance", triggers: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json", ETag: '"revision-1"' },
        });
      if (path.includes("/history"))
        return jsonResponse({
          items: [
            {
              id: "1",
              task_key: "database_maintenance",
              started_at: "2026-09-23T05:00:00Z",
              completed_at: "2026-09-23T05:00:01Z",
              status: "failed",
              duration_ms: 1000,
              error_message: "Task failed. Inspect administrator diagnostics for details.",
              steps: [
                { key: "cleanup_activity_log", name: "Cleanup Activity Log", status: "completed" },
                {
                  key: "cleanup_policy_decision_log",
                  name: "Cleanup Policy Decision Log",
                  status: "failed",
                },
              ],
            },
          ],
          page: { has_more: false },
        });
      return jsonResponse({
        key: "database_maintenance",
        name: "Database Maintenance",
        description: "Task description",
        category: "system",
        state: "idle",
        progress: 0,
        manual_only: false,
        triggers: [],
        execution_scope: "process",
      });
    }),
  );
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={["/admin/tasks/database_maintenance"]}>
        <Routes>
          <Route path="/admin/tasks/:key" element={<AdminTaskDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  expect(await screen.findByText("Failed steps: Cleanup Policy Decision Log")).toBeInTheDocument();
});
