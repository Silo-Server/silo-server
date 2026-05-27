import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: (...args: unknown[]) => mocks.useAuth(...args),
}));

import SettingsLayout from "./SettingsLayout";

describe("SettingsLayout", () => {
  beforeEach(() => {
    mocks.useAuth.mockReset();
    mocks.useAuth.mockReturnValue({
      user: { role: "admin" },
    });
  });

  it("includes a PageBack control at the top of the page", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain('aria-label="Go back"');
  });

  it("does not include a plugins section in personal settings", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("/settings/plugins");
    expect(markup).not.toContain(">Plugins<");
  });

  it("includes the profiles section in personal settings", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/profiles"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain("/settings/profiles");
    expect(markup).toContain(">Profiles<");
  });

  it("includes the Webhook Sync section in personal settings", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/webhook-sync"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain("/settings/webhook-sync");
    expect(markup).toContain(">Webhook Sync<");
  });

  it("hides the profiles section for non-admin users without a primary profile", () => {
    mocks.useAuth.mockReturnValue({
      user: { role: "user" },
      profile: { is_primary: false },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("/settings/profiles");
    expect(markup).not.toContain(">Profiles<");
  });

  it("shows the profiles section for non-admin users on their primary profile", () => {
    mocks.useAuth.mockReturnValue({
      user: { role: "user" },
      profile: { is_primary: true },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/profiles"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain("/settings/profiles");
    expect(markup).toContain(">Profiles<");
  });
});
