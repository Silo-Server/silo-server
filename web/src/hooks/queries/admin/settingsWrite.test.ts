import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  useAdminServerSettings,
  useUpdateServerSettings,
  useUpdateServerSetting,
} from "./settings";
import { toast } from "sonner";
import { useSettingsForm } from "@/hooks/useSettingsForm";
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));
function fixture() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { client, wrapper };
}
const response = (body: unknown, status = 200, etag = '"displayed-v1"') =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ETag: etag },
  });
beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});
it("captures the displayed validator, copied batch and profile before an offline pause", async () => {
  const fetchMock = vi
    .fn()
    .mockImplementation(async (_url, init) =>
      init?.method === "PUT"
        ? response({ values: {}, restart_required: false })
        : response({ "server.log_level": "info" }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const { result } = renderHook(
    () => ({ read: useAdminServerSettings(), write: useUpdateServerSettings() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  onlineManager.setOnline(false);
  const values = { "server.log_level": "debug" };
  act(() => result.current.write.mutate(values));
  values["server.log_level"] = "error";
  await waitFor(() => expect(result.current.write.isPaused).toBe(true));
  const invalidate = vi.spyOn(client, "invalidateQueries");
  act(() => {
    setProfileId("profile-b");
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.write.isSuccess).toBe(true));
  const puts = fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT");
  expect(puts).toHaveLength(1);
  expect(puts[0]?.[1].headers).toMatchObject({
    "If-Match": '"displayed-v1"',
    "X-Profile-Id": "profile-a",
    "X-Profile-Token": "pin-a",
  });
  expect(JSON.parse(puts[0]?.[1].body)).toEqual({ values: { "server.log_level": "debug" } });
  expect(invalidate).not.toHaveBeenCalled();
  expect(toast.error).not.toHaveBeenCalled();
});
it("does not refresh or retry a single-setting write on 401", async () => {
  setRefreshToken("refresh-token");
  const fetchMock = vi
    .fn()
    .mockImplementation(async (_url, init) =>
      init?.method === "PUT"
        ? response({ title: "Unauthorized", status: 401 }, 401)
        : response({ "server.log_level": "info" }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({ read: useAdminServerSettings(), write: useUpdateServerSetting() }),
    fixture(),
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  act(() => result.current.write.mutate({ key: "server.log_level", value: "debug" }));
  await waitFor(() => expect(result.current.write.isError).toBe(true));
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1);
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("refresh"))).toBe(false);
});
it("keeps the original displayed guard after a background refetch and a 412", async () => {
  let etag = '"displayed-v1"';
  const fetchMock = vi
    .fn()
    .mockImplementation(async (_url, init) =>
      init?.method === "PUT"
        ? response({ title: "Precondition failed", status: 412 }, 412)
        : response({ "server.log_level": "info" }, 200, etag),
    );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({ read: useAdminServerSettings(), write: useUpdateServerSettings() }),
    fixture(),
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  const original = result.current.read.data;
  etag = '"secret-changed-v2"';
  await act(async () => {
    await result.current.read.refetch();
  });
  await waitFor(() => expect(result.current.read.data).not.toBe(original));
  for (let i = 0; i < 2; i++) {
    await act(async () => {
      await expect(
        result.current.write.mutateAsync({ "server.log_level": "debug" }),
      ).rejects.toThrow();
    });
  }
  const puts = fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT");
  expect(puts).toHaveLength(2);
  for (const [, init] of puts) expect(init.headers["If-Match"]).toBe('"displayed-v1"');
});

it("rejects a decoded completion after the acting profile changes", async () => {
  let release: ((value: unknown) => void) | undefined;
  const decoded = new Promise((resolve) => {
    release = resolve;
  });
  const fetchMock = vi.fn().mockImplementation(async (_url, init) => {
    if (init?.method !== "PUT") return response({ "server.log_level": "info" });
    const pending = response({});
    pending.text = async () => JSON.stringify(await decoded);
    return pending;
  });
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const { result } = renderHook(
    () => ({ read: useAdminServerSettings(), write: useUpdateServerSettings() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  const invalidate = vi.spyOn(client, "invalidateQueries");
  let settled: Promise<unknown>;
  act(() => {
    settled = result.current.write
      .mutateAsync({ "server.log_level": "debug" })
      .catch((error: unknown) => error);
  });
  await waitFor(() =>
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true),
  );
  act(() => setProfileId("profile-b"));
  await act(async () => {
    release?.({ values: { "server.log_level": "debug" }, restart_required: false });
  });
  expect(await settled!).toBeInstanceOf(Error);
  expect(invalidate).not.toHaveBeenCalled();
});

it.each([false, true])(
  "reconciles retained edits only with the acknowledged revision (competitor=%s)",
  async (competitor) => {
    let stored = { "branding.server_name": "Silo", "server.log_level": "info" };
    let tag = '"tagA"';
    let acknowledgeFirst: (() => void) | undefined;
    const firstAcknowledgment = new Promise<void>((resolve) => {
      acknowledgeFirst = resolve;
    });
    const writes: Array<{ values: Record<string, string>; tag: string }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (init?.method === "PUT") {
          const values = JSON.parse(String(init.body)).values as Record<string, string>;
          const submittedTag = new Headers(init.headers).get("If-Match") ?? "";
          writes.push({ values, tag: submittedTag });
          if (submittedTag !== tag)
            return response(
              {
                type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
                title: "Precondition failed",
                status: 412,
              },
              412,
            );
          if (writes.length === 1) await firstAcknowledgment;
          stored = { ...stored, ...values };
          tag = writes.length === 1 ? '"tagB"' : '"tagD"';
          const receipt = response({ values, restart_required: false }, 200, tag);
          // A different writer commits after our PUT and before its canonical GET.
          if (competitor && writes.length === 1) {
            stored["server.log_level"] = "error";
            tag = '"tagC"';
          }
          return receipt;
        }
        if (url.endsWith("/sensitive-status"))
          return response({ configured: [], managed_by_env: [] });
        return response(stored, 200, tag);
      }),
    );
    const { result } = renderHook(
      () => useSettingsForm({ keys: ["branding.server_name", "server.log_level"] }),
      fixture(),
    );
    await waitFor(() => expect(result.current.getValue("branding.server_name")).toBe("Silo"));
    act(() => result.current.setValue("branding.server_name", "Casa"));
    let firstSave: Promise<void> | undefined;
    act(() => {
      firstSave = result.current.save();
    });
    await waitFor(() => expect(writes).toHaveLength(1));
    const retainedKey = competitor ? "server.log_level" : "branding.server_name";
    const retainedValue = competitor ? "debug" : "Villa";
    act(() => result.current.setValue(retainedKey, retainedValue));
    await act(async () => {
      acknowledgeFirst?.();
      await firstSave;
    });
    expect(stored["branding.server_name"]).toBe("Casa");
    expect(result.current.getValue(retainedKey)).toBe(retainedValue);
    expect(result.current.dirtyCount).toBe(1);
    expect(writes).toEqual([{ values: { "branding.server_name": "Casa" }, tag: '"tagA"' }]);
    await act(async () => {
      if (competitor) await expect(result.current.save()).rejects.toThrow();
      else await result.current.save();
    });
    expect(writes).toEqual([
      { values: { "branding.server_name": "Casa" }, tag: '"tagA"' },
      { values: { [retainedKey]: retainedValue }, tag: competitor ? '"tagA"' : '"tagB"' },
    ]);
    expect(stored[retainedKey]).toBe(competitor ? "error" : "Villa");
    expect(result.current.getValue(retainedKey)).toBe(retainedValue);
    expect(result.current.dirtyCount).toBe(competitor ? 1 : 0);
  },
);
