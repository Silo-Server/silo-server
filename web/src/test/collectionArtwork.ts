import { fireEvent, screen, within } from "@testing-library/react";

type Slot = "poster" | "backdrop";

const LABEL: Record<Slot, string> = { poster: "Poster", backdrop: "Backdrop" };

/** Opens the editor's Look card if it is closed, and returns the tile for `slot`. */
export async function artworkTile(slot: Slot) {
  const look = await screen.findByRole("region", { name: "Look" });
  const toggle = within(look).queryByRole("button", { name: "Look" });
  if (toggle) fireEvent.click(toggle);
  return within(look).findByRole("group", { name: LABEL[slot] });
}

/** Stages a file in `slot`, as Upload image… does once the file is picked. */
export async function uploadArtwork(
  slot: Slot,
  file = new File(["image"], `${slot}.png`, { type: "image/png" }),
) {
  const tile = await artworkTile(slot);
  fireEvent.change(within(tile).getByLabelText(`Upload ${slot}`), { target: { files: [file] } });
}

/** Opens the pencil menu on `slot`'s tile. */
export async function openArtworkMenu(slot: Slot) {
  const tile = await artworkTile(slot);
  const pencil = within(tile).getByRole("button", { name: `Change ${slot}` });
  fireEvent.pointerDown(pencil, { button: 0, ctrlKey: false, pointerType: "mouse" });
  return screen.findByRole("menu");
}

/** Picks `item` ("Use the collage", "Remove backdrop", …) from `slot`'s pencil menu. */
export async function chooseArtwork(slot: Slot, item: string) {
  const menu = await openArtworkMenu(slot);
  fireEvent.click(within(menu).getByRole("menuitem", { name: item }));
}

/** Stages a link in `slot` through Paste a link… and Use link. */
export async function pasteArtworkLink(slot: Slot, url: string) {
  await chooseArtwork(slot, "Paste a link…");
  const look = screen.getByRole("region", { name: "Look" });
  fireEvent.change(within(look).getByRole("textbox", { name: `${LABEL[slot]} image link` }), {
    target: { value: url },
  });
  fireEvent.click(within(look).getByRole("button", { name: "Use link" }));
}
