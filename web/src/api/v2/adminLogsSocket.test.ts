import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
  StaleApiRequestContextError,
} from "../client";
import { V2ProblemError } from "./request";
import {
  adminLogsSocketProtocols,
  buildAdminLogsSocketQuery,
  buildAdminLogsSocketUrl,
  mintAdminLogsSocketTicket,
} from "./adminLogsSocket";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => vi.unstubAllGlobals());
const ticketResponse = (protocol = "silo.admin-logs.v2") =>
  new Response(
    JSON.stringify({
      ticket: "c".repeat(43),
      expires_in: 30,
      max_connection_seconds: 300,
      protocol,
    }),
    { headers: { "Content-Type": "application/json", "Cache-Control": "no-store" } },
  );

it("serializes only defined filters and never puts a credential in the URL", () => {
  expect(
    buildAdminLogsSocketQuery({ request_id: "req-1", component: "api", q: "", limit: 50 }),
  ).toBe("request_id=req-1&component=api&limit=50");
  expect(
    buildAdminLogsSocketUrl(
      "audit",
      { playback_session_id: "p-1", request_id: "req-9" },
      {
        protocol: "https:",
        host: "example.com",
      },
    ),
  ).toBe(
    "wss://example.com/api/v2/admin/logs/ws?stream=audit&playback_session_id=p-1&request_id=req-9",
  );
  expect(adminLogsSocketProtocols("abc")).toEqual(["silo.admin-logs.v2", "silo.ticket.abc"]);
});

it("mints under captured authority once, with no authentication replay", async () => {
  const fetchMock = vi.fn().mockResolvedValue(ticketResponse());
  vi.stubGlobal("fetch", fetchMock);
  const authority = captureProfileRequestContext()!;
  const ticket = await mintAdminLogsSocketTicket(authority);
  expect(ticket.ticket).toBe("c".repeat(43));
  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(url).toBe("/api/v2/admin/logs/ws-ticket");
  expect(init.method).toBe("POST");
  expect((init.headers as Record<string, string>)["X-Profile-Id"]).toBe("profile-a");
});

it("refuses a wrong protocol, a 403, and a stale authority without retrying", async () => {
  const authority = captureProfileRequestContext()!;
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(ticketResponse("silo.events.v2")));
  await expect(mintAdminLogsSocketTicket(authority)).rejects.toThrow(
    "Invalid log stream credential.",
  );
  const denied = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        type: "https://silo.dev/problems/permission_denied",
        title: "Forbidden",
        status: 403,
        detail: "synthetic",
        instance: "synthetic",
      }),
      { status: 403, headers: { "Content-Type": "application/problem+json" } },
    ),
  );
  vi.stubGlobal("fetch", denied);
  await expect(mintAdminLogsSocketTicket(authority)).rejects.toBeInstanceOf(V2ProblemError);
  expect(denied).toHaveBeenCalledOnce();
  setProfileToken("pin-b");
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  await expect(mintAdminLogsSocketTicket(authority)).rejects.toBeInstanceOf(
    StaleApiRequestContextError,
  );
  expect(fetchMock).not.toHaveBeenCalled();
});
