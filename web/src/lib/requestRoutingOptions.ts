import { t, type MessageKey, type TextValues } from "@/i18n";

/**
 * Choices for request routing conditions that TMDB describes with standard
 * codes: a title's original language (ISO 639-1) and its origin countries
 * (ISO 3166-1). These are the common ones, not the full standards; a rule
 * saved through the API with another code still shows it by code.
 */
export interface CodeOption {
  code: string;
  nameKey: MessageKey;
  /** The name in the interface language, read when used. */
  readonly name: string;
}

/** Formats a message: `t` for a name shown, `englishText` for a name the client saves. */
type Text = (id: MessageKey, values?: TextValues) => string;

function option(code: string, nameKey: MessageKey): CodeOption {
  return {
    code,
    nameKey,
    get name() {
      return t(nameKey);
    },
  };
}

export const ROUTING_LANGUAGES: readonly CodeOption[] = [
  option("en", "language.en"),
  option("ja", "language.ja"),
  option("ko", "language.ko"),
  option("zh", "language.zh"),
  // TMDB's own code for Cantonese; it is not ISO 639-1, but it is what TMDB
  // reports as the original language of most Hong Kong films.
  option("cn", "web.admin.common.requests.language.cn"),
  option("fr", "language.fr"),
  option("de", "language.de"),
  option("es", "language.es"),
  option("it", "language.it"),
  option("pt", "language.pt"),
  option("ru", "language.ru"),
  option("hi", "language.hi"),
  option("ta", "language.ta"),
  option("te", "language.te"),
  option("ml", "web.admin.common.requests.language.ml"),
  option("th", "language.th"),
  option("id", "language.id"),
  option("tl", "web.admin.common.requests.language.tl"),
  option("vi", "language.vi"),
  option("tr", "language.tr"),
  option("ar", "language.ar"),
  option("he", "language.he"),
  option("fa", "language.fa"),
  option("nl", "language.nl"),
  option("sv", "language.sv"),
  option("da", "language.da"),
  option("no", "language.no"),
  option("fi", "language.fi"),
  option("is", "language.is"),
  option("pl", "language.pl"),
  option("cs", "language.cs"),
  option("hu", "language.hu"),
  option("ro", "language.ro"),
  option("el", "language.el"),
  option("uk", "language.uk"),
];

export const ROUTING_COUNTRIES: readonly CodeOption[] = [
  option("US", "web.admin.common.requests.country.us"),
  option("GB", "web.admin.common.requests.country.gb"),
  option("CA", "web.admin.common.requests.country.ca"),
  option("AU", "web.admin.common.requests.country.au"),
  option("NZ", "web.admin.common.requests.country.nz"),
  option("IE", "web.admin.common.requests.country.ie"),
  option("JP", "web.admin.common.requests.country.jp"),
  option("KR", "web.admin.common.requests.country.kr"),
  option("CN", "web.admin.common.requests.country.cn"),
  option("HK", "web.admin.common.requests.country.hk"),
  option("TW", "web.admin.common.requests.country.tw"),
  option("IN", "web.admin.common.requests.country.in"),
  option("TH", "web.admin.common.requests.country.th"),
  option("ID", "web.admin.common.requests.country.id"),
  option("PH", "web.admin.common.requests.country.ph"),
  option("FR", "web.admin.common.requests.country.fr"),
  option("DE", "web.admin.common.requests.country.de"),
  option("ES", "web.admin.common.requests.country.es"),
  option("IT", "web.admin.common.requests.country.it"),
  option("PT", "web.admin.common.requests.country.pt"),
  option("NL", "web.admin.common.requests.country.nl"),
  option("BE", "web.admin.common.requests.country.be"),
  option("CH", "web.admin.common.requests.country.ch"),
  option("AT", "web.admin.common.requests.country.at"),
  option("SE", "web.admin.common.requests.country.se"),
  option("NO", "web.admin.common.requests.country.no"),
  option("DK", "web.admin.common.requests.country.dk"),
  option("FI", "web.admin.common.requests.country.fi"),
  option("IS", "web.admin.common.requests.country.is"),
  option("PL", "web.admin.common.requests.country.pl"),
  option("CZ", "web.admin.common.requests.country.cz"),
  option("HU", "web.admin.common.requests.country.hu"),
  option("GR", "web.admin.common.requests.country.gr"),
  option("RU", "web.admin.common.requests.country.ru"),
  option("UA", "web.admin.common.requests.country.ua"),
  option("TR", "web.admin.common.requests.country.tr"),
  option("IL", "web.admin.common.requests.country.il"),
  option("IR", "web.admin.common.requests.country.ir"),
  option("EG", "web.admin.common.requests.country.eg"),
  option("NG", "web.admin.common.requests.country.ng"),
  option("ZA", "web.admin.common.requests.country.za"),
  option("BR", "web.admin.common.requests.country.br"),
  option("MX", "web.admin.common.requests.country.mx"),
  option("AR", "web.admin.common.requests.country.ar"),
];

/** Decade shortcuts for a rule's release-year range; each `label` is read in the interface language when used. */
export const ROUTING_DECADES: readonly { readonly label: string; from: number; to: number }[] = [
  1970, 1980, 1990, 2000, 2010, 2020,
].map((from) => ({
  get label() {
    return t("web.admin.common.requests.decade", { decade: from });
  },
  from,
  to: from + 9,
}));

function nameOf(options: readonly CodeOption[], code: string, tx: Text): string {
  const found = options.find((option) => option.code === code);
  return found ? tx(found.nameKey) : code;
}

/** A language's name, or with `englishText` its English name for a rule name the client saves. */
export function routingLanguageName(code: string, tx: Text = t): string {
  return nameOf(ROUTING_LANGUAGES, code.toLowerCase(), tx);
}

/** A country's name, or with `englishText` its English name for a rule name the client saves. */
export function routingCountryName(code: string, tx: Text = t): string {
  return nameOf(ROUTING_COUNTRIES, code.toUpperCase(), tx);
}
