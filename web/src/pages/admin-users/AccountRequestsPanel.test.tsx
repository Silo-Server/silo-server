// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminUser } from "@/api/types";
import { v2, V2ProblemError } from "@/api/v2/request";

import { AccountRequestsPanel } from "./AccountRequestsPanel";

vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

// Radix Select needs these to open under jsdom.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const USER: AdminUser = {
  id: 7,
  username: "taylor",
  email: "taylor@example.test",
  role: "user",
  permissions: [],
  enabled: true,
  library_ids: null,
  access_group_id: 3,
  max_playback_quality: null,
  max_streams: null,
  max_transcodes: null,
  max_remote_stream_bitrate_kbps: null,
  max_local_stream_bitrate_kbps: null,
  transcode_allowed: null,
  audio_transcode_allowed: null,
  max_profiles: 4,
  download_allowed: null,
  download_transcode_allowed: null,
  requests_allowed: null,
  password_login: true,
  password_change_required: false,
  is_owner: false,
  effective_policy: {
    library_ids: null,
    max_playback_quality: "",
    max_streams: 0,
    max_transcodes: 0,
    max_remote_stream_bitrate_kbps: 0,
    max_local_stream_bitrate_kbps: 0,
    transcode_allowed: true,
    audio_transcode_allowed: true,
    download_allowed: true,
    download_transcode_allowed: false,
    requests_allowed: true,
    permissions: [],
  },
  created_at: "2026-07-01T12:00:00Z",
  updated_at: "2026-07-01T12:00:00Z",
};

const SETTINGS = {
  requests_enabled: true,
  global_max_requests: 12,
  global_window_days: 14,
  global_auto_approval_enabled: true,
  force_dual_quality: false,
};
const INHERIT = { max_requests: null, window_days: null };

type Options = {
  path?: Record<string, string>;
  headers?: Record<string, string>;
  body?: Record<string, unknown>;
  onResponse?: (r: Response) => void;
};
type Reply = { body: unknown; etag?: string } | Error;

/** Answers each v2 operation from `handlers`; any other operation fails. */
function serve(handlers: Record<string, (options: Options) => Reply>) {
  vi.mocked(v2).mockImplementation((operation, raw) => {
    const options = (raw ?? {}) as Options;
    const handler = handlers[operation];
    if (!handler) return Promise.reject(new Error(`unexpected ${operation}`)) as never;
    const reply = handler(options);
    if (reply instanceof Error) return Promise.reject(reply) as never;
    options.onResponse?.(new Response(null, { headers: { ETag: reply.etag ?? '"initial"' } }));
    return Promise.resolve(reply.body) as never;
  });
}
function calls(operation: string) {
  return vi
    .mocked(v2)
    .mock.calls.filter(([op]) => op === operation)
    .map(([, options]) => options as Options);
}
const conflict = () =>
  new V2ProblemError(
    "updateAdminRequestUserLimit",
    {
      type: "https://silo.test/problems/precondition_failed",
      title: "Changed",
      status: 412,
      detail: "The account's request limit changed; reload before saving.",
      instance: "test",
    },
    null,
    '"newer"',
  );

function mount(user: AdminUser = USER, groupName: string | undefined = "Kids") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AccountRequestsPanel user={user} groupName={groupName} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
const nowLine = () => screen.findByTestId("request-policy-now");
async function pick(user: ReturnType<typeof userEvent.setup>, combobox: string, option: string) {
  await user.click(screen.getByRole("combobox", { name: combobox }));
  await user.click(await screen.findByRole("option", { name: option }));
}

const USER_LIMIT = "GET /api/v2/admin/request-users/{user_id}/limit";
const PUT_USER_LIMIT = "PUT /api/v2/admin/request-users/{user_id}/limit";
const GROUP_LIMIT = "GET /api/v2/admin/request-groups/{group_id}/limit";
const SETTINGS_OP = "GET /api/v2/admin/request-settings";

beforeEach(() => {
  vi.mocked(v2).mockReset();
});
afterEach(() => {
  cleanup();
});

