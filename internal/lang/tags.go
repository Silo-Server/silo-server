package lang

import (
	"regexp"
	"strings"
)

var languageTagPattern = regexp.MustCompile(
	`^([a-zA-Z]{2,3}(-[a-zA-Z]{3}){0,3}(-[a-zA-Z]{4})?(-([a-zA-Z]{2}|[0-9]{3}))?` +
		`(-([0-9a-zA-Z]{5,8}|[0-9][0-9a-zA-Z]{3}))*` +
		`(-[0-9a-wy-zA-WY-Z](-[0-9a-zA-Z]{2,8})+)*` +
		`(-[xX](-[0-9a-zA-Z]{1,8})+)?` +
		`|[xX](-[0-9a-zA-Z]{1,8})+)$`)

func canonicalTagCase(tag string) string {
	parts := strings.Split(tag, "-")
	// Case is not significant in BCP 47, but the conventional casing is what
	// every client library produces: lowercase language, Titlecase script,
	// UPPERCASE region, lowercase everything else.
	parts[0] = strings.ToLower(parts[0])
	// A tag that is entirely private use ("x-whatever") has no script or
	// region positions — everything after the leading singleton is private-use
	// content and stays lowercase.
	inExtension := parts[0] == "x"
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		switch {
		case len(part) == 1:
			// A singleton opens an extension ("u", "t") or private use ("x").
			// Everything after it is extension content, so the two-letter
			// region rule must stop applying — "nu" in "ar-EG-u-nu-latn" is an
			// extension key, not a region.
			inExtension = true
			parts[i] = strings.ToLower(part)
		case inExtension:
			parts[i] = strings.ToLower(part)
		case len(part) == 4 && isAlpha(part):
			// A script. Not necessarily at index 1: an extlang can precede it
			// (`zh-cmn-Hans-CN`). No collision with variants — a four-character
			// variant must begin with a digit.
			parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		case len(part) == 2 && isAlpha(part):
			parts[i] = strings.ToUpper(part)
		default:
			parts[i] = strings.ToLower(part)
		}
	}
	return strings.Join(parts, "-")
}

func isAlpha(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}
