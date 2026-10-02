import { describe, expect, it } from "vitest";

import { CARD_QUICK_ACTION_MODES } from "@/lib/cardQuickActions";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

// The mode literals exist only because the generated definition types its
// members as unknown, which cannot produce the literal union the card props
// need. This assertion is what keeps the hand-written copy honest; the option
// labels derive from the contract directly.
describe("card quick action modes", () => {
  it("lists exactly the contract's enum members, in manifest order", () => {
    const members = SETTING_DEFINITIONS[SETTING_KEYS.UI_CARD_QUICK_ACTIONS].values ?? [];
    expect(CARD_QUICK_ACTION_MODES).toEqual(members.map((member) => member.value));
  });
});
