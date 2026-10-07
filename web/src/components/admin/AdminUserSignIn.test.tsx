// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { AdminUser, PluginInstallation } from "@/api/types";
import { v2, V2ProblemError } from "@/api/v2/request";

import { AdminUserSignIn } from "./AdminUserSignIn";

const state = vi.hoisted(() => ({
  installations: [] as unknown[],
  getAdminUser: vi.fn(),
  updateAdminUser: vi.fn(),
}));

vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/v2/adminUsers", async (original) => ({
  ...(await original<typeof import("@/api/v2/adminUsers")>()),
  getAdminUser: state.getAdminUser,
  updateAdminUser: state.updateAdminUser,
}));
vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({ data: state.installations, isLoading: false }),
}));

class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const USER = {
  id: 7,
  username: "alice",
  email: "alice@example.test",
  role: "user",
  enabled: true,
  password_login: false,
  password_change_required: false,
  is_owner: false,
  break_glass: false,
} as AdminUser;

const IDENTITY = {
  id: "4",
  installation_id: "5",
  provider_id: "plugin:5:oidc",
  provider_name: "Company SSO",
  username: "alice",
  email: "alice@idp.example.test",
  display_name: "Alice Example",
  external_subject: "https://id.example.test/realms/silo|8f14e45f",
  issuer: "https://id.example.test/realms/silo",
  linked_at: "2026-09-01T10:00:00.000Z",
  last_sign_in_at: "2026-09-20T10:00:00.000Z",
  last_checked_at: "2026-09-29T10:00:00.000Z",
  last_check_status: "active",
};

const OIDC = {
  id: 5,
  plugin_id: "silo.auth.oidc",
  enabled: true,
  capabilities: [{ type: "auth_provider.v1", id: "oidc", display_name: "Single sign-on" }],
  presentation: { display_name: "OpenID Connect Sign-in" },
  auth_bindings: [{ capability_id: "oidc", enabled: true }],
} as unknown as PluginInstallation;
const LDAP = {
  id: 6,
  plugin_id: "silo.auth.ldap",
  enabled: true,
  capabilities: [{ type: "auth_provider.v1", id: "ldap", display_name: "LDAP" }],
  presentation: { display_name: "LDAP Sign-in" },
  auth_bindings: [{ capability_id: "ldap", enabled: false }],
} as unknown as PluginInstallation;
const TAILSCALE = {
  id: 7,
  plugin_id: "silo.network.tailscale",
  enabled: true,
  capabilities: [
    {
      type: "auth_provider.v1",
      id: "tailscale",
      display_name: "Tailscale",
      sign_in_mode: "network",
    },
  ],
  presentation: { display_name: "Tailscale" },
  auth_bindings: [{ capability_id: "tailscale", enabled: true }],
} as unknown as PluginInstallation;

let capabilities: Record<string, unknown>;
let identities: Array<typeof IDENTITY>;
let failures: Record<string, unknown>;
let serverPasswordLogin: string;
const calls: Array<{ op: string; options?: { body?: unknown; path?: unknown } }> = [];

function problem(type: string, status: number, detail = `raw ${type}`) {
  return new V2ProblemError("op", {
    type: `https://siloserver.org/docs/api/v2/problems/${type}`,
    title: type,
    status,
    detail,
  } as never);
}

