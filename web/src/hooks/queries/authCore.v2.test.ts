/**
 * The auth-core hooks against the committed v2 fixtures: each migrated call
 * site sends the operation's method, path, and body, and projects the
 * fixture body onto the shape its consumers read.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getDeviceLoginOk from "../../../../contracts/api/v2/fixtures/get_device_login_ok.json";
import getSignupStatusOk from "../../../../contracts/api/v2/fixtures/get_signup_status_ok.json";
import loginOk from "../../../../contracts/api/v2/fixtures/login_ok.json";
import pollDeviceLoginOk from "../../../../contracts/api/v2/fixtures/poll_device_login_ok.json";
import refreshSessionOk from "../../../../contracts/api/v2/fixtures/refresh_session_ok.json";
import refreshSessionRevoked from "../../../../contracts/api/v2/fixtures/refresh_session_revoked.json";
import startDeviceLoginOk from "../../../../contracts/api/v2/fixtures/start_device_login_ok.json";

import {
  refreshAccessToken,
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import { sessionFromTokenPair } from "@/api/v2/account";
import { v2 } from "@/api/v2/request";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { pluginRouteHref } from "@/lib/pluginRouteHref";

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ profile: { id: "p-owner" } }),
}));

const JSON_HEADERS = { "Content-Type": "application/json" };

function json(body: unknown, status = 200, headers: Record<string, string> = JSON_HEADERS) {
  return new Response(JSON.stringify(body), { status, headers });
}

type Call = { url: string; init: RequestInit & { headers: Record<string, string> } };

function calls(fetchMock: ReturnType<typeof vi.fn<typeof fetch>>): Call[] {
  return fetchMock.mock.calls.map(([input, init]) => ({
    url: String(input),
    init: init as Call["init"],
  }));
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("tok-user");
  setRefreshToken("ref");
  setProfileId("p-owner");
  setProfileToken(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("credential responses", () => {
  it.each([
    [
      "login",
      () => v2("POST /api/v2/auth/login", { body: { username: "laura", password: "wrong" } }),
    ],
    ["OAuth completion", () => v2("POST /api/v2/auth/oauth/complete", { body: { code: "used" } })],
    ["refresh", () => v2("POST /api/v2/auth/refresh", { body: { refresh_token: "expired" } })],
    [
      "setup",
      () =>
        v2("POST /api/v2/auth/setup", {
          body: { username: "alice", email: "alice@example.test", password: "password" },
        }),
    ],
    [
      "signup",
      () =>
        v2("POST /api/v2/auth/signup", {
          body: {
            username: "alice",
            email: "alice@example.test",
            password: "password",
            invite_code: "invite",
          },
        }),
    ],
    ["device start", () => v2("POST /api/v2/auth/device/start", { body: {} })],
    [
      "device poll",
      () => v2("POST /api/v2/auth/device/poll", { body: { device_code: "expired" } }),
    ],
  ] as const)(
    "does not refresh an existing session or replay rejected %s",
    async (_name, exchange) => {
      const refusal = () =>
        json(refreshSessionRevoked, 401, { "Content-Type": "application/problem+json" });
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(refusal())
        .mockResolvedValueOnce(json(refreshSessionOk))
        .mockResolvedValueOnce(refusal());
      vi.stubGlobal("fetch", fetchMock);

      await expect(exchange()).rejects.toMatchObject({ status: 401 });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(localStorage.getItem("refresh_token")).toBe("ref");
    },
  );

  it("projects the login token pair onto the session the auth provider applies", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => json(loginOk));
    vi.stubGlobal("fetch", fetchMock);

    const tokens = await v2("POST /api/v2/auth/login", {
      body: { username: "laura", password: "pw" },
    });
    const [call] = calls(fetchMock);
    expect(call?.url).toBe("/api/v2/auth/login");
    expect(call?.init.method).toBe("POST");
    expect(JSON.parse(String(call?.init.body))).toEqual({ username: "laura", password: "pw" });

    expect(sessionFromTokenPair(tokens)).toEqual({
      access_token: "acc",
      refresh_token: "ref",
      expires_in: 3600,
      user: {
        id: 1,
        username: "laura",
        email: "laura@example.test",
        role: "user",
        permissions: ["marker_edit"],
        download_allowed: true,
        password_change_required: false,
        impersonation: null,
      },
    });
  });

  it("rotates the refresh token through refreshSession and treats a revoked token as no session", async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(json(refreshSessionOk))
      .mockResolvedValueOnce(
        json(refreshSessionRevoked, 401, { "Content-Type": "application/problem+json" }),
      );

    await expect(refreshAccessToken("ref", fetchMock)).resolves.toEqual(refreshSessionOk);
    const [call] = calls(fetchMock);
    expect(call?.url).toBe("/api/v2/auth/refresh");
    expect(call?.init.method).toBe("POST");
    expect(JSON.parse(String(call?.init.body))).toEqual({ refresh_token: "ref" });

    await expect(refreshAccessToken("ref", fetchMock)).resolves.toBeNull();
  });
});

describe("device pairing", () => {
  it("starts a pairing, then reads the approved token pair from the poll answer", async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(json(startDeviceLoginOk, 201))
      .mockResolvedValueOnce(json(pollDeviceLoginOk));
    vi.stubGlobal("fetch", fetchMock);

    const started = await v2("POST /api/v2/auth/device/start", {
      body: { device_name: "Living room TV", device_platform: "tvos" },
    });
    expect(started.device_code).toBe("dev-1");
    expect(started.interval).toBe(5);

    const polled = await v2("POST /api/v2/auth/device/poll", {
      body: { device_code: started.device_code },
    });
    expect(polled.status).toBe("approved");
    expect(polled.tokens && sessionFromTokenPair(polled.tokens).user.username).toBe("laura");

    const [start, poll] = calls(fetchMock);
    expect(start?.url).toBe("/api/v2/auth/device/start");
    expect(poll?.url).toBe("/api/v2/auth/device/poll");
    expect(JSON.parse(String(poll?.init.body))).toEqual({ device_code: "dev-1" });
  });

  it("looks a pairing up by code and sends the same code to the decision", async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(json(getDeviceLoginOk))
      .mockResolvedValueOnce(json({ status: "approved" }));
    vi.stubGlobal("fetch", fetchMock);

    const details = await v2("GET /api/v2/auth/device", { query: { code: "ABCD-1234" } });
    expect(details.status).toBe("pending");
    expect(details.match_code).toBe("42");

    await v2("POST /api/v2/auth/device/approve", { body: { code: "ABCD-1234" } });
    const [lookup, approve] = calls(fetchMock);
    expect(lookup?.url).toBe("/api/v2/auth/device?code=ABCD-1234");
    expect(approve?.url).toBe("/api/v2/auth/device/approve");
    expect(JSON.parse(String(approve?.init.body))).toEqual({ code: "ABCD-1234" });
  });
});

describe("signup status", () => {
  it("decodes the committed signup status", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => json(getSignupStatusOk)),
    );
    await expect(v2("GET /api/v2/auth/signup")).resolves.toEqual({ enabled: true });
  });
});

describe("plugin launch", () => {
  it("preserves launch before a household profile is selected", async () => {
    setProfileId(null);
    const fetchMock = vi.fn<typeof fetch>(async () => json({ expires_in: 300 }));
    vi.stubGlobal("fetch", fetchMock);
    const location = { href: "" };
    vi.stubGlobal("location", location);
    await navigateToPluginRoute(pluginRouteHref(3, "/"));
    expect(calls(fetchMock)[0]?.url).toBe("/api/v2/auth/plugin-launch");
    expect(location.href).toContain("/api/v2/plugin-content/plugins/3/");
  });

  it("keeps the current page when launch is denied", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        json(
          {
            type: "https://silo.dev/problems/permission_denied",
            title: "Forbidden",
            status: 403,
            detail: "Denied",
            instance: "fixture",
          },
          403,
          { "Content-Type": "application/problem+json" },
        ),
      ),
    );
    const location = { href: "/admin/plugins" };
    vi.stubGlobal("location", location);
    await navigateToPluginRoute(pluginRouteHref(3, "/"));
    expect(location.href).toBe("/admin/plugins");
  });

  it("waits for the cookie and discards navigation after a profile switch", async () => {
    let complete!: (value: Response) => void;
    const pending = new Promise<Response>((resolve) => {
      complete = resolve;
    });
    const fetchMock = vi.fn<typeof fetch>(() => pending);
    vi.stubGlobal("fetch", fetchMock);
    const location = { href: "/admin/plugins" };
    vi.stubGlobal("location", location);
    const launch = navigateToPluginRoute(pluginRouteHref(3, "/"));
    expect(location.href).toBe("/admin/plugins");
    setProfileId("another-profile");
    complete(json({ expires_in: 300 }));
    await launch;
    expect(location.href).toBe("/admin/plugins");
  });

  it("prepares the launch cookie on the API prefix the plugin page is served from", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => json({ expires_in: 300 }));
    vi.stubGlobal("fetch", fetchMock);
    const location = { href: "" };
    vi.stubGlobal("location", location);

    const href = pluginRouteHref(3, "/");
    await navigateToPluginRoute(href);

    const [call] = calls(fetchMock);
    expect(call?.url).toBe("/api/v2/auth/plugin-launch");
    expect(call?.init.method).toBe("POST");
    expect(location.href).toContain("/api/v2/plugin-content/plugins/3/");
    expect(call?.init.headers["X-Profile-Id"]).toBe("p-owner");
    expect(location.href.startsWith("/api/v2/plugin-content/plugins/")).toBe(true);
  });
});
