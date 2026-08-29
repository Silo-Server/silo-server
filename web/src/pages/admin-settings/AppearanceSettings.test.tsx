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

vi.mock("@/components/theme/TokenEditor", () => ({
  TokenEditor: ({ onSetVar }: { onSetVar: (token: "primary", value: string) => void }) => (
    <button type="button" onClick={() => onSetVar("primary", "#112233")}>
      Set primary token
    </button>
  ),
}));

vi.mock("@/components/theme/RawCssEditor", () => ({
  RawCssEditor: ({ value, onChange }: { value: string; onChange: (css: string) => void }) => (
    <textarea
      aria-label="Custom CSS editor"
      value={value}
      onChange={(event) => onChange(event.target.value)}
    />
  ),
}));

vi.mock("@/components/theme/ThemePreviewCard", () => ({
  ThemePreviewCard: () => null,
}));

vi.mock("@/components/overlays/OverlayPreviewCard", () => ({
  OverlayPreviewCard: ({ variant }: { variant?: string }) => (
    <div data-testid="overlay-preview">{variant}</div>
  ),
}));

import { buildDefaultPrefs, serializeOverlayPrefs } from "@/lib/overlays";

import AppearanceSettings from "./AppearanceSettings";

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

  it("renders every field group heading", () => {
    render(<AppearanceSettings />);

    for (const heading of ["Logos and icons", "Colors and theme", "Card overlays"]) {
      expect(screen.getByRole("group", { name: heading })).toBeInTheDocument();
    }
  });

  it("renders the tab title and nothing else in the header", () => {
    render(<AppearanceSettings />);

    expect(screen.getByRole("heading", { name: "Appearance" })).toBeInTheDocument();
    expect(screen.getByText("Default theme")).toBeInTheDocument();
    expect(screen.getByText("Accent color")).toBeInTheDocument();
  });

  it("stages the union of appearance keys and leaves identity to General", () => {
    render(<AppearanceSettings />);

    const keys = useSettingsFormMock.mock.calls[0]?.[0]?.keys as string[];
    expect(keys).toEqual(
      expect.arrayContaining([
        "branding.accent_color",
        "branding.default_theme",
        "ui.admin_theme_vars",
        "ui.admin_custom_css",
        "theme.catalog_url",
        "overlays.enabled",
        "defaults.card_overlays",
      ]),
    );
    expect(keys).not.toContain("branding.server_name");
    expect(keys).not.toContain("branding.login_subtitle");
  });

  it("stages the accent color and its theme tokens instead of saving immediately", () => {
    render(<AppearanceSettings />);

    fireEvent.click(screen.getByRole("button", { name: "Use accent #10b981" }));

    expect(form.save).not.toHaveBeenCalled();
    expect(form.setValue).toHaveBeenCalledWith("branding.accent_color", "#10b981");
    expect(form.setValue).toHaveBeenCalledWith(
      "ui.admin_theme_vars",
      JSON.stringify({ primary: "#10b981", ring: "#10b981", "sidebar-primary": "#10b981" }),
    );
  });

  it("keeps the token editor, custom CSS and theme list behind one advanced disclosure", () => {
    render(<AppearanceSettings />);

    expect(screen.queryByRole("button", { name: "Set primary token" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Advanced · 3 settings/ }));

    expect(screen.getByRole("button", { name: "Set primary token" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Custom CSS editor" })).toBeInTheDocument();
    expect(screen.getByLabelText("Community theme list")).toBeInTheDocument();
  });

  // Show-only overlays (network, show status) are invisible against the movie
  // sample, so the admin editing server defaults needs the same toggle the user
  // page has. It is view state: switching it stages nothing.
  it("previews the badge defaults against either a movie or a show sample", () => {
    render(<AppearanceSettings />);

    expect(screen.getByTestId("overlay-preview")).toHaveTextContent("movie");

    fireEvent.click(screen.getByRole("button", { name: "show" }));

    expect(screen.getByTestId("overlay-preview")).toHaveTextContent("show");
    expect(form.setValue).not.toHaveBeenCalled();
  });

  // Restoring is an ordinary staged edit: the admin still confirms the batch
  // through the SaveBar, and Discard puts the previous defaults back.
  it("stages the registry's built-in overlay document instead of saving it", () => {
    form = makeForm({ "defaults.card_overlays": customizedOverlayDefaults() });
    render(<AppearanceSettings />);

    fireEvent.click(screen.getByRole("button", { name: /Restore defaults/ }));
    fireEvent.click(screen.getByRole("button", { name: "Restore" }));

    expect(form.setValue).toHaveBeenCalledWith("defaults.card_overlays", BUILT_IN_OVERLAY_DEFAULTS);
    expect(form.save).not.toHaveBeenCalled();
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
    expect(form.setValue).not.toHaveBeenCalledWith("overlays.enabled", expect.anything());
  });

  it("offers nothing to restore while the defaults already match the registry", () => {
    form = makeForm({ "defaults.card_overlays": BUILT_IN_OVERLAY_DEFAULTS });
    render(<AppearanceSettings />);

    expect(screen.getByRole("button", { name: /Restore defaults/ })).toBeDisabled();
  });

  it("stages sanitized CSS while the editor keeps showing what was typed", () => {
    render(<AppearanceSettings />);

    fireEvent.click(screen.getByRole("button", { name: /Advanced · 3 settings/ }));
    const editor = screen.getByRole("textbox", { name: "Custom CSS editor" });
    fireEvent.change(editor, {
      target: { value: '@import "https://example.invalid/x.css"; .card { color: red; }' },
    });

    expect(form.save).not.toHaveBeenCalled();
    expect(form.setValue).toHaveBeenCalledWith(
      "ui.admin_custom_css",
      "/* [blocked @import] */ .card { color: red; }",
    );
    expect(editor).toHaveValue('@import "https://example.invalid/x.css"; .card { color: red; }');
  });
});