describe("AccountRequestsPanel", () => {
  it("says what applies now and where it comes from, and links to the account's requests", async () => {
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [USER_LIMIT]: () => ({
        body: { user_id: "7", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
      [GROUP_LIMIT]: () => ({
        body: {
          group_id: "3",
          limit_mode: "custom",
          max_requests: 5,
          window_days: 7,
          approval_mode: "manual",
        },
      }),
    });
    mount();
    expect(await nowLine()).toHaveTextContent(
      "Now: 5 requests per 7 days, an admin approves (from Kids group)",
    );
    expect(screen.getByRole("link", { name: /Requests from this account/ })).toHaveAttribute(
      "href",
      "/admin/requests?user=7",
    );
    expect(calls(GROUP_LIMIT)[0]?.path).toEqual({ group_id: "3" });
    // Inheriting fields name the group's value; blocking is not an option.
    expect(screen.getByText("Kids group: an admin approves")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Approval" })).toHaveTextContent(
      "Use group default",
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: "Approval" }));
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual([
      "Use group default",
      "An admin approves",
      "Approve automatically",
    ]);
  });

  it("skips the group for an admin account", async () => {
    serve({
      [SETTINGS_OP]: () => ({ body: { ...SETTINGS, global_auto_approval_enabled: false } }),
      [USER_LIMIT]: () => ({
        body: { user_id: "7", limit_mode: "unlimited", approval_mode: "inherit", ...INHERIT },
      }),
    });
    mount({ ...USER, role: "admin" });
    expect(await nowLine()).toHaveTextContent(
      "Now: no limit (set on this account), an admin approves (server default)",
    );
    expect(calls(GROUP_LIMIT)).toHaveLength(0);
    expect(screen.getByRole("combobox", { name: "Limit" })).toHaveTextContent("No limit");
    expect(screen.getByRole("combobox", { name: "Approval" })).toHaveTextContent(
      "Use server default",
    );
    expect(
      screen.getByText("Admin accounts don't use an access group's request settings."),
    ).toBeInTheDocument();
  });

  it("saves the account's approval and limit with the validator it read, and reloads after a conflict", async () => {
    // The stored row: the first save finds it changed by someone else.
    let stored: Reply = {
      body: { user_id: "7", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      etag: '"initial"',
    };
    let writes = 0;
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({
        body: { group_id: "3", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
      [USER_LIMIT]: () => stored,
      [PUT_USER_LIMIT]: ({ body }) => {
        if (++writes === 1) {
          stored = { body: (stored as { body: unknown }).body, etag: '"reloaded"' };
          return conflict();
        }
        stored = { body: { user_id: "7", ...body }, etag: '"saved"' };
        return stored;
      },
    });
    const user = userEvent.setup();
    mount();
    expect(await nowLine()).toHaveTextContent(
      "Now: 12 requests per 14 days, approved automatically (server default)",
    );
    expect(screen.getByText("Kids group uses the server default: 12 requests per 14 days"));

    await pick(user, "Approval", "An admin approves");
    await pick(user, "Limit", "Custom limit");
    // A new custom limit starts from what the account had.
    const max = screen.getByRole("spinbutton", { name: "Requests allowed" });
    expect(max).toHaveValue(12);
    await user.clear(max);
    await user.type(max, "3");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText(/changed by another administrator/)).toBeInTheDocument();
    expect(calls(PUT_USER_LIMIT)[0]).toMatchObject({
      path: { user_id: "7" },
      headers: { "If-Match": '"initial"' },
      body: { limit_mode: "custom", max_requests: 3, window_days: 14, approval_mode: "manual" },
    });
    // The edits stay until an explicit reload.
    expect(screen.getByRole("spinbutton", { name: "Requests allowed" })).toHaveValue(3);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Reload latest version" }));
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Limit" })).toHaveTextContent(
        "Use group default",
      ),
    );
    await pick(user, "Limit", "No limit");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls(PUT_USER_LIMIT)).toHaveLength(2));
    expect(calls(PUT_USER_LIMIT)[1]).toMatchObject({
      headers: { "If-Match": '"reloaded"' },
      body: { limit_mode: "unlimited", max_requests: null, window_days: null },
    });
    await waitFor(async () =>
      expect(await nowLine()).toHaveTextContent(
        "Now: no limit (set on this account), approved automatically (server default)",
      ),
    );
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("keeps a saved limit of zero and saves an approval change with it", async () => {
    let stored: Record<string, unknown> = {
      user_id: "7",
      limit_mode: "custom",
      max_requests: 0,
      window_days: 7,
      approval_mode: "auto",
    };
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({
        body: { group_id: "3", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
      [USER_LIMIT]: () => ({ body: stored, etag: '"zero"' }),
      [PUT_USER_LIMIT]: ({ body }) => {
        stored = { user_id: "7", ...body };
        return { body: stored, etag: '"saved"' };
      },
    });
    const user = userEvent.setup();
    mount();
    expect(await nowLine()).toHaveTextContent(
      "Now: no new requests (0 per 7 days), approved automatically (set on this account)",
    );
    const max = screen.getByRole("spinbutton", { name: "Requests allowed" });
    expect(max).toHaveValue(0);
    expect(max).toHaveAccessibleDescription(
      "0 stops new requests; to block this account, turn off Media requests instead.",
    );
    expect(screen.queryByRole("alert")).toBeNull();

    await pick(user, "Approval", "An admin approves");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls(PUT_USER_LIMIT)).toHaveLength(1));
    expect(calls(PUT_USER_LIMIT)[0]).toMatchObject({
      headers: { "If-Match": '"zero"' },
      body: { limit_mode: "custom", max_requests: 0, window_days: 7, approval_mode: "manual" },
    });
    await waitFor(async () =>
      expect(await nowLine()).toHaveTextContent(
        "Now: no new requests (0 per 7 days), an admin approves (set on this account)",
      ),
    );
  });

  it("ties errors and hints to their fields, and announces an error once typed", async () => {
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({
        body: { group_id: "3", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
      [USER_LIMIT]: () => ({
        body: {
          user_id: "7",
          limit_mode: "custom",
          max_requests: 4,
          window_days: 2,
          approval_mode: "inherit",
        },
      }),
    });
    const user = userEvent.setup();
    mount();
    expect(await screen.findByRole("combobox", { name: "Approval" })).toHaveAccessibleDescription(
      "Kids group uses the server default: approved automatically",
    );
    const window = screen.getByRole("spinbutton", { name: "Days in the limit window" });
    await user.clear(window);
    await user.type(window, "0");
    expect(screen.getByRole("alert")).toHaveTextContent("Use at least 1 day.");
    expect(window).toHaveAccessibleDescription("Use at least 1 day.");
    expect(window).toBeInvalid();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("shows a row an old setting still blocks and resets it to the default", async () => {
    let limit: Record<string, unknown> = {
      user_id: "7",
      limit_mode: "blocked",
      approval_mode: "auto",
      ...INHERIT,
    };
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({
        body: { group_id: "3", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
      [USER_LIMIT]: () => ({ body: limit, etag: '"blocked"' }),
      [PUT_USER_LIMIT]: ({ body }) => {
        limit = { user_id: "7", ...body };
        return { body: limit, etag: '"cleared"' };
      },
    });
    const user = userEvent.setup();
    mount();
    expect(await nowLine()).toHaveTextContent(
      "Now: can't request, because an old request setting blocks this account",
    );
    // A standing note, not an alert announced on every load.
    const notice = screen.getByRole("note", { name: "Blocked by an old setting" });
    expect(notice).toHaveTextContent("Reset it to the default");
    expect(screen.queryByRole("alert")).toBeNull();
    // The editor waits until the old setting is gone.
    expect(screen.queryByRole("combobox", { name: "Limit" })).toBeNull();

    await user.click(within(notice).getByRole("button", { name: "Reset to default" }));
    await waitFor(() => expect(calls(PUT_USER_LIMIT)).toHaveLength(1));
    expect(calls(PUT_USER_LIMIT)[0]).toMatchObject({
      headers: { "If-Match": '"blocked"' },
      body: { limit_mode: "inherit", approval_mode: "auto", max_requests: null, window_days: null },
    });
    expect(await screen.findByRole("combobox", { name: "Limit" })).toHaveTextContent(
      "Use group default",
    );
    expect(await nowLine()).toHaveTextContent(
      "Now: 12 requests per 14 days (server default), approved automatically (set on this account)",
    );
    expect(screen.queryByText("Blocked by an old setting")).toBeNull();
  });

  it("names the switch when requests are off for the account", async () => {
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({
        body: { group_id: "3", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
      [USER_LIMIT]: () => ({
        body: { user_id: "7", limit_mode: "inherit", approval_mode: "auto", ...INHERIT },
      }),
    });
    mount({
      ...USER,
      requests_allowed: false,
      effective_policy: { ...USER.effective_policy, requests_allowed: false },
    });
    expect(await nowLine()).toHaveTextContent(
      "Now: can't request, because Media Requests is off for this account",
    );
  });
});
