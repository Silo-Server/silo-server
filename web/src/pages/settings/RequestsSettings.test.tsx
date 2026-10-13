import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { requestKeys } from "@/hooks/queries/keys";
import { SETTING_KEYS } from "@/lib/settingsContract";
import RequestsSettings from "./RequestsSettings";

const mocks = vi.hoisted(() => ({
  featureStatus: vi.fn(),
  effective: vi.fn(),
  setValue: vi.fn(),
  clearValue: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => mocks.featureStatus(),
}));
vi.mock("@/hooks/queries/settingValues", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/settingValues")>()),
  useEffectiveSettings: () => mocks.effective(),
  useSetSettingValue: () => ({ mutateAsync: mocks.setValue, isPending: false }),
  useClearSettingValue: () => ({ mutateAsync: mocks.clearValue, isPending: false }),
}));
vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

const KEY = SETTING_KEYS.REQUESTS_WATCHLIST_AUTO_REQUEST;
const LABEL = "Request titles I add to my watchlist";

function profileValue(value: boolean | undefined) {
  mocks.effective.mockReturnValue({
    data: value === undefined ? {} : { [KEY]: { value, source: "profile" } },
    isLoading: false,
  });
}

function renderPage() {
  const queryClient = new QueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  render(
    <QueryClientProvider client={queryClient}>
      <RequestsSettings />
    </QueryClientProvider>,
  );
  return invalidate;
}

describe("Settings › Requests", () => {
  beforeEach(() => {
    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: true,
        allowed: true,
        watchlist_titles_supported: true,
        watchlist_requests: true,
      },
      isLoading: false,
    });
    profileValue(undefined);
    mocks.setValue.mockResolvedValue(undefined);
    mocks.clearValue.mockResolvedValue(undefined);
  });
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("turns watchlist requests off for this profile", async () => {
    const invalidate = renderPage();
    const toggle = screen.getByRole("switch", { name: LABEL });
    expect(toggle).toHaveAttribute("aria-checked", "true");

    fireEvent.click(toggle);

    await waitFor(() =>
      expect(mocks.toastSuccess).toHaveBeenCalledWith("Request preference saved"),
    );
    expect(mocks.setValue).toHaveBeenCalledWith({
      key: KEY,
      value: false,
      identity: { scope: "profile" },
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: requestKeys.status() });
  });

  it("turns them back on by clearing the profile's value", async () => {
    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: true,
        allowed: true,
        watchlist_titles_supported: true,
        watchlist_requests: false,
      },
      isLoading: false,
    });
    profileValue(false);
    renderPage();
    const toggle = screen.getByRole("switch", { name: LABEL });
    expect(toggle).toHaveAttribute("aria-checked", "false");

    fireEvent.click(toggle);

    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalled());
    expect(mocks.clearValue).toHaveBeenCalledWith({ key: KEY, identity: { scope: "profile" } });
    expect(mocks.setValue).not.toHaveBeenCalled();
  });

  it("reports a failed save", async () => {
    mocks.setValue.mockRejectedValue(new Error("boom"));
    renderPage();
    fireEvent.click(screen.getByRole("switch", { name: LABEL }));
    await waitFor(() =>
      expect(mocks.toastError).toHaveBeenCalledWith("Failed to save request preference"),
    );
  });

  it("hides the switch while the server doesn't request watchlist titles", () => {
    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: true,
        allowed: true,
        watchlist_titles_supported: true,
        watchlist_requests: false,
      },
      isLoading: false,
    });
    renderPage();
    expect(screen.queryByRole("switch", { name: LABEL })).toBeNull();
    expect(screen.getByTestId("watchlist-auto-request-off")).toHaveTextContent(
      "Adding a title to your watchlist doesn’t request it on this server. You can still request titles from Discover.",
    );
  });

  it("says requests are off instead of pointing to Discover", () => {
    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: false,
        allowed: true,
        watchlist_titles_supported: false,
        watchlist_requests: false,
      },
      isLoading: false,
    });
    profileValue(false);
    renderPage();
    expect(screen.queryByRole("switch", { name: LABEL })).toBeNull();
    expect(screen.getByTestId("watchlist-auto-request-off")).toHaveTextContent(
      "Requests are turned off on this server.",
    );
  });

  it("doesn't point to Discover when the viewer may not request", () => {
    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: true,
        allowed: false,
        watchlist_titles_supported: true,
        watchlist_requests: false,
      },
      isLoading: false,
    });
    renderPage();
    expect(screen.getByTestId("watchlist-auto-request-off").textContent).not.toContain("Discover");
  });
});
