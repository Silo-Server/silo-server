// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

vi.mock("@/hooks/useBranding", () => ({
  useBranding: () => ({
    storageAvailable: true,
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
  return {
    isLoading: false,
    getValue: (key: string) => staged[key] ?? "",
    setValue: vi.fn((key: string, value: string) => {
      staged[key] = value;
    }),
    isDirty: () => false,
    dirtyCount: 0,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
  };
}

let form: ReturnType<typeof makeForm>;

describe("AppearanceSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    form = makeForm();
    useSettingsFormMock.mockReset();
    useSettingsFormMock.mockImplementation(() => form);
  });

  // The accent is the only color an admin picks: it is staged as the one
  // branding key, with no token overrides or custom CSS beside it.
  it("stages the accent color alone and offers no other theme controls", () => {
    render(<AppearanceSettings />);

    const keys = useSettingsFormMock.mock.calls[0]?.[0]?.keys as string[];
    expect(keys).toEqual(["branding.accent_color", "overlays.enabled", "defaults.card_overlays"]);

    fireEvent.click(screen.getByRole("button", { name: "Use accent #10b981" }));

    expect(form.save).not.toHaveBeenCalled();
    expect(form.setValue).toHaveBeenCalledTimes(1);
    expect(form.setValue).toHaveBeenCalledWith("branding.accent_color", "#10b981");

    expect(screen.queryByText("Custom CSS")).toBeNull();
    expect(screen.queryByText("Individual colors and fonts")).toBeNull();
    expect(screen.queryByRole("button", { name: /Reset to Cinema Dark/ })).toBeNull();
  });

  it("clears the accent with the default swatch", () => {
    form = makeForm({ "branding.accent_color": "#10b981" });
    render(<AppearanceSettings />);

    fireEvent.click(screen.getByRole("button", { name: "Use theme default accent" }));

    expect(form.setValue).toHaveBeenCalledWith("branding.accent_color", "");
    expect(form.save).not.toHaveBeenCalled();
  });

  // Restoring is an ordinary staged edit: the admin still confirms the batch
  // through the SaveBar, and Discard puts the previous defaults back.

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
});
