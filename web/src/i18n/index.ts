// The one message API (see README.md). Components use `useT()` / `useServerText()`;
// plain code uses `t()` / `serverText()`. Nothing else imports react-intl.
export type { MessageKey } from "@/locales/keys";
export {
  adminMessagesRequested,
  loadAdminEnglishForTests,
  loadAdminTranslations,
  loadTranslations,
  PSEUDO_LOCALES_ENABLED,
  requestAdminMessages,
  setAdminLoadError,
  subscribeAdminRequest,
  type Messages,
} from "./catalog";
export {
  appIntl,
  createAppIntl,
  englishText,
  messageNumberingSystem,
  numberText,
  serverText,
  setAppIntl,
  subscribeAppIntl,
  t,
  type RichValues,
  type TextValues,
} from "./format";
export { useAdminMessages, useAppLanguage, useServerText, useT, type Translate } from "./hooks";
export { AppIntlProvider } from "./provider";
