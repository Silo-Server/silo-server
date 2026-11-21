import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";
import { useSettingsForm } from "./useSettingsForm";

// A refused settings save and the Reload that follows it, against a fake
// settings server with real query hooks and ETags.
vi.mock("sonner", () => ({ toast: { error: vi.fn(), dismiss: vi.fn() } }));
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

it("lets a refused settings save through after a reload, keeping the edits only when it can vouch for all", async () => {
  let stored: Record<string, string> = {
    "branding.server_name": "Silo",
    "branding.login_subtitle": "Welcome",
    "server.log_level": "info",
  };
  let tag = '"tagA"';
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
        stored = { ...stored, ...values };
        tag = '"tagC"';
        return response({ values, restart_required: false }, 200, tag);
      }
      if (url.endsWith("/sensitive-status"))
        return response({ configured: ["email.smtp_password"], managed_by_env: [] });
      return response(stored, 200, tag);
    }),
  );
  const { result } = renderHook(
    () =>
      useSettingsForm({
        // server.log_level belongs to another page.
        keys: [
          "branding.server_name",
          "branding.login_subtitle",
          "download.artifact_dir",
          "email.smtp_host",
          "email.smtp_password",
        ],
      }),
    fixture(),
  );
  async function reloadAfterRefusal() {
    const action = vi.mocked(toast.error).mock.calls.at(-1)?.[1]?.action as
      | { onClick: (event: { preventDefault: () => void }) => void }
      | undefined;
    expect(action).toBeDefined();
    const preventDefault = vi.fn();
    const dismissals = vi.mocked(toast.dismiss).mock.calls.length;
    act(() => action?.onClick({ preventDefault }));
    expect(preventDefault).toHaveBeenCalled();
    await waitFor(() =>
      expect(vi.mocked(toast.dismiss).mock.calls.length).toBeGreaterThan(dismissals),
    );
  }
  await waitFor(() => expect(result.current.sensitiveStatusReady).toBe(true));
  await waitFor(() => expect(result.current.getValue("branding.server_name")).toBe("Silo"));

  // Every edit can be vouched for, so Reload keeps them all.
  act(() => result.current.setValue("branding.server_name", "Casa"));
  // Another administrator saves the subtitle, and a background read shows it
  // before this admin edits it in turn.
  stored = { ...stored, "branding.login_subtitle": "Hello" };
  tag = '"tagA2"';
  await act(async () => {
    await result.current.retryLoad();
  });
  await waitFor(() => expect(result.current.getValue("branding.login_subtitle")).toBe("Hello"));
  act(() => result.current.setValue("branding.login_subtitle", "Hello there"));
  // Unset, so no snapshot carries it.
  act(() => result.current.setValue("download.artifact_dir", "/srv/artifacts"));
  // The other administrator then saves a setting on another page.
  stored = { ...stored, "server.log_level": "warn" };
  tag = '"tagB"';
  for (let attempt = 0; attempt < 2; attempt++) {
    await act(async () => {
      await expect(result.current.save()).rejects.toBeInstanceOf(Error);
    });
  }
  expect(writes.map((write) => write.tag)).toEqual(['"tagA"', '"tagA"']);
  // Each refusal is reported once, by the form, with a way to reload.
  expect(vi.mocked(toast.error).mock.calls).toHaveLength(2);
  await reloadAfterRefusal();
  expect([...result.current.dirtyKeys].sort()).toEqual([
    "branding.login_subtitle",
    "branding.server_name",
    "download.artifact_dir",
  ]);
  await act(async () => {
    await result.current.save();
  });
  expect(writes.at(-1)).toEqual({
    values: {
      "branding.server_name": "Casa",
      "branding.login_subtitle": "Hello there",
      "download.artifact_dir": "/srv/artifacts",
    },
    tag: '"tagB"',
  });
  expect(result.current.dirtyCount).toBe(0);

  // A new mail server with a new password for it: nothing here can tell
  // whether the saved password changed, so Reload undoes every edit rather
  // than save the server without the password it was entered with.
  act(() => result.current.setValue("branding.server_name", "Villa"));
  act(() => result.current.setValue("email.smtp_host", "mail.example.invalid"));
  act(() => result.current.setValue("email.smtp_password", "new-secret"));
  stored = { ...stored, "server.log_level": "error" };
  tag = '"tagD"';
  await act(async () => {
    await expect(result.current.save()).rejects.toBeInstanceOf(Error);
  });
  await reloadAfterRefusal();
  expect(result.current.dirtyCount).toBe(0);
  expect(result.current.getValue("branding.server_name")).toBe("Casa");
  expect(result.current.getValue("email.smtp_host")).toBe("");
  expect(result.current.getValue("email.smtp_password")).toBe("");

  // A setting on this page that this admin didn't edit changes: the edits
  // were made against a page that is gone, so Reload undoes them too.
  act(() => result.current.setValue("download.artifact_dir", "/srv/other"));
  stored = { ...stored, "branding.login_subtitle": "Changed again" };
  tag = '"tagE"';
  await act(async () => {
    await expect(result.current.save()).rejects.toBeInstanceOf(Error);
  });
  await reloadAfterRefusal();
  expect(result.current.dirtyCount).toBe(0);
  expect(result.current.getValue("branding.login_subtitle")).toBe("Changed again");
  expect(result.current.getValue("download.artifact_dir")).toBe("/srv/artifacts");
});

it("keeps every edit when a reload after a refused save can't read which secrets are set", async () => {
  const stored = { "branding.server_name": "Silo", "server.log_level": "info" };
  let tag = '"tagA"';
  let statusFails = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT")
        return response(
          {
            type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
            title: "Precondition failed",
            status: 412,
          },
          412,
        );
      if (url.endsWith("/sensitive-status"))
        return statusFails
          ? response(
              {
                type: "https://siloserver.org/docs/api/v2/problems/internal_error",
                title: "Internal error",
                status: 500,
              },
              500,
            )
          : response({ configured: [], managed_by_env: [] });
      return response(stored, 200, tag);
    }),
  );
  const { result } = renderHook(
    () =>
      useSettingsForm({
        keys: ["branding.server_name", "server.log_level", "download.artifact_dir"],
      }),
    fixture(),
  );
  await waitFor(() => expect(result.current.sensitiveStatusReady).toBe(true));
  await waitFor(() => expect(result.current.getValue("branding.server_name")).toBe("Silo"));
  act(() => result.current.setValue("branding.server_name", "Casa"));
  // Unset, so no snapshot carries it: only the status tells it from a secret.
  act(() => result.current.setValue("download.artifact_dir", "/srv/artifacts"));
  tag = '"tagB"';
  statusFails = true;
  await act(async () => {
    await expect(result.current.save()).rejects.toBeInstanceOf(Error);
  });
  const action = vi.mocked(toast.error).mock.calls.at(-1)?.[1]?.action as
    | { onClick: (event: { preventDefault: () => void }) => void }
    | undefined;
  const failures = vi.mocked(toast.error).mock.calls.length;
  act(() => action?.onClick({ preventDefault() {} }));
  await waitFor(() => expect(vi.mocked(toast.error).mock.calls.length).toBeGreaterThan(failures));

  expect([...result.current.dirtyKeys].sort()).toEqual([
    "branding.server_name",
    "download.artifact_dir",
  ]);
  expect(result.current.getValue("download.artifact_dir")).toBe("/srv/artifacts");
  expect(toast.dismiss).not.toHaveBeenCalled();
});
