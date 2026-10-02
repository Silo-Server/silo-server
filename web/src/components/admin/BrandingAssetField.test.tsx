// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/hooks/queries/admin/branding", () => ({
  useUploadBrandingAsset: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteBrandingAsset: () => ({ mutate: vi.fn(), isPending: false }),
}));

import { BrandingAssetField } from "./BrandingAssetField";
import { BRANDING_ASSET_SPECS } from "./brandingAssetSpecs";

describe("BrandingAssetField", () => {
  it("previews the bundled default, dimmed and captioned, when nothing is uploaded", () => {
    render(
      <BrandingAssetField
        label="Logo (wordmark)"
        kind="wordmark"
        currentUrl={null}
        accept="image/png"
        enabled
      />,
    );

    const preview = screen.getByAltText("Logo (wordmark) preview");
    expect(preview).toHaveAttribute("src", "/silo-wordmark-sidebar.png");
    expect(preview.className).toContain("opacity-40");
    expect(screen.getByText("Default")).toBeInTheDocument();
    // Nothing to remove while the default is what is being served.
    expect(
      screen.queryByRole("button", { name: "Remove Logo (wordmark)" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Upload/ })).toBeInTheDocument();
  });

  it("labels the empty login background as the theme gradient instead of an image", () => {
    render(
      <BrandingAssetField
        label="Login background"
        kind="login_bg"
        currentUrl={null}
        accept="image/png"
        enabled
      />,
    );

    expect(screen.getByText("Theme gradient")).toBeInTheDocument();
    expect(screen.queryByAltText("Login background preview")).not.toBeInTheDocument();
  });

  it("shows an uploaded asset undimmed, with a remove action", () => {
    render(
      <BrandingAssetField
        label="Favicon"
        kind="favicon"
        currentUrl="/api/v1/branding/assets/favicon?v=abc.png"
        accept="image/png"
        enabled
      />,
    );

    const preview = screen.getByAltText("Favicon preview");
    expect(preview).toHaveAttribute("src", "/api/v1/branding/assets/favicon?v=abc.png");
    expect(preview.className).not.toContain("opacity-40");
    expect(screen.queryByText("Default")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove Favicon" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Replace/ })).toBeInTheDocument();
  });

  it("states the pipeline's own dimensions and caps per slot", () => {
    render(
      <BrandingAssetField
        label="Logo (icon)"
        description="Shown in the collapsed sidebar."
        kind="mark"
        currentUrl={null}
        accept="image/png"
        enabled
      />,
    );

    expect(screen.getByText(BRANDING_ASSET_SPECS.mark.guidance)).toBeInTheDocument();
    expect(screen.getByText("Shown in the collapsed sidebar.")).toBeInTheDocument();
  });

  // The guidance is only useful if it keeps matching internal/branding/assets.go
  // and internal/imageutil/imageutil.go; these are the numbers to change on both
  // sides together.
  it("keeps the stored sizes and upload caps in step with the server pipeline", () => {
    expect(BRANDING_ASSET_SPECS.wordmark.storedPx).toBe(640);
    expect(BRANDING_ASSET_SPECS.mark.storedPx).toBe(512);
    expect(BRANDING_ASSET_SPECS.login_bg.storedPx).toBe(2560);
    // The favicon is stored byte-for-byte so .ico and .svg keep working.
    expect(BRANDING_ASSET_SPECS.favicon.storedPx).toBeNull();

    expect(BRANDING_ASSET_SPECS.wordmark.maxUploadBytes).toBe(8 << 20);
    expect(BRANDING_ASSET_SPECS.mark.maxUploadBytes).toBe(8 << 20);
    expect(BRANDING_ASSET_SPECS.favicon.maxUploadBytes).toBe(1 << 20);
    expect(BRANDING_ASSET_SPECS.login_bg.maxUploadBytes).toBe(12 << 20);

    for (const spec of Object.values(BRANDING_ASSET_SPECS)) {
      if (spec.storedPx !== null) {
        expect(spec.guidance).toContain(String(spec.storedPx));
      }
      expect(spec.guidance).toContain(`${spec.maxUploadBytes / (1024 * 1024)} MB`);
    }
  });
});
