import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import FilterEasyMode from "./FilterEasyMode";

describe("FilterEasyMode", () => {
  it("renders quick-start templates and chip area", () => {
    render(<FilterEasyMode initialConfig={{ match: "all", groups: [] }} onChange={() => {}} />);
    expect(screen.getByText("Highly rated")).toBeInTheDocument();
    expect(screen.getByText(/Add filter/)).toBeInTheDocument();
  });

  it("adds a chip via the popover", async () => {
    const onChange = vi.fn();
    render(<FilterEasyMode initialConfig={{ match: "all", groups: [] }} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /Add filter/ }));
    await userEvent.selectOptions(screen.getByLabelText(/field/i), "genre");
    await userEvent.type(screen.getByLabelText(/value/i), "Drama");
    await userEvent.click(screen.getByRole("button", { name: /^Add$/ }));
    expect(onChange).toHaveBeenCalled();
    const lastCall = onChange.mock.calls[onChange.mock.calls.length - 1];
    expect(lastCall?.[0].groups[0].rules[0]).toEqual({
      field: "genre",
      op: "contains",
      value: "Drama",
    });
  });

  it("removes a chip", async () => {
    const onChange = vi.fn();
    render(
      <FilterEasyMode
        initialConfig={{
          match: "all",
          groups: [{ match: "all", rules: [{ field: "genre", op: "contains", value: "Drama" }] }],
        }}
        onChange={onChange}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /remove filter/i }));
    expect(onChange).toHaveBeenCalled();
  });
});
