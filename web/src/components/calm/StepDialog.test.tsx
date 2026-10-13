import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { StepCount, StepDialog } from "./StepDialog";

function Harness({ onClose = vi.fn() }: { onClose?: () => void }) {
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<"pick" | "form">("pick");
  const heading = useRef<HTMLHeadingElement>(null);
  const search = useRef<HTMLInputElement>(null);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        New collection
      </button>
      {open ? (
        <StepDialog
          size={step === "pick" ? "picker" : "form"}
          onClose={() => {
            onClose();
            setOpen(false);
          }}
          onOpenFocus={() => (step === "pick" ? search : heading).current?.focus()}
          back={step === "form" ? { label: "All types", onClick: () => setStep("pick") } : null}
          title={step === "pick" ? "New collection" : "Manual"}
          titleRef={heading}
          description="Pick a type."
          header={<input ref={search} aria-label="Search types" />}
          footerStart={<StepCount step={step === "pick" ? 1 : 2}>Goes under No heading</StepCount>}
          actions={step === "form" ? <button type="button">Create</button> : null}
        >
          <button type="button" onClick={() => setStep("form")}>
            Manual
          </button>
        </StepDialog>
      ) : null}
    </>
  );
}

afterEach(cleanup);

describe("StepDialog", () => {
  it("closes on Escape and gives focus back to what opened it", async () => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    const opener = screen.getByRole("button", { name: "New collection" });
    await userEvent.click(opener);
    const dialog = await screen.findByRole("dialog", { name: "New collection" });
    expect(dialog).toHaveAccessibleDescription("Pick a type.");
    expect(screen.getByRole("textbox", { name: "Search types" })).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("is a bottom sheet below 1024px, and the form step fills the screen", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "New collection" }));
    const picker = await screen.findByRole("dialog", { name: "New collection" });
    // jsdom can't lay out, so the classes that do the work are read instead.
    expect(picker).toHaveClass("max-lg:bottom-0", "max-lg:w-full", "max-lg:rounded-b-none");
    expect(picker).toHaveClass("lg:w-[min(1000px,calc(100vw-3rem))]");
    expect(picker).toHaveTextContent("Step 1 of 2");

    await userEvent.click(screen.getByRole("button", { name: "Manual" }));
    const form = screen.getByRole("dialog", { name: "Manual" });
    expect(form).toHaveClass("max-lg:bottom-0", "max-lg:h-dvh", "max-lg:rounded-none");
    expect(form).toHaveClass("lg:w-[min(880px,calc(100vw-3rem))]");
    expect(form).toHaveTextContent("Step 2 of 2");
    expect(screen.getByRole("button", { name: "Create" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "All types" }));
    expect(screen.getByRole("dialog", { name: "New collection" })).toBeInTheDocument();
  });

  it("closes from Cancel and the close button", async () => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    await userEvent.click(screen.getByRole("button", { name: "New collection" }));
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "New collection" }));
    await userEvent.click(await screen.findByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("returns focus to the page when the dialog that opened it has gone", async () => {
    // A link in one dialog swaps it for the next in one render (the picker's
    // "Add a starter pack"), so the element that opened the second is removed.
    function Swap() {
      const [open, setOpen] = useState<"none" | "first" | "second">("none");
      const close = () => setOpen("none");
      const packsTitle = useRef<HTMLHeadingElement>(null);
      return (
        <>
          <button type="button" onClick={() => setOpen("first")}>
            New collection
          </button>
          {open === "first" ? (
            <StepDialog
              size="choice"
              onClose={close}
              onOpenFocus={vi.fn()}
              title="New collection"
              description="Pick a type."
            >
              <button type="button" onClick={() => setOpen("second")}>
                Add a starter pack
              </button>
            </StepDialog>
          ) : null}
          {open === "second" ? (
            <StepDialog
              size="picker"
              onClose={close}
              onOpenFocus={() => packsTitle.current?.focus()}
              title="Starter packs"
              titleRef={packsTitle}
              description="Pick a pack."
            >
              <p>Packs</p>
            </StepDialog>
          ) : null}
        </>
      );
    }
    render(<Swap />);
    const opener = screen.getByRole("button", { name: "New collection" });
    await userEvent.click(opener);
    await userEvent.click(await screen.findByRole("button", { name: "Add a starter pack" }));
    await screen.findByRole("dialog", { name: "Starter packs" });
    expect(screen.getByRole("heading", { name: "Starter packs" })).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });
});
