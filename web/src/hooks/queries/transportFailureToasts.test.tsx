import { onlineManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { StaleApiRequestContextError } from "@/api/client";
import { v2Problem } from "@/api/v2/problems.test-support";
import { V2TimeoutError, V2TransportError } from "@/api/v2/request";
import { SETTING_KEYS } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  toastError: vi.fn(),
  setAdminSettingValue: vi.fn(),
  deleteAdminSettingValue: vi.fn(),
  v2: vi.fn(),
  sendAudioChoiceRequest: vi.fn(),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: mocks.toastError } }));
vi.mock("@/api/v2/adminAccountSettings", async () => ({
  ...(await vi.importActual<object>("@/api/v2/adminAccountSettings")),
  captureAdminSettingAuthority: () => ({ profileId: "p1" }),
  setAdminSettingValue: mocks.setAdminSettingValue,
  deleteAdminSettingValue: mocks.deleteAdminSettingValue,
}));
vi.mock("@/api/client", async () => ({
  ...(await vi.importActual<object>("@/api/client")),
  captureProfileRequestContext: () => ({ profileId: "p1" }),
  isCapturedProfileAuthorityActive: () => true,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<object>("@/api/v2/request")),
  v2: mocks.v2,
}));

vi.mock("@/lib/recipes", () => ({ fetchRecipeCatalog: async () => ({ categories: {} }) }));
vi.mock("@/player/context/PlayerConfigContext", () => ({ usePlayerConfig: () => ({}) }));
vi.mock("@/player/utils/audioChoicePersistence", async () => ({
  ...(await vi.importActual<object>("@/player/utils/audioChoicePersistence")),
  sendAudioChoiceRequest: mocks.sendAudioChoiceRequest,
}));

import { ProfileHomeSections } from "@/pages/admin-users/detail/preferences/ProfileHomeSections";
import { PlayerFetchError } from "@/player/player-fetch";
import { useTrackChoicePersistence } from "@/player/hooks/useTrackChoicePersistence";
import {
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserSetting,
} from "./admin/users";
import { useSetSubtitlePreference } from "./subtitles";
import { useSetAudioPreference } from "./trackPreferences";

const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
);

async function toastFor(run: () => Promise<unknown>): Promise<string> {
  mocks.toastError.mockClear();
  await act(() => run().catch(() => undefined));
  expect(mocks.toastError).toHaveBeenCalledTimes(1);
  return mocks.toastError.mock.calls[0]?.[0] as string;
}

const developerText = [
  new TypeError("Failed to fetch"),
  new V2TimeoutError("setSettingValue", 15_000),
  new V2TransportError("setSettingValue", 502, "non-JSON body"),
  new StaleApiRequestContextError(),
];

