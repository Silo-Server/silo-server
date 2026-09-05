import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getOverlayConfigOk from "../../../../contracts/api/v2/fixtures/get_overlay_config_ok.json";
import getPluginSettingsOk from "../../../../contracts/api/v2/fixtures/get_plugin_settings_ok.json";
import getSettingsContractCapabilitiesOk from "../../../../contracts/api/v2/fixtures/get_settings_contract_capabilities_ok.json";
import listEffectiveSettingsOk from "../../../../contracts/api/v2/fixtures/list_effective_settings_ok.json";
import listPluginSettingsOk from "../../../../contracts/api/v2/fixtures/list_plugin_settings_ok.json";
import updateNavigationShortcutOk from "../../../../contracts/api/v2/fixtures/update_navigation_shortcut_ok.json";
import updatePluginSettingsOk from "../../../../contracts/api/v2/fixtures/update_plugin_settings_ok.json";
import updateSettingValueOk from "../../../../contracts/api/v2/fixtures/update_setting_value_ok.json";
import updateSettingValueUnknownKey from "../../../../contracts/api/v2/fixtures/update_setting_value_unknown_key.json";

import {
  captureProfileRequestContext,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
} from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { storage } from "@/utils/storage";
import {
  usePluginSettingsDetail,
  usePluginSettingsList,
  useUpdatePluginSettings,
} from "./pluginSettings";
import {
  settingsCapabilitiesSupportAtomicShortcuts,
  useEffectiveSettings,
  useSetNavigationShortcutPresence,
  useSetSettingValue,
  useSettingsCapabilities,
} from "./settingValues";

const JSON_HEADERS = { "Content-Type": "application/json" };
const PROBLEM_HEADERS = { "Content-Type": "application/problem+json" };

function json(body: unknown, status = 200, headers: Record<string, string> = JSON_HEADERS) {
  return new Response(JSON.stringify(body), { status, headers });
}

interface SeenRequest {
  url: string;
  init: RequestInit & { headers: Record<string, string> };
}

