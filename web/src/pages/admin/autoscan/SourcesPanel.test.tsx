import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";
import {
  ARR_POLL_PLUGIN,
  ARR_WEBHOOK_PLUGIN,
  SONARR_CONNECTION,
  json,
  pollSource,
  stubAutoscanServer,
  webhookSource,
  withDescriptor,
} from "@/test/autoscanServer";

import SourcesPanel from "./SourcesPanel";

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 2, name: "TV Shows", type: "series", paths: ["/mnt/media/tv"], enabled: true },
      { id: 3, name: "Movies", type: "movie", paths: ["/mnt/media/movies"], enabled: true },
    ],
  }),
}));

// Radix Select and DropdownMenu open through pointer capture, which jsdom lacks.
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken(null);
  setProfileId("a");
  setProfileToken("pin-a");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderPanel(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <SourcesPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return user;
}

const plugins = [ARR_POLL_PLUGIN, ARR_WEBHOOK_PLUGIN];

describe("Sources list", () => {
  it("titles a source by its label and still says what it is", async () => {
    stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [
        pollSource({ label: "Living room TV", poll_interval_seconds: 300 }),
        pollSource({ id: "poll-b", connection_id: null, poll_interval_seconds: 1800 }),
        webhookSource({ label: "4K shows" }),
        webhookSource({ id: "hook-b" }),
        webhookSource({ id: "hook-c", source_config: {} }),
      ],
    });
    renderPanel();

    const list = await screen.findByRole("list");
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(5);

    // A per-source interval below the default has no effect on the schedule.
    expect(within(rows[0]!).getByText("Living room TV")).toBeInTheDocument();
    expect(
      within(rows[0]!).getByText("Sonarr / Radarr · via Sonarr 4K · polls every 10 min"),
    ).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Sonarr / Radarr")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Polls every 30 min")).toBeInTheDocument();

    expect(within(rows[2]!).getByText("4K shows")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Sonarr/Radarr Webhook · Sonarr")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("Sonarr")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("Sonarr/Radarr Webhook")).toBeInTheDocument();
    expect(within(rows[4]!).getByText("Sonarr/Radarr Webhook")).toBeInTheDocument();
    expect(
      within(rows[4]!).getByText("Webhook · auto-detects Sonarr or Radarr"),
    ).toBeInTheDocument();

    expect(within(rows[0]!).getByText("TV Shows")).toBeInTheDocument();
  });

  it("has no inline inputs, only labelled actions per source", async () => {
    stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [pollSource({ label: "Living room TV" }), webhookSource()],
    });
    renderPanel();

    const list = await screen.findByRole("list");
    expect(within(list).queryAllByRole("textbox")).toHaveLength(0);
    expect(within(list).queryAllByRole("combobox")).toHaveLength(0);
    expect(within(list).queryAllByRole("spinbutton")).toHaveLength(0);

    expect(within(list).getByRole("switch", { name: "Living room TV enabled" })).toBeChecked();
    expect(within(list).getByRole("button", { name: "Edit Living room TV" })).toBeEnabled();
    expect(
      within(list).getByRole("button", { name: "More actions for Living room TV" }),
    ).toBeEnabled();
    // Only webhook sources offer a URL to copy.
    expect(
      within(list).queryByRole("button", { name: "Copy webhook URL for Living room TV" }),
    ).not.toBeInTheDocument();
    expect(within(list).getByRole("button", { name: "Copy webhook URL for Sonarr" })).toBeEnabled();
  });

  it("surfaces errors and misconfiguration in the status column", async () => {
    stubAutoscanServer({
      plugins,
      sources: [
        pollSource({
          label: "Broken",
          last_error: "dial tcp: connection refused",
          last_run_at: new Date().toISOString(),
        }),
        webhookSource({ id: "hook-x", label: "Stray", path_rewrites: [{ from: "/x", to: "/x" }] }),
        webhookSource({ id: "hook-off", label: "Paused", enabled: false }),
      ],
    });
    renderPanel();

    const list = await screen.findByRole("list");
    const [broken, stray, paused] = within(list).getAllByRole("listitem");
    expect(within(broken!).getByText(/Last poll failed/)).toBeInTheDocument();
    expect(within(broken!).getByTitle("dial tcp: connection refused")).toBeInTheDocument();
    expect(within(stray!).getByText(/Paths don.t match any library root/)).toBeInTheDocument();
    expect(within(paused!).getByText("Off")).toBeInTheDocument();
  });

  it("enabled switch resends the stored source in full", async () => {
    const source = pollSource({ label: "Living room TV", poll_interval_seconds: 1200 });
    const { writes } = stubAutoscanServer({ plugins, sources: [source] });
    const user = renderPanel();

    await user.click(await screen.findByRole("switch", { name: "Living room TV enabled" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.method).toBe("PUT");
    expect(writes[0]!.path).toMatch(/\/admin\/autoscan\/sources\/poll-a$/);
    expect(writes[0]!.body).toEqual({
      connection_id: "conn-sonarr",
      enabled: false,
      delivery_mode: "poll",
      poll_interval_seconds: 1200,
      path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
      source_config: { lookback: "24h" },
      label: "Living room TV",
    });
  });

  // The two 422 shapes the source write returns: the source rules' fixed
  // detail with no field errors, and Huma's schema detail with field errors.
  it.each([
    {
      shape: "a refused source",
      problem: {
        detail:
          "Invalid source identity, delivery mode, interval, rewrites or provider configuration.",
      },
      shown:
        "Invalid source identity, delivery mode, interval, rewrites or provider configuration.",
    },
    {
      shape: "a schema failure",
      problem: {
        detail: "The request did not pass validation; see errors.",
        errors: [
          {
            location: "body.poll_interval_seconds",
            code: "out_of_range",
            detail: "expected number <= 2147483647",
          },
        ],
      },
      shown: "expected number <= 2147483647",
    },
  ])("shows the server's reason for $shape instead of an uncertain outcome", async (fixture) => {
    const source = pollSource({ enabled: false, label: "Refused" });
    const { writes } = stubAutoscanServer({
      plugins,
      sources: [source],
      handle: (method) =>
        method === "PUT"
          ? new Response(
              JSON.stringify({
                type: "https://siloserver.org/docs/api/v2/problems/validation_failed",
                title: "Validation failed",
                status: 422,
                instance: "urn:silo:request:000000000000000000000001",
                ...fixture.problem,
              }),
              { status: 422, headers: { "Content-Type": "application/problem+json" } },
            )
          : undefined,
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("switch", { name: "Refused enabled" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(fixture.shown));
  });

  it("shows one missing-server message instead of the plugin's error", async () => {
    stubAutoscanServer({
      plugins: [withDescriptor(ARR_POLL_PLUGIN, { connection: "required" })],
      sources: [
        pollSource({
          connection_id: null,
          last_error: "scan_source: no connection supplied",
        }),
      ],
    });
    renderPanel();

    expect(await screen.findAllByText(/No server selected/)).toHaveLength(1);
    expect(screen.getByText(/Last poll failed/)).toBeInTheDocument();
    expect(screen.queryByText("scan_source: no connection supplied")).toBeNull();
  });

  it("never copies a revoked URL from the row after an unconfirmed rotation in Edit", async () => {
    const source = webhookSource();
    const state = {
      plugins,
      sources: [source],
      handle: (method: string, path: string) => {
        if (method === "POST" && path.endsWith("/webhook/rotate")) {
          // The rotation lands on the server, but its response is lost.
          state.sources = [{ ...source, webhook_url: "/api/v2/autoscan/webhooks/rotated-secret" }];
          return new Response(null, { status: 500 });
        }
        return undefined;
      },
    };
    stubAutoscanServer(state);
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Rotate webhook URL" }));
    await user.click(await screen.findByRole("button", { name: "Rotate" }));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "Webhook change could not be confirmed. Refresh source state before another explicit submission.",
      ),
    );
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));

    await navigator.clipboard.writeText("");
    await waitFor(async () => {
      await user.click(screen.getByRole("button", { name: "Copy webhook URL for Sonarr" }));
      expect(await navigator.clipboard.readText()).toBe(
        `${window.location.origin}/api/v2/autoscan/webhooks/rotated-secret`,
      );
    });
  });

  it("keeps the URL hidden after an unconfirmed rotation until a source read succeeds", async () => {
    const source = webhookSource();
    const rotated = `${window.location.origin}/api/v2/autoscan/webhooks/rotated-secret`;
    let readsFail = false;
    const state = {
      plugins,
      sources: [source],
      handle: (method: string, path: string) => {
        if (method === "POST" && path.endsWith("/webhook/rotate")) {
          // The rotation lands on the server, but its response is lost, and the
          // re-read that follows it fails too.
          state.sources = [{ ...source, webhook_url: "/api/v2/autoscan/webhooks/rotated-secret" }];
          readsFail = true;
          return new Response(null, { status: 500 });
        }
        if (method === "GET" && path.endsWith("/admin/autoscan/sources") && readsFail)
          return new Response(null, { status: 500 });
        return undefined;
      },
    };
    stubAutoscanServer(state);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = renderPanel(client);

    await user.click(await screen.findByRole("button", { name: "More actions for Sonarr" }));
    await user.click(await screen.findByRole("menuitem", { name: "Rotate webhook URL" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Rotate" }),
    );
    // The failed re-read keeps the last list, whose URL may be the revoked one.
    expect(
      await screen.findByText("Could not refresh scan sources. Showing the last loaded list."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy webhook URL for Sonarr" })).toBeDisabled();

    // A later successful read, from any trigger, settles the rotation: the
    // current URL can be copied and rotated again without reloading the page.
    readsFail = false;
    await act(() => client.invalidateQueries({ queryKey: ["admin", "autoscan", "sources"] }));
    await navigator.clipboard.writeText("");
    await user.click(await screen.findByRole("button", { name: "Copy webhook URL for Sonarr" }));
    await waitFor(async () => expect(await navigator.clipboard.readText()).toBe(rotated));
    await user.click(screen.getByRole("button", { name: "More actions for Sonarr" }));
    expect(await screen.findByRole("menuitem", { name: "Rotate webhook URL" })).not.toHaveAttribute(
      "data-disabled",
    );
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Webhook delivery URL")).toHaveValue(rotated);
    expect(within(dialog).getByRole("button", { name: "Rotate webhook URL" })).toBeEnabled();
  });

  it("holds the enabled switch while an edit is saved and the sources are re-read", async () => {
    const source = pollSource({ label: "Living room TV" });
    let finishWrite!: (response: Response) => void;
    let finishRead: ((response: Response) => void) | null = null;
    let saved = false;
    const state = {
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [source],
      handle: (method: string, path: string, body: unknown) => {
        if (method === "PUT")
          return new Promise<Response>((resolve) => {
            finishWrite = resolve;
            state.sources = [{ ...source, ...(body as Partial<typeof source>) }];
            saved = true;
          });
        if (method === "GET" && path.endsWith("/admin/autoscan/sources") && saved)
          return new Promise<Response>((resolve) => {
            finishRead = resolve;
          });
        return undefined;
      },
    };
    const { writes } = stubAutoscanServer(state);
    const user = renderPanel();

    const toggle = await screen.findByRole("switch", { name: "Living room TV enabled" });
    expect(toggle).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Edit Living room TV" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "General" }));
    const label = within(dialog).getByRole("textbox", { name: /Custom label/ });
    await user.clear(label);
    await user.type(label, "Den TV");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    // The save is in flight: the row's snapshot is about to be outdated, and
    // the dialog cannot be dismissed before the outcome is known.
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(toggle).toBeDisabled();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // Saved: the row shows the saved source at once, but its switch and Edit
    // wait until the list has been re-read.
    await act(async () =>
      finishWrite(
        new Response(JSON.stringify(state.sources[0]), {
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    await waitFor(() => expect(finishRead).toBeTypeOf("function"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("switch", { name: "Den TV enabled" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit Den TV" })).toBeDisabled();

    await act(async () =>
      finishRead!(
        new Response(JSON.stringify({ items: state.sources, page: { has_more: false } }), {
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Den TV enabled" })).toBeEnabled(),
    );
    expect(screen.getByRole("button", { name: "Edit Den TV" })).toBeEnabled();
  });

  it("shows a rotation from the row in a newly opened Edit when the re-read fails", async () => {
    const source = webhookSource();
    const rotatedPath = "/api/v2/autoscan/webhooks/rotated-secret";
    let readsFail = false;
    const state = {
      plugins,
      sources: [source],
      handle: (method: string, path: string) => {
        if (method === "POST" && path.endsWith("/webhook/rotate")) {
          state.sources = [{ ...source, webhook_url: rotatedPath }];
          readsFail = true;
          return json(state.sources[0]);
        }
        if (method === "GET" && path.endsWith("/admin/autoscan/sources") && readsFail)
          return new Response(null, { status: 500 });
        return undefined;
      },
    };
    stubAutoscanServer(state);
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "More actions for Sonarr" }));
    await user.click(await screen.findByRole("menuitem", { name: "Rotate webhook URL" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Rotate" }),
    );
    await screen.findByText("Could not refresh scan sources. Showing the last loaded list.");

    await user.click(screen.getByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Webhook delivery URL")).toHaveValue(
      `${window.location.origin}${rotatedPath}`,
    );
  });

  it("copies the webhook URL from the row", async () => {
    stubAutoscanServer({ plugins, sources: [webhookSource()] });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Copy webhook URL for Sonarr" }));

    await waitFor(async () =>
      expect(await navigator.clipboard.readText()).toBe(
        `${window.location.origin}/api/v2/autoscan/webhooks/synthetic-secret`,
      ),
    );
  });

  it("rotates a webhook URL from the menu only after confirmation", async () => {
    const { writes } = stubAutoscanServer({ plugins, sources: [webhookSource()] });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "More actions for Sonarr" }));
    await user.click(await screen.findByRole("menuitem", { name: "Rotate webhook URL" }));
    const confirm = await screen.findByRole("alertdialog", {
      name: "Rotate the webhook URL for Sonarr?",
    });
    await user.click(within(confirm).getByRole("button", { name: "Cancel" }));
    expect(writes).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "More actions for Sonarr" }));
    await user.click(await screen.findByRole("menuitem", { name: "Rotate webhook URL" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Rotate" }),
    );
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toMatchObject({ method: "POST" });
    expect(writes[0]!.path).toMatch(/\/sources\/hook-a\/webhook\/rotate$/);
  });

  it("deletes from the menu after confirmation", async () => {
    const { writes } = stubAutoscanServer({ plugins, sources: [pollSource({ label: "Old" })] });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "More actions for Old" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    const confirm = await screen.findByRole("alertdialog", { name: "Delete source?" });
    expect(confirm).toHaveTextContent("“Old” will be permanently removed.");
    await user.click(within(confirm).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toMatchObject({ method: "DELETE" });
    expect(writes[0]!.path).toMatch(/\/sources\/poll-a$/);
  });
});

describe("Edit source dialog", () => {
  it("saves a complete body and keeps the bound connection", async () => {
    const { writes } = stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [pollSource({ label: "Living room TV", poll_interval_seconds: 1200 })],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Living room TV" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit source · Living room TV" });
    expect(
      within(dialog)
        .getAllByRole("tab")
        .map((tab) => tab.textContent),
    ).toEqual(["Connection", "Settings", "Match paths", "General"]);

    await user.click(within(dialog).getByRole("tab", { name: "General" }));
    const label = within(dialog).getByRole("textbox", { name: "Custom label (optional)" });
    await user.clear(label);
    await user.type(label, "Basement TV");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.method).toBe("PUT");
    expect(writes[0]!.body).toEqual({
      connection_id: "conn-sonarr",
      enabled: true,
      delivery_mode: "poll",
      poll_interval_seconds: 1200,
      path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
      source_config: { lookback: "24h" },
      label: "Basement TV",
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("sends edited settings, interval and mappings together", async () => {
    const { writes } = stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [pollSource()],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr / Radarr" }));
    const dialog = await screen.findByRole("dialog");

    await user.click(within(dialog).getByRole("tab", { name: "Settings" }));
    await user.type(within(dialog).getByRole("textbox", { name: /Check interval/ }), "900");
    const lookback = within(dialog).getByRole("textbox", { name: "Lookback" });
    await user.clear(lookback);
    await user.type(lookback, "48h");

    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    await user.click(within(dialog).getByRole("button", { name: "Add a mapping" }));
    const froms = within(dialog).getAllByRole("textbox", { name: "Path the source reports" });
    const tos = within(dialog).getAllByRole("textbox", { name: "Path Silo uses" });
    await user.type(froms[1]!, "/data/movies");
    await user.type(tos[1]!, "/mnt/media/movies");

    await user.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toEqual({
      connection_id: "conn-sonarr",
      enabled: true,
      delivery_mode: "poll",
      poll_interval_seconds: 900,
      path_rewrites: [
        { from: "/data/tv", to: "/mnt/media/tv" },
        { from: "/data/movies", to: "/mnt/media/movies" },
      ],
      source_config: { lookback: "48h" },
      label: "",
    });
  });

  it("blocks saving a half-filled mapping and shows where it is", async () => {
    const { writes } = stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [pollSource()],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr / Radarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    await user.click(within(dialog).getByRole("button", { name: "Add a mapping" }));
    await user.type(
      within(dialog).getAllByRole("textbox", { name: "Path the source reports" })[1]!,
      "/data/movies",
    );
    await user.click(within(dialog).getByRole("tab", { name: "General" }));
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(within(dialog).getByRole("tab", { name: /Match paths/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Each path mapping needs both paths.",
    );
    expect(writes).toHaveLength(0);
  });

  it("blocks saving an interval larger than the server stores", async () => {
    const { writes } = stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [pollSource()],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr / Radarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Settings" }));
    await user.type(within(dialog).getByRole("textbox", { name: /Check interval/ }), "2147483648");
    expect(
      within(dialog).getByText("Must be a whole number from 1 to 2,147,483,647."),
    ).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "The check interval must be a whole number of seconds.",
    );
    expect(writes).toHaveLength(0);
  });

  it("blocks saving until a required plugin setting is filled in", async () => {
    const requiredRoot = withDescriptor(ARR_POLL_PLUGIN, {
      config_form: {
        fields: [
          {
            key: "root",
            label: "Root",
            control: "TEXT",
            required: true,
            secret: false,
            multiline: false,
          },
        ],
      },
    });
    const { writes } = stubAutoscanServer({
      plugins: [requiredRoot, ARR_WEBHOOK_PLUGIN],
      connections: [SONARR_CONNECTION],
      sources: [pollSource()],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr / Radarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(within(dialog).getByRole("tab", { name: /Settings/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Some settings are missing or invalid.",
    );
    expect(writes).toHaveLength(0);

    await user.type(within(dialog).getByLabelText(/Root/), "/synthetic/library");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ source_config: { root: "/synthetic/library" } });
  });

  it("offers Sync from server only for a saved connection", async () => {
    stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [
        pollSource({ label: "Bound" }),
        pollSource({ id: "poll-b", label: "Unbound", connection_id: null }),
      ],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Bound" }));
    let dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    expect(within(dialog).getByRole("button", { name: "Sync from server" })).toBeEnabled();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Edit Unbound" }));
    dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    expect(within(dialog).getByRole("button", { name: "Sync from server" })).toBeDisabled();
    expect(
      within(dialog).getByText("Choose a server on the Connection tab and save to sync from it."),
    ).toBeInTheDocument();
  });

  it("edits a webhook source's provider without dropping its paths", async () => {
    const { writes } = stubAutoscanServer({ plugins, sources: [webhookSource()] });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit source · Sonarr" });
    expect(
      within(dialog)
        .getAllByRole("tab")
        .map((tab) => tab.textContent),
    ).toEqual(["Connect", "Match paths", "General"]);
    expect(within(dialog).getByLabelText("Webhook delivery URL")).toHaveValue(
      `${window.location.origin}/api/v2/autoscan/webhooks/synthetic-secret`,
    );
    expect(within(dialog).getByText(/Tick On File Import/)).toHaveTextContent(
      "On Episode File Delete",
    );

    await user.click(within(dialog).getByRole("combobox", { name: "Sent by" }));
    await user.click(await screen.findByRole("option", { name: "Radarr" }));
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    // The transport omits null connection and interval: the server stores
    // "none" for an absent field, which is what this source has.
    expect(writes[0]!.body).toEqual({
      enabled: true,
      delivery_mode: "webhook",
      path_rewrites: [{ from: "/tv", to: "/mnt/media/tv" }],
      source_config: { webhook_provider: "radarr" },
      label: "",
    });
  });

  it("blocks saving a webhook source with no complete path mapping", async () => {
    const { writes } = stubAutoscanServer({ plugins, sources: [webhookSource()] });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    await user.click(within(dialog).getByRole("button", { name: "Remove mapping 1" }));
    await user.click(within(dialog).getByRole("tab", { name: "General" }));
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(within(dialog).getByRole("tab", { name: /Match paths/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Add at least one complete path mapping, or deliveries will match no library.",
    );
    expect(writes).toHaveLength(0);
  });

  it("still saves a webhook source that was stored without mappings", async () => {
    const { writes } = stubAutoscanServer({
      plugins,
      sources: [webhookSource({ path_rewrites: [] })],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "General" }));
    await user.type(within(dialog).getByRole("textbox", { name: /custom label/i }), "Same paths");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ label: "Same paths", path_rewrites: [] });
  });

  it("saves a polling source with no path mappings", async () => {
    const { writes } = stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [pollSource()],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr / Radarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    await user.click(within(dialog).getByRole("button", { name: "Remove mapping 1" }));
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ path_rewrites: [] });
  });

  it("shows an absent provider as Detect automatically and keeps it absent", async () => {
    const { writes } = stubAutoscanServer({
      plugins,
      sources: [webhookSource({ source_config: {} })],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr/Radarr Webhook" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("combobox", { name: "Sent by" })).toHaveTextContent(
      "Detect automatically",
    );
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ source_config: {} });
  });

  it("requires a server when the plugin declares one required", async () => {
    const { writes } = stubAutoscanServer({
      plugins: [withDescriptor(ARR_POLL_PLUGIN, { connection: "required" }), ARR_WEBHOOK_PLUGIN],
      connections: [SONARR_CONNECTION],
      sources: [pollSource({ connection_id: null })],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr / Radarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "General" }));
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(within(dialog).getByRole("tab", { name: /Connection/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Choose the server this source reads from.",
    );
    expect(writes).toHaveLength(0);

    await user.click(within(dialog).getByRole("combobox", { name: /Which server\? \(required\)/ }));
    expect(screen.queryByRole("option", { name: /No server needed/ })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("option", { name: "Sonarr 4K" }));
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ connection_id: "conn-sonarr" });
  });

  it("offers to split a seeded root per type only for a webhook source", async () => {
    stubAutoscanServer({
      plugins,
      connections: [SONARR_CONNECTION],
      sources: [
        pollSource({ label: "Polled", path_rewrites: [{ from: "/data", to: "/mnt/media" }] }),
        webhookSource({ label: "Hooked", path_rewrites: [{ from: "/data", to: "/mnt/media" }] }),
      ],
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Polled" }));
    let dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    expect(within(dialog).queryByRole("button", { name: /Split into/ })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Edit Hooked" }));
    dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("tab", { name: "Match paths" }));
    await user.click(within(dialog).getByRole("button", { name: /Split into 2 rows/ }));

    // Each branch has its own root on the Sonarr/Radarr side; copying "/data"
    // into both rows would send every delivery to the first one.
    expect(
      within(dialog)
        .getAllByRole("textbox", { name: "Sonarr/Radarr root folder" })
        .map((input) => (input as HTMLInputElement).value),
    ).toEqual(["", ""]);
  });

  it("keeps a provider chosen before the plugin list loads", async () => {
    let releasePlugins!: () => void;
    const pluginsHeld = new Promise<void>((resolve) => {
      releasePlugins = resolve;
    });
    const { writes } = stubAutoscanServer({
      plugins,
      sources: [webhookSource()],
      handle: (method, path) =>
        method === "GET" && path.endsWith("/admin/autoscan/scan-source-plugins")
          ? pluginsHeld.then(() => json({ items: plugins, page: { has_more: false } }))
          : undefined,
    });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("combobox", { name: "Sent by" }));
    await user.click(await screen.findByRole("option", { name: "Radarr" }));

    expect(screen.queryByText("Sonarr/Radarr Webhook")).toBeNull();
    await act(async () => releasePlugins());
    // The plugin's display name only appears once its descriptor has loaded.
    await screen.findAllByText("Sonarr/Radarr Webhook");
    expect(within(dialog).getByRole("combobox", { name: "Sent by" })).toHaveTextContent("Radarr");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ source_config: { webhook_provider: "radarr" } });
  });

  it("deletes the source from the dialog footer after confirmation", async () => {
    const { writes } = stubAutoscanServer({ plugins, sources: [webhookSource()] });
    const user = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Edit Sonarr" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Delete source" }));
    const confirm = await screen.findByRole("alertdialog", { name: "Delete source?" });
    await user.click(within(confirm).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toMatchObject({ method: "DELETE" });
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: /Edit source/ })).not.toBeInTheDocument(),
    );
  });
});

describe("Add source dialog", () => {
  it("sends the label and path mappings for a polling source", async () => {
    const { writes } = stubAutoscanServer({ plugins, connections: [], sources: [] });
    const user = renderPanel();

    await screen.findByText(/No scan sources yet/);
    await user.click(screen.getByRole("button", { name: "Add source" }));
    const dialog = await screen.findByRole("dialog", { name: "Add scan source" });
    await user.click(within(dialog).getByRole("button", { name: /^Sonarr \/ Radarr/ }));

    await user.click(within(dialog).getByRole("button", { name: "Add a mapping" }));
    await user.type(
      within(dialog).getByRole("textbox", { name: "Path the source reports" }),
      "/data/tv",
    );
    await user.type(
      within(dialog).getByRole("textbox", { name: "Path Silo uses" }),
      "/mnt/media/tv",
    );
    await user.type(
      within(dialog).getByRole("textbox", { name: "Custom label (optional)" }),
      "  Basement  ",
    );
    expect(
      within(dialog).getByText(/Edit → Match paths can read these from the server/),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Add source" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.method).toBe("POST");
    expect(writes[0]!.body).toMatchObject({
      plugin_id: "silo.autoscan.arr",
      capability_id: "arr",
      // Everything a polling source needs is collected here, so it starts on.
      enabled: true,
      delivery_mode: "poll",
      path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
      label: "Basement",
    });
  });

  it("finishes webhook setup when the operator types or presses Escape during the create", async () => {
    let finishCreate!: () => void;
    const { writes } = stubAutoscanServer({
      plugins,
      sources: [],
      handle: (method, path, body) => {
        const created = {
          ...(body as object),
          id: "created-source",
          last_run_at: null,
          last_error: null,
          webhook_configured: false,
        };
        if (method === "POST" && path.endsWith("/admin/autoscan/sources"))
          return new Promise<Response>((resolve) => {
            finishCreate = () => resolve(json(created, 201));
          });
        if (method === "POST" && path.endsWith("/sources/created-source/webhook"))
          return json({
            ...writes[0]!.body,
            id: "created-source",
            last_run_at: null,
            last_error: null,
            webhook_configured: true,
            webhook_url: "/api/v2/autoscan/webhooks/new-secret",
          });
        return undefined;
      },
    });
    const user = renderPanel();

    await screen.findByText(/No scan sources yet/);
    await user.click(screen.getByRole("button", { name: "Add source" }));
    const dialog = await screen.findByRole("dialog", { name: "Add scan source" });
    await user.click(within(dialog).getByRole("button", { name: /^Sonarr\/Radarr Webhook/ }));
    await user.type(
      within(dialog).getByRole("textbox", { name: "Sonarr/Radarr root folder" }),
      "/data",
    );
    await user.click(within(dialog).getByRole("button", { name: "Create and continue" }));
    await waitFor(() => expect(writes).toHaveLength(1));

    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Add scan source" })).toBeInTheDocument();
    await user.type(
      within(dialog).getByRole("textbox", { name: "Custom label (optional)" }),
      "Late",
    );
    await act(async () => finishCreate());

    const connect = await screen.findByRole("dialog", {
      name: "Almost done — connect your service",
    });
    expect(within(connect).getByRole("textbox", { name: "1. Copy this URL" })).toHaveValue(
      `${window.location.origin}/api/v2/autoscan/webhooks/new-secret`,
    );
  });

  it("explains instead of creating a webhook source with no complete mapping", async () => {
    const { writes } = stubAutoscanServer({ plugins, sources: [] });
    const user = renderPanel();

    await screen.findByText(/No scan sources yet/);
    await user.click(screen.getByRole("button", { name: "Add source" }));
    const dialog = await screen.findByRole("dialog", { name: "Add scan source" });
    await user.click(within(dialog).getByRole("button", { name: /^Sonarr\/Radarr Webhook/ }));
    await user.click(within(dialog).getByRole("button", { name: "Create and continue" }));

    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Add at least one complete path mapping",
    );
    expect(writes).toHaveLength(0);
  });

  it("re-seeds the suggested mapping when the provider changes, until it is edited", async () => {
    stubAutoscanServer({ plugins, sources: [] });
    const user = renderPanel();

    await screen.findByText(/No scan sources yet/);
    await user.click(screen.getByRole("button", { name: "Add source" }));
    const dialog = await screen.findByRole("dialog", { name: "Add scan source" });
    await user.click(within(dialog).getByRole("button", { name: /^Sonarr\/Radarr Webhook/ }));
    const silo = () =>
      within(dialog)
        .getAllByRole("textbox", { name: "Path Silo uses" })
        .map((input) => (input as HTMLInputElement).value);
    expect(silo()).toEqual(["/mnt/media"]);

    const chooseProvider = async (name: string) => {
      await user.click(within(dialog).getByRole("combobox", { name: "Sent by" }));
      await user.click(await screen.findByRole("option", { name }));
    };
    await chooseProvider("Sonarr");
    expect(silo()).toEqual(["/mnt/media/tv"]);
    await chooseProvider("Radarr");
    expect(silo()).toEqual(["/mnt/media/movies"]);

    await user.type(
      within(dialog).getByRole("textbox", { name: "Sonarr/Radarr root folder" }),
      "/movies",
    );
    await chooseProvider("Sonarr");
    expect(silo()).toEqual(["/mnt/media/movies"]);
  });

  it("marks a step done only once it is answered", async () => {
    const changeLog = withDescriptor(
      {
        ...ARR_POLL_PLUGIN,
        plugin_id: "silo.autoscan.changelog",
        capability_id: "changelog",
        display_name: "Change log file",
      },
      {
        connection: "none",
        connection_kinds: [],
        config_form: {
          fields: [
            {
              key: "path",
              label: "Change log path",
              control: "TEXT",
              required: true,
              secret: false,
              multiline: false,
            },
          ],
        },
      },
    );
    stubAutoscanServer({ plugins: [changeLog, ...plugins], sources: [] });
    const user = renderPanel();

    await screen.findByText(/No scan sources yet/);
    await user.click(screen.getByRole("button", { name: "Add source" }));
    const dialog = await screen.findByRole("dialog", { name: "Add scan source" });
    await user.click(within(dialog).getByRole("button", { name: /^Change log file/ }));

    const trail = () => {
      const items = within(within(dialog).getByRole("list")).getAllByRole("listitem");
      return items.map((item) => ({
        text: item.textContent,
        current: item.querySelector("[aria-current=step]") !== null,
      }));
    };
    expect(trail()).toEqual([
      { text: "✓What changes?(done)", current: false },
      { text: "2Set it up", current: true },
      { text: "3Match paths", current: false },
    ]);

    await user.type(within(dialog).getByRole("textbox", { name: /Change log path/ }), "/x.log");
    expect(trail()).toEqual([
      { text: "✓What changes?(done)", current: false },
      { text: "✓Set it up(done)", current: false },
      { text: "3Match paths", current: true },
    ]);
  });

  it("asks for a server before adding when the plugin declares one required", async () => {
    const { writes } = stubAutoscanServer({
      plugins: [withDescriptor(ARR_POLL_PLUGIN, { connection: "required" })],
      connections: [SONARR_CONNECTION],
      sources: [],
    });
    const user = renderPanel();

    await screen.findByText(/No scan sources yet/);
    await user.click(screen.getByRole("button", { name: "Add source" }));
    const dialog = await screen.findByRole("dialog", { name: "Add scan source" });
    await user.click(within(dialog).getByRole("button", { name: /^Sonarr \/ Radarr/ }));
    await user.click(within(dialog).getByRole("button", { name: "Add source" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Choose the server this source reads from.",
    );
    expect(writes).toHaveLength(0);

    await user.click(within(dialog).getByRole("combobox", { name: /Which server\? \(required\)/ }));
    expect(screen.queryByRole("option", { name: /No server needed/ })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("option", { name: "Sonarr 4K" }));
    await user.click(within(dialog).getByRole("button", { name: "Add source" }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ connection_id: "conn-sonarr", enabled: true });
  });
});
