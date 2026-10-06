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
// Operations that answer only when the test resolves them.
let held: Record<string, Promise<unknown>>;
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
  held = {};
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
    if (held[op]) return held[op];
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

const providerChoice = (name: RegExp) => screen.getByRole("button", { name });

async function pick(user: ReturnType<typeof userEvent.setup>, name: RegExp) {
  await user.click(providerChoice(name));
}

/** An OpenID Connect install whose manifest lays its fields out in sections. */
function sectionedOidc(overrides: Partial<PluginInstallation> = {}): PluginInstallation {
  return oidcInstallation({
    global_config_schema: [
      {
        key: "connection",
        title: "Provider connection",
        description: "Where the provider is.",
        json_schema: "{}",
        required: true,
        admin_form: {
          fields: [
            { key: "issuer_url", label: "Issuer URL", control: "TEXT", required: true },
            { key: "client_secret", label: "Client secret", control: "PASSWORD", secret: true },
            { key: "provider_logout", label: "Sign out at the provider", control: "SWITCH" },
          ],
          sections: [
            {
              key: "client",
              title: "Connect to the provider",
              collapsible: false,
              collapsed_default: false,
              field_keys: ["issuer_url", "client_secret"],
            },
            {
              key: "logout",
              title: "Sign-out",
              collapsible: true,
              collapsed_default: true,
              field_keys: ["provider_logout"],
            },
          ],
        },
      },
      {
        key: "access",
        title: "Choose who can sign in",
        json_schema: "{}",
        required: false,
        admin_form: {
          fields: [{ key: "admin_groups", label: "Admin groups", control: "TEXTAREA" }],
        },
      },
      {
        key: "display_name",
        title: "Button label",
        json_schema: "{}",
        required: false,
        admin_form: {
          fields: [
            { key: "value", label: "Button label", control: "TEXT", placeholder: "Single sign-on" },
          ],
        },
      },
    ] as never,
    ...overrides,
  });
}