beforeEach(() => {
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  vi.stubGlobal("ResizeObserver", MockResizeObserver);
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
  state.installations = [OIDC, LDAP];
  state.getAdminUser.mockReset().mockImplementation(async () => ({
    user: USER,
    etag: '"e1"',
    profileContext: (await import("@/api/client")).captureProfileRequestContext()!,
  }));
  state.updateAdminUser.mockReset().mockResolvedValue(undefined);
  capabilities = {
    available: true,
    identities: true,
    admin_identities: true,
    break_glass: true,
    connection_test: true,
    live_provider_changes: true,
    provider_recheck: true,
    revision: "r1",
    state: "available",
  };
  identities = [IDENTITY];
  failures = {};
  serverPasswordLogin = "true";
  calls.length = 0;
  vi.mocked(v2).mockImplementation(((op: string, options?: { body?: unknown }) => {
    calls.push({ op, options });
    if (failures[op]) return Promise.reject(failures[op]);
    switch (op) {
      case "GET /api/v2/auth/external-sign-in/capabilities":
        return Promise.resolve(capabilities);
      case "GET /api/v2/admin/settings/{key}":
        return Promise.resolve({ key: "auth.local_password_login", value: serverPasswordLogin });
      case "GET /api/v2/admin/users/{id}/identities":
        return Promise.resolve({ items: identities });
      case "POST /api/v2/admin/users/{id}/identities":
        identities = [{ ...IDENTITY, id: "9" }];
        return Promise.resolve(identities[0]);
      case "DELETE /api/v2/admin/users/{id}/identities/{identity_id}":
        identities = [];
        return Promise.resolve(undefined);
    }
    return Promise.reject(new Error(`unexpected ${op}`));
  }) as never);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mount(
  user: AdminUser = USER,
  options: { manageable?: boolean; viewerIsOwner?: boolean } = {},
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AdminUserSignIn
          user={user}
          manageable={options.manageable ?? true}
          viewerIsOwner={options.viewerIsOwner ?? true}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const opCalls = (op: string) => calls.filter((call) => call.op === op);

describe("AdminUserSignIn identities", () => {
  it("lists the linked identity with its last provider check", async () => {
    mount();
    const list = await screen.findByRole("list", { name: "Connected identities" });
    const row = within(list).getByRole("listitem");
    expect(screen.queryByRole("switch", { name: "Break-glass account" })).toBeNull();
    expect(row).toHaveTextContent("Company SSO · alice · alice@idp.example.test");
    expect(row).toHaveTextContent("Subject: https://id.example.test/realms/silo|8f14e45f");
    expect(row).toHaveTextContent("Last provider check: Active at the provider");
    expect(opCalls("GET /api/v2/admin/users/{id}/identities")[0]?.options?.path).toEqual({
      id: "7",
    });
  });

  it("marks a provider that signed the account out", async () => {
    identities = [{ ...IDENTITY, last_check_status: "disabled", provider_name: "" }];
    mount();
    expect(await screen.findByText(/Disabled at the provider \(signed out\)/)).toBeInTheDocument();
    expect(screen.getByText(/Provider not enabled/)).toBeInTheDocument();
  });

  it("leaves the check out when the server doesn't re-check", async () => {
    capabilities = { ...capabilities, provider_recheck: false };
    mount();
    await screen.findByRole("list", { name: "Connected identities" });
    expect(screen.queryByText(/Last provider check/)).toBeNull();
  });

  it("says when nothing is linked, and hides identities the server doesn't serve", async () => {
    identities = [];
    const { unmount } = mount();
    expect(await screen.findByText("Not connected to a sign-in provider.")).toBeInTheDocument();
    unmount();
    capabilities = { ...capabilities, admin_identities: false };
    mount();
    await waitFor(() =>
      expect(opCalls("GET /api/v2/auth/external-sign-in/capabilities").length).toBeGreaterThan(1),
    );
    expect(screen.queryByRole("heading", { name: "Sign-in provider identities" })).toBeNull();
    expect(opCalls("GET /api/v2/admin/users/{id}/identities")).toHaveLength(1);
  });

  it("reports an identity list that failed to load", async () => {
    failures["GET /api/v2/admin/users/{id}/identities"] = problem("internal_error", 500, "boom");
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
  });

  it("warns that unlinking the only way in locks the account out, then unlinks", async () => {
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Unlink Company SSO (alice)" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(
      "alice won't be able to sign in until you set a password or connect a provider again.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Unlink" }));
    await waitFor(() =>
      expect(opCalls("DELETE /api/v2/admin/users/{id}/identities/{identity_id}")).toHaveLength(1),
    );
    expect(
      opCalls("DELETE /api/v2/admin/users/{id}/identities/{identity_id}")[0]?.options?.path,
    ).toEqual({ id: "7", identity_id: "4" });
    expect(await screen.findByText("Unlinked Company SSO from alice.")).toBeInTheDocument();
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole("heading", { name: "Sign-in provider identities" }),
      ),
    );
  });

  it("does not warn about a lockout when a password still signs in", async () => {
    const user = userEvent.setup();
    mount({ ...USER, password_login: true });
    await user.click(await screen.findByRole("button", { name: /Unlink Company SSO/ }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).not.toHaveTextContent("won't be able to sign in");
  });

  it("explains an unlink the owner rule refuses", async () => {
    const user = userEvent.setup();
    failures["DELETE /api/v2/admin/users/{id}/identities/{identity_id}"] = problem(
      "permission_denied",
      403,
    );
    mount();
    await user.click(await screen.findByRole("button", { name: /Unlink Company SSO/ }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Unlink" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Only the server owner can change another admin's sign-in.",
    );
  });

  it("hides changes from an admin who may not manage the account", async () => {
    mount({ ...USER, role: "admin" }, { manageable: false, viewerIsOwner: false });
    await screen.findByRole("list", { name: "Connected identities" });
    expect(screen.queryByRole("button", { name: /Unlink/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Connect identity" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Set a password" })).toBeNull();
    expect(
      screen.getByText("Only the server owner can change another admin's sign-in."),
    ).toBeInTheDocument();
  });
});

