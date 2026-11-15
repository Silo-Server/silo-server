// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, cleanup, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { adminKeys } from "@/hooks/queries/keys";
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

it("lets a refetch's 404 replace a task that was already cached", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async () =>
      jsonResponse(
        {
          type: "https://siloserver.org/docs/api/v2/problems/not_found",
          title: "Not Found",
          status: 404,
          detail: "Task not found",
        },
        404,
      ),
    ),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(adminKeys.task("retired"), {
    key: "retired",
    name: "Retired task",
    description: "Gone after an update",
    category: "system",
    state: "idle",
    progress: 0,
    manual_only: false,
    triggers: [],
    execution_scope: "process",
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/admin/tasks/retired"]}>
        <Routes>
          <Route path="/admin/tasks/:key" element={<AdminTaskDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByRole("heading", { level: 1, name: "Task not found" }),
  ).toBeInTheDocument();
  expect(screen.queryByText("Retired task")).not.toBeInTheDocument();
});

function renderScheduleEditor(triggers: Array<{ type: "interval"; interval_ms: number }>) {
  const writes: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (url, init) => {
      const path = String(url);
      if (path.endsWith("/triggers")) {
        if (init?.method === "PUT") {
          writes.push(JSON.parse(String(init.body)));
          return jsonResponse({ task_key: "fixture", triggers: [] });
        }
        return new Response(JSON.stringify({ task_key: "fixture", triggers }), {
          status: 200,
          headers: { "Content-Type": "application/json", ETag: '"revision-1"' },
        });
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
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={["/admin/tasks/fixture"]}>
        <Routes>
          <Route path="/admin/tasks/:key" element={<AdminTaskDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return writes;
}

it("keeps a typed interval in the unit the admin picked, and an emptied box empty", async () => {
  const writes = renderScheduleEditor([{ type: "interval", interval_ms: 120_000 }]);
  fireEvent.click(await screen.findByRole("button", { name: "Edit Schedule" }));
  // Each row has an interval box and a max runtime box.
  const input = (await screen.findAllByRole("spinbutton"))[0] as HTMLInputElement;
  const save = screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;

  fireEvent.change(input, { target: { value: "60" } });
  expect(input.value).toBe("60");
  expect(screen.getAllByRole("combobox")[1]).toHaveTextContent("Minutes");

  fireEvent.change(input, { target: { value: "" } });
  expect(input.value).toBe("");
  expect(save.disabled).toBe(true);

  fireEvent.change(input, { target: { value: "90" } });
  fireEvent.click(save);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ triggers: [{ type: "interval", interval_ms: 5_400_000 }] });
});

it("keeps each row's unit when a row above it is removed", async () => {
  renderScheduleEditor([
    { type: "interval", interval_ms: 120_000 },
    { type: "interval", interval_ms: 7_200_000 },
  ]);
  fireEvent.click(await screen.findByRole("button", { name: "Edit Schedule" }));
  await screen.findAllByRole("spinbutton");
  // The row's remove button is icon-only, so it has no accessible name.
  fireEvent.click(screen.getAllByRole("button", { name: "" })[0]!);

  expect((screen.getAllByRole("spinbutton")[0] as HTMLInputElement).value).toBe("2");
  expect(screen.getAllByRole("combobox")[1]).toHaveTextContent("Hours");
});
