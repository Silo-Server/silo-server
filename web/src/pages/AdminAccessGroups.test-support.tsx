// Fixtures and helpers shared by the access group page's test files.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";

import AdminAccessGroups from "./AdminAccessGroups";

export async function pickOption(
  user: ReturnType<typeof userEvent.setup>,
  combobox: string,
  option: string,
) {
  await user.click(screen.getByRole("combobox", { name: combobox }));
  await user.click(await screen.findByRole("option", { name: option }));
}

export const GROUP = {
  id: "1",
  name: "Kids",
  description: "",
  library_ids: ["2"],
  max_playback_quality: "1080p",
  download_allowed: false,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 1,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  max_profiles: 5,
  allowed_permissions: [] as string[],
  requests_allowed: false,
  is_default: true,
  member_count: 3,
  created_at: "2026-07-02T12:00:00Z",
  updated_at: "2026-07-02T12:00:00Z",
};

export function renderPage(initialPath = "/admin/access-groups") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(
    [
      { path: "/admin", element: <p>Admin home</p> },
      { path: "/admin/access-groups", element: <AdminAccessGroups /> },
      { path: "/admin/access-groups/:id", element: <AdminAccessGroups /> },
    ],
    { initialEntries: ["/admin", initialPath], initialIndex: 1 },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

export const REQUEST_SETTINGS = {
  requests_enabled: true,
  global_max_requests: 12,
  global_window_days: 14,
  global_auto_approval_enabled: true,
  force_dual_quality: false,
};
export const INHERIT_LIMIT = {
  group_id: "1",
  limit_mode: "inherit",
  max_requests: null,
  window_days: null,
  approval_mode: "inherit",
};
