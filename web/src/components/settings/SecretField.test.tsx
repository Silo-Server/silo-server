import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { SecretField } from "@/components/settings/SecretField";

function Harness({
  configured,
  onChange,
  onKeep,
  onClear,
  cleared,
  disabled,
}: {
  configured: boolean;
  onChange?: (value: string) => void;
  onKeep?: () => void;
  onClear?: () => void;
  cleared?: boolean;
  disabled?: boolean;
}) {
  const [value, setValue] = useState("");
  return (
    <SecretField
      label="Secret key"
      value={value}
      configured={configured}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
      onKeep={onKeep}
      onClear={onClear}
      cleared={cleared}
      disabled={disabled}
    />
  );
}

/**
 * Models a settings-form parent: `null` is an untouched field, `""` is the
 * dirty empty value that a staged clear writes on save.
 */
function FormHarness() {
  const [staged, setStaged] = useState<string | null>(null);
  return (
    <SecretField
      label="Secret key"
      value={staged ?? ""}
      configured
      onChange={setStaged}
      onKeep={() => setStaged(null)}
      onClear={() => setStaged("")}
      cleared={staged === ""}
    />
  );
}

const CLEAR = { name: "Clear saved value" };
const KEEP = { name: "Keep saved value" };

describe("SecretField", () => {
  it("shows a password input when nothing is saved", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness configured={false} onChange={onChange} />);

    const input = screen.getByLabelText("Secret key");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAttribute("placeholder", "Not configured");
    await user.type(input, "abc");
    expect(onChange).toHaveBeenLastCalledWith("abc");
  });

  it("shows a saved secret as a masked, editable input", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness configured onChange={onChange} />);

    // No Replace step: the input is live and the mask stands in for the value.
    const input = screen.getByLabelText("Secret key");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    expect(
      screen.getByText("Type to replace the saved value; leave blank to keep it."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Replace/ })).not.toBeInTheDocument();

    await user.type(input, "new-secret");
    expect(onChange).toHaveBeenLastCalledWith("new-secret");
  });

  it("delegates emptying the input to onKeep so the parent's draft stays authoritative", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const onKeep = vi.fn();
    render(<Harness configured onChange={onChange} onKeep={onKeep} />);

    const input = screen.getByLabelText("Secret key");
    await user.type(input, "x");
    onChange.mockClear();

    // Deleting back to empty means "keep the saved secret" — the parent
    // reverts its draft instead of staging "" (which would clear on save).
    await user.clear(input);
    expect(onKeep).toHaveBeenCalled();
    expect(onChange).not.toHaveBeenCalledWith("");
  });

  it("clears its own draft when no onKeep is supplied", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness configured onChange={onChange} />);

    const input = screen.getByLabelText("Secret key");
    await user.type(input, "x");
    onChange.mockClear();

    await user.clear(input);
    expect(onChange).toHaveBeenCalledWith("");
  });

  it("does not treat an empty input as a keep while nothing is saved", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const onKeep = vi.fn();
    render(<Harness configured={false} onChange={onChange} onKeep={onKeep} />);

    const input = screen.getByLabelText("Secret key");
    await user.type(input, "x");
    await user.clear(input);
    expect(onKeep).not.toHaveBeenCalled();
    expect(onChange).toHaveBeenCalledWith("");
  });

  it("offers no clear action on a page that owns its own clear", () => {
    render(<Harness configured />);

    expect(screen.queryByRole("button", CLEAR)).not.toBeInTheDocument();
  });

  it("offers no clear action while nothing is saved", () => {
    render(<Harness configured={false} onClear={vi.fn()} />);

    expect(screen.queryByRole("button", CLEAR)).not.toBeInTheDocument();
  });

  it("offers no clear action while the field is read-only", () => {
    render(<Harness configured onClear={vi.fn()} disabled />);

    expect(screen.queryByRole("button", CLEAR)).not.toBeInTheDocument();
  });

  it("stages a clear from an action that carries a border at rest", async () => {
    const user = userEvent.setup();
    const onClear = vi.fn();
    render(<Harness configured onClear={onClear} />);

    const action = screen.getByRole("button", CLEAR);
    // Never `ghost`: an action that only appears on hover is invisible to the
    // admins who need it.
    expect(action).toHaveAttribute("data-variant", "outline");
    await user.click(action);
    expect(onClear).toHaveBeenCalled();
  });

  it("says a staged clear will be saved, and offers to keep the value instead", async () => {
    const user = userEvent.setup();
    render(<FormHarness />);

    await user.click(screen.getByRole("button", CLEAR));

    const input = screen.getByLabelText("Secret key");
    expect(input).toHaveAttribute("placeholder", "Will be cleared on save");
    expect(
      screen.getByText("Save clears the stored value; type to set a new one instead."),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", KEEP));
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    expect(screen.getByRole("button", CLEAR)).toBeInTheDocument();
  });

  it("stages a replacement when the admin types over a staged clear", async () => {
    const user = userEvent.setup();
    render(<FormHarness />);

    await user.click(screen.getByRole("button", CLEAR));
    const input = screen.getByLabelText("Secret key");
    await user.type(input, "replacement");

    // A typed value is neither a keep nor a clear, so the action steps aside.
    expect(input).toHaveValue("replacement");
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    expect(screen.queryByRole("button", CLEAR)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", KEEP)).not.toBeInTheDocument();
  });
});
