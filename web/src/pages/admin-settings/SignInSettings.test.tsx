// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { AdminUser, PluginInstallation } from "@/api/types";
import { v2, V2ProblemError } from "@/api/v2/request";
import { BREAK_GLASS_REQUIRED_TEXT } from "@/lib/externalSignInAdmin";

import SignInSettings from "./SignInSettings";

const state = vi.hoisted(() => ({
  values: {} as Record<string, string>,
  dirty: new Set<string>(),
  setValue: vi.fn(),
  save: vi.fn(),
  discard: vi.fn(),
  installations: undefined as unknown,
  installationsLoading: false,
  installationsError: false,
  users: undefined as unknown,
  copy: vi.fn(),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: () => ({
    isLoading: false,
    getValue: (key: string) => state.values[key] ?? "",
    getPersistedValue: (key: string) => state.values[key] ?? "",
    setValue: state.setValue,
    isDirty: (key: string) => state.dirty.has(key),
    dirtyCount: state.dirty.size,
    save: state.save,
    discard: state.discard,
    isSaving: false,
  }),
}));
vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({
    data: state.installations,
    isLoading: state.installationsLoading,
    isError: state.installationsError,
  }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({ data: state.users }),
}));
vi.mock("@/lib/clipboard", () => ({ copyTextToClipboard: state.copy }));
vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));

const STAMP = "2026-09-01T00:00:00.000Z";

function oidcInstallation(overrides: Partial<PluginInstallation> = {}): PluginInstallation {
  return {
    id: 5,
    plugin_id: "silo.auth.oidc",
    version: "0.1.0",
    install_path: "/plugins/oidc",
    enabled: true,
    runtime: {},
    capabilities: [
      {
        type: "auth_provider.v1",
        id: "oidc",
        display_name: "Single sign-on",
        metadata: { connection_test: true },
        sign_in_mode: "oauth",
        callback_url: "https://silo.example.test/api/v2/auth/oauth/5/callback",
        post_logout_redirect_url: "https://silo.example.test/login",
      },
    ],
    presentation: { display_name: "OpenID Connect Sign-in" },
    global_config_schema: [
      {
        key: "connection",
        title: "Provider connection",
        json_schema: "{}",
        required: true,
        admin_form: {
          fields: [
            { key: "issuer_url", label: "Issuer URL", control: "TEXT" },
            { key: "client_secret", label: "Client secret", control: "PASSWORD", secret: true },
            { key: "provider_logout", label: "Sign out at the provider", control: "SWITCH" },
          ],
        },
      },
    ],
    global_configs: [
      {
        key: "connection",
        value: { issuer_url: "https://id.example.test/realms/silo", provider_logout: false },
        configured_secrets: ["client_secret"],
      },
    ],
    user_config_schema: [],
    routes: [],
    assets: [],
    auth_bindings: [
      {
        capability_id: "oidc",
        enabled: true,
        display_order: 3,
        auto_provision: true,
        default_login: false,
        callback_url: "https://silo.example.test/api/v2/auth/oauth/5/callback",
        post_logout_redirect_url: "https://silo.example.test/login",
        created_at: STAMP,
        updated_at: STAMP,
      },
    ],
    task_bindings: [],
    update_policy: "manual",
    source_kind: "silo",
    updates_paused: false,
    ...overrides,
  } as unknown as PluginInstallation;
}

function ldapInstallation(overrides: Partial<PluginInstallation> = {}): PluginInstallation {
  return {
    ...oidcInstallation(),
    id: 6,
    plugin_id: "silo.auth.ldap",
    capabilities: [
      {
        type: "auth_provider.v1",
        id: "ldap",
        display_name: "LDAP",
        metadata: { connection_test: true },
        sign_in_mode: "credentials",
      },
    ],
    presentation: { display_name: "LDAP Sign-in" },
    global_config_schema: [
      {
        key: "directory",
        title: "Directory",
        json_schema: "{}",
        required: false,
        admin_form: { fields: [{ key: "url", label: "Directory URL", control: "TEXT" }] },
      },
    ],
    global_configs: [],
    auth_bindings: [
      {
        capability_id: "ldap",
        enabled: false,
        display_order: 1,
        auto_provision: false,
        default_login: false,
        callback_url: "",
        post_logout_redirect_url: "",
        created_at: STAMP,
        updated_at: STAMP,
      },
    ],
    ...overrides,
  } as unknown as PluginInstallation;
}

