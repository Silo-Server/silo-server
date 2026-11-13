// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";

import { usePluginSettingsValues, useUpdatePluginEntry } from "./pluginSettings";

const request = vi.hoisted(() => vi.fn());

vi.mock("@/api/v2/request", () => ({ v2: request }));

const GET_VALUES = "GET /api/v2/settings/plugins/{installation_id}";
const PUT_VALUES = "PUT /api/v2/settings/plugins/{installation_id}";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return createElement(QueryClientProvider, { client }, children);
}

/** The body the write was asked to store, or a failure naming what went out instead. */
function writtenValues() {
  const call = request.mock.calls.find(([operation]) => operation === PUT_VALUES);
  if (!call) throw new Error("expected a write");
  return (call[1] as { body: { values: Record<string, string> } }).body.values;
}

beforeEach(() => {
  request.mockReset();
});

it("merges the write onto a read taken when the write was asked for", async () => {
  // The stored set belongs to the account, not to one tab: another tab (or another
  // device) can store a value between this screen's read and its write, and a
  // replace-all write of the screen's own copy would revert that change.
  let reads = 0;
  request.mockImplementation(async (operation: string) => {
    if (operation !== GET_VALUES) return {};
    reads += 1;
    // The first read is the one a screen would be holding; the second is what is stored
    // by the time the write happens, after another tab changed it.
    return reads === 1
      ? { values: { report: '{"show_report":false}', something_else: '{"kept":"older"}' } }
      : { values: { report: '{"show_report":false}', something_else: '{"kept":"newer"}' } };
  });

  const { result } = renderHook(
    () => ({ write: useUpdatePluginEntry(4), read: usePluginSettingsValues(4) }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.read.data).toBeDefined());

  await act(async () => {
    result.current.write.setEntryValue("report", { show_report: true });
  });

  await waitFor(() => expect(request).toHaveBeenCalledWith(PUT_VALUES, expect.anything()));
  expect(writtenValues()).toEqual({
    report: '{"show_report":true}',
    // What the second read found, not the value this screen's own read returned.
    something_else: '{"kept":"newer"}',
  });
});

it("keeps the other fields of the entry it changes", async () => {
  request.mockImplementation(async (operation: string) => {
    if (operation === GET_VALUES) {
      return { values: { report: '{"show_report":false,"mapping":"dvd"}' } };
    }
    return {};
  });

  const { result } = renderHook(() => useUpdatePluginEntry(4), { wrapper });
  await act(async () => {
    result.current.setEntryValue("report", { show_report: true });
  });

  await waitFor(() => expect(request).toHaveBeenCalledWith(PUT_VALUES, expect.anything()));
  expect(JSON.parse(writtenValues()["report"] ?? "{}")).toEqual({
    show_report: true,
    mapping: "dvd",
  });
});

it("writes nothing when the read that would seed it fails", async () => {
  // An empty base would delete everything stored, so a failed read is a refused write
  // rather than a write of whatever the screen happens to hold.
  request.mockRejectedValue(new Error("offline"));

  const onError = vi.fn();
  const { result } = renderHook(() => useUpdatePluginEntry(4), { wrapper });
  await act(async () => {
    result.current.setEntryValue("report", { show_report: true }, { onError });
  });

  await waitFor(() => expect(onError).toHaveBeenCalled());
  expect(request.mock.calls.some(([operation]) => operation === PUT_VALUES)).toBe(false);
});
