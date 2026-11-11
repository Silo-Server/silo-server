import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useEffect } from "react";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useRealtimeEvents } from "./realtimeEventsContext";
import {
  EVENTS_ACCESS_CHANGED_CLOSE_CODE,
  HIDDEN_EVENTS_SOCKET_GRACE_MS,
  RealtimeEventsProvider,
} from "./RealtimeEventsProvider";

const state = vi.hoisted(() => ({
  user: { id: 1, role: "admin" } as { id: number; role: string } | null,
  profile: null as { id: string; has_pin: boolean } | null,
  activity: {
    isVisible: true,
    isFocused: true,
    isFrozen: false,
    canPollDashboard: true,
    canApplyRealtimeUpdates: true,
  },
  refreshAccount: vi.fn(async () => {}),
}));

vi.mock("@/hooks/useAuth", () => {
  const useAuth = () => ({
    user: state.user,
    profile: state.profile,
    refreshAccount: state.refreshAccount,
  });
  return { useAuth, useOptionalAuth: useAuth };
});
vi.mock("@/hooks/usePageActivity", () => ({ usePageActivity: () => state.activity }));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => false }));
vi.mock("react-router", () => ({ useLocation: () => ({ pathname: "/" }) }));

// All sockets, responses and credentials in this file are finite local fixtures.
class FixtureSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 3;
  static instances: FixtureSocket[] = [];
  static failConstruction = false;
  readyState = FixtureSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  send = vi.fn();
  close = vi.fn(() => {
    this.readyState = FixtureSocket.CLOSED;
  });

  constructor(
    public url: string,
    public protocols: string[],
  ) {
    if (FixtureSocket.failConstruction) throw new Error("fixture constructor failure");
    FixtureSocket.instances.push(this);
  }
  open() {
    this.readyState = FixtureSocket.OPEN;
    this.onopen?.();
  }
  message(message: unknown) {
    this.onmessage?.({ data: JSON.stringify(message) } as MessageEvent);
  }
  end(code = 1006) {
    this.readyState = FixtureSocket.CLOSED;
    this.onclose?.({ code } as CloseEvent);
  }
}

function ticketResponse(index: number) {
  return new Response(
    JSON.stringify({
      ticket: String(index).padStart(43, "a"),
      protocol: "silo.events.v2",
      expires_in: 30,
      max_connection_seconds: 300,
    }),
    { headers: { "Content-Type": "application/json" } },
  );
}

function problemResponse(retryAfter?: string) {
  return new Response(
    JSON.stringify({
      type: "about:blank",
      title: "Unavailable",
      status: 503,
      detail: "Fixture unavailable",
      instance: "/api/v2/events/ws-ticket",
    }),
    {
      status: 503,
      headers: {
        "Content-Type": "application/problem+json",
        ...(retryAfter ? { "Retry-After": retryAfter } : {}),
      },
    },
  );
}

let ticketCount = 0;
let clients: QueryClient[] = [];
const fetchFixture = vi.fn<typeof fetch>();

function FixtureChannel() {
  const { subscribeChannel } = useRealtimeEvents();
  useEffect(() => subscribeChannel("catalog"), [subscribeChannel]);
  return null;
}

function mountProvider() {
  const client = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity } } });
  clients.push(client);
  const tree = () => (
    <QueryClientProvider client={client}>
      <RealtimeEventsProvider>
        <FixtureChannel />
      </RealtimeEventsProvider>
    </QueryClientProvider>
  );
  const view = render(tree());
  return { ...view, rerenderProvider: () => view.rerender(tree()) };
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

async function accept(socket: FixtureSocket) {
  await act(async () => {
    socket.open();
    socket.message({ type: "hello" });
  });
  const selection = JSON.parse(socket.send.mock.calls.at(-1)![0] as string) as {
    request_id: string;
  };
  await act(async () => {
    socket.message({ type: "subscribed", request_id: selection.request_id, channels: ["catalog"] });
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0);
  // The fetch fixture completes/defers explicitly; no native deadline timer is needed.
  vi.spyOn(AbortSignal, "timeout").mockImplementation(() => new AbortController().signal);
  setAccessToken("fixture-session");
  setProfileId(null);
  setProfileToken(null);
  state.user = { id: 1, role: "admin" };
  state.profile = null;
  state.activity = {
    isVisible: true,
    isFocused: true,
    isFrozen: false,
    canPollDashboard: true,
    canApplyRealtimeUpdates: true,
  };
  state.refreshAccount.mockClear();
  ticketCount = 0;
  clients = [];
  fetchFixture.mockReset();
  fetchFixture.mockImplementation(async (url) => {
    if (!String(url).endsWith("/api/v2/events/ws-ticket")) {
      throw new Error("unexpected fixture request");
    }
    return ticketResponse(++ticketCount);
  });
  FixtureSocket.instances = [];
  FixtureSocket.failConstruction = false;
  vi.stubGlobal("fetch", fetchFixture);
  vi.stubGlobal("WebSocket", FixtureSocket);
});

