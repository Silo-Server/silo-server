import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import GeneralSettings from "./GeneralSettings";

const settingsFormMock = vi.fn();
const useCheckAdminSettingsConnectionMock = vi.fn();

// Most cases drive the page from a hand-written form. The Redis editing cases
// that have to prove a value reaches the server flip this and run the real hook.
const { realForm, serverSettings, sensitiveStatus, updateSettingsMock } = vi.hoisted(() => ({
  realForm: { enabled: false },
  serverSettings: { current: {} as Record<string, string> },
  sensitiveStatus: { current: { configured: [] as string[], managed_by_env: [] as string[] } },
  updateSettingsMock: vi.fn(),
}));

vi.mock("@/hooks/useSettingsForm", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/useSettingsForm")>();
  return {
    useSettingsForm: (options: { keys: string[] }) =>
      realForm.enabled ? actual.useSettingsForm(options) : settingsFormMock(options),
  };
});

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: (...args: unknown[]) =>
    useCheckAdminSettingsConnectionMock(...args),
  useAdminServerSettings: () => ({ data: serverSettings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: sensitiveStatus.current, isError: false }),
  useUpdateServerSettings: () => ({ mutateAsync: updateSettingsMock, isPending: false }),
}));

const RESTART_KEYS = new Set<string>([
  "server.lan_discovery",
  "clientip.trusted_proxies",
  "database.max_connections",
  "redis.url",
  "redis.db",
]);
const restartKeysMock = vi.fn(() => ({ has: (key: string) => RESTART_KEYS.has(key) }));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => restartKeysMock(),
}));

function makeForm(values: Record<string, string> = {}, overrides: Record<string, unknown> = {}) {
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    getPersistedValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    resetValue: vi.fn(),
    isDirty: () => false,
    isClearStaged: () => false,
    dirtyCount: 0,
    dirtyKeys: [],
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
    sensitiveStatusReady: true,
    sensitiveStatusError: false,
    buildConnectionCheckRequest: vi.fn(),
    ...overrides,
  };
}

const network = () => within(screen.getByRole("group", { name: "Network" }));
const redis = () => within(screen.getByRole("group", { name: "Redis" }));

function renderPage() {
  return render(
    <MemoryRouter>
      <GeneralSettings />
    </MemoryRouter>,
  );
}

