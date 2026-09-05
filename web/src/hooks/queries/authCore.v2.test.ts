/**
 * The auth-core hooks against the committed v2 fixtures: each migrated call
 * site sends the operation's method, path, and body, and projects the
 * fixture body onto the shape its consumers read.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getDeviceLoginOk from "../../../../contracts/api/v2/fixtures/get_device_login_ok.json";
import getOnboardingFlowOk from "../../../../contracts/api/v2/fixtures/get_onboarding_flow_ok.json";
import getOnboardingStateOk from "../../../../contracts/api/v2/fixtures/get_onboarding_state_ok.json";
import getPolicyCapabilityOk from "../../../../contracts/api/v2/fixtures/get_policy_capability_ok.json";
import getSignupStatusOk from "../../../../contracts/api/v2/fixtures/get_signup_status_ok.json";
import launchPluginOk from "../../../../contracts/api/v2/fixtures/launch_plugin_ok.json";
import listUserLibrariesOk from "../../../../contracts/api/v2/fixtures/list_user_libraries_ok.json";
import loginOk from "../../../../contracts/api/v2/fixtures/login_ok.json";
import pollDeviceLoginOk from "../../../../contracts/api/v2/fixtures/poll_device_login_ok.json";
import refreshSessionOk from "../../../../contracts/api/v2/fixtures/refresh_session_ok.json";
import refreshSessionRevoked from "../../../../contracts/api/v2/fixtures/refresh_session_revoked.json";
import startDeviceLoginOk from "../../../../contracts/api/v2/fixtures/start_device_login_ok.json";

import { refreshAccessToken, setAccessToken, setRefreshToken } from "@/api/client";
import { sessionFromTokenPair } from "@/api/v2/account";
import { v2 } from "@/api/v2/request";
import { v2Fixture } from "@/api/v2/testing";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { useOnboardingFlow, useOnboardingProgress, useOnboardingState } from "./onboarding";
import { userLibraryFromV2 } from "./libraries";
import { usePolicyCapability } from "./policy";

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

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("tok-user");
  setRefreshToken("ref");
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("credential responses", () => {
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
  it("prepares the launch cookie through launchPlugin before navigating", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => json(launchPluginOk));
    vi.stubGlobal("fetch", fetchMock);
    const location = { href: "" };
    vi.stubGlobal("location", location);

    await navigateToPluginRoute("/api/v1/plugins/3/");

    const [call] = calls(fetchMock);
    expect(call?.url).toBe("/api/v2/auth/plugin-launch");
    expect(call?.init.method).toBe("POST");
    expect(location.href).toContain("/api/v1/plugins/3/");
  });
});

describe("onboarding", () => {
  it("reads the flow for the web surface and the profile state", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url.startsWith("/api/v2/onboarding/flow")) return json(getOnboardingFlowOk);
      if (url === "/api/v2/onboarding/state") return json(getOnboardingStateOk);
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const wrapper = createWrapper();

    const flow = renderHook(() => useOnboardingFlow(), { wrapper });
    const state = renderHook(() => useOnboardingState(), { wrapper });
    await waitFor(() => expect(flow.result.current.isSuccess).toBe(true));
    await waitFor(() => expect(state.result.current.isSuccess).toBe(true));

    expect(flow.result.current.data?.tour_id).toBe(getOnboardingFlowOk.tour_id);
    expect(flow.result.current.data?.steps[0]?.id).toBe("welcome");
    expect(state.result.current.data).toEqual(getOnboardingStateOk);
    expect(calls(fetchMock).map((call) => call.url)).toEqual(
      expect.arrayContaining(["/api/v2/onboarding/flow?surface=web", "/api/v2/onboarding/state"]),
    );
  });

  it("records progress with the typed body", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useOnboardingProgress(), { wrapper: createWrapper() });
    await act(async () => {
      await result.current.mutateAsync({ tour_id: "core-2026-07", skipped: true });
    });

    const [call] = calls(fetchMock);
    expect(call?.url).toBe("/api/v2/onboarding/progress");
    expect(call?.init.method).toBe("POST");
    expect(JSON.parse(String(call?.init.body))).toEqual({ tour_id: "core-2026-07", skipped: true });
  });
});

describe("policy capability", () => {
  it("decodes the committed capability document", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => json(getPolicyCapabilityOk)),
    );

    const { result } = renderHook(() => usePolicyCapability(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.state).toBe("available");
    expect(result.current.data?.editor_available).toBe(true);
    expect(result.current.data?.decision_types).toEqual(["download", "playback"]);
  });
});

describe("user libraries", () => {
  it("projects the committed collection onto the numeric-id UserLibrary shape", () => {
    const libraries = v2Fixture<"GET /api/v2/user/libraries">(listUserLibrariesOk);
    expect(libraries.items.map(userLibraryFromV2)).toEqual([
      {
        id: 1,
        name: "Movies",
        type: "movies",
        sort_order: 0,
        poster_url: "https://s3.example.test/silo/posters/1.jpg",
      },
      { id: 3, name: "Kids", type: "series", sort_order: 2, poster_url: undefined },
    ]);
  });
});
