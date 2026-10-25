// @vitest-environment jsdom

import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Radix Select reads element sizes via ResizeObserver and opens through
// pointer capture; jsdom provides neither.
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

import type {
  EffectiveSetting,
  EffectiveSettingsMap,
  SettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useEffectiveSettings: vi.fn(),
  useSetSettingValue: vi.fn(),
  useClearSettingValue: vi.fn(),
  capabilities: {
    api_version: 1,
    manifest_revision: 7,
    contract_etag: "revision-seven",
    supports_batched_effective: true,
    supports_idempotent_writes: true,
  } as SettingsCapabilities | undefined,
  /** Whether the capability check has answered; false covers pending and failed. */
  capabilitiesSettled: true,
  /** A failed capability request, which reads as "unknown", never as "old". */
  capabilitiesError: undefined as Error | undefined,
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("sonner", () => ({ toast: mocks.toast }));

// The TheIntroDB section reads plugin settings through react-query and has its own
// test. Here the plugin is not installed, so this file tests the settings page alone
// without standing up a query client for a section it does not cover.
vi.mock("@/hooks/queries/pluginSettings", () => ({
  usePluginInstallation: () => ({ installation: null }),
  usePluginSettingsValues: () => ({ data: undefined }),
  useUpdatePluginSettings: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/settingValues", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/settingValues")>(
    "@/hooks/queries/settingValues",
  );

  return {
    ...actual,
    useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
    useSetSettingValue: (...args: unknown[]) => mocks.useSetSettingValue(...args),
    useClearSettingValue: (...args: unknown[]) => mocks.useClearSettingValue(...args),
    useSettingsCapabilities: () => ({
      data: mocks.capabilities,
      isLoading: false,
      isPending: !mocks.capabilitiesSettled && !mocks.capabilitiesError,
      isSuccess: mocks.capabilitiesSettled,
      error: mocks.capabilitiesError,
    }),
  };
});

import PlaybackSettings from "./PlaybackSettings";
import { SEEK_KEYS } from "@/lib/seekIntervals";
import { storage } from "@/utils/storage";

function capabilitiesAtRevision(revision: number): SettingsCapabilities {
  return {
    api_version: 1,
    manifest_revision: revision,
    contract_etag: `revision-${revision}`,
    supports_batched_effective: true,
    supports_idempotent_writes: true,
  };
}

function showResolved(key: SettingKey, value: unknown, source: EffectiveSetting["source"]) {
  const data: EffectiveSettingsMap = { [key]: { key, value, source } };
  mocks.useEffectiveSettings.mockReturnValue({ data, isLoading: false });
}

const importLegacyIntervals = () =>
  userEvent.click(screen.getByRole("button", { name: "Use this browser's audiobook intervals" }));

describe("PlaybackSettings", () => {
  let mutate: ReturnType<typeof vi.fn>;
  let mutateAsync: ReturnType<typeof vi.fn>;
  let clearMutateAsync: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.useEffectiveSettings.mockReset();
    mocks.useSetSettingValue.mockReset();
    mocks.useClearSettingValue.mockReset();
    mocks.capabilities = capabilitiesAtRevision(7);
    mocks.capabilitiesSettled = true;
    mocks.capabilitiesError = undefined;
    mocks.toast.error.mockReset();
    mocks.toast.success.mockReset();
    // The environment's localStorage global is inert; legacy audiobook
    // intervals are read through it, so give each test a fresh in-memory one.
    const memory = new Map<string, string>();
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: {
        getItem: (key: string) => memory.get(key) ?? null,
        setItem: (key: string, value: string) => memory.set(key, value),
        removeItem: (key: string) => memory.delete(key),
      },
    });
    mutate = vi.fn();
    mutateAsync = vi.fn().mockResolvedValue(undefined);
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);

    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutate, mutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: clearMutateAsync,
    });
  });

  afterEach(cleanup);

  it("keeps the legacy switch when the connected server predates revision 7", () => {
    mocks.capabilities = capabilitiesAtRevision(6);

    render(<PlaybackSettings />);

    expect(screen.getByLabelText("Auto-skip intros")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Skip intros" })).not.toBeInTheDocument();
  });

  // The deprecated boolean cannot represent "never": writing it against a
  // revision-7 server turns a deliberate "never" into "ask" through the
  // compatibility mirror. So an unanswered capability check — pending or
  // failed, which look the same from here — must not render the switch.
  it("offers no writable intro control while the capability check is unresolved", () => {
    mocks.capabilities = undefined;
    mocks.capabilitiesSettled = false;

    render(<PlaybackSettings />);

    expect(screen.queryByLabelText("Auto-skip intros")).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Skip intros" })).toBeDisabled();
  });

  it("clears a shadowing device row when auto-play is saved", async () => {
    // The player's post-roll toggle and this switch edit the same setting, and
    // the contract resolves profile_device above profile. A device row left in
    // place would keep shadowing this save and snap the switch back, with no
    // other web affordance able to remove it — so the save clears it.
    showResolved(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT, false, "profile_device");

    render(<PlaybackSettings />);
    expect(screen.getByLabelText("Auto-play next episode").getAttribute("aria-checked")).toBe(
      "false",
    );

    fireEvent.click(screen.getByLabelText("Auto-play next episode"));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT,
        value: true,
        identity: { scope: "profile" },
      }),
    );
    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT,
        identity: { scope: "profile_device" },
      }),
    );
  });

  it("leaves other scopes alone when no device row is shadowing auto-play", async () => {
    showResolved(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT, false, "profile");

    render(<PlaybackSettings />);
    expect(screen.getByLabelText("Auto-play next episode")).toHaveAttribute(
      "aria-checked",
      "false",
    );
    fireEvent.click(screen.getByLabelText("Auto-play next episode"));

    await waitFor(() => expect(mutateAsync).toHaveBeenCalled());
    expect(clearMutateAsync).not.toHaveBeenCalled();
  });

  // Quality is the same two axes the device screen edits — a resolution cap
  // and a bandwidth cap — not a compound preset only this screen understands.

  it("shows a stored original quality as Auto and no longer offers Original", async () => {
    showResolved(SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY, "original", "profile");
    render(<PlaybackSettings />);

    const select = screen.getByRole("combobox", { name: "Preferred quality" });
    expect(select).toHaveTextContent("Auto");
    expect(
      screen.getByText(
        "Plays the original file whenever your device and connection can handle it.",
      ),
    ).toBeInTheDocument();

    await userEvent.click(select);
    expect(await screen.findByRole("option", { name: "Auto" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Original/ })).toBeNull();
  });

  it("clears the bandwidth cap rather than storing a sentinel for No limit", async () => {
    showResolved(SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS, 6000, "profile");

    render(<PlaybackSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Maximum bitrate" }));
    await userEvent.click(await screen.findByRole("option", { name: "No limit" }));

    // "No cap" is the absence of a value at every layer, so choosing it
    // deletes the profile row (and any device row) instead of writing one.
    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS,
        identity: { scope: "profile" },
      }),
    );
    expect(mutateAsync).not.toHaveBeenCalled();
  });

  it("keeps a bandwidth cap set elsewhere selectable", async () => {
    // A value from another client or the API that is not on the ladder must
    // read back as itself, not silently as "No limit".
    showResolved(SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS, 12345, "profile");

    render(<PlaybackSettings />);

    expect(screen.getByRole("combobox", { name: "Maximum bitrate" }).textContent).toContain(
      "12.3 Mbps",
    );
  });

  describe("seek controls", () => {
    const group = (name: string) => within(screen.getByRole("group", { name }));

    it("hides the shared controls on a server that predates revision 9", () => {
      render(<PlaybackSettings />);

      expect(screen.getByText("Seek controls")).toBeInTheDocument();
      expect(screen.queryByRole("group", { name: "Video" })).not.toBeInTheDocument();
      expect(screen.queryByRole("group", { name: "Audiobooks" })).not.toBeInTheDocument();
      expect(screen.getByText(/does not store seek intervals per profile yet/)).toBeInTheDocument();
    });

    it("reports a rejected write and keeps showing the resolved value", async () => {
      mocks.capabilities = capabilitiesAtRevision(9);
      mutateAsync.mockRejectedValue(new Error("boom"));

      render(<PlaybackSettings />);

      await userEvent.click(
        group("Video").getByRole("combobox", { name: "Fast-forward interval" }),
      );
      await userEvent.click(await screen.findByRole("option", { name: "60 seconds" }));

      await waitFor(() =>
        expect(mocks.toast.error).toHaveBeenCalledWith(
          "Failed to save fast-forward interval for video",
        ),
      );
      expect(
        group("Video").getByRole("combobox", { name: "Fast-forward interval" }),
      ).toHaveTextContent("30 seconds");
    });

    it("surfaces a failed read instead of a default the server never confirmed", () => {
      mocks.capabilities = capabilitiesAtRevision(9);
      mocks.useEffectiveSettings.mockReturnValue({
        data: undefined,
        isPending: false,
        error: new Error("Offline"),
      });

      render(<PlaybackSettings />);

      const back = group("Video").getByRole("combobox", { name: "Rewind interval" });
      expect(back).toBeDisabled();
      expect(back).toHaveTextContent("Unavailable");
      expect(
        group("Video").getByText("Could not load the rewind interval for video."),
      ).toBeInTheDocument();
    });

    it("distinguishes a failed capability check from an old server", () => {
      mocks.capabilities = undefined;
      mocks.capabilitiesSettled = false;
      mocks.capabilitiesError = new Error("Offline");

      render(<PlaybackSettings />);

      expect(screen.getByRole("alert")).toHaveTextContent(/Could not check whether this server/);
      expect(screen.queryByText(/does not store seek intervals per profile yet/)).toBeNull();
    });

    it("imports only on a click, listing exactly what will be written", async () => {
      mocks.capabilities = capabilitiesAtRevision(9);
      storage.set(storage.KEYS.AUDIOBOOK_SKIP_BACK, "15");
      storage.set(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, "60");

      render(<PlaybackSettings />);

      expect(
        screen.getByText(/rewind 15 seconds and fast-forward 60 seconds saved locally/),
      ).toBeInTheDocument();
      expect(
        screen.getByText(/replaces both intervals for the active profile/),
      ).toBeInTheDocument();
      // Nothing is written on mount.
      expect(mutateAsync).not.toHaveBeenCalled();

      await importLegacyIntervals();

      await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(2));
      expect(mutateAsync).toHaveBeenCalledWith({
        key: SEEK_KEYS.audiobook.back,
        value: 15,
        identity: { scope: "profile" },
      });
      expect(mutateAsync).toHaveBeenCalledWith({
        key: SEEK_KEYS.audiobook.forward,
        value: 60,
        identity: { scope: "profile" },
      });
      expect(await screen.findByRole("status")).toHaveTextContent("Saved rewind and fast-forward.");
      expect(mocks.toast.success).toHaveBeenCalled();
      // Legacy values stay, so the row remains available as an explicit overwrite.
      expect(storage.get(storage.KEYS.AUDIOBOOK_SKIP_BACK)).toBe("15");
      expect(
        screen.getByRole("button", { name: "Use this browser's audiobook intervals" }),
      ).toBeInTheDocument();
    });

    it("imports a single legacy direction and says the other stays unchanged", async () => {
      mocks.capabilities = capabilitiesAtRevision(9);
      storage.set(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, "45");
      // An out-of-contract value is not a legacy preference.
      storage.set(storage.KEYS.AUDIOBOOK_SKIP_BACK, "7");

      render(<PlaybackSettings />);

      expect(screen.getByText(/fast-forward 45 seconds saved locally/)).toBeInTheDocument();
      expect(screen.getByText(/the rewind interval stays as it is/)).toBeInTheDocument();

      await importLegacyIntervals();

      await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(1));
      expect(mutateAsync).toHaveBeenCalledWith({
        key: SEEK_KEYS.audiobook.forward,
        value: 45,
        identity: { scope: "profile" },
      });
    });

    it("reports a partial import failure per direction and keeps the retry available", async () => {
      mocks.capabilities = capabilitiesAtRevision(9);
      storage.set(storage.KEYS.AUDIOBOOK_SKIP_BACK, "15");
      storage.set(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, "60");
      mutateAsync.mockImplementation(async ({ key }: { key: string }) => {
        if (key === SEEK_KEYS.audiobook.forward) throw new Error("Save failed");
      });

      render(<PlaybackSettings />);

      await importLegacyIntervals();

      const status = await screen.findByRole("status");
      expect(status).toHaveTextContent("Saved rewind.");
      expect(status).toHaveTextContent("Could not save fast-forward.");
      expect(mocks.toast.error).toHaveBeenCalledWith(
        "Imported rewind interval, but fast-forward failed",
      );
      expect(storage.get(storage.KEYS.AUDIOBOOK_SKIP_FORWARD)).toBe("60");
      expect(
        screen.getByRole("button", { name: "Use this browser's audiobook intervals" }),
      ).toBeEnabled();
    });
  });
});
