// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { v2, V2ProblemError } from "@/api/v2/request";
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
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({
    data: [
      { id: 1, username: "member" },
      { id: 7, username: "sam" },
    ],
    isLoading: false,
  }),
}));

const CREATED = "2026-09-01T00:00:00Z";
function reply(options: unknown, body: unknown, etag = '"initial"') {
  (options as { onResponse?: (r: Response) => void })?.onResponse?.(
    new Response(null, { headers: { ETag: etag } }),
  );
  return Promise.resolve(body) as never;
}

type Options = {
  query?: Record<string, unknown>;
  path?: Record<string, string>;
  body?: Record<string, unknown>;
};
type Handler = (options: Options) => unknown;

/** Answers each v2 operation from `handlers`; any other operation fails. */
function serve(handlers: Record<string, Handler>) {
  vi.mocked(v2).mockImplementation((operation, options) => {
    const handler = handlers[operation];
    if (!handler) return Promise.reject(new Error(`unexpected ${operation}`)) as never;
    return reply(options, handler((options ?? {}) as Options));
  });
}
function calls(operation: string) {
  return vi
    .mocked(v2)
    .mock.calls.filter(([op]) => op === operation)
    .map(([, options]) => options as Options);
}

function request(id: string, title: string, fields: Record<string, unknown> = {}) {
  return {
    id,
    provider: "tmdb",
    media_type: "movie",
    tmdb_id: Number(id.replace(/\D/g, "")) || 1,
    title,
    status: "pending",
    outcome: "active",
    state: "pending",
    requested_by_user_id: "1",
    is_anime: false,
    seasons: [],
    season_progress: [],
    targets: [],
    created_at: CREATED,
    updated_at: CREATED,
    ...fields,
  };
}
const target = (id: string, fields: Record<string, unknown> = {}) => ({
  id,
  request_id: "r",
  quality: "1080p",
  is_anime: false,
  status: "queued",
  instance_name: "Radarr",
  route_name: "Everything else",
  created_at: CREATED,
  updated_at: CREATED,
  ...fields,
});
const COUNTS = { needs_approval: 1, in_progress: 2, failed: 1, done: 1 };
const capabilities = () => ({ available: true, guarded_configuration: true });
const page = (items: unknown[]) => ({ items, page: { has_more: false } });

