import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";

import { AdminNavTopicsProvider, useAdminNavTopics } from "./useAdminNavTopics";

function Sidebar({ topic }: { topic: string }) {
  const topics = useAdminNavTopics(topic);
  return (
    <button type="button" onClick={() => topics.toggle("integrations")}>
      {topics.isOpen("integrations") ? "open" : "closed"}
    </button>
  );
}

// The phone drawer remounts its sidebar each time it opens.
function Shell({ topic, drawerOpen }: { topic: string; drawerOpen: boolean }) {
  return (
    <AdminNavTopicsProvider>{drawerOpen ? <Sidebar topic={topic} /> : null}</AdminNavTopicsProvider>
  );
}

it("keeps a choice when the drawer's sidebar remounts, and drops it on arriving in another topic", () => {
  const { rerender } = render(<Shell topic="libraries" drawerOpen />);
  fireEvent.click(screen.getByRole("button"));

  rerender(<Shell topic="libraries" drawerOpen={false} />);
  rerender(<Shell topic="libraries" drawerOpen />);
  expect(screen.getByRole("button")).toHaveTextContent("open");

  rerender(<Shell topic="server" drawerOpen />);
  expect(screen.getByRole("button")).toHaveTextContent("closed");
  rerender(<Shell topic="libraries" drawerOpen />);
  expect(screen.getByRole("button")).toHaveTextContent("closed");
});
