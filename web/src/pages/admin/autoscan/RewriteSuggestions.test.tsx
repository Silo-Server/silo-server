import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { RewriteSuggestions } from "./RewriteSuggestions";

const suggestions = {
  proposed: [{ from: "/synthetic/remote", to: "/synthetic/local", match_depth: 1 }],
  unmatched: [],
  ambiguous: [],
  covered: [],
};
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": status === 200 ? "application/json" : "application/problem+json" },
  });
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken(null);
  setProfileId("a");
  setProfileToken("proof-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function fixture() {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
  const onApply = vi.fn();
  const editor = (id: string, disabledReason: string | null = null) => (
    <QueryClientProvider client={client}>
      <RewriteSuggestions sourceId={id} disabledReason={disabledReason} onApply={onApply} />
    </QueryClientProvider>
  );
  return { client, onApply, editor };
}
it("previews the captured source without applying rewrites", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response(suggestions));
  vi.stubGlobal("fetch", fetchMock);
  const { editor, onApply } = fixture();
  render(editor("source-a"));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await screen.findByRole("button", { name: "Add selected" });
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/api/v2/admin/autoscan/sources/source-a/rewrite-suggestions",
  );
  expect(onApply).not.toHaveBeenCalled();
});
it("discards an old source preview after editor replacement", async () => {
  let finish!: (text: string) => void;
  const res = response({});
  res.text = () =>
    new Promise((resolve) => {
      finish = resolve;
    });
  const fetchMock = vi.fn().mockResolvedValue(res);
  vi.stubGlobal("fetch", fetchMock);
  const { editor } = fixture();
  const { rerender } = render(editor("source-a"));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  rerender(editor("source-b"));
  await act(async () => {
    finish(JSON.stringify(suggestions));
  });
  expect(screen.queryByRole("button", { name: "Add selected" })).not.toBeInTheDocument();
});
it("discards a preview decoded after same-profile PIN replacement", async () => {
  let finish!: (text: string) => void;
  const res = response({});
  res.text = () =>
    new Promise((resolve) => {
      finish = resolve;
    });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(res));
  const { editor } = fixture();
  render(editor("source-a"));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  act(() => setProfileToken("proof-b"));
  await act(async () => {
    finish(JSON.stringify(suggestions));
  });
  expect(screen.queryByRole("button", { name: "Add selected" })).not.toBeInTheDocument();
});
it("does not refresh or replay the explicit provider read after 401", async () => {
  setRefreshToken("synthetic-refresh");
  const fetchMock = vi.fn().mockImplementation(() =>
    Promise.resolve(
      response(
        {
          type: "https://silo.example/problems/authentication_required",
          title: "Authentication required",
          status: 401,
        },
        401,
      ),
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { editor, client } = fixture();
  render(editor("source-a"));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await waitFor(() => expect(client.getMutationCache().getAll()[0]?.state.status).toBe("error"));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "Add selected" })).not.toBeInTheDocument();
});

it("hands checked proposals to the draft without writing anything", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    response({
      ...suggestions,
      proposed: [
        ...suggestions.proposed,
        { from: "/synthetic/other", to: "/synthetic/other-local", match_depth: 2 },
      ],
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { editor, onApply } = fixture();
  render(editor("source-a"));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await screen.findByRole("button", { name: "Add selected" });
  fireEvent.click(screen.getByRole("checkbox", { name: /\/synthetic\/remote/ }));
  fireEvent.click(screen.getByRole("button", { name: "Add selected" }));
  expect(onApply).toHaveBeenCalledWith([
    { from: "/synthetic/other", to: "/synthetic/other-local" },
  ]);
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("region", { name: "Rewrite suggestions" })).not.toBeInTheDocument();
});

it("explains why syncing is unavailable and sends nothing", () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { editor } = fixture();
  render(editor("source-a", "Save the new server choice first, then sync from it."));
  expect(screen.getByRole("button", { name: "Sync from server" })).toBeDisabled();
  expect(
    screen.getByText("Save the new server choice first, then sync from it."),
  ).toBeInTheDocument();
  expect(fetchMock).not.toHaveBeenCalled();
});
