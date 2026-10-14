// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { v2, V2ProblemError } from "@/api/v2/request";
import ActivateDevice from "./ActivateDevice";

const auth = vi.hoisted(() => ({
  user: null as null | { username: string },
  profile: null as null | { id: string },
  isImpersonating: false,
  logout: vi.fn(),
  logoutOfSiloOnly: vi.fn(),
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ ...auth, loading: false, setupLoading: false }),
}));
vi.mock("@/hooks/useBranding", () => ({
  useBranding: () => ({ serverName: "Branded" }),
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));
vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
const profiles = vi.hoisted(() => ({
  list: [] as Array<{ id: string; name: string; has_pin: boolean }>,
  verify: vi.fn(),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: profiles.list, isLoading: false, isError: false, refetch: vi.fn() }),
  verifyProfilePIN: (...args: unknown[]) => profiles.verify(...args),
}));
vi.mock("@/api/client", async (original) => ({
  ...(await original<typeof import("@/api/client")>()),
  captureProfileRequestContext: () => null,
  captureProfileRequestContextAs: (profileId: string, profileToken: string | null) => ({
    accessToken: "acc",
    authContextVersion: 1,
    serverOrigin: "",
    profileId,
    profileToken,
  }),
}));

const pending = {
  status: "pending",
  user_code: "4821-7730",
  match_code: "warm pony",
  device_name: "Living room TV",
  device_platform: "tvos",
  ip_address_hint: "192.168.*.*",
  expires_at: "2026-01-02T03:14:05.678Z",
  requested_at: new Date(Date.now() - 10_000).toISOString(),
  client_purpose: "device_login",
  temporary: false,
  server_name: "Media Room",
};

let lookups: Array<Record<string, unknown>>;
const decisions: Array<{ op: string; body: unknown }> = [];

function notFound() {
  return new V2ProblemError("getDeviceLogin", {
    type: "https://siloserver.org/docs/api/v2/problems/not_found",
    title: "Not found",
    status: 404,
    detail: "Device login request not found",
    instance: "urn:silo:request:1",
  } as never);
}

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

function mount(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route
          path="/activate"
          element={
            <>
              <ActivateDevice />
              <Where />
            </>
          }
        />
        <Route path="/login" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  auth.user = { username: "laura" };
  auth.profile = null;
  profiles.list = [];
  profiles.verify.mockReset();
  auth.isImpersonating = false;
  auth.logout.mockReset();
  auth.logoutOfSiloOnly.mockReset();
  decisions.length = 0;
  lookups = [pending];
  vi.mocked(v2).mockImplementation(((op: string, options?: { body?: unknown }) => {
    if (op === "GET /api/v2/auth/device") {
      const next = lookups.length > 1 ? lookups.shift() : lookups[0];
      if (next instanceof Error) return Promise.reject(next);
      return Promise.resolve(next);
    }
    decisions.push({ op, body: options?.body });
    return Promise.resolve({ status: op.endsWith("/approve") ? "approved" : "denied" });
  }) as never);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

it("takes the TV's code as grouped digits and looks it up", async () => {
  mount("/activate");
  const input = screen.getByLabelText("Enter the code on your TV or app");
  expect(input.getAttribute("inputmode")).toBe("numeric");
  fireEvent.change(input, { target: { value: "48217730" } });
  expect((input as HTMLInputElement).value).toBe("4821 7730");
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  await screen.findByText("Sign in Living room TV?");
  expect(vi.mocked(v2)).toHaveBeenCalledWith("GET /api/v2/auth/device", {
    query: { code: "48217730" },
  });
});

it("shows what the approver is approving and signs the TV in", async () => {
  lookups = [pending, { ...pending, status: "consumed" }];
  mount("/activate?code=48217730");
  await screen.findByText("Sign in Living room TV?");
  expect(screen.getByText("Apple TV · requested just now")).toBeTruthy();
  expect(screen.getByText(window.location.host)).toBeTruthy();
  // The code is read digit by digit; the visual grouping is hidden.
  expect(screen.getByText("4821 7730").getAttribute("aria-hidden")).toBe("true");
  expect(screen.getByText("4 8 2 1 7 7 3 0")).toBeTruthy();
  // TV apps released before user codes show only the match words.
  expect(screen.getByText("Older TV apps show WARM PONY instead.")).toBeTruthy();
  expect(screen.getByText(/sign it in to Media Room as/)).toBeTruthy();
  expect(screen.getByText("Only approve a TV that's in front of you right now.")).toBeTruthy();
  // The result region is mounted, empty, before any decision.
  expect(screen.getByRole("status").textContent).toBe("");

  fireEvent.click(screen.getByRole("button", { name: "Sign in TV" }));
  const result = await screen.findByText("Your TV is signed in.");
  expect(result.getAttribute("role")).toBe("status");
  await waitFor(() => expect(document.activeElement).toBe(result));
  // A finished request no longer asks for a comparison.
  expect(screen.queryByText("Check that your TV shows")).toBeNull();
  expect(decisions).toEqual([
    { op: "POST /api/v2/auth/device/approve", body: { code: "48217730" } },
  ]);
});

it("follows an approval whose reload failed", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  lookups = [pending, new Error("offline") as never, { ...pending, status: "consumed" }];
  mount("/activate?code=48217730");
  await screen.findByText("Sign in Living room TV?");
  fireEvent.click(screen.getByRole("button", { name: "Sign in TV" }));
  await screen.findByText("Done. Your TV is signing in.");
  expect(screen.queryByRole("button", { name: "Sign in TV" })).toBeNull();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  await screen.findByText("Your TV is signed in.");
});

