// @vitest-environment jsdom

import { useState } from "react";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, Link, RouterProvider } from "react-router";
import { describe, expect, it } from "vitest";

import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";

import { UnsavedChangesGuard } from "./UnsavedChangesGuard";

// Radix marks the rest of the page inert while a modal dialog is open, which
// jsdom reports as pointer-events: none on everything below <body>.
const user = userEvent.setup({ pointerEventsCheck: 0 });

function DraftPage() {
  const [draft, setDraft] = useState("");
  useReportUnsavedChanges(draft !== "");

  return (
    <div>
      <h1>Draft page</h1>
      <label>
        Server name
        <input value={draft} onChange={(event) => setDraft(event.target.value)} />
      </label>
      <Link to="/other">Other page</Link>
      <Link to="/draft?tab=advanced">Same page, other tab</Link>
    </div>
  );
}

function renderGuard(initialEntries: string[] = ["/draft"]) {
  const router = createMemoryRouter(
    [
      {
        path: "/draft",
        element: (
          <>
            <UnsavedChangesGuard />
            <DraftPage />
          </>
        ),
      },
      { path: "/other", element: <h1>Other page</h1> },
    ],
    { initialEntries },
  );

  return { router, ...render(<RouterProvider router={router} />) };
}

describe("UnsavedChangesGuard", () => {
  it("lets a clean page navigate away without asking", async () => {
    renderGuard();

    await user.click(screen.getByRole("link", { name: "Other page" }));

    expect(screen.getByRole("heading", { name: "Other page" })).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("keeps the page and its edits when the prompt is cancelled", async () => {
    renderGuard();

    const field = screen.getByLabelText("Server name");
    await user.type(field, "Casa");
    await user.click(screen.getByRole("link", { name: "Other page" }));

    expect(
      await screen.findByRole("alertdialog", { name: "Discard unsaved changes?" }),
    ).toBeInTheDocument();
    // The page behind an open modal is aria-hidden, so it is only reachable by
    // text while the prompt is up.
    expect(screen.getByText("Draft page")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Draft page" })).toBeInTheDocument();
    expect(screen.getByLabelText("Server name")).toHaveValue("Casa");
  });

  it("navigates once the edits are discarded", async () => {
    renderGuard();

    await user.type(screen.getByLabelText("Server name"), "Casa");
    await user.click(screen.getByRole("link", { name: "Other page" }));
    await user.click(await screen.findByRole("button", { name: "Discard" }));

    expect(await screen.findByRole("heading", { name: "Other page" })).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("prompts on browser back as well as on in-app links", async () => {
    const { router } = renderGuard(["/other", "/draft"]);

    await user.type(screen.getByLabelText("Server name"), "Casa");
    await act(async () => {
      await router.navigate(-1);
    });

    expect(await screen.findByRole("alertdialog")).toBeInTheDocument();
    expect(screen.getByText("Draft page")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Discard" }));

    expect(await screen.findByRole("heading", { name: "Other page" })).toBeInTheDocument();
  });

  it("does not prompt when only the search string changes", async () => {
    renderGuard();

    await user.type(screen.getByLabelText("Server name"), "Casa");
    await user.click(screen.getByRole("link", { name: "Same page, other tab" }));

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Draft page" })).toBeInTheDocument();
  });

  it("stops guarding once the form goes clean", async () => {
    renderGuard();

    const field = screen.getByLabelText("Server name");
    await user.type(field, "Casa");
    await user.clear(field);
    await user.click(screen.getByRole("link", { name: "Other page" }));

    expect(screen.getByRole("heading", { name: "Other page" })).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });
});
