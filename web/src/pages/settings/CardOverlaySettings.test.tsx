import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import CardOverlaySettings from "./CardOverlaySettings";

const mocks = vi.hoisted(() => ({
  overlaysEnabled: true,
  setOverlaysEnabled: vi.fn(),
  setQuickActionsEnabled: vi.fn(),
}));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({
    prefs: null,
    setPrefs: vi.fn(),
    quickActionPreference: "favorites",
    setQuickActionMode: vi.fn(),
    quickActionsEnabled: false,
    setQuickActionsEnabled: mocks.setQuickActionsEnabled,
    overlaysEnabled: mocks.overlaysEnabled,
    setOverlaysEnabled: mocks.setOverlaysEnabled,
    isLoading: false,
  }),
}));

vi.mock("@/components/overlays/OverlayPreviewCard", () => ({
  OverlayPreviewCard: () => <div>Overlay preview</div>,
}));

vi.mock("@/components/ui/select", () => ({
  Select: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectItem: ({ children, value }: { children: ReactNode; value: string }) => (
    <div data-value={value}>{children}</div>
  ),
  SelectTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectValue: () => null,
}));

function switchMarkup(markup: string, label: string) {
  return markup.match(new RegExp(`<button[^>]*aria-label="${label}"[^>]*>`))?.[0];
}

describe("CardOverlaySettings", () => {
  beforeEach(() => {
    mocks.overlaysEnabled = true;
    mocks.setOverlaysEnabled.mockReset();
    mocks.setQuickActionsEnabled.mockReset();
  });

  it("places the profile quick-action override above the overlay preview", () => {
    const markup = renderToStaticMarkup(<CardOverlaySettings />);

    expect(markup).toContain("Override the server defaults for this profile.");
    expect(markup).toContain("Card quick actions");
    expect(markup).toContain("Both");
    expect(markup).toContain("Favorites only");
    expect(markup).toContain("Watch indicator only");
    expect(markup.indexOf("Card quick actions")).toBeLessThan(markup.indexOf("Overlay preview"));
    expect(markup).toContain('aria-label="Enable card quick actions"');
  });

  it("keeps the profile quick-action switch operable when the server default is off", () => {
    const markup = renderToStaticMarkup(<CardOverlaySettings />);

    expect(markup).not.toContain("Card quick actions have been disabled by your server");
    const quickActionsSwitch = switchMarkup(markup, "Enable card quick actions");
    expect(quickActionsSwitch).toBeDefined();
    // Tailwind `disabled:` variants live in the class list, so match the
    // rendered attribute rather than the bare word.
    expect(quickActionsSwitch).not.toContain('disabled=""');
    expect(quickActionsSwitch).not.toContain("data-disabled");
  });

  it("offers a profile overlay-badge switch above the badge configuration", () => {
    const markup = renderToStaticMarkup(<CardOverlaySettings />);

    expect(markup).toContain("Card overlay badges");
    expect(markup).toContain('aria-label="Enable card overlay badges"');
    expect(markup.indexOf("Card overlay badges")).toBeLessThan(markup.indexOf("Overlay preview"));
    expect(markup).not.toContain("pointer-events-none opacity-50");
  });

  it("dims the badge configuration but keeps its switch operable when overlays are off", () => {
    mocks.overlaysEnabled = false;
    const markup = renderToStaticMarkup(<CardOverlaySettings />);

    expect(markup).not.toContain("Card overlays have been disabled by your server");
    expect(markup).toContain("pointer-events-none opacity-50");
    const overlaysSwitch = switchMarkup(markup, "Enable card overlay badges");
    expect(overlaysSwitch).toBeDefined();
    expect(overlaysSwitch).not.toContain('disabled=""');
    expect(overlaysSwitch).not.toContain("data-disabled");
    // The switch sits outside the dimmed region.
    expect(markup.indexOf('aria-label="Enable card overlay badges"')).toBeLessThan(
      markup.indexOf("pointer-events-none opacity-50"),
    );
  });
});
