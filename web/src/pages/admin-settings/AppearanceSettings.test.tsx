// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const useSettingsFormMock = vi.fn();
const brandingState = { storageAvailable: true };

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

vi.mock("@/hooks/useBranding", () => ({
  useBranding: () => ({
    storageAvailable: brandingState.storageAvailable,
    wordmarkUrl: null,
    markUrl: null,
    faviconUrl: null,
    loginBgUrl: null,
  }),
}));

vi.mock("@/components/admin/BrandingAssetField", () => ({
  BrandingAssetField: ({ label }: { label: string }) => <div>{label}</div>,
}));

vi.mock("@/components/overlays/OverlayPreviewCard", () => ({
  OverlayPreviewCard: ({ variant }: { variant?: string }) => (
    <div data-testid="overlay-preview">{variant}</div>
  ),
}));

import { buildDefaultPrefs, serializeOverlayPrefs } from "@/lib/overlays";

import AppearanceSettings from "./AppearanceSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

const BUILT_IN_OVERLAY_DEFAULTS = serializeOverlayPrefs(buildDefaultPrefs());

/** The built-in document with one badge flipped, so it is not already default. */
function customizedOverlayDefaults() {
  const prefs = buildDefaultPrefs();
  prefs.preset = "vibrant";
  prefs.items.resolution = {
    ...prefs.items.resolution,
    enabled: !prefs.items.resolution.enabled,
  };
  return serializeOverlayPrefs(prefs);
}

function makeForm(values: Record<string, string> = {}) {
  const staged: Record<string, string> = { ...values };
  return fakeSettingsForm(staged, {
    setValue: vi.fn((key: string, value: string) => {
      staged[key] = value;
    }),
  });
}

let form: ReturnType<typeof makeForm>;

describe("AppearanceSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    brandingState.storageAvailable = true;
    form = makeForm();
    useSettingsFormMock.mockReset();
    useSettingsFormMock.mockImplementation(() => form);
  });

  // Restoring is an ordinary staged edit: the admin still confirms the batch
  // through the SaveBar, and Discard puts the previous defaults back.

  it("stages the server-wide title art default, showing the built-in default while unset", () => {
    render(<AppearanceSettings />);

    const keys = useSettingsFormMock.mock.calls[0]?.[0]?.keys as string[];
    expect(keys).toContain("defaults.ui.title_art");

    const toggle = screen.getByRole("switch", { name: "Show title art" });
    expect(toggle).toBeChecked();

    fireEvent.click(toggle);

    expect(form.save).not.toHaveBeenCalled();
    expect(form.setValue).toHaveBeenCalledWith("defaults.ui.title_art", "false");
  });

  it("leaves the badge kill switch alone when restoring the defaults", () => {
    form = makeForm({
      "defaults.card_overlays": customizedOverlayDefaults(),
      "overlays.enabled": "false",
    });
    render(<AppearanceSettings />);

    fireEvent.click(screen.getByRole("button", { name: /Restore defaults/ }));
    fireEvent.click(screen.getByRole("button", { name: "Restore" }));

    expect(form.setValue).toHaveBeenCalledTimes(1);
    expect(form.setValue).toHaveBeenCalledWith("defaults.card_overlays", BUILT_IN_OVERLAY_DEFAULTS);
    expect(form.save).not.toHaveBeenCalled();
    expect(form.setValue).not.toHaveBeenCalledWith("overlays.enabled", expect.anything());
  });

  it("lists every badge in view and stages a badge edit", () => {
    render(<AppearanceSettings />);

    expect(screen.queryByRole("button", { name: /^Advanced/ })).not.toBeInTheDocument();
    const toggle = screen.getByRole("switch", { name: "Show Resolution" });
    const wasOn = toggle.getAttribute("aria-checked") === "true";
    fireEvent.click(toggle);

    expect(form.setValue).toHaveBeenCalledTimes(1);
    const [key, value] = form.setValue.mock.calls[0] as [string, string];
    expect(key).toBe("defaults.card_overlays");
    expect(JSON.parse(value).items.resolution.enabled).toBe(!wasOn);
  });
});
