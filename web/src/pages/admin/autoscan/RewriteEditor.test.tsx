import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { RewriteEditor } from "./SourcesPanel";

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
  const onSave = vi.fn();
  const onChange = vi.fn();
  const editor = (id: string) => (
    <QueryClientProvider client={client}>
      <RewriteEditor
        sourceId={id}
        hasConnection
        rewrites={[]}
        onChange={onChange}
        onSave={onSave}
        isSaving={false}
      />
    </QueryClientProvider>
  );
  return { client, onSave, onChange, editor };
}
it("previews the captured source without applying rewrites", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response(suggestions));
  vi.stubGlobal("fetch", fetchMock);
  const { editor, onSave, onChange } = fixture();
  render(editor("source-a"));
  fireEvent.click(screen.getByRole("button", { name: "Path rewrites" }));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await screen.findByRole("button", { name: "Apply selected" });
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/api/v2/admin/autoscan/sources/source-a/rewrite-suggestions",
  );
  expect(onSave).not.toHaveBeenCalled();
  expect(onChange).not.toHaveBeenCalled();
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
  fireEvent.click(screen.getByRole("button", { name: "Path rewrites" }));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  rerender(editor("source-b"));
  await act(async () => {
    finish(JSON.stringify(suggestions));
  });
  expect(screen.queryByRole("button", { name: "Apply selected" })).not.toBeInTheDocument();
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
  fireEvent.click(screen.getByRole("button", { name: "Path rewrites" }));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  act(() => setProfileToken("proof-b"));
  await act(async () => {
    finish(JSON.stringify(suggestions));
  });
  expect(screen.queryByRole("button", { name: "Apply selected" })).not.toBeInTheDocument();
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
  fireEvent.click(screen.getByRole("button", { name: "Path rewrites" }));
  fireEvent.click(screen.getByRole("button", { name: "Sync from server" }));
  await waitFor(() => expect(client.getMutationCache().getAll()[0]?.state.status).toBe("error"));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "Apply selected" })).not.toBeInTheDocument();
});
