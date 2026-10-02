// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AccessGroup, AdminUser } from "@/api/types";
import { v2, V2ProblemError } from "@/api/v2/request";
import { policyInheritHints, savedUserPolicyInheritHints } from "@/components/UserPolicyFields";

import { CardEditingProvider } from "../cardEditing";
import { inheritContextFor } from "./policySources";
import { RequestsCard } from "./RequestsCard";

const mocks = vi.hoisted(() => ({
  updateUser: vi.fn(),
  usage: undefined as unknown,
  user: null as AdminUser | null,
}));

vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));
vi.mock("@/api/v2/adminUsers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/adminUsers")>()),
  getAdminUser: async () => ({ user: mocks.user!, etag: '"account-2"', profileContext: CONTEXT }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUserCapabilities: () => ({ data: { available: true, request_usage: true } }),
  useUpdateUser: () => ({ mutateAsync: mocks.updateUser, isPending: false }),
}));
vi.mock("@/hooks/queries/admin/userActivity", () => ({
  useAdminUserRequestUsage: () => ({ data: mocks.usage }),
}));

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

const CONTEXT = { profileId: "owner" } as unknown as ProfileRequestContextSnapshot;

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
  break_glass: false,
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

const KIDS = {
  id: 3,
  name: "Kids",
  library_ids: null,
  max_playback_quality: "",
  download_allowed: true,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 0,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  allowed_permissions: null,
  requests_allowed: true,
} as AccessGroup;

const SETTINGS = {
  requests_enabled: true,
  global_max_requests: 12,
  global_window_days: 14,
  global_auto_approval_enabled: true,
  force_dual_quality: false,
};
const INHERIT = { max_requests: null, window_days: null };
const GROUP_INHERITS = {
  group_id: "3",
  limit_mode: "inherit",
  approval_mode: "inherit",
  ...INHERIT,
};

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