describe("GeneralSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    settingsFormMock.mockReset();
    settingsFormMock.mockReturnValue(makeForm({ "branding.server_name": "Silo" }));
    useCheckAdminSettingsConnectionMock.mockReset();
    useCheckAdminSettingsConnectionMock.mockReturnValue({
      isPending: false,
      mutateAsync: vi.fn(),
    });
  });

  // Signups and password reset moved to Sign-in, and logging to its own page;
  // their tests followed them to SignInSettings.test.tsx and
  // LoggingSettings.test.tsx. Redis and the Postgres pool moved here from
  // Storage.
  it("manages identity, network, database and Redis keys on one save bar", () => {
    renderPage();

    expect(settingsFormMock.mock.calls[0]?.[0]?.keys).toEqual([
      "server.public_url",
      "server.lan_discovery",
      "server.local_url",
      "clientip.trusted_proxies",
      "branding.server_name",
      "branding.login_subtitle",
      "database.max_connections",
      "redis.url",
      "redis.db",
    ]);
    expect(screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent)).toEqual([
      "Identity",
      "Network",
      "Database",
      "Redis",
    ]);
  });

  // The local address applies live, like the public URL: apps read it from the
  // connections document on their next refresh.
  it("stages the local network address without a restart chip", async () => {
    const form = makeForm({ "server.local_url": "http://192.168.1.20:8080" });
    settingsFormMock.mockReturnValue(form);
    renderPage();

    const field = network().getByLabelText("Local network address");
    expect(field).toHaveValue("http://192.168.1.20:8080");
    expect(
      network()
        .queryAllByLabelText("Takes effect after a server restart")
        .find((el) => el.parentElement?.textContent?.includes("Local network address")),
    ).toBeUndefined();

    await userEvent.clear(field);
    expect(form.setValue).toHaveBeenLastCalledWith("server.local_url", "");
  });

  // Trusted proxies moved here from Security, beside the public URL, the way
  // the setup wizard asks for them.
  it("shows trusted proxies with no disclosure and stages edits", async () => {
    const form = makeForm({ "clientip.trusted_proxies": "10.0.0.0/8" });
    settingsFormMock.mockReturnValue(form);
    renderPage();

    expect(network().getByLabelText("Silo public URL")).toBeInTheDocument();
    expect(network().queryByRole("button", { name: /Behind a reverse proxy/ })).toBeNull();

    const proxies = network().getByLabelText("Trusted proxies");
    expect(proxies).toHaveValue("10.0.0.0/8");
    expect(proxies).toBeEnabled();
    expect(network().getByText(/empty keeps the private defaults/)).toBeInTheDocument();
    // The proxy list needs a restart; the public URL beside it does not, so the
    // chip stays on the row instead of the group.
    const badge = network()
      .getAllByLabelText("Takes effect after a server restart")
      .find((el) => el.parentElement?.textContent?.includes("Trusted proxies"));
    expect(badge).toBeDefined();

    await userEvent.type(proxies, ",");
    expect(form.setValue).toHaveBeenCalledWith("clientip.trusted_proxies", "10.0.0.0/8,");
  });

  it("locks trusted proxies when SILO_TRUSTED_PROXIES manages them", () => {
    settingsFormMock.mockReturnValue(
      makeForm({}, { sensitiveManagedByEnv: ["clientip.trusted_proxies"] }),
    );
    renderPage();

    expect(network().getByLabelText("Trusted proxies")).toBeDisabled();
    expect(network().getByText("Managed by SILO_TRUSTED_PROXIES.")).toBeInTheDocument();
  });

  it("shows the Postgres connection limit with no disclosure", () => {
    const form = makeForm({ "database.max_connections": "25" });
    settingsFormMock.mockReturnValue(form);
    renderPage();

    const database = within(screen.getByRole("group", { name: "Database" }));
    expect(database.queryByRole("button", { name: /Advanced/ })).toBeNull();
    const field = database.getByLabelText("Maximum Postgres connections");
    expect(field).toHaveValue(25);
    fireEvent.change(field, { target: { value: "40" } });
    expect(form.setValue).toHaveBeenCalledWith("database.max_connections", "40");
    // The per-user database backend and its SQLite tuning are Hidden.
    expect(database.queryByText("Where per-user data is stored")).toBeNull();
  });

  it("shows only the Redis switch while Redis is off", () => {
    renderPage();

    expect(redis().getByRole("switch", { name: "Use Redis" })).not.toBeChecked();
    expect(redis().queryByLabelText("Connection URL")).toBeNull();
    expect(redis().queryByLabelText("Database number")).toBeNull();
  });

  it("keeps the Redis connection check available when REDIS_URL comes from the environment", () => {
    settingsFormMock.mockReturnValue(
      makeForm({}, { sensitiveConfigured: ["redis.url"], sensitiveManagedByEnv: ["redis.url"] }),
    );
    renderPage();

    // The value stays read-only — only writes are refused for env-managed keys —
    // but the check runs against the value the server merged from REDIS_URL.
    expect(redis().getByLabelText("Connection URL")).toBeDisabled();
    expect(redis().getByRole("button", { name: "Check Connection" })).toBeEnabled();
    // The switch already stages the empty URL, so the field offers no clear.
    expect(redis().queryByRole("button", { name: "Clear saved value" })).toBeNull();
  });

  it("shows the Redis database number in use and stages an edit to it", () => {
    const form = makeForm({ "redis.db": "3" }, { sensitiveConfigured: ["redis.url"] });
    settingsFormMock.mockReturnValue(form);
    renderPage();

    const field = redis().getByLabelText("Database number");
    expect(field).toHaveValue(3);
    expect(field).toBeEnabled();
    fireEvent.change(field, { target: { value: "5" } });
    expect(form.setValue).toHaveBeenCalledWith("redis.db", "5");
  });

  it("holds the Redis switch and database number while a save is in flight", () => {
    settingsFormMock.mockReturnValue(
      makeForm({ "redis.db": "3" }, { sensitiveConfigured: ["redis.url"], isSaving: true }),
    );
    renderPage();

    expect(redis().getByRole("switch", { name: "Use Redis" })).toBeDisabled();
    expect(redis().getByLabelText("Database number")).toBeDisabled();
  });

  it("makes the Redis database number read-only when REDIS_URL comes from the environment", () => {
    settingsFormMock.mockReturnValue(
      makeForm(
        { "redis.db": "1" },
        {
          sensitiveConfigured: ["redis.url"],
          sensitiveManagedByEnv: ["redis.url", "redis.db"],
        },
      ),
    );
    renderPage();

    // The server reports the number in REDIS_URL; it cannot be saved here.
    expect(redis().getByLabelText("Database number")).toHaveValue(1);
    expect(redis().getByLabelText("Database number")).toBeDisabled();
  });

  it("checks the Redis connection with the database number", async () => {
    const mutateAsync = vi.fn().mockResolvedValue({ success: true, message: "ok" });
    useCheckAdminSettingsConnectionMock.mockReturnValue({ isPending: false, mutateAsync });
    const request = { values: { "redis.url": "", "redis.db": "5" }, dirty_keys: ["redis.db"] };
    const form = makeForm(
      {},
      {
        sensitiveConfigured: ["redis.url"],
        buildConnectionCheckRequest: vi.fn(() => request),
      },
    );
    settingsFormMock.mockReturnValue(form);
    renderPage();

    fireEvent.click(redis().getByRole("button", { name: "Check Connection" }));

    await waitFor(() => expect(mutateAsync).toHaveBeenCalledWith({ kind: "redis", body: request }));
    expect(form.buildConnectionCheckRequest).toHaveBeenCalledWith(["redis.url", "redis.db"]);
  });

  it("marks the Redis database number for a restart on its own", () => {
    // A registry where only the number needs a restart: the group cannot say
    // so for every field, which leaves the chip to this one.
    restartKeysMock.mockReturnValueOnce({ has: (key: string) => key === "redis.db" });
    settingsFormMock.mockReturnValue(makeForm({}, { sensitiveConfigured: ["redis.url"] }));
    renderPage();

    const badges = redis().getAllByLabelText("Takes effect after a server restart");
    expect(badges).toHaveLength(1);
    expect(badges[0]?.parentElement).toHaveTextContent("Database number");
  });

  it("holds only Redis when protected credential status cannot be loaded", () => {
    settingsFormMock.mockReturnValue(
      makeForm({}, { sensitiveStatusReady: false, sensitiveStatusError: true }),
    );
    renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent("Redis settings are unavailable");
    expect(screen.queryByRole("group", { name: "Redis" })).toBeNull();
    expect(network().getByLabelText("Silo public URL")).toBeEnabled();
  });

  it("saves other edits but not a Redis draft hidden by a credential status error", async () => {
    const form = makeForm(
      {},
      {
        sensitiveStatusReady: false,
        sensitiveStatusError: true,
        dirtyCount: 2,
        dirtyKeys: ["server.public_url", "redis.url"],
        isDirty: (key: string) => key === "server.public_url" || key === "redis.url",
      },
    );
    settingsFormMock.mockReturnValue(form);
    renderPage();

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(form.save).toHaveBeenCalledWith(["server.public_url"]);
  });

  describe("editing the Redis connection", () => {
    const newUrl = "redis://new.example.invalid:6379/7";

    beforeEach(() => {
      realForm.enabled = true;
      // No redis.db is saved: the server reports the 3 in the saved URL.
      serverSettings.current = { "redis.db": "3" };
      sensitiveStatus.current = { configured: ["redis.url"], managed_by_env: [] };
      updateSettingsMock.mockReset();
      updateSettingsMock.mockResolvedValue({ values: {}, restart_required: false });
    });

    afterEach(() => {
      realForm.enabled = false;
    });

    it("saves the number on screen with a new URL", async () => {
      renderPage();

      await userEvent.type(redis().getByLabelText("Connection URL"), newUrl);

      expect(redis().getByLabelText("Database number")).toHaveValue(3);
      await userEvent.click(screen.getByRole("button", { name: "Save" }));
      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({ "redis.url": newUrl, "redis.db": "3" }),
      );
    });

    it("checks a new URL with the number on screen", async () => {
      const mutateAsync = vi.fn().mockResolvedValue({ success: true, message: "ok" });
      useCheckAdminSettingsConnectionMock.mockReturnValue({ isPending: false, mutateAsync });
      renderPage();

      await userEvent.type(redis().getByLabelText("Connection URL"), newUrl);
      await userEvent.click(redis().getByRole("button", { name: "Check Connection" }));

      await waitFor(() =>
        expect(mutateAsync).toHaveBeenCalledWith({
          kind: "redis",
          body: {
            values: { "redis.url": newUrl, "redis.db": "3" },
            dirty_keys: ["redis.url", "redis.db"],
          },
        }),
      );
    });

    it("stages nothing once the new URL is taken back", async () => {
      renderPage();

      await userEvent.type(redis().getByLabelText("Connection URL"), newUrl);
      expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
      await userEvent.clear(redis().getByLabelText("Connection URL"));

      expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument();
      expect(redis().getByLabelText("Database number")).toHaveValue(3);
    });

    it("drops a URL keystroke that arrives while a save is in flight", async () => {
      updateSettingsMock.mockReturnValue(new Promise(() => {}));
      renderPage();
      fireEvent.change(redis().getByLabelText("Database number"), { target: { value: "5" } });
      await userEvent.click(screen.getByRole("button", { name: "Save" }));
      await waitFor(() => expect(updateSettingsMock).toHaveBeenCalledWith({ "redis.db": "5" }));

      fireEvent.change(redis().getByLabelText("Connection URL"), { target: { value: "r" } });

      expect(redis().getByLabelText("Connection URL")).toHaveValue("");
    });

    it("sends the number with the empty URL when Redis is switched off", async () => {
      // The server stores no number without a URL, so this removes a saved one.
      renderPage();

      await userEvent.click(screen.getByRole("switch", { name: "Use Redis" }));
      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({ "redis.url": "", "redis.db": "3" }),
      );
    });

    it("stages nothing once Redis is switched back on", async () => {
      renderPage();

      await userEvent.click(screen.getByRole("switch", { name: "Use Redis" }));
      expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("switch", { name: "Use Redis" }));

      expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument();
      expect(redis().getByLabelText("Database number")).toHaveValue(3);
    });
  });

  // The save bar moves to components/ but must render the same markup on every
  // settings page that uses it: scroll room, scrim and the pill.
  it("renders the settings save bar for a dirty form", () => {
    settingsFormMock.mockReturnValue({ ...makeForm(), dirtyCount: 2 });
    renderPage();

    const pill = screen.getByText("2 unsaved changes").closest("[role=status]");
    const scrim = pill?.previousElementSibling;
    expect([scrim?.previousElementSibling, scrim, pill]).toMatchInlineSnapshot(`
      [
        <div
          aria-hidden="true"
          class="h-28"
        />,
        <div
          aria-hidden="true"
          class="pointer-events-none fixed right-0 bottom-0 left-0 z-30 h-40 bg-gradient-to-t from-[var(--background)] via-[color-mix(in_srgb,var(--background)_72%,transparent)] to-transparent lg:left-[240px]"
        />,
        <div
          class="pointer-events-none fixed right-0 bottom-6 left-0 z-40 flex justify-center px-4 lg:left-[240px]"
          role="status"
        >
          <div
            class="glass pointer-events-auto flex max-w-full items-center gap-3 rounded-full py-2 pr-2 pl-4 shadow-2xl backdrop-blur-xl sm:gap-4 sm:pl-5"
          >
            <span
              class="min-w-0 truncate text-[13px] font-medium"
            >
              2 unsaved changes
            </span>
            <span
              class="flex shrink-0 items-center gap-1.5"
            >
              <button
                class="inline-flex shrink-0 items-center justify-center text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 duration-150 hover:bg-accent hover:text-accent-foreground h-8 gap-1.5 px-3 has-[>svg]:px-2.5 rounded-full"
                data-size="sm"
                data-slot="button"
                data-variant="ghost"
              >
                Discard
              </button>
              <button
                class="inline-flex shrink-0 items-center justify-center text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 shadow-sm duration-150 h-8 gap-1.5 px-3 has-[>svg]:px-2.5 rounded-full bg-[var(--settings-accent)] text-[#15151a] hover:bg-[var(--settings-accent)] hover:brightness-110"
                data-size="sm"
                data-slot="button"
                data-variant="default"
              >
                Save
              </button>
            </span>
          </div>
        </div>,
      ]
    `);
  });
});
