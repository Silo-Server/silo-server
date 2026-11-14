import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DownloadsServerNotice } from "./DownloadsServerNotice";

const mocks = vi.hoisted(() => ({ settings: undefined as Record<string, string> | undefined }));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: mocks.settings }),
}));

function renderNotice() {
  return render(
    <MemoryRouter>
      <DownloadsServerNotice />
    </MemoryRouter>,
  );
}

describe("DownloadsServerNotice", () => {
  beforeEach(() => {
    mocks.settings = undefined;
  });

  it("stays hidden until the server settings are known", () => {
    const { container } = renderNotice();
    expect(container).toBeEmptyDOMElement();
  });

  it("says downloads are off server-wide and links to the Downloads settings", () => {
    mocks.settings = { "download.enabled": "false", "download.transcode_enabled": "false" };
    renderNotice();
    expect(screen.getByText(/turned off for the whole server/)).toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/admin/settings/downloads");
  });

  it("names conversion only when downloads themselves are on", () => {
    mocks.settings = { "download.enabled": "true", "download.transcode_enabled": "false" };
    renderNotice();
    expect(screen.getByText(/isn't preparing device-friendly copies/)).toBeInTheDocument();
  });

  it("stays hidden when both switches are on", () => {
    mocks.settings = { "download.enabled": "true", "download.transcode_enabled": "true" };
    const { container } = renderNotice();
    expect(container).toBeEmptyDOMElement();
  });
});