describe("SignInSettings provider choice", () => {
  it("shows a loading state and a read failure", () => {
    state.installationsLoading = true;
    state.installations = undefined;
    const { unmount } = mount();
    expect(
      screen.getByRole("group", { name: "Single sign-on" }).querySelector("[aria-busy=true]"),
    ).not.toBeNull();
    unmount();
    state.installationsLoading = false;
    state.installationsError = true;
    mount();
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn't read the installed plugins");
  });

  it("shows only the provider that is on, with the URLs to register", () => {
    mount();
    expect(providerChoice(/^OpenID Connect/)).toHaveAttribute("aria-pressed", "true");
    expect(providerChoice(/^LDAP/)).toHaveAttribute("aria-pressed", "false");
    expect(screen.getAllByTestId("sign-in-provider")).toHaveLength(1);
    const oidc = providerPanel("OpenID Connect");
    expect(within(oidc).getByTestId("sign-in-provider-state")).toHaveTextContent("On");
    expect(within(oidc).getByLabelText("Redirect URI")).toHaveValue(
      "https://silo.example.test/api/v2/auth/oauth/5/callback",
    );
    expect(within(oidc).getByLabelText("Post-logout redirect URI")).toHaveValue(
      "https://silo.example.test/login",
    );
    expect(screen.getByText(/To switch, turn off OpenID Connect first/)).toBeInTheDocument();
  });

  it("shows another provider's setup without letting it turn on", async () => {
    const user = userEvent.setup();
    const nameSchema = {
      key: "display_name",
      title: "Button label",
      json_schema: "{}",
      required: false,
      admin_form: { fields: [{ key: "value", label: "Button label", control: "TEXT" }] },
    };
    state.installations = [oidcInstallation(), ldapInstallation()].map((installation, index) => ({
      ...installation,
      global_config_schema: [...installation.global_config_schema, nameSchema],
      global_configs: [
        ...(installation.global_configs ?? []),
        { key: "display_name", value: { value: index === 0 ? "Keycloak" : "Company directory" } },
      ],
    }));
    mount();
    expect(within(providerPanel("OpenID Connect")).getByLabelText("Provider name")).toHaveValue(
      "Keycloak",
    );
    await pick(user, /^LDAP/);
    const ldap = providerPanel("LDAP");
    expect(within(ldap).getByLabelText("Directory name")).toHaveValue("Company directory");
    expect(within(ldap).getByRole("button", { name: "Turn on" })).toBeDisabled();
    expect(within(ldap).getByText(/OpenID Connect is the sign-in provider now/)).toBeTruthy();
    // A password (LDAP) provider has nothing to register.
    expect(within(ldap).queryByLabelText("Redirect URI")).toBeNull();
    expect(screen.queryByRole("region", { name: "OpenID Connect" })).toBeNull();
  });

  it("offers to turn the provider off when None is picked", async () => {
    const user = userEvent.setup();
    mount();
    await pick(user, /^None/);
    await user.click(screen.getByRole("button", { name: "Turn off Single sign-on" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
    await waitFor(() =>
      expect(
        opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")[0]?.options?.body,
      ).toMatchObject({ capability_id: "oidc", enabled: false }),
    );
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

  it("shows a fresh OpenID Connect install's redirect URI before any binding exists", () => {
    state.installations = [oidcInstallation({ auth_bindings: [] })];
    mount();
    const oidc = providerPanel("OpenID Connect");
    expect(within(oidc).getByLabelText("Redirect URI")).toHaveValue(
      "https://silo.example.test/api/v2/auth/oauth/5/callback",
    );
    expect(within(oidc).queryByText("silo.auth.oidc")).toBeNull();
  });
});

describe("SignInSettings turning a provider on and off", () => {
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
    const oidc = providerPanel("OpenID Connect");
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

  it("says who can still sign in when the provider goes off with passwords off", async () => {
    const user = userEvent.setup();
    state.values["auth.local_password_login"] = "false";
    mount();
    const oidc = providerPanel("OpenID Connect");
    await user.click(within(oidc).getByRole("button", { name: "Turn off" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("only break-glass admins can sign in");
    await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
    expect(
      await screen.findByText(/Password sign-in is off too, so only break-glass admins/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Only Silo passwords sign in now/)).toBeNull();
  });

  it("keeps Turn on off until required fields are saved", async () => {
    const user = userEvent.setup();
    state.installations = [
      sectionedOidc({
        global_configs: [],
        auth_bindings: [],
      }),
    ];
    mount();
    const oidc = providerPanel("OpenID Connect");
    expect(within(oidc).getByTestId("sign-in-provider-state")).toHaveTextContent("Needs setup");
    expect(within(oidc).getByText(/Needs setup: fill in Issuer URL and save/)).toBeTruthy();
    expect(within(oidc).getByRole("button", { name: "Turn on" })).toBeDisabled();
    await user.type(within(oidc).getByLabelText("Issuer URL"), "https://id.example.test");
    expect(within(oidc).getByText("Save your changes to turn it on.")).toBeInTheDocument();
  });
});

function tailscaleInstallation(overrides: Partial<PluginInstallation> = {}): PluginInstallation {
  return {
    ...oidcInstallation(),
    id: 9,
    plugin_id: "community.network-access.tailscale",
    capabilities: [
      { type: "network_access_provider.v1", id: "tailscale", display_name: "Tailscale" },
      {
        type: "auth_provider.v1",
        id: "tailscale",
        display_name: "Tailscale",
        metadata: { display_name: "Tailscale" },
        sign_in_mode: "network",
      },
    ],
    presentation: { display_name: "Tailscale" },
    global_config_schema: [],
    global_configs: [],
    auth_bindings: [],
    ...overrides,
  } as unknown as PluginInstallation;
}

describe("SignInSettings network sign-in", () => {
  it("turns it on at once, keeping account creation on by default", async () => {
    const user = userEvent.setup();
    state.installations = [oidcInstallation(), tailscaleInstallation()];
    mount();
    expect(screen.getByText("Network sign-in")).toBeInTheDocument();
    const toggle = screen.getByRole("switch", { name: "Sign in with Tailscale" });
    expect(toggle).not.toBeChecked();
    expect(
      within(screen.getByRole("group", { name: "Sign-in provider to show" })).queryByRole(
        "button",
        { name: /Tailscale/ },
      ),
    ).toBeNull();
    await user.click(toggle);
    await waitFor(() =>
      expect(opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")).toHaveLength(1),
    );
    const call = opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")[0]!;
    expect(call.options?.path).toEqual({ id: "9" });
    expect(call.options?.body).toEqual({
      capability_id: "tailscale",
      enabled: true,
      display_order: 1,
      auto_provision: true,
      default_login: false,
    });
  });
});

describe("SignInSettings guided setup", () => {
  it("opens an Advanced section that holds a required field still missing", () => {
    const installation = sectionedOidc({ global_configs: [] });
    const connection = installation.global_config_schema[0]!;
    connection.admin_form!.fields[2]!.required = true;
    state.installations = [installation];
    mount();
    expect(
      within(providerPanel("OpenID Connect")).getByRole("switch", {
        name: "Sign out at the provider",
      }),
    ).toBeInTheDocument();
  });
});

describe("SignInSettings saving provider changes", () => {
  it("stages account creation on first sign-in until the page is saved", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect");
    const toggle = within(oidc).getByRole("switch", { name: "Create accounts on first sign-in" });
    expect(toggle).toBeChecked();
    await user.click(toggle);
    expect(opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")).toHaveLength(0);
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(
        opCalls("PUT /api/v2/admin/plugins/installations/{id}/auth-binding")[0]?.options?.body,
      ).toMatchObject({ enabled: true, auto_provision: false }),
    );
    // Only the provider changed, so the server settings are not saved.
    expect(state.save).not.toHaveBeenCalled();
  });

  it("saves edited configuration from the save bar, then the server settings", async () => {
    const user = userEvent.setup();
    state.dirty = new Set(["auth.email_auto_match"]);
    mount();
    const oidc = providerPanel("OpenID Connect");
    const issuer = within(oidc).getByLabelText("Issuer URL");
    await user.clear(issuer);
    await user.type(issuer, "https://id.example.test/new");
    expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(state.save).toHaveBeenCalledTimes(1));
    const saves = opCalls("PUT /api/v2/admin/plugins/installations/{id}/config");
    expect(saves).toHaveLength(1);
    expect(saves[0]?.options?.body).toMatchObject({
      key: "connection",
      value: expect.objectContaining({ issuer_url: "https://id.example.test/new" }),
      clear_secrets: [],
    });
  });

  it("stops at a provider save that fails and says which provider", async () => {
    const user = userEvent.setup();
    state.dirty = new Set(["auth.email_auto_match"]);
    failures["PUT /api/v2/admin/plugins/installations/{id}/config"] = problem(
      "validation_failed",
      422,
      "issuer_url must use https",
    );
    mount();
    const oidc = providerPanel("OpenID Connect");
    await user.type(within(oidc).getByLabelText("Issuer URL"), "x");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "OpenID Connect Sign-in: issuer_url must use https",
    );
    expect(state.save).not.toHaveBeenCalled();
    expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
  });

  it("keeps an edit made while its entry was saving", async () => {
    const user = userEvent.setup();
    let finish: (value?: unknown) => void = () => {};
    held["PUT /api/v2/admin/plugins/installations/{id}/config"] = new Promise((resolve) => {
      finish = resolve;
    });
    mount();
    const oidc = providerPanel("OpenID Connect");
    const issuer = within(oidc).getByLabelText("Issuer URL");
    await user.type(issuer, "/a");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(opCalls("PUT /api/v2/admin/plugins/installations/{id}/config")).toHaveLength(1),
    );
    expect(within(oidc).getByRole("button", { name: "Turn off" })).toBeDisabled();
    await user.type(issuer, "b");
    finish();
    await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
    expect(within(oidc).getByRole("button", { name: "Turn off" })).toBeEnabled();
    expect(issuer).toHaveValue("https://id.example.test/realms/silo/ab");
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
  });

  it("holds the save while a provider is being turned off", async () => {
    const user = userEvent.setup();
    let finish: (value?: unknown) => void = () => {};
    held["PUT /api/v2/admin/plugins/installations/{id}/auth-binding"] = new Promise((resolve) => {
      finish = resolve;
    });
    mount();
    const oidc = providerPanel("OpenID Connect");
    await user.click(
      within(oidc).getByRole("switch", { name: "Create accounts on first sign-in" }),
    );
    await user.click(within(oidc).getByRole("button", { name: "Turn off" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeDisabled());
    finish();
    await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
  });

  it("clears a saved secret only when asked", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect");
    expect(within(oidc).getByLabelText("Client secret")).toHaveAttribute(
      "placeholder",
      "Saved. Type to replace.",
    );
    await user.click(within(oidc).getByRole("button", { name: "Clear" }));
    expect(within(oidc).getByText("Will be cleared when you save.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(
        opCalls("PUT /api/v2/admin/plugins/installations/{id}/config")[0]?.options?.body,
      ).toMatchObject({ key: "connection", clear_secrets: ["client_secret"] }),
    );
  });

  it("discards staged provider edits with the server settings", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect");
    const issuer = within(oidc).getByLabelText("Issuer URL");
    await user.type(issuer, "/extra");
    await user.click(screen.getByRole("button", { name: "Discard" }));
    expect(issuer).toHaveValue("https://id.example.test/realms/silo");
    expect(state.discard).toHaveBeenCalled();
    expect(screen.queryByText(/unsaved change/)).toBeNull();
  });
});

describe("SignInSettings connection test", () => {
  it("runs the connection test and lists every step", async () => {
    const user = userEvent.setup();
    mount();
    const oidc = providerPanel("OpenID Connect");
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
    const oidc = providerPanel("OpenID Connect");
    const issuer = within(oidc).getByLabelText("Issuer URL");
    await user.clear(issuer);
    await user.type(issuer, "https://id.example.test/new");
    expect(within(oidc).getByText(/Tests your unsaved changes/)).toBeInTheDocument();
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

  it("asks for the test's look-up user in the test and never saves it", async () => {
    const user = userEvent.setup();
    testResult = { ok: true, callback_url: "", steps: [] };
    state.installations = [
      ldapInstallation({
        capabilities: [
          {
            type: "auth_provider.v1",
            id: "ldap",
            display_name: "LDAP",
            metadata: {
              connection_test: true,
              connection_test_username_field: "users.test_username",
            },
            sign_in_mode: "credentials",
          },
        ] as never,
        global_config_schema: [
          {
            key: "users",
            title: "Find people",
            json_schema: "{}",
            required: false,
            admin_form: {
              fields: [
                { key: "base_dn", label: "User search base DN", control: "TEXT" },
                { key: "test_username", label: "Test username", control: "TEXT" },
              ],
            },
          },
        ] as never,
        global_configs: [{ key: "users", value: { base_dn: "ou=people,dc=example,dc=test" } }],
      }),
    ];
    mount();
    const ldap = providerPanel("LDAP");
    expect(within(ldap).queryByLabelText("Test username")).toBeNull();
    await user.type(await within(ldap).findByLabelText("Look up a user (optional)"), "nora");
    expect(screen.queryByText(/unsaved change/)).toBeNull();
    await user.click(within(ldap).getByRole("button", { name: "Test connection" }));
    await within(ldap).findByText("Every check passed.");
    const call = opCalls("POST /api/v2/admin/plugins/installations/{id}/auth-binding/test")[0]!;
    expect(call.options?.body).toMatchObject({
      capability_id: "ldap",
      config: [
        {
          key: "users",
          value: { base_dn: "ou=people,dc=example,dc=test", test_username: "nora" },
        },
      ],
    });
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
    const ldap = providerPanel("LDAP");
    await user.click(await within(ldap).findByRole("button", { name: "Test connection" }));
    await within(ldap).findByTestId("sign-in-test-result");
    expect(within(ldap).queryByLabelText("Redirect URI")).toBeNull();
  });

  it("shows why a connection test could not run", async () => {
    const user = userEvent.setup();
    failures["POST /api/v2/admin/plugins/installations/{id}/auth-binding/test"] = problem(
      "conflict",
      409,
      "Enable the plugin before testing its connection.",
    );
    mount();
    const oidc = providerPanel("OpenID Connect");
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
    const oidc = providerPanel("OpenID Connect");
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
    await user.click(screen.getByRole("switch", { name: "Allow password sign-in" }));
    expect(state.setValue).not.toHaveBeenCalled();
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Password sign-in can't be turned off yet");
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("refuses to turn password sign-in off while no provider is on", async () => {
    const user = userEvent.setup();
    state.installations = [ldapInstallation()];
    mount();
    await user.click(screen.getByRole("switch", { name: "Allow password sign-in" }));
    expect(state.setValue).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("Turn on a sign-in provider first");
  });

  it("does not count a network sign-in as the provider that is on", async () => {
    const user = userEvent.setup();
    state.installations = [
      ldapInstallation(),
      tailscaleInstallation({
        auth_bindings: [
          {
            capability_id: "tailscale",
            enabled: true,
            display_order: 1,
            auto_provision: true,
            default_login: false,
            created_at: STAMP,
            updated_at: STAMP,
          },
        ],
      }),
    ];
    mount();
    await user.click(screen.getByRole("switch", { name: "Allow password sign-in" }));
    expect(state.setValue).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("Turn on a sign-in provider first");
  });

  it("stages turning password sign-in off when a break-glass admin exists", async () => {
    const user = userEvent.setup();
    mount();
    await user.click(screen.getByRole("switch", { name: "Allow password sign-in" }));
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

  it("points at the recovery paths", async () => {
    const user = userEvent.setup();
    mount();
    await user.click(screen.getByRole("button", { name: "Locked out?" }));
    expect(screen.getByText("/login?local=1")).toBeInTheDocument();
    expect(screen.getByText("silo auth local-login enable")).toBeInTheDocument();
  });
});

describe("SignInSettings accounts and sessions", () => {
  it("warns about account takeover and stages email matching", async () => {
    const user = userEvent.setup();
    mount();
    expect(screen.getByText(/takes over the Silo account/)).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "Match existing accounts by email" }));
    expect(state.setValue).toHaveBeenCalledWith("auth.email_auto_match", "true");
  });

  it("keeps a stored interval no preset names and explains the outage policy", () => {
    state.values["auth.provider_recheck_interval"] = "3h";
    state.values["auth.provider_recheck_outage_policy"] = "fail_closed";
    mount();
    expect(screen.getByRole("combobox", { name: "Re-check access every" })).toHaveTextContent("3h");
    expect(
      screen.getByRole("combobox", { name: "If the provider is unreachable" }),
    ).toHaveTextContent("Stop sessions from renewing");
    expect(screen.getByText(/apps stop working during an outage/)).toBeInTheDocument();
  });
});
