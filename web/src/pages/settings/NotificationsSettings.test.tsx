// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { NotificationPreference, PushDeviceInfo } from "@/api/types";

const mutateMock = vi.fn();
const enableMock = vi.fn();
const disableMock = vi.fn();
const toggleDeviceMutateMock = vi.fn();

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
  useNotificationPreferences: vi.fn(),
  useSetNotificationPreferences: vi.fn(),
  usePushDevice: vi.fn(),
  usePushDevices: vi.fn(),
  useTogglePushDevice: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: (...args: unknown[]) => mocks.useAuth(...args),
}));

vi.mock("@/hooks/queries/notifications", () => ({
  useNotificationPreferences: (...args: unknown[]) => mocks.useNotificationPreferences(...args),
  useSetNotificationPreferences: (...args: unknown[]) =>
    mocks.useSetNotificationPreferences(...args),
}));

vi.mock("@/hooks/usePushDevice", () => ({
  usePushDevice: (...args: unknown[]) => mocks.usePushDevice(...args),
}));

vi.mock("@/hooks/queries/push", () => ({
  usePushDevices: (...args: unknown[]) => mocks.usePushDevices(...args),
  useTogglePushDevice: (...args: unknown[]) => mocks.useTogglePushDevice(...args),
}));

import NotificationsSettings from "./NotificationsSettings";

const DEVICES: PushDeviceInfo[] = [
  {
    device_id: "dev-1",
    name: "Chrome on Mac",
    platform: "web",
    transport: "webpush",
    push_enabled: true,
  },
  {
    device_id: "dev-2",
    name: "Firefox",
    platform: "web",
    transport: "webpush",
    push_enabled: false,
  },
];

function makePreference(
  category: NotificationPreference["category"],
  enabled: boolean,
): NotificationPreference {
  return { category, enabled };
}

const ALL_PREFS: NotificationPreference[] = [
  makePreference("request", true),
  makePreference("content", true),
  makePreference("system", true),
  makePreference("admin", true),
  makePreference("content_digest", false),
];