it.each(["success", "failure"])(
  "keeps a new code pending when the previous approval returns %s",
  async (outcome) => {
    let finishApproval = () => {};
    const approval = new Promise((resolve, reject) => {
      finishApproval = () =>
        outcome === "success"
          ? resolve({ status: "approved" })
          : reject(new Error("approval failed"));
    });
    let firstLookup = true;
    const second = { ...pending, user_code: "9153-6204", device_name: "Bedroom TV" };
    vi.mocked(v2).mockImplementation(((
      op: string,
      options?: { body?: unknown; query?: { code?: string } },
    ) => {
      if (op === "GET /api/v2/auth/device") {
        if (options?.query?.code === "91536204") return Promise.resolve(second);
        if (firstLookup) {
          firstLookup = false;
          return Promise.resolve(pending);
        }
        return Promise.reject(new Error("reload failed"));
      }
      decisions.push({ op, body: options?.body });
      return approval;
    }) as never);

    mount("/activate?code=48217730");
    await screen.findByText("Sign in Living room TV?");
    fireEvent.click(screen.getByRole("button", { name: "Sign in TV" }));
    fireEvent.click(screen.getByRole("button", { name: "Enter another code" }));
    const input = await screen.findByLabelText("Enter the code on your TV or app");
    fireEvent.change(input, { target: { value: "91536204" } });
    fireEvent.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText("Sign in Bedroom TV?");

    await act(async () => finishApproval());
    expect(screen.getByText("9153 6204")).toBeTruthy();
    expect(screen.getByText("Sign in Bedroom TV?")).toBeTruthy();
    expect(screen.queryByText("Done. Your TV is signing in.")).toBeNull();
    expect(screen.queryByText("Couldn't sign in the TV. Try again.")).toBeNull();
    expect(screen.getByRole("button", { name: "Sign in TV" }).hasAttribute("disabled")).toBe(false);
    expect(decisions).toEqual([
      { op: "POST /api/v2/auth/device/approve", body: { code: "48217730" } },
    ]);
  },
);

it("keeps watching past the first minutes and through a failed lookup", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const approved = { ...pending, status: "approved" };
  // Initial lookup, the reload after approving, then 40 fast watch lookups
  // (two minutes), one that fails, and the TV collecting on a slow one.
  lookups = [
    pending,
    approved,
    ...Array.from({ length: 40 }, () => approved),
    new Error("offline") as never,
    approved,
    { ...pending, status: "consumed" },
  ];
  mount("/activate?code=48217730");
  await screen.findByText("Sign in Living room TV?");
  fireEvent.click(screen.getByRole("button", { name: "Sign in TV" }));
  await screen.findByText("Done. Your TV is signing in.");
  for (let i = 0; i < 40; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
  }
  const before = vi.mocked(v2).mock.calls.length;
  // Past two minutes the page checks every 15 seconds, not every 3.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(vi.mocked(v2).mock.calls.length).toBe(before);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(12000);
  });
  expect(vi.mocked(v2).mock.calls.length).toBe(before + 1);
  // That lookup failed: the card stays instead of a lookup error.
  expect(screen.getByText("Done. Your TV is signing in.")).toBeTruthy();
  expect(screen.queryByText("Couldn't look up this code. Try again.")).toBeNull();
  for (let i = 0; i < 2; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000);
    });
  }
  await screen.findByText("Your TV is signed in.");
});