/** One queue per view, as the server filters it. */
function queue(byView: Record<string, unknown[]>, counts = COUNTS): Record<string, Handler> {
  return {
    "GET /api/v2/admin/requests/capabilities": capabilities,
    "GET /api/v2/admin/requests/counts": () => counts,
    "GET /api/v2/admin/requests": ({ query }) => page(byView[String(query?.view)] ?? []),
  };
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{location.search}</output>;
}
function mount(path: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route
            path="/admin/requests"
            element={
              <>
                <AdminRequests />
                <LocationProbe />
              </>
            }
          />
          <Route path="/admin/settings/requests" element={<h1>Request settings page</h1>} />
          <Route path="/admin/users" element={<h1>Users page</h1>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}
const rowOf = async (title: string) =>
  (await screen.findByRole("link", { name: title })).closest("li") as HTMLElement;
const buttonNames = (row: HTMLElement) =>
  within(row)
    .getAllByRole("button")
    .map((button) => button.getAttribute("aria-label") ?? button.textContent)
    .filter((name) => !name?.startsWith("Details") && name !== "member");

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("request administration", () => {
  it.each(["settings", "integrations"])(
    "sends the retired ?tab=%s to the Requests settings page",
    async (tab) => {
      serve({ "GET /api/v2/admin/requests/capabilities": capabilities });
      mount(`/admin/requests?tab=${tab}`);
      expect(
        await screen.findByRole("heading", { name: "Request settings page" }),
      ).toBeInTheDocument();
    },
  );

  it("links to the Requests settings page and shows only the queue", async () => {
    serve(queue({}));
    mount("/admin/requests");
    expect(await screen.findByRole("link", { name: "Request settings" })).toHaveAttribute(
      "href",
      "/admin/settings/requests",
    );
    // The queue's views are the page's only tabs.
    expect(screen.getAllByRole("tablist").map((list) => list.getAttribute("aria-label"))).toEqual([
      "Request views",
    ]);
    expect(screen.queryByRole("tab", { name: "User Overrides" })).toBeNull();
  });

  it("sends the retired ?tab=overrides to the accounts list and says where limits went", async () => {
    serve({ "GET /api/v2/admin/requests/capabilities": capabilities });
    mount("/admin/requests?tab=overrides");
    expect(await screen.findByRole("heading", { name: "Users page" })).toBeInTheDocument();
    expect(toast.info).toHaveBeenCalledWith(
      "Request limits moved to each account",
      expect.objectContaining({ id: "request-overrides-moved" }),
    );
    expect(calls("GET /api/v2/admin/request-users/{user_id}/limit")).toHaveLength(0);
  });

  it("opens on In progress when nothing needs approval, and shows each view's count", async () => {
    serve(
      queue(
        { in_progress: [request("r2", "Approved Title", { status: "approved" })] },
        { needs_approval: 0, in_progress: 2, failed: 1, done: 3 },
      ),
    );
    mount("/admin/requests");
    expect(await rowOf("Approved Title")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent("view=in_progress");
    const views = screen.getByRole("tablist", { name: "Request views" });
    expect(
      within(views)
        .getAllByRole("tab")
        .map((tab) => tab.textContent),
    ).toEqual(["Needs approval 0", "In progress 2", "Failed 1", "Done 3"]);
    expect(screen.getByRole("tab", { name: "In progress 2" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(calls("GET /api/v2/admin/requests").map((options) => options.query?.view)).toEqual([
      "in_progress",
    ]);
  });

  it("opens on Needs approval while something waits, and keeps the view a link names", async () => {
    serve(queue({ needs_approval: [request("r1", "Waiting Title")] }));
    mount("/admin/requests");
    expect(await rowOf("Waiting Title")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent("view=needs_approval");
    cleanup();

    serve(queue({ failed: [request("r4", "Broken Title", { outcome: "failed" })] }));
    mount("/admin/requests?view=failed");
    expect(await rowOf("Broken Title")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Failed 1" })).toHaveAttribute("aria-selected", "true");
  });

  it("offers only the actions the server allows in each view", async () => {
    serve(
      queue({
        needs_approval: [request("r1", "Waiting Title")],
        in_progress: [
          request("r2", "Unsent Title", { status: "approved", state: "approved" }),
          request("r3", "Sent Title", {
            status: "queued",
            state: "processing",
            targets: [target("t3")],
          }),
        ],
        failed: [
          request("r4", "Broken Title", {
            status: "approved",
            outcome: "failed",
            state: "failed",
            last_error: "no fulfillment backend configured",
          }),
        ],
        done: [request("r5", "Finished Title", { status: "completed", state: "available" })],
      }),
    );
    mount("/admin/requests?view=needs_approval");
    expect(buttonNames(await rowOf("Waiting Title"))).toEqual([
      "Approve: Waiting Title",
      "Decline: Waiting Title",
    ]);

    fireEvent.mouseDown(screen.getByRole("tab", { name: /^In progress/ }));
    expect(buttonNames(await rowOf("Unsent Title"))).toEqual(["Cancel request: Unsent Title"]);
    expect(buttonNames(await rowOf("Sent Title"))).toEqual([]);

    fireEvent.mouseDown(screen.getByRole("tab", { name: /^Failed/ }));
    const broken = await rowOf("Broken Title");
    // An admin retries a failed request or closes it.
    expect(buttonNames(broken)).toEqual(["Retry: Broken Title", "Cancel request: Broken Title"]);
    expect(within(broken).getByText("no fulfillment backend configured")).toBeInTheDocument();

    fireEvent.mouseDown(screen.getByRole("tab", { name: /^Done/ }));
    expect(buttonNames(await rowOf("Finished Title"))).toEqual([]);
  });

  it("declines with the reason given and refreshes the counts", async () => {
    const handlers = queue({ needs_approval: [request("r1", "Waiting Title")] });
    serve({
      ...handlers,
      "POST /api/v2/admin/requests/{id}/decline": ({ path }) =>
        request(path!.id!, "Waiting Title", { outcome: "declined", state: "declined" }),
    });
    mount("/admin/requests?view=needs_approval");
    fireEvent.click(within(await rowOf("Waiting Title")).getByRole("button", { name: /^Decline/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Reason (optional)"), {
      target: { value: "  Already in the library  " },
    });
    const countsBefore = calls("GET /api/v2/admin/requests/counts").length;
    fireEvent.click(within(dialog).getByRole("button", { name: "Decline" }));
    await waitFor(() =>
      expect(calls("POST /api/v2/admin/requests/{id}/decline")).toEqual([
        expect.objectContaining({ path: { id: "r1" }, body: { reason: "Already in the library" } }),
      ]),
    );
    await waitFor(() =>
      expect(calls("GET /api/v2/admin/requests/counts").length).toBeGreaterThan(countsBefore),
    );
  });

  it("shows a spinner on the running action until the server answers", async () => {
    const failed = request("r4", "Broken Title", {
      status: "approved",
      outcome: "failed",
      state: "failed",
    });
    let finish: (() => void) | undefined;
    serve({
      ...queue({ failed: [failed] }),
      "POST /api/v2/admin/requests/{id}/retry": ({ path }) =>
        new Promise((resolve) => {
          finish = () => resolve(request(path!.id!, "Broken Title", { status: "queued" }));
        }),
    });
    mount("/admin/requests?view=failed");
    const row = await rowOf("Broken Title");
    const retry = within(row).getByRole("button", { name: "Retry: Broken Title" });
    const cancel = within(row).getByRole("button", { name: "Cancel request: Broken Title" });
    expect(retry).not.toHaveAttribute("aria-busy");

    fireEvent.click(retry);
    await waitFor(() => expect(retry).toHaveAttribute("aria-busy", "true"));
    expect(retry.querySelector(".animate-spin")).not.toBeNull();
    // The row's other actions wait too, without a spinner of their own.
    expect(retry).toBeDisabled();
    expect(cancel).toBeDisabled();
    expect(cancel).not.toHaveAttribute("aria-busy");

    await act(async () => finish!());
    await waitFor(() => expect(retry).not.toHaveAttribute("aria-busy"));
    expect(retry.querySelector(".animate-spin")).toBeNull();
    expect(retry).toBeEnabled();
  });

  it("approves in bulk four at a time and reports the ones that failed", async () => {
    const rows = Array.from({ length: 6 }, (_, i) => request(`p${i + 1}`, `Title ${i + 1}`));
    const pending = new Map<string, { resolve: () => void; reject: (err: unknown) => void }>();
    serve({
      ...queue({ needs_approval: rows }, { ...COUNTS, needs_approval: 6 }),
      "POST /api/v2/admin/requests/{id}/approve": ({ path }) =>
        new Promise((resolve, reject) => {
          const id = path!.id!;
          pending.set(id, {
            resolve: () => resolve(request(id, id, { status: "approved", state: "approved" })),
            reject,
          });
        }),
    });
    mount("/admin/requests?view=needs_approval");
    await rowOf("Title 6");
    fireEvent.click(screen.getByLabelText("Select all 6 shown"));
    const countsBefore = calls("GET /api/v2/admin/requests/counts").length;
    fireEvent.click(screen.getByRole("button", { name: "Approve 6" }));

    await waitFor(() => expect(pending.size).toBe(4));
    expect(screen.getByText(/Approving 0 of 6/)).toBeInTheDocument();
    // Row actions wait while the bulk action runs.
    expect(
      within(await rowOf("Title 1")).getByRole("button", { name: "Approve: Title 1" }),
    ).toBeDisabled();

    await act(async () => pending.get("p1")!.resolve());
    await waitFor(() => expect(pending.size).toBe(5));
    await act(async () => {
      pending.get("p2")!.resolve();
      pending.get("p3")!.reject(
        new V2ProblemError("adminApproveRequest", {
          type: "https://silo.test/problems/conflict",
          title: "Conflict",
          status: 409,
          detail: "The request is no longer pending.",
          instance: "test",
        }),
      );
      pending.get("p4")!.resolve();
    });
    await waitFor(() => expect(pending.size).toBe(6));
    await act(async () => {
      pending.get("p5")!.resolve();
      pending.get("p6")!.resolve();
    });

    const report = await screen.findByRole("alert");
    expect(report).toHaveTextContent("5 of 6 approved; 1 couldn't be.");
    expect(report).toHaveTextContent("Title 3: The request is no longer pending.");
    expect(calls("POST /api/v2/admin/requests/{id}/approve")).toHaveLength(6);
    await waitFor(() =>
      expect(calls("GET /api/v2/admin/requests/counts").length).toBeGreaterThan(countsBefore),
    );
    // The failed one stays selected, ready to try again.
    expect(screen.getByRole("checkbox", { name: "Select Title 3" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Select Title 1" })).not.toBeChecked();
  });

  it("shows a request's servers, where it would go, and its history in plain words", async () => {
    serve({
      ...queue({
        needs_approval: [
          request("r1", "Waiting Title", {
            last_error: "Radarr did not answer",
            targets: [
              target("t1", {
                quality: "2160p",
                instance_name: "Radarr 4K",
                route_name: "4K rule",
                status: "failed",
                external_status: "rejected",
                last_error: "quality profile missing",
              }),
            ],
          }),
        ],
      }),
      "GET /api/v2/admin/requests/{id}/events": () => ({
        items: [
          { id: "4", type: "mystery_step", message: "custom note", created_at: CREATED },
          { id: "3", type: "submit_deferred", message: "Radarr unreachable", created_at: CREATED },
          {
            id: "2",
            type: "status_approved",
            actor_user_id: "1",
            actor_username: "admin",
            created_at: CREATED,
          },
          { id: "1", type: "created", actor_user_id: "9", created_at: CREATED },
        ],
      }),
      "POST /api/v2/admin/request-routes/preview": () => ({
        facts: {
          anime: false,
          company_ids: [],
          genre_ids: [],
          keyword_ids: [],
          network_ids: [],
          origin_countries: [],
          year: 2019,
          content_rating: "PG-13",
        },
        rules: [
          {
            route_id: "kids",
            route_name: "Kids",
            is_fallback: false,
            enabled: true,
            unmet_conditions: ["max_content_rating"],
            hd: "no_match",
            uhd: "no_match",
          },
          {
            route_id: "fallback-movie",
            route_name: "Everything else",
            is_fallback: true,
            enabled: true,
            unmet_conditions: [],
            hd: "sends",
            uhd: "skips",
          },
        ],
        tiers: [
          {
            quality: "1080p",
            route_id: "fallback-movie",
            integration_id: "s1",
            integration_name: "Radarr",
            route_name: "Everything else",
          },
          { quality: "2160p", route_id: "fallback-movie", route_name: "Everything else" },
        ],
      }),
    });
    mount("/admin/requests?view=needs_approval");
    const row = await rowOf("Waiting Title");
    // A click anywhere on the row but its controls opens the dialog.
    fireEvent.click(within(row).getByText("Radarr did not answer"));
    const dialog = await screen.findByRole("dialog");

    const history = await within(dialog).findByRole("list", { name: "Request history" });
    const entries = within(history).getAllByRole("listitem");
    expect(entries).toHaveLength(4);
    // Newest first; a type this client doesn't know shows as it is.
    expect(entries[0]).toHaveTextContent(/^mystery_step.*custom note$/);
    expect(entries[1]).toHaveTextContent(/^Couldn't send yet.*Radarr unreachable$/);
    expect(entries[2]).toHaveTextContent(/^Approved by admin/);
    expect(entries[3]).toHaveTextContent(/^Requested by User 9/);

    const servers = within(dialog).getByRole("table");
    expect(servers).toHaveTextContent("2160p");
    expect(servers).toHaveTextContent("Radarr 4K");
    expect(servers).toHaveTextContent("4K rule");
    expect(servers).toHaveTextContent("rejected");
    expect(servers).toHaveTextContent("quality profile missing");

    expect(await within(dialog).findByText("Radarr (Everything else)")).toBeInTheDocument();
    expect(
      within(dialog).getByText("none (Everything else doesn't send 4K versions)"),
    ).toBeInTheDocument();
    // How it was decided starts collapsed in the dialog.
    const how = within(dialog).getByRole("button", { name: "How it was decided" });
    expect(how).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(how);
    expect(
      within(dialog).getByText("Everything else — decides HD · decides 4K: none"),
    ).toBeTruthy();
    expect(calls("POST /api/v2/admin/request-routes/preview")[0]?.body).toEqual({
      media_type: "movie",
      tmdb_id: 1,
      requester_user_id: "1",
    });
    expect(within(dialog).getByRole("button", { name: "Approve: Waiting Title" })).toBeEnabled();
  });

  it("says Standard decided, with no rule-by-rule explanation", async () => {
    serve({
      ...queue({ needs_approval: [request("r1", "Waiting Title")] }),
      "GET /api/v2/admin/requests/{id}/events": () => ({ items: [] }),
      "POST /api/v2/admin/request-routes/preview": () => ({
        facts: {
          anime: false,
          company_ids: [],
          genre_ids: [],
          keyword_ids: [],
          network_ids: [],
          origin_countries: [],
          year: 2019,
        },
        rules: [
          {
            route_id: "standard-movie",
            route_name: "Standard",
            is_fallback: true,
            enabled: true,
            unmet_conditions: [],
            hd: "sends",
            uhd: "skips",
          },
        ],
        tiers: [
          {
            quality: "1080p",
            route_id: "standard-movie",
            integration_id: "s1",
            integration_name: "Radarr",
            route_name: "Standard",
          },
          {
            quality: "2160p",
            route_id: "standard-movie",
            route_name: "Standard",
            note: "No server is marked 4K, so there is no 4K version.",
          },
        ],
      }),
    });
    mount("/admin/requests?view=needs_approval");
    fireEvent.click(
      within(await rowOf("Waiting Title")).getByRole("button", { name: "Details: Waiting Title" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Radarr (Standard)")).toBeInTheDocument();
    expect(within(dialog).getByText("none (no server is marked 4K)")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "How it was decided" })).toBeNull();
  });

  it("shows the refetched request in the open dialog after another admin acted first", async () => {
    const LATER = "2026-09-02T00:00:00Z";
    let sent = false;
    serve({
      ...queue({}),
      "GET /api/v2/admin/requests": ({ query }) => {
        if (query?.view === "in_progress") {
          return page([
            sent
              ? request("r2", "Unsent Title", {
                  status: "queued",
                  state: "processing",
                  targets: [target("t2")],
                  updated_at: LATER,
                })
              : request("r2", "Unsent Title", { status: "approved", state: "approved" }),
          ]);
        }
        return page(
          query?.view === "needs_approval" && !sent ? [request("r1", "Waiting Title")] : [],
        );
      },
      "GET /api/v2/admin/requests/{id}/events": () => ({ items: [] }),
      "POST /api/v2/admin/request-routes/preview": () => ({
        facts: {
          anime: false,
          company_ids: [],
          genre_ids: [],
          keyword_ids: [],
          network_ids: [],
          origin_countries: [],
        },
        rules: [],
        tiers: [],
      }),
      // Another admin sent the one and approved the other a moment ago.
      "POST /api/v2/admin/requests/{id}/cancel": () => {
        sent = true;
        return Promise.reject(new Error("The request changed; reload it."));
      },
      "POST /api/v2/admin/requests/{id}/approve": () => {
        sent = true;
        return Promise.reject(new Error("The request changed; reload it."));
      },
    });
    mount("/admin/requests?view=in_progress");
    fireEvent.click(
      within(await rowOf("Unsent Title")).getByRole("button", { name: "Details: Unsent Title" }),
    );
    let dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel request: Unsent Title" }));
    const prompt = (await screen.findAllByRole("dialog")).find((d) => d !== dialog)!;
    fireEvent.click(within(prompt).getByRole("button", { name: "Cancel request" }));
    // The refused cancel refetched the queue; the dialog shows the sent
    // request, which can no longer be cancelled.
    await waitFor(() =>
      expect(
        within(dialog).queryByRole("button", { name: "Cancel request: Unsent Title" }),
      ).not.toBeInTheDocument(),
    );
    expect(within(dialog).getByRole("table")).toHaveTextContent("Radarr");

    // A request that left the view closes its dialog.
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    sent = false;
    fireEvent.mouseDown(screen.getByRole("tab", { name: /^Needs approval/ }));
    fireEvent.click(
      within(await rowOf("Waiting Title")).getByRole("button", { name: "Details: Waiting Title" }),
    );
    dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Approve: Waiting Title" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByRole("link", { name: "Waiting Title" })).not.toBeInTheDocument();
  });

  it("shows each server's download progress and names a blocked import", async () => {
    const heard = "2026-09-01T00:05:00Z";
    serve({
      ...queue({
        in_progress: [
          request("r3", "Downloading Title", {
            status: "downloading",
            state: "processing",
            targets: [
              target("t1", {
                status: "downloading",
                download: {
                  phase: "import_blocked",
                  percent: 100,
                  downloads: 1,
                  updated_at: heard,
                },
              }),
              target("t2", {
                quality: "2160p",
                instance_name: "Radarr 4K",
                status: "downloading",
                download: { phase: "downloading", percent: 43, downloads: 1, updated_at: heard },
              }),
            ],
            download: { phase: "import_blocked", percent: 71, downloads: 2, updated_at: heard },
          }),
        ],
      }),
      "GET /api/v2/admin/requests/{id}/events": () => ({ items: [] }),
    });
    mount("/admin/requests?view=in_progress");
    const row = await rowOf("Downloading Title");
    expect(within(row).getByText("Import blocked")).toBeInTheDocument();
    expect(within(row).getByText("Downloading · 43%")).toBeInTheDocument();
    expect(
      within(row)
        .getAllByRole("progressbar")
        .map((bar) => bar.getAttribute("aria-valuenow")),
    ).toEqual(["100", "43"]);
    expect(within(row).queryByText("Waiting for import")).not.toBeInTheDocument();

    fireEvent.click(within(row).getByRole("button", { name: "Details: Downloading Title" }));
    const servers = within(await screen.findByRole("dialog")).getByRole("table");
    expect(within(servers).getByText("Import blocked")).toBeInTheDocument();
    expect(within(servers).getByText("Downloading · 43%")).toBeInTheDocument();
  });

  it("loads the next page from the cursor the last one returned", async () => {
    serve({
      ...queue({}),
      "GET /api/v2/admin/requests": ({ query }) =>
        query?.cursor === "c2"
          ? page([request("r2", "Second Page Title")])
          : {
              items: [request("r1", "First Page Title")],
              page: { has_more: true, next_cursor: "c2" },
            },
    });
    mount("/admin/requests?view=needs_approval");
    await rowOf("First Page Title");
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await rowOf("Second Page Title")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "First Page Title" })).toBeInTheDocument();
    expect(calls("GET /api/v2/admin/requests").map((options) => options.query?.cursor)).toEqual([
      undefined,
      "c2",
    ]);
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("limits the queue to one account from ?user= and searches as the admin types", async () => {
    serve(queue({ in_progress: [request("r2", "Approved Title", { status: "approved" })] }));
    mount("/admin/requests?view=in_progress&user=7");
    await rowOf("Approved Title");
    expect(screen.getByText("Requested by sam")).toBeInTheDocument();
    expect(calls("GET /api/v2/admin/requests")[0]?.query).toMatchObject({
      view: "in_progress",
      requested_by_user_id: "7",
    });

    fireEvent.change(screen.getByRole("searchbox", { name: "Search requests" }), {
      target: { value: " dune " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Series" }));
    await waitFor(() =>
      expect(calls("GET /api/v2/admin/requests").at(-1)?.query).toMatchObject({
        q: "dune",
        media_type: "series",
        requested_by_user_id: "7",
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Show requests from every account" }));
    await waitFor(() => expect(screen.getByTestId("location")).not.toHaveTextContent("user="));
    await waitFor(() =>
      expect(calls("GET /api/v2/admin/requests").at(-1)?.query).not.toHaveProperty(
        "requested_by_user_id",
        "7",
      ),
    );
  });
});
