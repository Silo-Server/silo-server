import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";

import { AdminNavTopicsProvider, useAdminNavTopics } from "./useAdminNavTopics";

// `place` is the active topic's id, or the page path outside every topic.
function Sidebar({ place }: { place: string }) {
  const topics = useAdminNavTopics(null, place);
  return (
    <button type="button" onClick={() => topics.toggle("integrations")}>
      {topics.isOpen("integrations") ? "open" : "closed"}
    </button>
  );
}

// The phone drawer remounts its sidebar each time it opens.
function Shell({ place, drawerOpen = true }: { place: string; drawerOpen?: boolean }) {
  return (
    <AdminNavTopicsProvider>{drawerOpen ? <Sidebar place={place} /> : null}</AdminNavTopicsProvider>
  );
}

it("keeps a choice when the drawer's sidebar remounts, and drops it on moving elsewhere", () => {
  const { rerender } = render(<Shell place="libraries" />);
  fireEvent.click(screen.getByRole("button"));

  rerender(<Shell place="libraries" drawerOpen={false} />);
  rerender(<Shell place="libraries" />);
  expect(screen.getByRole("button")).toHaveTextContent("open");

  rerender(<Shell place="server" />);
  expect(screen.getByRole("button")).toHaveTextContent("closed");
  rerender(<Shell place="libraries" />);
  expect(screen.getByRole("button")).toHaveTextContent("closed");

  // Two pages outside every topic, such as Dashboard and Activity.
  rerender(<Shell place="/admin" />);
  fireEvent.click(screen.getByRole("button"));
  rerender(<Shell place="/admin/activity" />);
  expect(screen.getByRole("button")).toHaveTextContent("closed");
});
