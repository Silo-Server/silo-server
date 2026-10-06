import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";
import SourcesPanel from "./SourcesPanel";

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [{ id: 2, name: "TV Shows", type: "series", paths: ["/mnt/media/tv"], enabled: true }],
  }),
}));

// Radix Select opens through pointer capture, which jsdom lacks.
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const webhookURL = "/api/v2/autoscan/webhooks/first-synthetic";

const created = {
  id: "source-first",
  plugin_id: "silo.autoscan.arr-webhook",
  capability_id: "arr-webhook",
  connection_id: null,
  enabled: true,
  delivery_mode: "webhook",
  poll_interval_seconds: null,
  path_rewrites: [{ from: "/data/media/tv", to: "/mnt/media/tv" }],
  source_config: { webhook_provider: "sonarr" },
  label: "",
  last_run_at: null,
  last_error: null,
  webhook_configured: false,
};

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

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

// stubAPI serves the autoscan endpoints the panel reads. With
// `failSourceReadsAfterCreate`, every sources GET after the create fails.
function stubAPI({ failSourceReadsAfterCreate = false } = {}) {
  let sources: unknown[] = [];
  let createdSource = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input instanceof Request ? input.url : input), "http://test");
      const method = (
        init?.method ?? (input instanceof Request ? input.method : "GET")
      ).toUpperCase();
      const path = url.pathname;
      if (path.endsWith("/admin/autoscan/sources") && method === "GET") {
        if (failSourceReadsAfterCreate && createdSource) {
          return json({ title: "Internal Server Error", status: 500 }, 500);
        }
        return json({ items: sources, page: { has_more: false } });
      }
      if (path.endsWith("/admin/autoscan/sources") && method === "POST") {
        createdSource = true;
        sources = [created];
        return json(created, 201);
      }
      if (path.endsWith("/admin/autoscan/sources/source-first/webhook") && method === "POST") {
        const withWebhook = { ...created, webhook_configured: true, webhook_url: webhookURL };
        sources = [withWebhook];
        return json(withWebhook);
      }
      if (path.endsWith("/admin/autoscan/connections")) {
        return json({ items: [], page: { has_more: false } });
      }
      if (path.endsWith("/admin/autoscan/settings")) {
        return json({ enabled: true, default_poll_interval_seconds: 600, debounce_seconds: 60 });
      }
      if (path.endsWith("/admin/autoscan/scan-source-plugins")) {
        return json({
          items: [
            {
              plugin_id: "silo.autoscan.arr-webhook",
              capability_id: "arr-webhook",
              display_name: "Sonarr/Radarr Webhook",
              description: "Sonarr or Radarr posts to Silo the moment an import finishes.",
              descriptor: {
                delivery_modes: ["webhook"],
                connection: "none",
                connection_kinds: ["sonarr", "radarr"],
                emits_native_paths: false,
                summary: "No API key needed.",
                icon_url: "",
              },
            },
          ],
          page: { has_more: false },
        });
      }
      return json({ title: "unexpected request" }, 404);
    }),
  );
}

async function addFirstWebhookSource() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <SourcesPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  const user = userEvent.setup();

  await screen.findByText(/No scan sources yet/);
  await user.click(screen.getByRole("button", { name: "Add source" }));
  await user.click(await screen.findByRole("button", { name: /Sonarr\/Radarr Webhook/ }));
  const remote = screen.getByRole("textbox", { name: "Sonarr/Radarr root folder" });
  await user.clear(remote);
  await user.type(remote, "/data/media/tv");
  const local = screen.getByRole("textbox", { name: "Path Silo uses" });
  await user.clear(local);
  await user.type(local, "/mnt/media/tv");
  await user.click(screen.getByRole("button", { name: "Create and continue" }));
}

it("shows the Connect it step after creating the first webhook source", async () => {
  stubAPI();
  await addFirstWebhookSource();

  expect(
    await screen.findByRole("dialog", { name: "Almost done — connect your service" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "1. Copy this URL" })).toHaveValue(
    `${window.location.origin}${webhookURL}`,
  );
});

it("keeps the Connect it step when the sources list fails to refresh", async () => {
  stubAPI({ failSourceReadsAfterCreate: true });
  await addFirstWebhookSource();

  expect(
    await screen.findByRole("dialog", { name: "Almost done — connect your service" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "1. Copy this URL" })).toHaveValue(
    `${window.location.origin}${webhookURL}`,
  );
  expect(
    await screen.findByText("Could not refresh scan sources. Showing the last loaded list."),
  ).toBeInTheDocument();
});