function mount(user: AdminUser = USER) {
  mocks.user = user;
  const groups = [KIDS];
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <CardEditingProvider>
          <RequestsCard
            user={user}
            editor={{ user, etag: '"account-1"', profileContext: CONTEXT }}
            manageable
            available
            groups={groups}
            libraries={[]}
            ctx={inheritContextFor(user, groups)}
            hints={savedUserPolicyInheritHints(
              user,
              policyInheritHints(user.role === "admin" ? null : user.access_group_id, groups),
            )}
          />
        </CardEditingProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
type Ui = ReturnType<typeof userEvent.setup>;
const cardEl = () => screen.getByRole("region", { name: "Requests" });
/** The value side of a row: value, the value it replaces, and the source tag. */
async function row(label: string) {
  const node = await within(cardEl()).findByText(label, { selector: "div" });
  return node.closest("[class*='justify-between']")?.lastElementChild?.textContent ?? "";
}
async function pick(ui: Ui, combobox: string, option: string) {
  await ui.click(screen.getByRole("combobox", { name: combobox }));
  await ui.click(await screen.findByRole("option", { name: option }));
}
async function edit(ui: Ui) {
  const button = await screen.findByRole("button", { name: "Edit Requests" });
  await waitFor(() => expect(button).toBeEnabled());
  await ui.click(button);
}

const USER_LIMIT = "GET /api/v2/admin/request-users/{user_id}/limit";
const PUT_USER_LIMIT = "PUT /api/v2/admin/request-users/{user_id}/limit";
const GROUP_LIMIT = "GET /api/v2/admin/request-groups/{group_id}/limit";
const SETTINGS_OP = "GET /api/v2/admin/request-settings";

beforeEach(() => {
  vi.mocked(v2).mockReset();
  mocks.updateUser.mockReset().mockResolvedValue(undefined);
  mocks.usage = undefined;
});
afterEach(() => {
  cleanup();
});

describe("RequestsCard", () => {
  it("says what applies and where it comes from, and links to the account's requests", async () => {
    mocks.usage = { unlimited: false, used: 2, max_requests: 5, window_days: 7 };
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
    const ui = userEvent.setup();
    mount();
    expect(await row("Limit")).toBe("5 per 7 days · 2 usedGROUP");
    expect(await row("Approval")).toBe("Needs approvalGROUP");
    expect(await row("Can request media")).toBe("YesGROUP");
    expect(screen.getByRole("link", { name: /Requests/ })).toHaveAttribute(
      "href",
      "/admin/requests?user=7",
    );
    expect(calls(GROUP_LIMIT)[0]?.path).toEqual({ group_id: "3" });

    // Inheriting fields name the group's value; blocking is not an option.
    await edit(ui);
    expect(screen.getByText("Kids group: an admin approves")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Approval" })).toHaveTextContent(
      "Use group default",
    );
    await ui.click(screen.getByRole("combobox", { name: "Approval" }));
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
    const ui = userEvent.setup();
    mount({ ...USER, role: "admin" });
    expect(await row("Limit")).toBe("UnlimitedDefault: 12 per 14 daysCUSTOM");
    expect(await row("Approval")).toBe("Needs approvalDEFAULT");
    expect(calls(GROUP_LIMIT)).toHaveLength(0);
    expect(
      screen.getByText("Admin accounts don't use an access group's request settings."),
    ).toBeInTheDocument();
    await edit(ui);
    expect(screen.getByRole("combobox", { name: "Limit" })).toHaveTextContent("No limit");
    expect(screen.getByRole("combobox", { name: "Approval" })).toHaveTextContent(
      "Use server default",
    );
  });

  it("saves the account's approval and limit with the validator it read, and reloads after a conflict", async () => {
    let stored: Reply = {
      body: { user_id: "7", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      etag: '"initial"',
    };
    let writes = 0;
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({ body: GROUP_INHERITS }),
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
    const ui = userEvent.setup();
    mount();
    expect(await row("Limit")).toBe("12 per 14 daysDEFAULT");
    await edit(ui);
    expect(screen.getByText("Kids group uses the server default: 12 requests per 14 days"));

    await pick(ui, "Approval", "An admin approves");
    await pick(ui, "Limit", "Custom limit");
    // A new custom limit starts from what the account had.
    const max = screen.getByRole("spinbutton", { name: "Requests allowed" });
    expect(max).toHaveValue(12);
    await ui.clear(max);
    await ui.type(max, "3");
    expect(within(cardEl()).getByText("2 unsaved changes · Approval, Limit")).toBeInTheDocument();
    await ui.click(screen.getByRole("button", { name: "Save" }));

    expect(
      await screen.findByText(/Another admin changed this account after you started/),
    ).toBeInTheDocument();
    expect(calls(PUT_USER_LIMIT)[0]).toMatchObject({
      path: { user_id: "7" },
      headers: { "If-Match": '"initial"' },
      body: { limit_mode: "custom", max_requests: 3, window_days: 14, approval_mode: "manual" },
    });
    // The edits stay until an explicit reload, and stay after it too.
    expect(screen.getByRole("spinbutton", { name: "Requests allowed" })).toHaveValue(3);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();

    await ui.click(screen.getByRole("button", { name: "Reload and review" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
    expect(screen.getByRole("spinbutton", { name: "Requests allowed" })).toHaveValue(3);
    await ui.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls(PUT_USER_LIMIT)).toHaveLength(2));
    expect(calls(PUT_USER_LIMIT)[1]).toMatchObject({
      headers: { "If-Match": '"reloaded"' },
      body: { limit_mode: "custom", max_requests: 3, window_days: 14, approval_mode: "manual" },
    });
    // Nothing on the account itself changed, so no account PUT.
    expect(mocks.updateUser).not.toHaveBeenCalled();
    expect(await row("Limit")).toBe("3 per 14 daysDefault: 12 per 14 daysCUSTOM");
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
      [GROUP_LIMIT]: () => ({ body: GROUP_INHERITS }),
      [USER_LIMIT]: () => ({ body: stored, etag: '"zero"' }),
      [PUT_USER_LIMIT]: ({ body }) => {
        stored = { user_id: "7", ...body };
        return { body: stored, etag: '"saved"' };
      },
    });
    const ui = userEvent.setup();
    mount();
    expect(await row("Limit")).toMatch(/^0 per 7 days/);
    await edit(ui);
    const max = screen.getByRole("spinbutton", { name: "Requests allowed" });
    expect(max).toHaveValue(0);
    expect(max).toHaveAccessibleDescription(
      "0 stops new requests; to block this account, turn off Media requests instead.",
    );
    expect(screen.queryByRole("alert")).toBeNull();

    await pick(ui, "Approval", "An admin approves");
    await ui.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls(PUT_USER_LIMIT)).toHaveLength(1));
    expect(calls(PUT_USER_LIMIT)[0]).toMatchObject({
      headers: { "If-Match": '"zero"' },
      body: { limit_mode: "custom", max_requests: 0, window_days: 7, approval_mode: "manual" },
    });
    expect(await row("Approval")).toMatch(/^Needs approval/);
  });

  it("ties errors and hints to their fields, and announces an error once typed", async () => {
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({ body: GROUP_INHERITS }),
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
    const ui = userEvent.setup();
    mount();
    await edit(ui);
    expect(screen.getByRole("combobox", { name: "Approval" })).toHaveAccessibleDescription(
      "Kids group uses the server default: approved automatically",
    );
    const window = screen.getByRole("spinbutton", { name: "Days in the limit window" });
    await ui.clear(window);
    await ui.type(window, "0");
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
      [GROUP_LIMIT]: () => ({ body: GROUP_INHERITS }),
      [USER_LIMIT]: () => ({ body: limit, etag: '"blocked"' }),
      [PUT_USER_LIMIT]: ({ body }) => {
        limit = { user_id: "7", ...body };
        return { body: limit, etag: '"cleared"' };
      },
    });
    const ui = userEvent.setup();
    mount();
    // A standing note, not an alert announced on every load.
    const notice = await screen.findByRole("note", { name: "Blocked by an old setting" });
    expect(notice).toHaveTextContent("Reset it to the default");
    expect(screen.queryByRole("alert")).toBeNull();

    await ui.click(within(notice).getByRole("button", { name: "Reset to default" }));
    await waitFor(() => expect(calls(PUT_USER_LIMIT)).toHaveLength(1));
    expect(calls(PUT_USER_LIMIT)[0]).toMatchObject({
      headers: { "If-Match": '"blocked"' },
      body: { limit_mode: "inherit", approval_mode: "auto", max_requests: null, window_days: null },
    });
    expect(await row("Limit")).toBe("12 per 14 daysDEFAULT");
    expect(await row("Approval")).toBe("AutomaticDefault: automaticCUSTOM");
    expect(screen.queryByText("Blocked by an old setting")).toBeNull();
  });

  it("turns requests off for the account through the account save", async () => {
    serve({
      [SETTINGS_OP]: () => ({ body: SETTINGS }),
      [GROUP_LIMIT]: () => ({ body: GROUP_INHERITS }),
      [USER_LIMIT]: () => ({
        body: { user_id: "7", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
    });
    const ui = userEvent.setup();
    mount();
    await edit(ui);
    await ui.click(
      within(screen.getByRole("group", { name: "Can request media" })).getByRole("button", {
        name: "Custom",
      }),
    );
    await pick(ui, "Can request media", "No");
    await ui.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateUser).toHaveBeenCalledTimes(1));
    expect(mocks.updateUser.mock.calls[0]![0]).toMatchObject({
      editor: { etag: '"account-1"' },
      body: { requests_allowed: false },
    });
    expect(calls(PUT_USER_LIMIT)).toHaveLength(0);
  });

  it("names the account's switch and the server switch", async () => {
    serve({
      [SETTINGS_OP]: () => ({ body: { ...SETTINGS, requests_enabled: false } }),
      [GROUP_LIMIT]: () => ({ body: GROUP_INHERITS }),
      [USER_LIMIT]: () => ({
        body: { user_id: "7", limit_mode: "inherit", approval_mode: "auto", ...INHERIT },
      }),
    });
    mount({
      ...USER,
      requests_allowed: false,
      effective_policy: { ...USER.effective_policy, requests_allowed: false },
    });
    expect(await row("Can request media")).toBe("NoGroup: yesCUSTOM");
    expect(
      await screen.findByText("Requests are turned off for the whole server."),
    ).toBeInTheDocument();
  });

  it("offers a retry when the request settings can't load", async () => {
    serve({
      [SETTINGS_OP]: () => new Error("down"),
      [GROUP_LIMIT]: () => ({ body: GROUP_INHERITS }),
      [USER_LIMIT]: () => ({
        body: { user_id: "7", limit_mode: "inherit", approval_mode: "inherit", ...INHERIT },
      }),
    });
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Couldn't load this account's request settings.",
    );
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});