it("declines with Not now", async () => {
  lookups = [pending, { ...pending, status: "denied" }];
  mount("/activate?code=48217730");
  await screen.findByText("Sign in Living room TV?");
  fireEvent.click(screen.getByRole("button", { name: "Not now" }));
  await screen.findByText("This sign-in was declined.");
  expect(decisions).toEqual([{ op: "POST /api/v2/auth/device/deny", body: { code: "48217730" } }]);
});

it("asks a signed-out approver to sign in and keeps the code", async () => {
  auth.user = null;
  lookups = [{ ...pending, server_id: "3f2a9d5e-6b1c" }];
  mount("/activate?code=48217730");
  const link = await screen.findByRole("link", { name: "Sign in to approve" });
  expect(screen.queryByRole("link", { name: "Open in the Silo app" })).toBeNull();
  expect(link.getAttribute("href")).toBe("/login?redirect=%2Factivate%3Fcode%3D48217730");
  expect(screen.queryByRole("button", { name: "Sign in TV" })).toBeNull();
});

it("offers a phone the Silo app for a signed-out approval", async () => {
  auth.user = null;
  lookups = [{ ...pending, server_id: "3f2a9d5e-6b1c" }];
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
    "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)",
  );
  mount("/activate?code=48217730");
  const app = await screen.findByRole("link", { name: "Open in the Silo app" });
  expect(app.getAttribute("href")).toBe(
    `silo://device?server=3f2a9d5e-6b1c&url=${encodeURIComponent(window.location.origin)}&code=48217730`,
  );
  expect(screen.getByRole("link", { name: "Sign in to approve" })).toBeTruthy();
});