afterEach(() => {
  cleanup();
  for (const client of clients) client.clear();
  vi.clearAllTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("generic events provider recovery", () => {
  it.each([
    [0, [5_000, 10_000, 20_000, 30_000, 30_000]],
    [1, [7_500, 15_000, 30_000, 45_000, 45_000]],
  ])("retries failed upgrades with fresh tickets at jitter endpoint %s", async (sample, delays) => {
    vi.mocked(Math.random).mockReturnValue(sample);
    const view = mountProvider();
    await act(async () => {});
    for (const [index, delay] of delays.entries()) {
      const socket = FixtureSocket.instances[index]!;
      await act(async () => {
        socket.onerror?.();
        socket.end();
        // Repeated terminal callbacks cannot create a second retry.
        socket.end();
        socket.onerror?.();
      });
      expect(vi.getTimerCount()).toBe(1);
      await advance(delay - 1);
      expect(fetchFixture).toHaveBeenCalledTimes(index + 1);
      await advance(1);
      expect(fetchFixture).toHaveBeenCalledTimes(index + 2);
      expect(FixtureSocket.instances).toHaveLength(index + 2);
    }
    const credentials = FixtureSocket.instances.map((socket) => socket.protocols[1]);
    expect(new Set(credentials).size).toBe(6);
    expect(credentials).toEqual(
      Array.from({ length: 6 }, (_, i) => `silo.ticket.${String(i + 1).padStart(43, "a")}`),
    );
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each([
    [29_999, 10_000],
    [30_000, 1_000],
    [300_000, 1_000],
  ])("reconnects after %s ms of accepted subscription at %s ms", async (age, delay) => {
    mountProvider();
    await act(async () => {});
    await act(async () => FixtureSocket.instances[0]!.end());
    await advance(5_000);
    const socket = FixtureSocket.instances[1]!;
    await accept(socket);
    await advance(age);
    await act(async () => {
      // A later ACK must not restart the healthy clock.
      socket.message({ type: "subscribed", channels: ["catalog", "user_state"] });
      socket.end(1000);
    });
    await advance(delay - 1);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(3);
    // After a healthy reset the next failure starts at five seconds again.
    if (age >= 30_000) {
      await act(async () => FixtureSocket.instances[2]!.end());
      await advance(4_999);
      expect(fetchFixture).toHaveBeenCalledTimes(3);
      await advance(1);
      expect(fetchFixture).toHaveBeenCalledTimes(4);
    }
  });

  it("honors a readable floor longer than one browser timer without an immediate retry", async () => {
    const seconds = 2_147_484;
    fetchFixture.mockResolvedValueOnce(problemResponse(String(seconds)));
    const view = mountProvider();
    await act(async () => {});
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
    await advance(2_147_483_646);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
    view.unmount();
    await advance(353);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
  });

  it("does not count open/hello time, empty ACKs, snapshots or events as healthy acceptance", async () => {
    mountProvider();
    await act(async () => {});
    const socket = FixtureSocket.instances[0]!;
    await act(async () => {
      socket.open();
      socket.message({ type: "hello" });
      socket.message({ type: "subscribed", channels: [], rejected: [] });
      socket.message({ type: "snapshot", channel: "catalog", data: [] });
      socket.message({ type: "event", channel: "catalog", event: "fixture", data: {} });
    });
    await advance(30_000);
    await act(async () => socket.end());
    await advance(4_999);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
  });

  it("starts the healthy clock at a delayed accepted ACK", async () => {
    mountProvider();
    await act(async () => {});
    const socket = FixtureSocket.instances[0]!;
    await act(async () => socket.open());
    await advance(30_000);
    await accept(socket);
    await advance(29_999);
    await act(async () => socket.end());
    await advance(4_999);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
  });

  it("keeps immediate access_changed recovery and clears the previous accepted clock", async () => {
    mountProvider();
    await act(async () => {});
    const socket = FixtureSocket.instances[0]!;
    await accept(socket);
    await advance(30_000);
    await act(async () => {
      socket.message({ type: "access_changed" });
      socket.end(EVENTS_ACCESS_CHANGED_CLOSE_CODE);
    });
    await advance(0);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
    expect(state.refreshAccount).toHaveBeenCalledTimes(1);
    await act(async () => FixtureSocket.instances[1]!.end());
    await advance(4_999);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(3);
  });

  it.each([
    ["problem", 5_000],
    ["retry-after", 12_000],
    ["opaque", 5_000],
    ["transport", 5_000],
    ["transport-retry-after-429", 12_000],
    ["transport-retry-after-503", 12_000],
  ])("backs off a %s mint failure, then mints a fresh ticket", async (kind, delay) => {
    if (kind === "opaque") fetchFixture.mockRejectedValueOnce(new TypeError("fixture failure"));
    else if (kind.startsWith("transport-retry-after-")) {
      fetchFixture.mockResolvedValueOnce(
        new Response("Unavailable", {
          status: kind.endsWith("429") ? 429 : 503,
          headers: { "Content-Type": "text/plain", "Retry-After": "8" },
        }),
      );
    } else if (kind === "transport") {
      fetchFixture.mockResolvedValueOnce(new Response("Unavailable", { status: 503 }));
    } else
      fetchFixture.mockResolvedValueOnce(problemResponse(kind === "retry-after" ? "8" : undefined));
    vi.mocked(Math.random).mockReturnValue(kind.includes("retry-after") ? 1 : 0);
    mountProvider();
    await act(async () => {});
    expect(FixtureSocket.instances).toHaveLength(0);
    expect(vi.getTimerCount()).toBe(1);
    await advance(delay - 1);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
    expect(FixtureSocket.instances).toHaveLength(1);
  });

  it("uses a new ticket after socket construction fails", async () => {
    FixtureSocket.failConstruction = true;
    mountProvider();
    await act(async () => {});
    expect(FixtureSocket.instances).toHaveLength(0);
    await advance(4_999);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
    FixtureSocket.failConstruction = false;
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
    expect(FixtureSocket.instances[0]!.protocols[1]).toBe(`silo.ticket.${"a".repeat(42)}2`);
  });

  it("keeps mint failures and short subscriptions on the same failure ladder", async () => {
    fetchFixture.mockResolvedValueOnce(problemResponse());
    mountProvider();
    await act(async () => {});
    await advance(5_000);
    const socket = FixtureSocket.instances[0]!;
    await accept(socket);
    await advance(29_999);
    await act(async () => socket.end());
    fetchFixture.mockResolvedValueOnce(problemResponse());
    await advance(9_999);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(3);
    await advance(19_999);
    expect(fetchFixture).toHaveBeenCalledTimes(3);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(4);
  });

  it("advances the failure ladder through profile-rebind retries", async () => {
    setProfileId("fixture-profile");
    state.profile = { id: "fixture-profile", has_pin: false };
    mountProvider();
    await act(async () => {});
    // The early profile floors (2s, 4s) cannot undercut 5s, 10s. Later
    // profile floors remain an additional minimum, before jitter.
    for (const delay of [5_000, 10_000, 20_000, 30_000, 32_000]) {
      const attempts = fetchFixture.mock.calls.length;
      const socket = FixtureSocket.instances.at(-1)!;
      await act(async () => {
        socket.open();
        socket.message({
          type: "subscribed",
          channels: ["catalog"],
          rejected: [{ channel: "notifications", code: "profile_required" }],
        });
        socket.end();
      });
      await advance(delay - 1);
      expect(fetchFixture).toHaveBeenCalledTimes(attempts);
      await advance(1);
      expect(fetchFixture).toHaveBeenCalledTimes(attempts + 1);
    }
  });

  it("ignores an accepted ACK from a replaced socket", async () => {
    const view = mountProvider();
    await act(async () => {});
    const oldSocket = FixtureSocket.instances[0]!;
    await act(async () => {
      setProfileId("fixture-profile");
      state.profile = { id: "fixture-profile", has_pin: false };
      view.rerenderProvider();
    });
    await act(async () => oldSocket.message({ type: "subscribed", channels: ["catalog"] }));
    await advance(30_000);
    await act(async () => FixtureSocket.instances[1]!.end());
    await advance(4_999);
    expect(fetchFixture).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(fetchFixture).toHaveBeenCalledTimes(3);
  });

  it.each(["account", "profile", "pin"])(
    "replaces %s authority and cancels the old retry",
    async (kind) => {
      setProfileId("fixture-profile");
      state.profile = { id: "fixture-profile", has_pin: false };
      const view = mountProvider();
      await act(async () => {});
      const oldSocket = FixtureSocket.instances[0]!;
      await act(async () => oldSocket.end());
      await act(async () => {
        if (kind === "account") {
          setAccessToken(null);
          setAccessToken("fixture-replacement-session");
          state.user = { id: 2, role: "admin" };
        } else if (kind === "profile") {
          setProfileId("fixture-replacement-profile");
          state.profile = { id: "fixture-replacement-profile", has_pin: false };
        } else setProfileToken("fixture-replacement-pin");
        view.rerenderProvider();
      });
      expect(fetchFixture).toHaveBeenCalledTimes(2);
      expect(FixtureSocket.instances).toHaveLength(2);
      await act(async () => oldSocket.end(EVENTS_ACCESS_CHANGED_CLOSE_CODE));
      await advance(45_000);
      expect(fetchFixture).toHaveBeenCalledTimes(2);
      expect(state.refreshAccount).not.toHaveBeenCalled();
      expect(vi.getTimerCount()).toBe(0);
    },
  );

  it("checks authority again when a pending retry fires before a rerender", async () => {
    mountProvider();
    await act(async () => {});
    await act(async () => FixtureSocket.instances[0]!.end());
    setAccessToken(null);
    await advance(5_000);
    expect(fetchFixture).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(["unmount", "signout", "freeze", "hidden-stop"])(
    "cancels a pending retry on %s",
    async (kind) => {
      const view = mountProvider();
      await act(async () => {});
      // At the capped jitter endpoint, a retry can extend past the hidden grace.
      if (kind === "hidden-stop") {
        vi.mocked(Math.random).mockReturnValue(1);
        for (const delay of [7_500, 15_000, 30_000]) {
          await act(async () => FixtureSocket.instances.at(-1)!.end());
          await advance(delay);
        }
        await act(async () => {
          state.activity = { ...state.activity, isVisible: false, canApplyRealtimeUpdates: false };
          view.rerenderProvider();
        });
        await advance(30_000);
      }
      await act(async () => FixtureSocket.instances.at(-1)!.end());
      const mintsBeforeStop = fetchFixture.mock.calls.length;
      await act(async () => {
        if (kind === "unmount") view.unmount();
        else if (kind === "signout") {
          setAccessToken(null);
          state.user = null;
          view.rerenderProvider();
        } else if (kind === "freeze") {
          state.activity = { ...state.activity, isFrozen: true, canApplyRealtimeUpdates: false };
          view.rerenderProvider();
        }
      });
      if (kind === "hidden-stop") await advance(HIDDEN_EVENTS_SOCKET_GRACE_MS - 30_000);
      await advance(45_000);
      expect(fetchFixture).toHaveBeenCalledTimes(mintsBeforeStop);
      expect(FixtureSocket.instances.at(-1)!.readyState).toBe(FixtureSocket.CLOSED);
      view.unmount();
      expect(vi.getTimerCount()).toBe(0);
    },
  );

  it.each(["unmount", "freeze", "profile"])(
    "ignores a delayed mint result after %s",
    async (kind) => {
      let finish!: (response: Response) => void;
      fetchFixture.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
      const view = mountProvider();
      await act(async () => {});
      await act(async () => {
        if (kind === "unmount") view.unmount();
        else if (kind === "freeze") {
          state.activity = { ...state.activity, isFrozen: true, canApplyRealtimeUpdates: false };
          view.rerenderProvider();
        } else {
          setProfileId("fixture-replacement-profile");
          state.profile = { id: "fixture-replacement-profile", has_pin: false };
          view.rerenderProvider();
        }
      });
      const currentSockets = FixtureSocket.instances.length;
      await act(async () => finish(ticketResponse(99)));
      await advance(45_000);
      expect(FixtureSocket.instances).toHaveLength(currentSockets);
      expect(FixtureSocket.instances.some((socket) => socket.protocols[1]?.endsWith("99"))).toBe(
        false,
      );
      view.unmount();
      expect(vi.getTimerCount()).toBe(0);
    },
  );
});
