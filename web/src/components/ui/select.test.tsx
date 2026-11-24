import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./select";

function FormSelect({
  value,
  options,
  onValueChange,
}: {
  value: string;
  options: string[];
  onValueChange: (value: string) => void;
}) {
  return (
    <form>
      <Select value={value} onValueChange={onValueChange}>
        <SelectTrigger aria-label="Choice">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem key={option} value={option}>
              {option}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </form>
  );
}

describe("Select", () => {
  // Inside a form, Radix mirrors the value into a hidden native select. A new
  // value arriving with its own new option (a provider just created and
  // chosen) reaches that select before the option does, and the select
  // reported "" back, undoing the choice.
  it("keeps a value that arrives together with its option", () => {
    const onValueChange = vi.fn();
    const { rerender } = render(
      <FormSelect value="a" options={["a"]} onValueChange={onValueChange} />,
    );
    rerender(<FormSelect value="b" options={["a", "b"]} onValueChange={onValueChange} />);
    expect(onValueChange).not.toHaveBeenCalled();
  });
});
