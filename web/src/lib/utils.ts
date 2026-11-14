import { type ClassValue, clsx } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// tailwind-merge lets a later `px-4` replace an earlier `pl-2`, but keeps an
// earlier `ps-2`, which then wins in the CSS. The app uses logical utilities
// (eslint-rules/no-physical-direction.js), so give them the same overrides.
const twMerge = extendTailwindMerge({
  extend: {
    conflictingClassGroups: {
      px: ["ps", "pe"],
      mx: ["ms", "me"],
      "inset-x": ["start", "end"],
      "border-w-x": ["border-w-s", "border-w-e"],
      "border-color-x": ["border-color-s", "border-color-e"],
      "scroll-px": ["scroll-ps", "scroll-pe"],
      "scroll-mx": ["scroll-ms", "scroll-me"],
    },
  },
});

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