/** Answers each v2 request from its committed fixture and records what was sent. */
function stubFetch() {
  const seen: SeenRequest[] = [];
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    seen.push({ url, init: init as SeenRequest["init"] });
    const method = init?.method ?? "GET";
    const path = url.split("?")[0]!;
    if (path === "/api/v2/settings/values/effective") return json(listEffectiveSettingsOk);
    if (path === "/api/v2/settings/contract/capabilities")
      return json(getSettingsContractCapabilitiesOk);
    if (path === "/api/v2/settings/overlay-config") return json(getOverlayConfigOk);
    if (path === "/api/v2/settings/plugins") return json(listPluginSettingsOk);
    if (path === "/api/v2/settings/plugins/3" && method === "GET") return json(getPluginSettingsOk);
    if (path === "/api/v2/settings/plugins/3" && method === "PUT")
      return json(updatePluginSettingsOk);
    if (path === "/api/v2/settings/values/nav.shortcuts/item")
      return json(updateNavigationShortcutOk);
    if (path === "/api/v2/settings/values/ui.theme" && method === "PUT")
      return json(updateSettingValueOk);
    if (path === "/api/v2/settings/values/not.a.key" && method === "PUT")
      return json(updateSettingValueUnknownKey, 422, PROBLEM_HEADERS);
    throw new Error(`unexpected ${method} ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return seen;
}

function wrapper({ children }: { children: ReactNode }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

describe("settings hooks against the committed v2 fixtures", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    setAccessToken("tok-user");
    setRefreshToken(null);
    setProfileId("p-owner");
    setProfileToken(null);
    storage.set(storage.KEYS.PROFILE_ID, "p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("resolves effective settings from the item list, one repeated keys parameter per key", async () => {
    const seen = stubFetch();
    const keys: SettingKey[] = [SETTING_KEYS.UI_THEME, SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY];
    const { result } = renderHook(
      () => useEffectiveSettings({ keys, libraryIds: [3, 7], seriesIds: ["tv:1"] }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const [fixtureTheme, fixtureQuality] = listEffectiveSettingsOk.items;
    expect(result.current.data?.[SETTING_KEYS.UI_THEME]).toMatchObject({
      key: fixtureTheme!.key,
      value: fixtureTheme!.value,
      source: fixtureTheme!.source,
      scope: fixtureTheme!.scope,
    });
    expect(result.current.data?.[SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]).toMatchObject({
      value: fixtureQuality!.value,
      source: "default",
    });

    const request = seen[0]!;
    const query = new URL(request.url, "http://silo.test").searchParams;
    expect(query.getAll("keys")).toEqual(keys);
    expect(query.getAll("library_ids")).toEqual(["3", "7"]);
    expect(query.getAll("series_ids")).toEqual(["tv:1"]);
    expect(query.has("device_id")).toBe(false);
    expect(request.init.headers).toMatchObject({
      Authorization: "Bearer tok-user",
      "X-Profile-Id": "p-owner",
      "X-Silo-Client-Family": "web",
    });
  });

  it("reads the contract capabilities the gate helpers consume", async () => {
    stubFetch();
    const { result } = renderHook(() => useSettingsCapabilities(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual(getSettingsContractCapabilitiesOk);
    expect(settingsCapabilitiesSupportAtomicShortcuts(result.current.data)).toBe(true);
  });

  it("writes a value at one scope and surfaces an unknown key as a validation problem", async () => {
    stubFetch();
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });

    await expect(
      result.current.mutateAsync({
        key: SETTING_KEYS.UI_THEME,
        value: "cinema-light",
        identity: { scope: "profile" },
      }),
    ).resolves.toEqual(updateSettingValueOk);

    const rejected = result.current
      .mutateAsync({
        key: "not.a.key" as SettingKey,
        value: "x",
        identity: { scope: "profile" },
      })
      .catch((error: unknown) => error);
    const error = await rejected;
    expect(error).toBeInstanceOf(V2ProblemError);
    expect((error as V2ProblemError).status).toBe(422);
    expect((error as V2ProblemError).problemType).toBe("validation_failed");
    expect((error as V2ProblemError).problem.errors?.[0]?.location).toBe(
      updateSettingValueUnknownKey.errors[0]!.location,
    );
  });

  it("sends an atomic shortcut edit with the captured profile authority", async () => {
    const seen = stubFetch();
    const { result } = renderHook(() => useSetNavigationShortcutPresence(), { wrapper });
    // Capture the profile authority the way a queued intent does, then move
    // the live session on: the request must still carry the captured pair.
    setProfileToken("pin-token");
    const profileAuth = captureProfileRequestContext()!;
    setProfileToken(null);
    setProfileId("p-other");

    const stored = await result.current.mutateAsync({
      item: { type: "library", library_id: 3, label: "Movies" },
      present: true,
      mutationId: "m-1",
      profileAuth,
      invalidateOnSettled: false,
    });

    expect(stored).toEqual(updateNavigationShortcutOk);
    const request = seen[0]!;
    expect(request.url).toBe("/api/v2/settings/values/nav.shortcuts/item");
    expect(request.init.method).toBe("PUT");
    expect(JSON.parse(request.init.body as string)).toEqual({
      item: { type: "library", library_id: 3, label: "Movies" },
      present: true,
    });
    expect(request.init.headers).toMatchObject({
      Authorization: "Bearer tok-user",
      "X-Profile-Id": "p-owner",
      "X-Profile-Token": "pin-token",
    });
  });

  it("lists, reads, and updates plugin settings with numeric installation ids", async () => {
    const seen = stubFetch();
    const list = renderHook(() => usePluginSettingsList(), { wrapper });
    await waitFor(() => expect(list.result.current.isSuccess).toBe(true));
    const installation = list.result.current.data?.installations[0];
    expect(installation?.id).toBe(Number(listPluginSettingsOk.items[0]!.id));
    expect(installation?.routes).toEqual(listPluginSettingsOk.items[0]!.routes);
    expect(installation?.category).toBe(listPluginSettingsOk.items[0]!.category);

    const detail = renderHook(() => usePluginSettingsDetail(3), { wrapper });
    await waitFor(() => expect(detail.result.current.isSuccess).toBe(true));
    expect(detail.result.current.data?.values).toEqual(getPluginSettingsOk.values);
    expect(detail.result.current.data?.installation.id).toBe(3);

    const update = renderHook(() => useUpdatePluginSettings(), { wrapper });
    const updated = await update.result.current.mutateAsync({
      id: 3,
      body: { values: { region: "eu" } },
    });
    expect(updated.values).toEqual(updatePluginSettingsOk.values);
    const put = seen.find((request) => request.init.method === "PUT")!;
    expect(put.url).toBe("/api/v2/settings/plugins/3");
    expect(JSON.parse(put.init.body as string)).toEqual({ values: { region: "eu" } });
  });
});