it("tells an admin viewing as someone to stop before approving", async () => {
  auth.isImpersonating = true;
  mount("/activate?code=48217730");
  expect(await screen.findByText(/Stop viewing as laura to approve a TV/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Sign in TV" })).toBeNull();
});

it("explains the server refusing an impersonation session instead of offering a retry", async () => {
  lookups = [pending];
  vi.mocked(v2).mockImplementation(((op: string) => {
    if (op === "GET /api/v2/auth/device") return Promise.resolve(pending);
    return Promise.reject(
      new V2ProblemError("approveDeviceLogin", {
        type: "https://siloserver.org/docs/api/v2/problems/permission_denied",
        title: "Permission denied",
        status: 403,
        detail: "Approving or denying a device needs a signed-in session.",
      } as never),
    );
  }) as never);
  mount("/activate?code=48217730");
  fireEvent.click(await screen.findByRole("button", { name: "Sign in TV" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Stop viewing as laura to approve a TV.",
  );
  expect(screen.queryByText(/Try again/)).toBeNull();
});

it("switches account without losing the code or ending the provider's session", async () => {
  mount("/activate?code=48217730");
  fireEvent.click(await screen.findByRole("button", { name: "Not you? Switch account" }));
  // Silo sign-out only: the provider's own logout would sign the person out
  // of everything else that uses it.
  expect(auth.logoutOfSiloOnly).toHaveBeenCalled();
  expect(auth.logout).not.toHaveBeenCalled();
  await waitFor(() =>
    expect(screen.getByTestId("where").textContent).toBe(
      "/login?redirect=%2Factivate%3Fcode%3D48217730&switch_account=1",
    ),
  );
});

it("explains codes it can't act on", async () => {
  lookups = [notFound()] as never;
  mount("/activate?code=11112222");
  await screen.findByText("We couldn't find that code. Check the code on your TV.");

  cleanup();
  lookups = [{ ...pending, status: "canceled" }];
  mount("/activate?code=48217730");
  await screen.findByText("This TV stopped waiting. Start again on the TV.");
  expect(screen.queryByText("Check that your TV shows")).toBeNull();

  cleanup();
  lookups = [{ ...pending, status: "expired", user_code: "" }];
  mount("/activate?code=48217730");
  await screen.findByText("This code expired. Your TV is showing a new one; scan it again.");
});

const quickConnect = {
  ...pending,
  user_code: "482177",
  device_name: "Firefox",
  device_platform: "Jellyfin Web",
  client_purpose: "jellyfin_quick_connect",
};

function recordQuickConnectDecisions() {
  const calls: Array<{ op: string; body: unknown; profileContext?: unknown }> = [];
  let lookupCount = 0;
  vi.mocked(v2).mockImplementation(((
    op: string,
    options?: { body?: unknown; profileContext?: unknown },
  ) => {
    if (op === "GET /api/v2/auth/device") {
      lookupCount++;
      return Promise.resolve(quickConnect);
    }
    calls.push({ op, body: options?.body, profileContext: options?.profileContext });
    return Promise.resolve({ status: "approved" });
  }) as never);
  return { calls, lookups: () => lookupCount };
}

it("asks a signed-out person to sign in before looking up a Quick Connect code", async () => {
  auth.user = null;
  mount("/activate");
  const input = screen.getByLabelText("Enter the code on your TV or app");
  fireEvent.change(input, { target: { value: "482177" } });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  await screen.findByText(/to approve code 482177 from your Jellyfin app/);
  expect(screen.getByRole("link", { name: "Sign in to approve" }).getAttribute("href")).toBe(
    "/login?redirect=%2Factivate%3Fcode%3D482177",
  );
  const quickCalls = vi
    .mocked(v2)
    .mock.calls.filter(([, options]) => JSON.stringify(options) === '{"query":{"code":"482177"}}');
  expect(quickCalls).toEqual([]);
});

it("approves a Quick Connect request for the chosen profile with its PIN", async () => {
  profiles.list = [
    { id: "p-laura", name: "Laura", has_pin: false },
    { id: "p-kids", name: "Kids", has_pin: true },
  ];
  auth.profile = { id: "p-laura" };
  profiles.verify.mockResolvedValue({ valid: true, profile_token: "kids-token" });
  const recorded = recordQuickConnectDecisions();
  mount("/activate?code=482177");
  await screen.findByText("Sign in Firefox?");
  expect(screen.getByText("Check that your app shows")).toBeTruthy();
  expect(screen.getByText("482177")).toBeTruthy();
  expect(screen.queryByText(/Older TV apps show/)).toBeNull();
  expect(screen.getByText("Only approve a device that's in front of you right now.")).toBeTruthy();

  fireEvent.click(screen.getByRole("radio", { name: /Kids/ }));
  const pin = await screen.findByLabelText("Kids PIN");
  expect((screen.getByRole("button", { name: "Sign in" }) as HTMLButtonElement).disabled).toBe(
    true,
  );
  fireEvent.change(pin, { target: { value: "1234" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

  await screen.findByText("Done. Firefox is signing in as Kids.");
  expect(profiles.verify).toHaveBeenCalledWith("p-kids", "1234");
  expect(recorded.calls).toEqual([
    {
      op: "POST /api/v2/auth/device/approve-handoff",
      body: { code: "482177" },
      profileContext: expect.objectContaining({ profileId: "p-kids", profileToken: "kids-token" }),
    },
  ]);
  // Every Quick Connect lookup spends the account's budget: no watching.
  expect(recorded.lookups()).toBe(1);
});

it("approves a Quick Connect request for an unlocked profile without a PIN", async () => {
  profiles.list = [{ id: "p-laura", name: "Laura", has_pin: false }];
  const recorded = recordQuickConnectDecisions();
  mount("/activate?code=482177");
  await screen.findByText("Sign in Firefox?");
  expect(screen.queryByLabelText(/PIN/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await screen.findByText("Done. Firefox is signing in as Laura.");
  expect(profiles.verify).not.toHaveBeenCalled();
  expect(recorded.calls[0]?.profileContext).toEqual(
    expect.objectContaining({ profileId: "p-laura", profileToken: null }),
  );
});

it("keeps a Quick Connect request pending when the PIN is wrong", async () => {
  profiles.list = [{ id: "p-kids", name: "Kids", has_pin: true }];
  profiles.verify.mockResolvedValue({ valid: false });
  const recorded = recordQuickConnectDecisions();
  mount("/activate?code=482177");
  fireEvent.change(await screen.findByLabelText("Kids PIN"), { target: { value: "0000" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await screen.findByText("That PIN isn't right.");
  expect(recorded.calls).toEqual([]);
});

it("says when an account has tried too many Quick Connect codes", async () => {
  vi.mocked(v2).mockImplementation((() =>
    Promise.reject(
      new V2ProblemError(
        "getDeviceLogin",
        {
          type: "https://siloserver.org/docs/api/v2/problems/rate_limited",
          title: "Rate limited",
          status: 429,
          detail: "Too many Quick Connect codes tried. Try again later.",
          instance: "urn:silo:request:1",
        } as never,
        900,
      ),
    )) as never);
  mount("/activate?code=482177");
  await screen.findByText("You've tried too many codes. Try again in 15 minutes.");
  expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});

it("declines a Quick Connect request without looking it up again", async () => {
  profiles.list = [{ id: "p-laura", name: "Laura", has_pin: false }];
  const recorded = recordQuickConnectDecisions();
  mount("/activate?code=482177");
  await screen.findByText("Sign in Firefox?");
  fireEvent.click(screen.getByRole("button", { name: "Not now" }));
  await screen.findByText("This sign-in was declined.");
  expect(recorded.calls.map((c) => c.op)).toEqual(["POST /api/v2/auth/device/deny"]);
  expect(recorded.lookups()).toBe(1);
});

it("explains that an admin viewing as someone can't look up a Quick Connect code", async () => {
  vi.mocked(v2).mockImplementation((() =>
    Promise.reject(
      new V2ProblemError("getDeviceLogin", {
        type: "https://siloserver.org/docs/api/v2/problems/permission_denied",
        title: "Permission denied",
        status: 403,
        detail: "Only the account's own signed-in session can look up a Quick Connect code.",
        instance: "urn:silo:request:1",
      } as never),
    )) as never);
  mount("/activate?code=482177");
  await screen.findByText(/Stop viewing as laura to approve a TV/);
});

it("says a PIN lockout is about the profile, not the codes", async () => {
  profiles.list = [{ id: "p-kids", name: "Kids", has_pin: true }];
  profiles.verify.mockRejectedValue(
    new V2ProblemError(
      "verifyProfilePIN",
      {
        type: "https://siloserver.org/docs/api/v2/problems/rate_limited",
        title: "Rate limited",
        status: 429,
        detail: "Too many incorrect PINs.",
        instance: "urn:silo:request:1",
      } as never,
      300,
    ),
  );
  const recorded = recordQuickConnectDecisions();
  mount("/activate?code=482177");
  fireEvent.change(await screen.findByLabelText("Kids PIN"), { target: { value: "0000" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await screen.findByText("Too many incorrect PINs. Try again in 5 minutes.");
  expect(recorded.calls).toEqual([]);
});

it("reloads a Quick Connect request that moved on before it was declined", async () => {
  profiles.list = [{ id: "p-laura", name: "Laura", has_pin: false }];
  let lookupCount = 0;
  vi.mocked(v2).mockImplementation(((op: string) => {
    if (op === "GET /api/v2/auth/device") {
      lookupCount++;
      return Promise.resolve(
        lookupCount === 1 ? quickConnect : { ...quickConnect, status: "expired" },
      );
    }
    return Promise.reject(
      new V2ProblemError("denyDeviceLogin", {
        type: "https://siloserver.org/docs/api/v2/problems/device_login_expired",
        title: "Expired",
        status: 410,
        detail: "Device login request has expired.",
        instance: "urn:silo:request:1",
      } as never),
    );
  }) as never);
  mount("/activate?code=482177");
  await screen.findByText("Sign in Firefox?");
  fireEvent.click(screen.getByRole("button", { name: "Not now" }));
  await screen.findByText("This code expired. Start Quick Connect again in the app.");
  expect(lookupCount).toBe(2);
});
