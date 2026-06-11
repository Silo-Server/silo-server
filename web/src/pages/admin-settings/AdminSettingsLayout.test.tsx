import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

import AdminSettingsLayout from "./AdminSettingsLayout";

// The layout only needs the active tab's component to render; a loading form
// keeps every settings page on its skeleton state so no other hooks fire.
vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: () => ({ isLoading: true }),
}));

function renderLayout(search = "") {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[`/admin/settings${search}`]}>
      <AdminSettingsLayout />
    </MemoryRouter>,
  );
}

describe("AdminSettingsLayout", () => {
  it("renders the grouped navigation sections", () => {
    const markup = renderLayout();

    for (const group of ["Server", "Media", "Connections", "Data"]) {
      expect(markup).toContain(`>${group}<`);
    }
  });

  it("renders every settings tab", () => {
    const markup = renderLayout();

    for (const label of [
      "General",
      "Theming",
      "Card Overlays",
      "Scanner &amp; Matcher",
      "Intro Markers",
      "Subtitles",
      "Playback",
      "Downloads",
      "Watch Providers",
      "Integrations",
      "Notifications",
      "Compatibility Proxies",
      "Rate Limiting",
      "Database",
      "Storage",
      "Log Retention",
    ]) {
      expect(markup).toContain(label);
    }
  });

  it("defaults to the General tab", () => {
    const markup = renderLayout();

    expect(markup).toContain('aria-current="page"');
    expect(markup).toBe(renderLayout("?tab=general"));
  });

  it("resolves the legacy jellyfin tab alias to Compatibility Proxies", () => {
    const withAlias = renderLayout("?tab=jellyfin");
    const direct = renderLayout("?tab=compatibility-proxies");

    expect(withAlias).toBe(direct);
  });
});