function admin(overrides: Partial<AdminUser>): AdminUser {
  return {
    id: 1,
    username: "root",
    role: "admin",
    enabled: true,
    password_login: true,
    break_glass: false,
    ...overrides,
  } as AdminUser;
}

let capabilities: Record<string, unknown>;
let failures: Record<string, unknown>;
let testResult: unknown;
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
  state.values = {
    "auth.local_password_login": "true",
    "auth.email_auto_match": "false",
    "auth.provider_recheck_interval": "12h",
    "auth.provider_recheck_outage_policy": "fail_open",
    "server.public_url": "https://silo.example.test",
  };
  state.dirty = new Set();
  state.setValue.mockReset();
  state.save.mockReset().mockResolvedValue(undefined);
  state.discard.mockReset();
  state.installations = [oidcInstallation(), ldapInstallation()];
  state.installationsLoading = false;
  state.installationsError = false;
  state.users = [admin({ id: 1, username: "root", break_glass: true })];
  state.copy.mockReset().mockResolvedValue(undefined);
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
  failures = {};
  testResult = {
    ok: false,
    callback_url: "https://silo.example.test/api/v2/auth/oauth/6/callback",
    steps: [
      { id: "discovery", label: "Discovery document reachable", ok: true, message: "Loaded" },
      { id: "client", label: "Client credentials accepted", ok: false, message: "invalid_client" },
    ],
  };
  calls.length = 0;
  vi.mocked(v2).mockImplementation(((op: string, options?: { body?: unknown }) => {
    calls.push({ op, options });
    if (failures[op]) return Promise.reject(failures[op]);
    switch (op) {
      case "GET /api/v2/auth/external-sign-in/capabilities":
        return Promise.resolve(capabilities);
      case "GET /api/v2/auth/providers":
        return Promise.resolve({
          items: [
            { id: "plugin:5:oidc", installation_id: "5", mode: "oauth", display_name: "SSO" },
          ],
          password_login: false,
        });
      case "PUT /api/v2/admin/plugins/installations/{id}/auth-binding":
      case "PUT /api/v2/admin/plugins/installations/{id}/config":
        return Promise.resolve(undefined);
      case "POST /api/v2/admin/plugins/installations/{id}/auth-binding/test":
        return Promise.resolve(testResult);
    }
    return Promise.reject(new Error(`unexpected ${op}`));
  }) as never);
});

afterEach(() => {
  cleanup();
  localStorage.clear();
});

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <SignInSettings />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const opCalls = (op: string) => calls.filter((call) => call.op === op);
const providerPanel = (name: string) => screen.getByRole("region", { name }) as HTMLElement;