describe("NotificationsSettings", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    mocks.useAuth.mockReset();
    mocks.useNotificationPreferences.mockReset();
    mocks.useSetNotificationPreferences.mockReset();
    mocks.usePushDevice.mockReset();
    mocks.usePushDevices.mockReset();
    mocks.useTogglePushDevice.mockReset();
    mutateMock.mockReset();
    enableMock.mockReset();
    disableMock.mockReset();
    toggleDeviceMutateMock.mockReset();

    mocks.useSetNotificationPreferences.mockReturnValue({
      isPending: false,
      mutate: mutateMock,
    });
    mocks.usePushDevice.mockReturnValue({
      status: "off",
      enable: enableMock,
      disable: disableMock,
      refresh: vi.fn(),
    });
    mocks.usePushDevices.mockReturnValue({ data: DEVICES, isLoading: false });
    mocks.useTogglePushDevice.mockReturnValue({ mutate: toggleDeviceMutateMock });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  async function render(ui: ReactNode) {
    await act(async () => {
      root.render(ui);
    });
  }

  it("renders 5 toggle rows for admin users", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useNotificationPreferences.mockReturnValue({
      data: ALL_PREFS,
      isLoading: false,
    });

    await render(<NotificationsSettings />);

    // Scope to the first SettingsGroup (category preferences); the second group
    // holds the push-on-this-device control and device list.
    const categoryGroup = container.querySelector("section")!;
    const switches = categoryGroup.querySelectorAll('[role="switch"]');
    expect(switches).toHaveLength(5);
    expect(container.textContent).toContain("Admin alerts");
  });

  it("renders 4 toggle rows for non-admin users (admin alerts hidden)", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({
      data: ALL_PREFS.filter((p) => p.category !== "admin"),
      isLoading: false,
    });

    await render(<NotificationsSettings />);

    const categoryGroup = container.querySelector("section")!;
    const switches = categoryGroup.querySelectorAll('[role="switch"]');
    expect(switches).toHaveLength(4);
    expect(container.textContent).not.toContain("Admin alerts");
  });

  it("calls setPreferences with [{category:'content',enabled:false}] when toggling off an enabled row", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({
      data: ALL_PREFS,
      isLoading: false,
    });

    await render(<NotificationsSettings />);

    // SettingRow renders a <Label htmlFor={id}> and a <Switch id={id}>.
    // Find the label containing "New content", get its htmlFor, then find the switch.
    const labels = Array.from(container.querySelectorAll("label"));
    const contentLabel = labels.find((l) => l.textContent?.trim() === "New content");
    expect(contentLabel).toBeDefined();

    const forId = contentLabel!.getAttribute("for");
    expect(forId).toBeTruthy();

    const contentSwitch = container.querySelector(`[id="${forId}"]`) as HTMLButtonElement | null;
    expect(contentSwitch).not.toBeNull();

    await act(async () => {
      contentSwitch!.click();
    });

    expect(mutateMock).toHaveBeenCalledWith({
      preferences: [{ category: "content", enabled: false }],
    });
  });

  it("reflects enabled=false for content_digest from server prefs", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({
      data: ALL_PREFS,
      isLoading: false,
    });

    await render(<NotificationsSettings />);

    const labels = Array.from(container.querySelectorAll("label"));
    const digestLabel = labels.find((l) => l.textContent?.trim() === "Daily digest");
    expect(digestLabel).toBeDefined();

    const forId = digestLabel!.getAttribute("for");
    expect(forId).toBeTruthy();

    const digestSwitch = container.querySelector(`[id="${forId}"]`) as HTMLButtonElement | null;
    expect(digestSwitch).not.toBeNull();
    // aria-checked="false" means the switch is off (enabled=false from mocked prefs)
    expect(digestSwitch!.getAttribute("aria-checked")).toBe("false");
  });

  it("renders all switches disabled while loading", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({
      data: undefined,
      isLoading: true,
    });

    await render(<NotificationsSettings />);

    // Scope to the category-preferences group; push switches are independent.
    const categoryGroup = container.querySelector("section")!;
    const switches = Array.from(
      categoryGroup.querySelectorAll('[role="switch"]'),
    ) as HTMLButtonElement[];
    expect(switches.length).toBeGreaterThan(0);
    for (const sw of switches) {
      expect(sw.disabled).toBe(true);
    }
  });

  it("shows the announcements footer note", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({ data: [], isLoading: false });

    await render(<NotificationsSettings />);

    expect(container.textContent).toContain(
      "Announcements from your server admin can’t be turned off.",
    );
  });

  it("calls enable() when the 'Push on this device' switch is toggled on", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({ data: ALL_PREFS, isLoading: false });

    await render(<NotificationsSettings />);

    const labels = Array.from(container.querySelectorAll("label"));
    const deviceLabel = labels.find((l) => l.textContent?.trim() === "Push on this device");
    expect(deviceLabel).toBeDefined();

    const forId = deviceLabel!.getAttribute("for");
    const deviceSwitch = container.querySelector(`[id="${forId}"]`) as HTMLButtonElement | null;
    expect(deviceSwitch).not.toBeNull();

    await act(async () => {
      deviceSwitch!.click();
    });

    expect(enableMock).toHaveBeenCalledTimes(1);
    expect(disableMock).not.toHaveBeenCalled();
  });

  it("calls useTogglePushDevice mutate with the device_id when a device switch is toggled", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({ data: ALL_PREFS, isLoading: false });

    await render(<NotificationsSettings />);

    const labels = Array.from(container.querySelectorAll("label"));
    const deviceLabel = labels.find((l) => l.textContent?.trim() === "Chrome on Mac");
    expect(deviceLabel).toBeDefined();

    const forId = deviceLabel!.getAttribute("for");
    const deviceSwitch = container.querySelector(`[id="${forId}"]`) as HTMLButtonElement | null;
    expect(deviceSwitch).not.toBeNull();

    await act(async () => {
      deviceSwitch!.click();
    });

    // dev-1 starts push_enabled=true, so toggling sends enabled=false
    expect(toggleDeviceMutateMock).toHaveBeenCalledWith({ deviceId: "dev-1", enabled: false });
  });

  it("renders helper text and no this-device switch when push is unsupported", async () => {
    mocks.useAuth.mockReturnValue({ user: { role: "user" } });
    mocks.useNotificationPreferences.mockReturnValue({ data: ALL_PREFS, isLoading: false });
    mocks.usePushDevice.mockReturnValue({
      status: "unsupported",
      enable: enableMock,
      disable: disableMock,
      refresh: vi.fn(),
    });

    await render(<NotificationsSettings />);

    expect(container.textContent).toContain("This browser doesn’t support push notifications.");

    const labels = Array.from(container.querySelectorAll("label"));
    const deviceLabel = labels.find((l) => l.textContent?.trim() === "Push on this device");
    const forId = deviceLabel!.getAttribute("for");
    expect(container.querySelector(`[id="${forId}"]`)).toBeNull();
  });
});
