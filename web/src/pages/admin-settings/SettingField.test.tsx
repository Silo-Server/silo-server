import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { SettingField } from "./SettingField";

vi.mock("@/components/ui/select", () => ({
  Select: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectItem: ({ children, disabled }: { children: ReactNode; disabled?: boolean }) => (
    <button disabled={disabled}>{children}</button>
  ),
  SelectTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectValue: () => null,
}));

describe("SettingField", () => {
  it("renders unavailable select options as disabled", () => {
    render(
      <SettingField
        label="User DB Backend"
        type="select"
        options={[
          { value: "postgres", label: "PostgreSQL" },
          { value: "sqlite", label: "SQLite (TBD)", disabled: true },
        ]}
        value="postgres"
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: "PostgreSQL" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "SQLite (TBD)" })).toBeDisabled();
  });
});
