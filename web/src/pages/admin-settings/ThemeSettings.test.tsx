// @vitest-environment jsdom

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  updateMutate: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({
    data: {
      "ui.admin_theme_vars": "{}",
      "ui.admin_custom_css": "",
      "theme.catalog_url": "https://themes.example.invalid/catalog.json",
    },
  }),
  useUpdateServerSetting: () => ({
    mutate: (...args: unknown[]) => mocks.updateMutate(...args),
  }),
}));

vi.mock("@/components/theme/TokenEditor", () => ({
  TokenEditor: ({ onSetVar }: { onSetVar: (token: "primary", value: string) => void }) => (
    <>
      <button type="button" onClick={() => onSetVar("primary", "#112233")}>
        Set primary
      </button>
      <button type="button" onClick={() => onSetVar("primary", "#445566")}>
        Set latest primary
      </button>
    </>
  ),
}));

vi.mock("@/components/theme/RawCssEditor", () => ({
  RawCssEditor: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => (
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

import ThemeSettings from "./ThemeSettings";

describe("ThemeSettings autosave", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.updateMutate.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("flushes the latest pending variables and CSS when navigating away", () => {
    const { unmount } = render(<ThemeSettings />);

    fireEvent.click(screen.getByRole("button", { name: "Set primary" }));
    fireEvent.click(screen.getByRole("button", { name: "Set latest primary" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Custom CSS editor" }), {
      target: { value: '@import "https://example.invalid/theme.css"; .card { color: red; }' },
    });

    expect(mocks.updateMutate).not.toHaveBeenCalled();

    unmount();

    expect(mocks.updateMutate).toHaveBeenCalledTimes(2);
    expect(mocks.updateMutate).toHaveBeenCalledWith({
      key: "ui.admin_theme_vars",
      value: JSON.stringify({ primary: "#445566" }),
    });
    expect(mocks.updateMutate).toHaveBeenCalledWith({
      key: "ui.admin_custom_css",
      value: "/* [blocked @import] */ .card { color: red; }",
    });
  });

  it("does not save completed debounces again during unmount", () => {
    const { unmount } = render(<ThemeSettings />);

    fireEvent.click(screen.getByRole("button", { name: "Set primary" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Custom CSS editor" }), {
      target: { value: ".card { color: red; }" },
    });

    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(mocks.updateMutate).toHaveBeenCalledTimes(2);

    unmount();

    expect(mocks.updateMutate).toHaveBeenCalledTimes(2);
  });
});
