import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { PreviewImage } from "./PreviewImage";

it("contains image failures and recovers on a renewed URL", () => {
  const props = { alt: "GREEN", className: "h-12 w-20", placeholderClassName: "bg-white" };
  const { rerender } = render(<PreviewImage {...props} src="/green.webp?old" />);
  fireEvent.error(screen.getByRole("img", { name: "GREEN" }));
  expect(screen.queryByRole("img")).toBeNull();
  rerender(<PreviewImage {...props} src="/green.webp?old" />);
  expect(screen.queryByRole("img")).toBeNull();
  rerender(<PreviewImage {...props} src="/green.webp?renewed" />);
  expect(screen.getByRole("img", { name: "GREEN" }).getAttribute("src")).toBe(
    "/green.webp?renewed",
  );
});
