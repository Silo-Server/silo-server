import { createContext } from "react";

/**
 * The description of the admin settings page on screen, from the settings
 * index. The settings layout provides it so every page's header carries the
 * same sentence the sidebar search shows, without each page repeating it.
 */
export const SettingsPageDescriptionContext = createContext<string | undefined>(undefined);