describe("SignInSettings page", () => {
  it("shows the four groups under one header", () => {
    mount();
    expect(screen.getByRole("heading", { name: "Sign-in" })).toBeInTheDocument();
    for (const group of [
      "Sign-in provider",
      "Password sign-in",
      "Matching accounts",
      "Provider re-check",
    ]) {
      expect(screen.getByRole("group", { name: group })).toBeInTheDocument();
    }
  });

  it("says so when the server has no external sign-in", async () => {
    capabilities = { ...capabilities, available: false, state: "not_configured" };
    mount();
    expect(
      await screen.findByText(/External sign-in isn't available on this server/),
    ).toBeInTheDocument();
  });
});

describe("SignInSettings provider slot", () => {
  it("links to the plugin catalog when no sign-in plugin is installed", () => {
    state.installations = [];
    mount();
    const link = screen.getByRole("link", { name: "plugin catalog" });
    expect(link).toHaveAttribute("href", "/admin/plugins?tab=catalog");
    expect(screen.getByText(/No sign-in plugin is installed/)).toBeInTheDocument();
  });

  it("shows a loading state and a read failure", () => {
    state.installationsLoading = true;
    state.installations = undefined;
    const { unmount } = mount();
    expect(
      screen.getByRole("group", { name: "Sign-in provider" }).querySelector("[aria-busy=true]"),
    ).not.toBeNull();
    unmount();
    state.installationsLoading = false;
    state.installationsError = true;
    mount();
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn't read the installed plugins");
  });

  it("shows the enabled provider first with the URLs to register", async () => {
    mount();
    const panels = screen.getAllByTestId("sign-in-provider");
    expect(
      within(panels[0]!).getByRole("heading", { name: "OpenID Connect Sign-in" }),
    ).toBeTruthy();
    const oidc = providerPanel("OpenID Connect Sign-in");
    expect(within(oidc).getByTestId("sign-in-provider-state")).toHaveTextContent("On");
    expect(within(oidc).getByLabelText("Redirect URI")).toHaveValue(
      "https://silo.example.test/api/v2/auth/oauth/5/callback",
    );
    expect(within(oidc).getByLabelText("Post-logout redirect URI")).toHaveValue(
      "https://silo.example.test/login",
    );
    // The other installed plugin can't be turned on while one is on.
    const ldap = providerPanel("LDAP Sign-in");
    expect(within(ldap).getByRole("button", { name: "Turn on" })).toBeDisabled();
    expect(
      within(ldap).getByText(/OpenID Connect Sign-in is the sign-in provider now/),
    ).toBeTruthy();
    expect(within(ldap).queryByLabelText("Redirect URI")).toBeNull();
    // No redirect URI beside a public URL: a password (LDAP) provider.
    expect(within(ldap).getByText("Directory (LDAP)")).toBeInTheDocument();
    expect(within(oidc).getByText("OpenID Connect")).toBeInTheDocument();
  });

  it("copies a registration URL and reports a copy failure", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(within(oidc).getByRole("button", { name: "Copy redirect uri" }));
    expect(state.copy).toHaveBeenCalledWith(
      "https://silo.example.test/api/v2/auth/oauth/5/callback",
    );
    expect(await within(oidc).findByText("Copied")).toBeInTheDocument();

    state.copy.mockRejectedValueOnce(new Error("denied"));
    await user.click(within(oidc).getByRole("button", { name: "Copy post-logout redirect uri" }));
    expect(await within(oidc).findByText(/Couldn't copy/)).toBeInTheDocument();
  });

  it("warns that OpenID Connect needs the public URL", () => {
    state.values["server.public_url"] = "";
    state.installations = [
      oidcInstallation({
        capabilities: [
          {
            type: "auth_provider.v1",
            id: "oidc",
            display_name: "Single sign-on",
            metadata: { connection_test: true },
            sign_in_mode: "oauth",
            callback_url: "",
            post_logout_redirect_url: "",
          },
        ],
        auth_bindings: [
          {
            capability_id: "oidc",
            enabled: true,
            display_order: 1,
            auto_provision: true,
            default_login: false,
            callback_url: "",
            post_logout_redirect_url: "",
            created_at: STAMP,
            updated_at: STAMP,
          },
        ],
      }),
    ];
    mount();
    expect(screen.getByText(/needs the server's public URL/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "General settings" })).toHaveAttribute(
      "href",
      "/admin/settings/general",
    );
    expect(screen.queryByLabelText("Redirect URI")).toBeNull();
  });

  it("turns a provider on and moves focus to the result", async () => {
    const user = userEvent.setup();
    state.installations = [
      oidcInstallation({
        auth_bindings: [
          {
            capability_id: "oidc",
            enabled: false,
            display_order: 3,
            auto_provision: false,
            default_login: false,
            callback_url: "https://silo.example.test/api/v2/auth/oauth/5/callback",
            post_logout_redirect_url: "https://silo.example.test/login",
            created_at: STAMP,
            updated_at: STAMP,
          },
        ],
      }),
    ];
    mount();
    await user.click(screen.getByRole("button", { name: "Turn on" }));
    await waitFor(() =>
      expect(opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")).toHaveLength(1),
    );
    const call = opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")[0]!;
    expect(call.options?.path).toEqual({ id: "5" });
    expect(call.options?.body).toEqual({
      capability_id: "oidc",
      enabled: true,
      display_order: 3,
      auto_provision: false,
      default_login: false,
    });
    const status = await screen.findByText(/Single sign-on is on/);
    await waitFor(() => expect(document.activeElement).toBe(status));
  });

  it("explains a second provider being refused", async () => {
    const user = userEvent.setup();
    state.installations = [ldapInstallation()];
    failures["PUT /api/v2/admin/plugins/installations/{id}/auth-binding"] = problem(
      "provider_already_enabled",
      409,
    );
    mount();
    await user.click(screen.getByRole("button", { name: "Turn on" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Another sign-in provider is already on",
    );
  });

  it("says when the plugin itself is turned off", async () => {
    state.installations = [ldapInstallation({ enabled: false })];
    mount();
    expect(screen.getByTestId("sign-in-provider-state")).toHaveTextContent("Plugin turned off");
    expect(screen.getByRole("button", { name: "Turn on" })).toBeDisabled();
    expect(await screen.findByRole("button", { name: "Test connection" })).toBeDisabled();
  });

  it("asks before turning the provider off", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(within(oidc).getByRole("button", { name: "Turn off" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("can't sign in until it's back on");
    await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
    await waitFor(() =>
      expect(
        opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")[0]?.options?.body,
      ).toMatchObject({ capability_id: "oidc", enabled: false, auto_provision: true }),
    );
    // Without a saved button label, the capability's name stands in for it.
    expect(await screen.findByText(/Single sign-on is off/)).toBeInTheDocument();
  });

  it("names the provider by its button label when turning it off", async () => {
    const user = userEvent.setup();
    state.installations = [
      oidcInstallation({
        global_configs: [
          ...(oidcInstallation().global_configs ?? []),
          { key: "display_name", value: { value: "Keycloak" }, configured_secrets: [] },
        ],
      }),
    ];
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(within(oidc).getByRole("button", { name: "Turn off" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading")).toHaveTextContent("Turn off Keycloak?");
    expect(dialog).toHaveTextContent("People who sign in with Keycloak can't sign in");
    expect(dialog).not.toHaveTextContent("OpenID Connect Sign-in");
    await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
    expect(
      await screen.findByText("Keycloak is off. Only Silo passwords sign in now."),
    ).toBeInTheDocument();
  });

  it("switches account creation on first sign-in", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    const toggle = within(oidc).getByRole("switch", { name: "Create accounts on first sign-in" });
    expect(toggle).toBeChecked();
    await user.click(toggle);
    await waitFor(() =>
      expect(
        opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")[0]?.options?.body,
      ).toMatchObject({ enabled: true, auto_provision: false }),
    );
    expect(
      await screen.findByText("Only people with a connected account can sign in now."),
    ).toBeInTheDocument();
  });

  it("leaves provider sign-out to the plugin's own configuration form", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    expect(within(oidc).queryByRole("switch", { name: "Sign out at the provider" })).toBeNull();
    await user.click(within(oidc).getByRole("button", { name: /Configuration/ }));
    expect(within(oidc).getByLabelText("Sign out at the provider")).toBeInTheDocument();
  });

  it("keeps each provider's configuration labels on its own inputs", async () => {
    const user = userEvent.setup();
    const shared = {
      key: "display_name",
      title: "Button label",
      json_schema: "{}",
      required: false,
      admin_form: { fields: [{ key: "value", label: "Button label", control: "TEXT" }] },
    };
    state.installations = [
      oidcInstallation({
        global_config_schema: [shared] as never,
        global_configs: [{ key: "display_name", value: { value: "Keycloak" } }] as never,
      }),
      ldapInstallation({
        global_config_schema: [shared] as never,
        global_configs: [{ key: "display_name", value: { value: "Company directory" } }] as never,
      }),
    ];
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    const ldap = providerPanel("LDAP Sign-in");
    await user.click(within(oidc).getByRole("button", { name: /Configuration/ }));
    await user.click(within(ldap).getByRole("button", { name: /Configuration/ }));
    expect(within(oidc).getByLabelText("Button label")).toHaveValue("Keycloak");
    expect(within(ldap).getByLabelText("Button label")).toHaveValue("Company directory");
  });

  it("says what a provider without saved configuration still needs", () => {
    state.installations = [oidcInstallation({ global_configs: [] })];
    mount();
    expect(screen.getByTestId("sign-in-provider-state")).toHaveTextContent("On, needs setup");
    expect(screen.getByText(/Needs setup: save Provider connection/)).toBeInTheDocument();
  });

  it("shows a fresh OpenID Connect install's redirect URI before any binding exists", () => {
    state.installations = [oidcInstallation({ auth_bindings: [] })];
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    expect(within(oidc).getByText("OpenID Connect")).toBeInTheDocument();
    expect(within(oidc).getByLabelText("Redirect URI")).toHaveValue(
      "https://silo.example.test/api/v2/auth/oauth/5/callback",
    );
    expect(within(oidc).getByLabelText("Post-logout redirect URI")).toHaveValue(
      "https://silo.example.test/login",
    );
    expect(within(oidc).queryByText("silo.auth.oidc")).toBeNull();
  });

  it("does not ask an LDAP install for the public URL", () => {
    state.values["server.public_url"] = "";
    state.installations = [ldapInstallation()];
    mount();
    const ldap = providerPanel("LDAP Sign-in");
    expect(within(ldap).getByText("Directory (LDAP)")).toBeInTheDocument();
    expect(within(ldap).queryByText(/needs the server's public URL/)).toBeNull();
    expect(within(ldap).queryByLabelText("Redirect URI")).toBeNull();
  });

  it("shows no redirect URI for an LDAP install after its connection test", async () => {
    const user = userEvent.setup();
    state.installations = [ldapInstallation()];
    // An older server answers the test with a callback URL for any provider.
    testResult = {
      ok: true,
      callback_url: "https://silo.example.test/api/v2/auth/oauth/6/callback",
      steps: [],
    };
    mount();
    const ldap = providerPanel("LDAP Sign-in");
    await user.click(await within(ldap).findByRole("button", { name: "Test connection" }));
    await within(ldap).findByTestId("sign-in-test-result");
    expect(within(ldap).queryByLabelText("Redirect URI")).toBeNull();
  });

  it("keeps focus on a switch that stays on the page", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    const toggle = within(oidc).getByRole("switch", { name: "Create accounts on first sign-in" });
    await user.click(toggle);
    expect(
      await screen.findByText("Only people with a connected account can sign in now."),
    ).toBeInTheDocument();
    await new Promise((resolve) => requestAnimationFrame(resolve));
    expect(document.activeElement).toBe(toggle);
  });

  it("says who can still sign in when the provider goes off with passwords off", async () => {
    const user = userEvent.setup();
    state.values["auth.local_password_login"] = "false";
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(within(oidc).getByRole("button", { name: "Turn off" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("only break-glass admins can sign in");
    await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
    expect(
      await screen.findByText(/Password sign-in is off too, so only break-glass admins/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Only Silo passwords sign in now/)).toBeNull();
  });

  it("runs the connection test and lists every step", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(await within(oidc).findByRole("button", { name: "Test connection" }));
    const result = await within(oidc).findByTestId("sign-in-test-result");
    expect(result).toHaveTextContent("1 of 2 checks failed.");
    const steps = within(result).getAllByRole("listitem");
    expect(steps[0]).toHaveTextContent("Discovery document reachable: passed");
    expect(steps[1]).toHaveTextContent("Client credentials accepted: failed");
    expect(steps[1]).toHaveTextContent("invalid_client");
    const call = opCalls("POST /api/v2/admin/plugins/installations/{id}/auth-binding/test")[0]!;
    expect(call.options?.body).toEqual({ capability_id: "oidc", config: [] });
    await waitFor(() => expect(document.activeElement).toBe(within(result).getByRole("heading")));
  });

  it("tests staged configuration without saving it", async () => {
    const user = userEvent.setup();
    testResult = { ok: true, callback_url: "", steps: [] };
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(within(oidc).getByRole("button", { name: /Configuration/ }));
    const issuer = within(oidc).getByLabelText("Issuer URL");
    await user.clear(issuer);
    await user.type(issuer, "https://id.example.test/new");
    await user.click(await within(oidc).findByRole("button", { name: "Test connection" }));
    expect(await within(oidc).findByText("Every check passed.")).toBeInTheDocument();
    const call = opCalls("POST /api/v2/admin/plugins/installations/{id}/auth-binding/test")[0]!;
    expect(call.options?.body).toMatchObject({
      capability_id: "oidc",
      config: [
        {
          key: "connection",
          value: expect.objectContaining({ issuer_url: "https://id.example.test/new" }),
          clear_secrets: [],
        },
      ],
    });
    expect(opCalls("PUT /api/v2/admin/plugins/installations/{id}/config")).toHaveLength(0);
  });

  it("saves a configuration entry from the page", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(within(oidc).getByRole("button", { name: /Configuration/ }));
    await user.click(within(oidc).getByRole("button", { name: "Save config" }));
    await waitFor(() =>
      expect(opCalls("PUT /api/v2/admin/plugins/installations/{id}/config")).toHaveLength(1),
    );
    expect(
      opCalls("PUT /api/v2/admin/plugins/installations/{id}/config")[0]?.options?.body,
    ).toMatchObject({ key: "connection", clear_secrets: [] });
    expect(await screen.findByText("Saved Provider connection.")).toBeInTheDocument();
  });

  it("shows why a connection test could not run", async () => {
    const user = userEvent.setup();
    failures["POST /api/v2/admin/plugins/installations/{id}/auth-binding/test"] = problem(
      "conflict",
      409,
      "Enable the plugin before testing its connection.",
    );
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(await within(oidc).findByRole("button", { name: "Test connection" }));
    const alert = await within(oidc).findByRole("alert");
    expect(alert).toHaveTextContent("Enable the plugin before testing its connection.");
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("reports an unreachable server during the test", async () => {
    const user = userEvent.setup();
    failures["POST /api/v2/admin/plugins/installations/{id}/auth-binding/test"] = new TypeError(
      "fetch failed",
    );
    mount();
    const oidc = providerPanel("OpenID Connect Sign-in");
    await user.click(await within(oidc).findByRole("button", { name: "Test connection" }));
    expect(await within(oidc).findByRole("alert")).toHaveTextContent(
      "Couldn't reach the server. Try again.",
    );
  });

  it("hides the test when the server or the plugin does not offer it", async () => {
    capabilities = { ...capabilities, connection_test: false };
    mount();
    await waitFor(() =>
      expect(opCalls("GET /api/v2/auth/external-sign-in/capabilities")).toHaveLength(1),
    );
    expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
  });
});

describe("SignInSettings password sign-in", () => {
  it("names the break-glass admins", () => {
    state.users = [
      admin({ id: 1, username: "root", break_glass: true }),
      admin({ id: 2, username: "nora", break_glass: true, password_login: false }),
      admin({ id: 3, username: "ivan", break_glass: false }),
    ];
    mount();
    expect(screen.getByText("Break-glass admin: root")).toBeInTheDocument();
  });

  it("refuses to turn password sign-in off without a break-glass admin", async () => {
    const user = userEvent.setup();
    state.users = [admin({ break_glass: false })];
    mount();
    expect(screen.getByText(/No break-glass admin yet/)).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "Sign in with Silo passwords" }));
    expect(state.setValue).not.toHaveBeenCalled();
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Password sign-in can't be turned off yet");
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("refuses to turn password sign-in off while no provider is on", async () => {
    const user = userEvent.setup();
    state.installations = [ldapInstallation()];
    mount();
    await user.click(screen.getByRole("switch", { name: "Sign in with Silo passwords" }));
    expect(state.setValue).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("Turn on a sign-in provider first");
  });

  it("stages turning password sign-in off when a break-glass admin exists", async () => {
    const user = userEvent.setup();
    mount();
    await user.click(screen.getByRole("switch", { name: "Sign in with Silo passwords" }));
    expect(state.setValue).toHaveBeenCalledWith("auth.local_password_login", "false");
  });

  it("explains the server refusing the save for want of a break-glass admin", async () => {
    const user = userEvent.setup();
    state.values["auth.local_password_login"] = "false";
    state.dirty = new Set(["auth.local_password_login"]);
    state.save.mockRejectedValue(problem("break_glass_required", 409));
    mount();
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(BREAK_GLASS_REQUIRED_TEXT);
  });

  it("points at the recovery paths", () => {
    mount();
    expect(screen.getByText("/login?local=1")).toBeInTheDocument();
    expect(screen.getByText("silo auth local-login enable")).toBeInTheDocument();
  });
});

describe("SignInSettings matching and re-check", () => {
  it("warns about account takeover and stages email matching", async () => {
    const user = userEvent.setup();
    mount();
    expect(screen.getByText(/takes over the Silo account/)).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "Match accounts by email" }));
    expect(state.setValue).toHaveBeenCalledWith("auth.email_auto_match", "true");
  });

  it("keeps a stored interval no preset names and explains the outage policy", () => {
    state.values["auth.provider_recheck_interval"] = "3h";
    state.values["auth.provider_recheck_outage_policy"] = "fail_closed";
    mount();
    expect(screen.getByRole("combobox", { name: "Ask the provider again" })).toHaveTextContent(
      "3h",
    );
    expect(
      screen.getByRole("combobox", { name: "When the provider can't be reached" }),
    ).toHaveTextContent("Stop sessions from renewing");
    expect(screen.getByText(/apps stop working during an outage/)).toBeInTheDocument();
  });
});
