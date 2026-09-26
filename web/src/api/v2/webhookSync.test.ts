import { beforeEach, describe, expect, it, vi } from "vitest";
const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("./request", () => ({ v2: request }));
import { listWebhookConnections, createWebhookConnection, listWebhookEvents } from "./webhookSync";
describe("webhook management v2 adapters", () => {
  beforeEach(() => request.mockReset());
  it("follows connection cursors and resolves receiver URLs against the server origin", async () => {
    request
      .mockResolvedValueOnce({
        items: [{ id: "a", webhook_url: "/api/v2/webhook-sync/webhooks/secret" }],
        page: { has_more: true, next_cursor: "next" },
      })
      .mockResolvedValueOnce({
        items: [{ id: "b", webhook_url: "/api/v2/webhook-sync/webhooks/second" }],
        page: { has_more: false },
      });
    const result = await listWebhookConnections();
    expect(result.map((r) => r.id)).toEqual(["a", "b"]);
    expect(result[0]?.webhook_url).toBe(
      `${window.location.origin}/api/v2/webhook-sync/webhooks/secret`,
    );
    expect(request.mock.calls[1]).toEqual([
      "GET /api/v2/webhook-sync/connections",
      { query: { limit: 50, cursor: "next" } },
    ]);
  });
  it("creates providers that have no predefined server identity", async () => {
    request.mockResolvedValueOnce({
      connection: { id: "a", webhook_url: "/receiver" },
      webhook_url: "/receiver",
    });
    const body = { provider: "emby" as const, server_name: "Example", default_profile_id: "p" };
    const result = await createWebhookConnection(body);
    expect(request).toHaveBeenCalledWith("POST /api/v2/webhook-sync/connections", { body });
    expect(result.webhook_url).toBe(`${window.location.origin}/receiver`);
  });
  it("keeps the activity display bounded and adapts string event ids", async () => {
    request.mockResolvedValueOnce({
      items: [{ id: "42", outcome: "applied" }],
      page: { has_more: true, next_cursor: "more" },
    });
    expect(await listWebhookEvents("connection")).toEqual([{ id: 42, outcome: "applied" }]);
    expect(request).toHaveBeenCalledTimes(1);
    expect(request).toHaveBeenCalledWith("GET /api/v2/webhook-sync/connections/{id}/events", {
      path: { id: "connection" },
      query: { limit: 200 },
    });
  });
});