describe("write failures whose error text is developer English", () => {
  beforeEach(() => {
    mocks.setAdminSettingValue.mockReset();
    mocks.deleteAdminSettingValue.mockReset();
    mocks.v2.mockReset();
  });

  it("show an admin setting save's own message, and keep its translated errors", async () => {
    const { result } = renderHook(() => useUpdateAdminUserSetting(), { wrapper });
    const save = (key: string, value: string) => () =>
      result.current.mutateAsync({ userId: 1, key, identity: { scope: "account" }, value });

    for (const error of developerText) {
      mocks.setAdminSettingValue.mockRejectedValueOnce(error);
      expect(await toastFor(save(SETTING_KEYS.NAV_SHORTCUTS, "{}"))).toBe("Failed to save setting");
    }

    mocks.setAdminSettingValue.mockRejectedValueOnce(
      v2Problem(422, "validation_failed", "Value is out of range."),
    );
    expect(await toastFor(save(SETTING_KEYS.NAV_SHORTCUTS, "{}"))).toBe("Value is out of range.");

    expect(await toastFor(save(SETTING_KEYS.NAV_SHORTCUTS, "{not json"))).toBe(
      `${SETTING_KEYS.NAV_SHORTCUTS}: the value must be valid JSON`,
    );
  });

  it("say why a device reset stopped in the interface language", async () => {
    const { result } = renderHook(() => useDeleteAllAdminUserDeviceSettingsForDevice(), {
      wrapper,
    });
    mocks.deleteAdminSettingValue
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new TypeError("Failed to fetch"));

    const text = await toastFor(() =>
      result.current.mutateAsync({
        userId: 1,
        profileId: "p1",
        deviceId: "d1",
        keys: ["a", "b", "c"],
      }),
    );
    expect(text).toBe(
      "Reset stopped after 1 of 3 overrides. Reload the settings before continuing.",
    );
  });

  it("show a title's track choice's own message", async () => {
    const audio = renderHook(() => useSetAudioPreference(), { wrapper }).result;
    const subtitle = renderHook(() => useSetSubtitlePreference(), { wrapper }).result;
    mocks.v2.mockRejectedValue(new TypeError("Failed to fetch"));

    const audioText = await toastFor(() =>
      audio.current.mutateAsync({
        prefId: "series-1",
        trackIndex: 0,
        track: { index: 0, language: "eng", codec: "aac", channels: 2 } as never,
      }),
    );
    const subtitleText = await toastFor(() =>
      subtitle.current.mutateAsync({ prefId: "series-1", selection: null }),
    );
    expect(audioText).toBe("Failed to save audio preference");
    expect(subtitleText).toBe("Failed to save subtitle preference");
  });

  it("show a Home sections save's own message, and keep the server's reason", async () => {
    const row = {
      id: "continue_watching",
      section_type: "continue_watching",
      title: "Continue Watching",
      featured: false,
      item_limit: 20,
      hidden: false,
      is_custom: false,
      customized: false,
      position: 0,
    };
    const hideRowWhenSaveFails = async (error: unknown) => {
      mocks.v2.mockImplementation(async (route: string) => {
        if (route.startsWith("PUT ")) throw error;
        return { items: route.endsWith("/settings") ? [row] : [] };
      });
      document.body.replaceChildren();
      render(<ProfileHomeSections userId={1} profileId="p1" profileName="admin" />, { wrapper });
      const hide = await screen.findByRole("button", { name: "Hide Continue Watching" });
      await waitFor(() => expect(hide).toBeEnabled());
      return toastFor(async () => {
        fireEvent.click(hide);
        await waitFor(() => expect(mocks.toastError).toHaveBeenCalled());
      });
    };

    expect(await hideRowWhenSaveFails(new TypeError("Failed to fetch"))).toBe(
      "Couldn't save Home sections",
    );
    expect(
      await hideRowWhenSaveFails(v2Problem(422, "validation_failed", "Server refused the layout.")),
    ).toBe("Couldn't save Home sections: Server refused the layout.");
  });

  it("show the player's audio pick message, and keep the server's", async () => {
    const { result } = renderHook(
      () =>
        useTrackChoicePersistence({
          seriesId: "series-1",
          audioTracks: [{ index: 0, language: "eng", codec: "aac", channels: 2 } as never],
          subtitleTracks: [],
        }),
      { wrapper },
    );
    const pick = (error: unknown) => async () => {
      mocks.sendAudioChoiceRequest.mockRejectedValueOnce(error);
      result.current.saveAudioChoice(0);
      await vi.waitFor(() => expect(mocks.toastError).toHaveBeenCalled());
    };

    expect(await toastFor(pick(new TypeError("Failed to fetch")))).toBe(
      "Failed to save audio preference",
    );
    expect(await toastFor(pick(new PlayerFetchError(422, "That track isn't in this file.")))).toBe(
      "That track isn't in this file.",
    );
  });

  it("show one offline notice for a burst of saves made offline", async () => {
    const admin = renderHook(() => useUpdateAdminUserSetting(), { wrapper }).result;
    const audio = renderHook(() => useSetAudioPreference(), { wrapper }).result;
    mocks.toastError.mockClear();
    onlineManager.setOnline(false);
    try {
      await act(() =>
        Promise.all([
          admin.current
            .mutateAsync({
              userId: 1,
              key: SETTING_KEYS.NAV_SHORTCUTS,
              identity: { scope: "account" },
              value: "{}",
            })
            .catch(() => undefined),
          audio.current
            .mutateAsync({
              prefId: "series-1",
              trackIndex: 0,
              track: { index: 0, language: "eng", codec: "aac", channels: 2 } as never,
            })
            .catch(() => undefined),
        ]),
      );
    } finally {
      onlineManager.setOnline(true);
    }
    // Sonner shows toasts that share an id as one.
    const offline = [
      "You're offline, so this change wasn't saved",
      { id: "setting-write-offline" },
    ];
    expect(mocks.toastError.mock.calls).toEqual([offline, offline]);
    expect(mocks.setAdminSettingValue).not.toHaveBeenCalled();
    expect(mocks.v2).not.toHaveBeenCalled();
  });
});
