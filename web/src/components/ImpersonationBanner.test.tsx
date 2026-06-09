import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import ImpersonationBanner from "./ImpersonationBanner";

describe("ImpersonationBanner", () => {
  it("renders the active impersonation message and end action", () => {
    const onEnd = vi.fn();

    const markup = renderToStaticMarkup(
      <ImpersonationBanner userName="target-user" impersonatorName="admin-user" onEnd={onEnd} />,
    );

    expect(markup).toContain("Viewing as");
    expect(markup).toContain("target-user");
    expect(markup).toContain("admin-user");
    expect(markup).toContain("End impersonation session");
  });

  it("stays pinned above the page content while scrolling", () => {
    const markup = renderToStaticMarkup(
      <ImpersonationBanner userName="target-user" impersonatorName="admin-user" onEnd={() => {}} />,
    );

    expect(markup).toContain("sticky top-0");
  });
});