describe("AdminUserSignIn linking by subject", () => {
  it("says a network sign-in keeps the account's password", async () => {
    const user = userEvent.setup();
    identities = [];
    state.installations = [TAILSCALE];
    mount({ ...USER, password_login: true });
    await user.click(await screen.findByRole("button", { name: "Connect identity" }));
    const dialog = await screen.findByRole("dialog", { name: "Connect a sign-in identity" });
    expect(dialog).toHaveTextContent("It keeps its Silo password too.");
    expect(dialog).not.toHaveTextContent("Connecting turns off password sign-in");
  });

  it("promises no password to an account without one when linking a network sign-in", async () => {
    const user = userEvent.setup();
    identities = [];
    state.installations = [TAILSCALE];
    mount({ ...USER, password_login: false });
    await user.click(await screen.findByRole("button", { name: "Connect identity" }));
    const dialog = await screen.findByRole("dialog", { name: "Connect a sign-in identity" });
    expect(dialog).not.toHaveTextContent("Silo password");
    expect(dialog).not.toHaveTextContent("turns off password sign-in");
  });

  it("links an identity to the enabled provider by its exact subject", async () => {
    const user = userEvent.setup();
    identities = [];
    mount();
    await user.click(await screen.findByRole("button", { name: "Connect identity" }));
    const dialog = await screen.findByRole("dialog", { name: "Connect a sign-in identity" });
    expect(dialog).toHaveTextContent("Connecting turns off password sign-in for this account.");
    expect(within(dialog).getByRole("combobox", { name: "Provider" })).toHaveTextContent(
      "OpenID Connect Sign-in",
    );
    const subject = within(dialog).getByLabelText("Subject");
    expect(subject).toHaveFocus();
    await user.type(subject, "  https://id.example.test/realms/silo|abc  ");
    await user.type(within(dialog).getByLabelText("Username"), "alice");
    await user.click(within(dialog).getByRole("button", { name: "Connect" }));
    await waitFor(() =>
      expect(opCalls("POST /api/v2/admin/users/{id}/identities")).toHaveLength(1),
    );
    expect(opCalls("POST /api/v2/admin/users/{id}/identities")[0]?.options?.body).toEqual({
      installation_id: "5",
      external_subject: "https://id.example.test/realms/silo|abc",
      username: "alice",
    });
    expect(
      await screen.findByText("alice is connected to OpenID Connect Sign-in."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("requires a subject", async () => {
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Connect identity" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Connect" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Enter the provider's subject for this person.",
    );
    expect(opCalls("POST /api/v2/admin/users/{id}/identities")).toHaveLength(0);
  });

  it.each([
    ["identity_linked_elsewhere", 409, "already connected to another Silo account"],
    ["conflict", 409, "alice is already connected to this provider"],
    ["permission_denied", 403, "Only the server owner can change another admin's sign-in."],
    ["validation_failed", 422, "raw validation_failed"],
  ])("explains a %s refusal", async (type, status, text) => {
    const user = userEvent.setup();
    failures["POST /api/v2/admin/users/{id}/identities"] = problem(type, status);
    mount();
    await user.click(await screen.findByRole("button", { name: "Connect identity" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("Subject"), "subject-1");
    await user.click(within(dialog).getByRole("button", { name: "Connect" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(text);
  });

  it("points to the Sign-in settings when no provider is set up", async () => {
    const user = userEvent.setup();
    state.installations = [];
    mount();
    await user.click(await screen.findByRole("button", { name: "Connect identity" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("link", { name: "Settings → Sign-in" })).toHaveAttribute(
      "href",
      "/admin/settings/sign-in",
    );
    expect(within(dialog).getByRole("button", { name: "Connect" })).toBeDisabled();
  });

  it("tells a break-glass admin its password stays", async () => {
    const user = userEvent.setup();
    mount({ ...USER, role: "admin", break_glass: true, password_login: true });
    await user.click(await screen.findByRole("button", { name: "Connect identity" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(
      "As a break-glass account it keeps its Silo password.",
    );
  });
});

describe("AdminUserSignIn password and break-glass", () => {
  it("sets a temporary password on an account without password sign-in", async () => {
    const user = userEvent.setup();
    mount();
    expect(screen.getByText("Off: alice can't sign in with a Silo password.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Set a password" }));
    const dialog = await screen.findByRole("dialog", { name: "Set a password" });
    const input = within(dialog).getByLabelText("New password");
    expect(input).toHaveFocus();
    await user.type(input, "short");
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent("Use at least 8 characters.");
    expect(state.updateAdminUser).not.toHaveBeenCalled();

    await user.type(input, "-enough");
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));
    await waitFor(() => expect(state.updateAdminUser).toHaveBeenCalledTimes(1));
    expect(state.updateAdminUser.mock.calls[0]![1]).toEqual({
      password: "short-enough",
      require_password_change: true,
    });
    expect(
      await screen.findByText(/alice can sign in with the new password and must change it/),
    ).toBeInTheDocument();
  });

  it("can set a password that isn't temporary, and reports a refusal", async () => {
    const user = userEvent.setup();
    state.updateAdminUser.mockRejectedValueOnce(problem("precondition_failed", 412));
    mount();
    await user.click(screen.getByRole("button", { name: "Set a password" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("switch", { name: "Require change at next sign-in" }),
    );
    await user.type(within(dialog).getByLabelText("New password"), "long-enough-1");
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "The account changed while you were editing it.",
    );
    expect(state.updateAdminUser.mock.calls[0]![1]).toEqual({ password: "long-enough-1" });
  });

  it("counts password limits the way the server does and shows its field detail", async () => {
    const user = userEvent.setup();
    const detail = "The password must be at most 72 bytes.";
    state.updateAdminUser.mockRejectedValueOnce(
      new V2ProblemError("op", {
        type: "https://siloserver.org/docs/api/v2/problems/validation_failed",
        title: "Validation failed",
        status: 422,
        detail: "The request is not valid.",
        errors: [{ location: "body.password", code: "invalid", detail }],
      } as never),
    );
    mount();
    await user.click(screen.getByRole("button", { name: "Set a password" }));
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByLabelText("New password");
    // Eight emoji: 16 UTF-16 units and 32 bytes, but eight characters.
    await user.type(input, "😀😀😀😀😀😀😀");
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent("Use at least 8 characters.");
    await user.type(input, "😀");
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(detail);
    // 19 four-byte characters: 76 bytes, over the server's limit.
    await user.clear(input);
    await user.type(input, "😀".repeat(19));
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent("Use no more than 72 bytes.");
    expect(state.updateAdminUser).toHaveBeenCalledTimes(1);
  });

  it("says a password waits for the server switch while password sign-in is off", async () => {
    serverPasswordLogin = "false";
    mount({ ...USER, password_login: true });
    expect(
      await screen.findByText(/On for this account, but password sign-in is off for the server/),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings → Sign-in" })).toHaveAttribute(
      "href",
      "/admin/settings/sign-in",
    );
  });

  it("tells the admin a new password works once the server allows passwords", async () => {
    const user = userEvent.setup();
    serverPasswordLogin = "false";
    mount();
    await waitFor(() => expect(opCalls("GET /api/v2/admin/settings/{key}")).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "Set a password" }));
    const dialog = await screen.findByRole("dialog", { name: "Set a password" });
    expect(dialog).toHaveTextContent("Password sign-in is off for the server");
    await user.type(within(dialog).getByLabelText("New password"), "long-enough-1");
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));
    expect(
      await screen.findByText(/once password sign-in is on for the server/),
    ).toBeInTheDocument();
  });

  it("lets the owner make an admin break-glass", async () => {
    const user = userEvent.setup();
    mount({ ...USER, role: "admin", password_login: true });
    const toggle = await screen.findByRole("switch", { name: "Break-glass account" });
    expect(toggle).not.toBeChecked();
    await user.click(toggle);
    await waitFor(() => expect(state.updateAdminUser).toHaveBeenCalledTimes(1));
    expect(state.getAdminUser).toHaveBeenCalledWith(7, expect.anything());
    expect(state.updateAdminUser.mock.calls[0]![1]).toEqual({ break_glass: true });
    const status = await screen.findByText("alice is a break-glass account.");
    await waitFor(() => expect(document.activeElement).toBe(status));
  });

  it("explains clearing the last break-glass admin being refused", async () => {
    const user = userEvent.setup();
    state.updateAdminUser.mockRejectedValueOnce(problem("break_glass_required", 409));
    mount({ ...USER, role: "admin", password_login: true, break_glass: true });
    await user.click(await screen.findByRole("switch", { name: "Break-glass account" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "would leave no break-glass admin who can still sign in with a password",
    );
  });

  it("keeps break-glass read-only for anyone but the owner", async () => {
    mount({ ...USER, role: "admin", break_glass: true }, { viewerIsOwner: false });
    expect(await screen.findByRole("switch", { name: "Break-glass account" })).toBeDisabled();
    expect(screen.getByText("Only the server owner can change this.")).toBeInTheDocument();
    expect(screen.getByText(/can't act as break-glass/)).toBeInTheDocument();
  });

  it("explains that the server owner is break-glass by default", async () => {
    mount({ ...USER, role: "admin", is_owner: true, password_login: true, break_glass: true });
    expect(await screen.findByRole("switch", { name: "Break-glass account" })).toBeChecked();
    expect(screen.getByText(/server owner is break-glass by default/)).toBeInTheDocument();
  });

  it("leaves the owner note off other admins", async () => {
    mount({ ...USER, role: "admin", password_login: true, break_glass: true });
    await screen.findByRole("switch", { name: "Break-glass account" });
    expect(screen.queryByText(/server owner is break-glass by default/)).toBeNull();
  });

  it("hides break-glass on a server that doesn't support it", async () => {
    capabilities = { ...capabilities, break_glass: false };
    mount({ ...USER, role: "admin" });
    await screen.findByRole("list", { name: "Connected identities" });
    expect(screen.queryByRole("switch", { name: "Break-glass account" })).toBeNull();
  });
});
