package apiv2

import (
	"net/http"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
)

// locationDeviceHeader names the device header in a validation problem.
const locationDeviceHeader = "header.x-silo-device-id"

// deviceIDShape is the identifier a client may present in X-Silo-Device-Id:
// a UUID or an opaque token of letters, digits, dot, underscore, colon and
// hyphen. Commas and interior whitespace are excluded so a header a client
// sent twice, which Huma binds as the comma-joined list value, can never be
// stored as a device identity. Surrounding whitespace is ignored, as v1's
// clamp ignores it; the 128-character bound stays with each operation's
// declared maxLength so its documented out_of_range answer is unchanged.
var deviceIDShape = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// rejectMalformedDeviceHeader refuses a request whose X-Silo-Device-Id is
// repeated or does not have the device identifier shape, before any
// operation binds it. v1 reads the first line with Header.Get and never saw
// a joined value; v2 binds the header through Huma, which joins repeated
// lines with a comma, so an Android client that attached the header twice
// registered "id,id" as its device. A present but empty header is left to
// each operation's own required/minLength rule.
func rejectMalformedDeviceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header[textproto.CanonicalMIMEHeaderKey(deviceIDHeader)]
		switch {
		case len(values) > 1:
			writeProblem(w, r, deviceHeaderProblem("X-Silo-Device-Id must be sent once; the request carried it "+strconv.Itoa(len(values))+" times"))
			return
		case len(values) == 1:
			v := strings.TrimSpace(values[0])
			if v != "" && !deviceIDShape.MatchString(v) {
				writeProblem(w, r, deviceHeaderProblem("X-Silo-Device-Id must be a single device identifier: letters, digits, '.', '_', ':' or '-', with no comma or interior whitespace"))
				return
			}
			// Every operation then binds the same trimmed identity, as v1's
			// header clamp already trims before storing.
			r.Header.Set(deviceIDHeader, v)
		}
		next.ServeHTTP(w, r)
	})
}

func deviceHeaderProblem(detail string) *Problem {
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
		WithErrors(ProblemError{Location: locationDeviceHeader, Code: codeInvalid, Detail: detail})
}
