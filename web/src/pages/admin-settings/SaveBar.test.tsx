import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { SaveBar } from "./SaveBar";

function renderBar(props: Partial<Parameters<typeof SaveBar>[0]> = {}) {
  return render(
    <SaveBar dirtyCount={2} onSave={vi.fn()} onDiscard={vi.fn()} isSaving={false} {...props} />,
  );
}

describe("SaveBar", () => {
  it("does not pass the click event to a save callback that accepts selected keys", async () => {
    const onSave = vi.fn((selectedKeys?: string[]) =>
      selectedKeys?.includes("artwork.storage_backend"),
    );
    renderBar({ onSave });

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave.mock.calls).toEqual([[]]);
  });

  it("disables saving while a save is in flight", () => {
    renderBar({ isSaving: true });

    expect(screen.getByRole("button", { name: "Saving..." })).toBeDisabled();
  });
});
