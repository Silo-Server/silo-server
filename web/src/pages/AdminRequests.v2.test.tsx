// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { v2, V2ProblemError } from "@/api/v2/request";
import { adminKeys } from "@/hooks/queries/keys";
import AdminRequests from "./AdminRequests";

vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  captureProfileRequestContext: () => ({
    accessToken: "test",
    profileId: "profile",
    profileToken: null,
    authContextVersion: 1,
    serverOrigin: "",
  }),
  isProfileRequestContextCurrent: () => true,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({
    data: [
      { id: 1, plugin_id: "router", capabilities: [{ type: "request_router.v1", id: "arr" }] },
    ],
    isLoading: false,
  }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({ data: [{ id: 1, username: "member" }], isLoading: false }),
}));
const settings = {
  requests_enabled: true,
  global_max_requests: 5,
  global_window_days: 7,
  global_auto_approval_enabled: false,
  force_dual_quality: false,
};
const conflict = () =>
  new V2ProblemError(
    "updateAdminRequestSettings",
    {
      type: "https://silo.test/problems/precondition_failed",
      title: "Changed",
      status: 412,
      detail: "Changed",
      instance: "test",
    },
    null,
    '"newer"',
  );
function reply(options: unknown, body: unknown, etag = '"initial"') {
  (options as { onResponse?: (r: Response) => void })?.onResponse?.(
    new Response(null, { headers: { ETag: etag } }),
  );
  return Promise.resolve(body) as never;
}
function mount(tab: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/admin/requests?tab=${tab}`]}>
        <AdminRequests />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("request administration conflict handling", () => {
  it("retains settings and original validator across background refresh and 412, then explicitly reloads", async () => {
    let reads = 0;
    vi.mocked(v2).mockImplementation((operation, options) => {
      if (operation === "GET /api/v2/admin/requests/capabilities")
        return reply(options, { available: true, guarded_configuration: true });
      if (operation === "GET /api/v2/admin/request-settings")
        return reply(
          options,
          { ...settings, global_max_requests: ++reads === 1 ? 5 : 9 },
          reads === 1 ? '"initial"' : '"reloaded"',
        );
      if (operation === "PUT /api/v2/admin/request-settings") return Promise.reject(conflict());
      throw new Error(operation);
    });
    const client = mount("settings");
    await screen.findByText("Save Settings");
    const input = screen.getAllByRole("spinbutton")[0]!;
    fireEvent.change(input, { target: { value: "13" } });
    act(() =>
      client.setQueryData(adminKeys.requestSettings(), {
        ...settings,
        global_max_requests: 22,
        etag: '"background"',
        updated_at: "",
      }),
    );
    expect((input as HTMLInputElement).value).toBe("13");
    fireEvent.click(screen.getByText("Save Settings"));
    await screen.findByRole("alert");
    expect((input as HTMLInputElement).value).toBe("13");
    const writes = vi
      .mocked(v2)
      .mock.calls.filter(([op]) => op === "PUT /api/v2/admin/request-settings");
    expect(writes).toHaveLength(1);
    expect(writes[0]![1]!).toMatchObject({
      headers: { "If-Match": '"initial"' },
      body: { global_max_requests: 13 },
    });
    expect(
      (screen.getByText("Save Settings").closest("button") as HTMLButtonElement).disabled,
    ).toBe(true);
    fireEvent.click(screen.getByText("Reload latest version"));
    await waitFor(() =>
      expect((screen.getAllByRole("spinbutton")[0]! as HTMLInputElement).value).toBe("9"),
    );
    fireEvent.click(screen.getByText("Save Settings"));
    await waitFor(() =>
      expect(
        vi.mocked(v2).mock.calls.filter(([op]) => op === "PUT /api/v2/admin/request-settings"),
      ).toHaveLength(2),
    );
    expect(
      vi
        .mocked(v2)
        .mock.calls.filter(([op]) => op === "PUT /api/v2/admin/request-settings")[1]![1]!,
    ).toMatchObject({ headers: { "If-Match": '"reloaded"' } });
  });
  it("keeps deletion confirmation open after a stale integration delete", async () => {
    const row = {
      id: "integration",
      name: "Connection",
      enabled: true,
      base_url: "https://example.invalid",
      has_api_key: true,
      installation_id: "1",
      capability_id: "arr",
      plugin_config: {},
      supported_media_types: [],
      last_check_at: null,
      last_check_status: "",
      last_check_error: "",
      updated_at: "2026-09-05T00:00:00Z",
    };
    vi.mocked(v2).mockImplementation((operation, options) => {
      if (operation === "GET /api/v2/admin/requests/capabilities")
        return reply(options, { available: true, guarded_configuration: true });
      if (operation === "GET /api/v2/admin/request-integrations")
        return reply(options, { items: [row], page: { has_more: false } });
      if (operation === "GET /api/v2/admin/request-integrations/{id}") return reply(options, row);
      if (operation === "DELETE /api/v2/admin/request-integrations/{id}")
        return Promise.reject(conflict());
      if (operation === "POST /api/v2/admin/request-integrations/{id}/options")
        return reply(options, { options: {} });
      throw new Error(operation);
    });
    mount("integrations");
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(within(dialog).getByRole("alert")).toBeTruthy());
    expect(screen.getByRole("dialog")).toBeTruthy();
    const writes = vi
      .mocked(v2)
      .mock.calls.filter(([op]) => op === "DELETE /api/v2/admin/request-integrations/{id}");
    expect(writes).toHaveLength(1);
    expect(writes[0]![1]!).toMatchObject({ headers: { "If-Match": '"initial"' } });
  });
  it("renders generic integration field errors inline and clears them on edit", async () => {
    const row = {
      id: "integration",
      name: "Connection",
      enabled: true,
      base_url: "https://example.invalid",
      has_api_key: true,
      installation_id: "1",
      capability_id: "arr",
      plugin_config: {},
      supported_media_types: [],
      last_check_at: null,
      last_check_status: "",
      last_check_error: "",
      updated_at: "2026-09-05T00:00:00Z",
    };
    const errors = [
      { location: "body.name", code: "invalid", detail: "Connection name is rejected" },
      { location: "body.api_key_ref", code: "invalid", detail: "Re-enter the credential" },
      { location: "body.base_url", code: "invalid", detail: "Server URL is rejected" },
      { location: "body.installation_id", code: "invalid", detail: "Choose another installation" },
    ];
    vi.mocked(v2).mockImplementation((operation, options) => {
      if (operation === "GET /api/v2/admin/requests/capabilities")
        return reply(options, { available: true, guarded_configuration: true });
      if (operation === "GET /api/v2/admin/request-integrations")
        return reply(options, { items: [row], page: { has_more: false } });
      if (operation === "GET /api/v2/admin/request-integrations/{id}") return reply(options, row);
      if (operation === "POST /api/v2/admin/request-integrations/{id}/options")
        return reply(options, { options: {} });
      if (operation === "PUT /api/v2/admin/request-integrations/{id}")
        return Promise.reject(
          new V2ProblemError("updateRequestIntegration", {
            type: "https://example.invalid/problems/validation_failed",
            title: "Invalid",
            status: 422,
            detail: "Review invalid fields",
            instance: "test",
            errors,
          }),
        );
      throw new Error(operation);
    });
    mount("integrations");
    fireEvent.click(await screen.findByRole("button", { name: "Save" }));
    for (const error of errors) expect(await screen.findByText(error.detail)).toBeTruthy();
    const name = screen.getByPlaceholderText("Connection name");
    expect(name.getAttribute("aria-invalid")).toBe("true");
    expect(name.parentElement?.textContent).toContain("Connection name is rejected");
    fireEvent.change(name, { target: { value: "Corrected" } });
    expect(screen.queryByText("Connection name is rejected")).toBeNull();
    expect(name.getAttribute("aria-invalid")).toBe("false");
  });
  it("keeps user override edits and validator until explicit reload after a stale response", async () => {
    let reads = 0;
    vi.mocked(v2).mockImplementation((operation, options) => {
      if (operation === "GET /api/v2/admin/requests/capabilities")
        return reply(options, { available: true, guarded_configuration: true });
      if (operation === "GET /api/v2/admin/request-users/{user_id}/limit")
        return reply(
          options,
          {
            user_id: "1",
            limit_mode: "custom",
            max_requests: ++reads === 1 ? 3 : 6,
            window_days: 7,
            approval_mode: "inherit",
          },
          reads === 1 ? '"initial"' : '"reloaded"',
        );
      if (operation === "PUT /api/v2/admin/request-users/{user_id}/limit")
        return Promise.reject(conflict());
      throw new Error(operation);
    });
    mount("overrides");
    await screen.findByText("Save Override");
    const input = screen.getAllByRole("spinbutton")[0]!;
    fireEvent.change(input, { target: { value: "11" } });
    fireEvent.click(screen.getByText("Save Override"));
    await screen.findByRole("alert");
    expect((input as HTMLInputElement).value).toBe("11");
    expect(
      vi
        .mocked(v2)
        .mock.calls.filter(([op]) => op === "PUT /api/v2/admin/request-users/{user_id}/limit"),
    ).toHaveLength(1);
    const writes = () =>
      vi
        .mocked(v2)
        .mock.calls.filter(([op]) => op === "PUT /api/v2/admin/request-users/{user_id}/limit");
    expect(writes()[0]![1]).toMatchObject({
      headers: { "If-Match": '"initial"' },
      body: { max_requests: 11 },
    });
    expect(
      (screen.getByText("Save Override").closest("button") as HTMLButtonElement).disabled,
    ).toBe(true);
    fireEvent.click(screen.getByText("Reload latest version"));
    await waitFor(() =>
      expect((screen.getAllByRole("spinbutton")[0] as HTMLInputElement).value).toBe("6"),
    );
    fireEvent.click(screen.getByText("Save Override"));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes()[1]![1]).toMatchObject({
      headers: { "If-Match": '"reloaded"' },
      body: { max_requests: 6 },
    });
  });
});
