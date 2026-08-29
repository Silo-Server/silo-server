import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { RateLimitConfig } from "@/api/types";
import SecurityAccessSettings from "./SecurityAccessSettings";

// Radix Select reads element sizes via ResizeObserver, which jsdom does not
// provide, and opens through pointer capture, which jsdom also lacks.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
if (typeof window !== "undefined" && !window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const useSettingsFormMock = vi.fn();
const rateLimitConfigMock = vi.fn();
const updateRateLimitMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

const reportUnsavedMock = vi.fn();
vi.mock("@/hooks/useUnsavedChanges", () => ({
  useReportUnsavedChanges: (dirty: boolean) => reportUnsavedMock(dirty),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(["auth.access_token_expiry"]),
}));

vi.mock("@/hooks/queries/admin/rateLimits", () => ({
  useRateLimitConfig: () => rateLimitConfigMock(),
  useUpdateRateLimitConfig: () => updateRateLimitMock(),
}));

const SERVER_CONFIG: RateLimitConfig = {
  enabled: true,
  backend: "memory",
  global_requests_per_second: 1000,
  tiers: {
    standard: { requests_per_second: 20, requests_per_minute: 1200, burst: 20 },
    elevated: { requests_per_second: 100, requests_per_minute: 6000, burst: 100 },
  },
  ip_requests_per_second: 120,
  ip_requests_per_minute: 6000,
  ip_burst: 120,
  auth_endpoints: {
    login: { requests_per_minute: 20, burst: 10 },
    signup: { requests_per_minute: 10, burst: 6 },
    setup: { requests_per_minute: 10, burst: 6 },
    device_start: { requests_per_minute: 20, burst: 10 },
    device_lookup: { requests_per_minute: 60, burst: 20 },
    device_poll: { requests_per_minute: 120, burst: 30 },
    autoscan_webhook: { requests_per_minute: 60, burst: 30 },
  },
  active: true,
  active_backend: "memory",
  redis_available: true,
};

const REDIS_HINT = /Configure Redis under Storage & Database first/;

async function openBackendSelect() {
  await userEvent.click(screen.getByRole("button", { name: /Advanced/i }));
  await userEvent.click(screen.getByRole("combobox", { name: /Where counters are kept/i }));
}

function makeForm(overrides: Record<string, unknown> = {}) {
  return {
    isLoading: false,
    getValue: () => "",
    setValue: vi.fn(),
    isDirty: () => false,
    dirtyCount: 0,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
    ...overrides,
  };
}

describe("SecurityAccessSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    useSettingsFormMock.mockReset();
    useSettingsFormMock.mockReturnValue(makeForm());
    rateLimitConfigMock.mockReturnValue({ data: SERVER_CONFIG, isLoading: false });
    updateRateLimitMock.mockReturnValue({
      mutateAsync: vi.fn().mockResolvedValue(undefined),
      isPending: false,
      data: undefined,
    });
  });

  it("reports a rate-limit-only draft to the unsaved-changes registry", async () => {
    render(<SecurityAccessSettings />);
    expect(reportUnsavedMock).toHaveBeenLastCalledWith(false);

    await userEvent.click(screen.getByRole("switch", { name: /Enable rate limiting/i }));

    // The guard and the reload prompt read the registry, not the SaveBar, so
    // the separate rate-limit draft has to announce itself there too.
    expect(reportUnsavedMock).toHaveBeenLastCalledWith(true);
  });

  it("renders every field group", () => {
    render(<SecurityAccessSettings />);

    for (const heading of ["Sign-in sessions", "Network", "Rate limiting"]) {
      expect(screen.getByRole("group", { name: heading })).toBeInTheDocument();
    }
  });

  it("renders the tab title", () => {
    render(<SecurityAccessSettings />);

    expect(screen.getByRole("heading", { name: "Security & Access" })).toBeInTheDocument();
  });

  it("keeps the token and proxy keys on the batched settings form", () => {
    render(<SecurityAccessSettings />);

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toEqual([
      "auth.access_token_expiry",
      "auth.refresh_token_expiry",
      "clientip.trusted_proxies",
    ]);
  });

  it("shows only the rate limiting switch until Advanced is opened", async () => {
    render(<SecurityAccessSettings />);

    expect(screen.getByRole("switch", { name: /Enable rate limiting/i })).toBeInTheDocument();
    expect(screen.queryByText("Per client address")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Advanced/i }));

    expect(screen.getByText("Per client address")).toBeInTheDocument();
    expect(screen.getByText("Standard API keys")).toBeInTheDocument();
    expect(screen.getByText("Sign in")).toBeInTheDocument();
  });

  it("stages an edited rate-limit row on the shared save bar", async () => {
    render(<SecurityAccessSettings />);

    await userEvent.click(screen.getByRole("button", { name: /Advanced/i }));

    // The control rejects empty/zero input, so append rather than clear first.
    const rpsInput = screen.getByLabelText("Whole-server limit") as HTMLInputElement;
    await userEvent.type(rpsInput, "5");

    expect(rpsInput.value).toBe("10005");
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
  });

  it("counts an edited rate limit toward the shared save bar and saves both writers in order", async () => {
    const order: string[] = [];
    const save = vi.fn(async () => {
      order.push("settings");
    });
    const mutateAsync = vi.fn(async () => {
      order.push("rate-limits");
    });
    useSettingsFormMock.mockReturnValue(makeForm({ dirtyCount: 1, save }));
    updateRateLimitMock.mockReturnValue({ mutateAsync, isPending: false, data: undefined });

    render(<SecurityAccessSettings />);

    await userEvent.click(screen.getByRole("switch", { name: /Enable rate limiting/i }));
    expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /^Save$/i }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith(expect.objectContaining({ enabled: false })),
    );
    expect(save).toHaveBeenCalled();
    // PUT /admin/rate-limits/config validates `backend: redis` against the
    // persisted settings, so running the two writers concurrently lets the
    // limiter be judged against the state this very save is replacing.
    expect(order).toEqual(["settings", "rate-limits"]);
  });

  it("leaves the rate-limit write alone when the settings batch fails", async () => {
    const save = vi.fn().mockRejectedValue(new Error("invalid duration"));
    const mutateAsync = vi.fn();
    useSettingsFormMock.mockReturnValue(makeForm({ dirtyCount: 1, save }));
    updateRateLimitMock.mockReturnValue({ mutateAsync, isPending: false, data: undefined });

    render(<SecurityAccessSettings />);

    await userEvent.click(screen.getByRole("switch", { name: /Enable rate limiting/i }));
    await userEvent.click(screen.getByRole("button", { name: /^Save$/i }));

    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(mutateAsync).not.toHaveBeenCalled();
    // The staged rate-limit edit survives so the admin can fix the cause and
    // save again; both mutations toast their own failure.
    expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
  });

  it("offers the Redis backend when the server reports Redis configured", async () => {
    render(<SecurityAccessSettings />);
    await openBackendSelect();

    expect(screen.getByRole("option", { name: "Shared via Redis" })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(screen.queryByText(REDIS_HINT)).not.toBeInTheDocument();
  });

  it("disables the Redis backend and points at Storage & Database when Redis is unconfigured", async () => {
    rateLimitConfigMock.mockReturnValue({
      data: { ...SERVER_CONFIG, redis_available: false },
      isLoading: false,
    });

    render(<SecurityAccessSettings />);
    await openBackendSelect();

    expect(screen.getByRole("option", { name: "Shared via Redis" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(screen.getByRole("option", { name: "This server only" })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(screen.getByText(REDIS_HINT)).toBeInTheDocument();
  });

  it("leaves the restart prompt to the admin shell", () => {
    rateLimitConfigMock.mockReturnValue({
      data: { ...SERVER_CONFIG, backend: "redis", active_backend: "memory" },
      isLoading: false,
    });

    render(<SecurityAccessSettings />);

    // AdminLayout renders the one banner for the whole admin area, driven by
    // GET /admin/server/status; saving a backend change marks that flag
    // server-side, so this page adds nothing of its own.
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();
  });

  it("warns on the backend row when the running limiter disagrees with the saved backend", async () => {
    rateLimitConfigMock.mockReturnValue({
      data: { ...SERVER_CONFIG, backend: "redis", active_backend: "memory" },
      isLoading: false,
    });

    render(<SecurityAccessSettings />);
    await userEvent.click(screen.getByRole("button", { name: /Advanced/i }));

    expect(
      screen.getByText(/running limiter is using in-memory counters, not the saved Redis/i),
    ).toBeInTheDocument();
  });

  it("warns when rate limiting is enabled but no limiter is running", () => {
    rateLimitConfigMock.mockReturnValue({
      data: { ...SERVER_CONFIG, active: false, active_backend: "" },
      isLoading: false,
    });

    render(<SecurityAccessSettings />);

    expect(screen.getByText(/no limiter is running in this process/i)).toBeInTheDocument();
  });

  it("shows no drift warnings when the running limiter matches the saved config", async () => {
    rateLimitConfigMock.mockReturnValue({ data: SERVER_CONFIG, isLoading: false });

    render(<SecurityAccessSettings />);
    await userEvent.click(screen.getByRole("button", { name: /Advanced/i }));

    expect(screen.queryByText(/no limiter is running/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/running limiter is using/i)).not.toBeInTheDocument();
  });
});
