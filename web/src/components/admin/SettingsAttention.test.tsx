import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { SettingsHealthItem } from "@/hooks/admin/useSettingsHealth";
import { SettingsAttention } from "./SettingsAttention";

const email: SettingsHealthItem = {
  id: "email",
  label: "Email",
  state: "off",
  stateText: "Not set up",
  detail: "Invites and resets can't send",
  action: { label: "Set up", page: "email" },
};
const searchDown: SettingsHealthItem = {
  id: "search",
  label: "Search",
  state: "warn",
  stateText: "Meilisearch",
  detail: "Meilisearch not reachable",
  action: { label: "Fix", page: "search" },
};

function renderAttention(onDismiss?: (item: SettingsHealthItem) => void) {
  render(
    <MemoryRouter>
      <SettingsAttention items={[email, searchDown]} onDismiss={onDismiss} />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe("SettingsAttention", () => {
  it("offers to hide setup that was not done, but not a broken service", () => {
    const onDismiss = vi.fn();
    renderAttention(onDismiss);

    fireEvent.click(screen.getByRole("button", { name: "Hide Email notice" }));
    expect(onDismiss).toHaveBeenCalledWith(email);
    expect(screen.queryByRole("button", { name: "Hide Search notice" })).toBeNull();
    // The hide button sits beside the link, not inside it.
    expect(screen.getByRole("link", { name: /Email · Not set up/ })).toHaveAttribute(
      "href",
      "/admin/settings/email",
    );
  });

  it("has no hide buttons without a dismiss handler", () => {
    renderAttention();

    expect(screen.queryByRole("button")).toBeNull();
  });
});
