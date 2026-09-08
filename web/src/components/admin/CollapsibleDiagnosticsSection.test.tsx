import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { CollapsibleDiagnosticsSection } from "./CollapsibleDiagnosticsSection";

describe("CollapsibleDiagnosticsSection", () => {
  it("renders collapsed with title, description, and count", () => {
    render(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={5}
        icon={<span>Icon</span>}
        open={false}
        onOpenChange={vi.fn()}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    expect(screen.getByText("Test Diagnostics")).toBeInTheDocument();
    expect(screen.getByText("A test section description")).toBeInTheDocument();
    expect(screen.getByText("5")).toBeInTheDocument();
    expect(screen.queryByText("Child content")).not.toBeInTheDocument();
  });

  it("renders an accessible loading indicator when isLoading is true in both states", () => {
    const { rerender } = render(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={0}
        isLoading={true}
        icon={<span>Icon</span>}
        open={false}
        onOpenChange={vi.fn()}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    expect(screen.getByText("Loading count")).toBeInTheDocument();
    expect(screen.getByText("—")).toHaveAttribute("aria-hidden", "true");

    rerender(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={0}
        isLoading={true}
        icon={<span>Icon</span>}
        open={true}
        onOpenChange={vi.fn()}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    expect(screen.getByText("Loading count")).toBeInTheDocument();
    expect(screen.getByText("—")).toHaveAttribute("aria-hidden", "true");
  });

  it("applies neutral text styling to zero count", () => {
    render(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={0}
        icon={<span>Icon</span>}
        open={false}
        onOpenChange={vi.fn()}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    const countElem = screen.getByText("0");
    expect(countElem.className).toContain("text-muted-foreground");
  });

  it("renders an accessible error indicator when isError is true in both states", () => {
    const { rerender } = render(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={0}
        isError={true}
        icon={<span>Icon</span>}
        open={false}
        onOpenChange={vi.fn()}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    expect(screen.getByText("Error loading count")).toBeInTheDocument();
    expect(screen.getByText("!")).toHaveAttribute("aria-hidden", "true");

    rerender(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={0}
        isError={true}
        icon={<span>Icon</span>}
        open={true}
        onOpenChange={vi.fn()}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    expect(screen.getByText("Error loading count")).toBeInTheDocument();
    expect(screen.getByText("!")).toHaveAttribute("aria-hidden", "true");
  });

  it("toggles and renders child content when open", async () => {
    const onOpenChange = vi.fn();
    const { rerender } = render(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={3}
        icon={<span>Icon</span>}
        open={false}
        onOpenChange={onOpenChange}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    await userEvent.click(screen.getByRole("button"));
    expect(onOpenChange).toHaveBeenCalledWith(true);

    rerender(
      <CollapsibleDiagnosticsSection
        title="Test Diagnostics"
        description="A test section description"
        count={3}
        icon={<span>Icon</span>}
        open={true}
        onOpenChange={onOpenChange}
      >
        <div>Child content</div>
      </CollapsibleDiagnosticsSection>,
    );

    expect(screen.getByText("Child content")).toBeInTheDocument();
  });
});
