// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { v2, V2ProblemError } from "@/api/v2/request";
import ActivateDevice from "./ActivateDevice";

const auth = vi.hoisted(() => ({
  user: null as null | { username: string },
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
  const input = screen.getByLabelText("Enter the code on your TV");
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
    const input = await screen.findByLabelText("Enter the code on your TV");
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
