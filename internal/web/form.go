package web

import (
	"fmt"
	"net/http"
	"strings"
)

// maxFormMemory bounds how much of a multipart body is held in memory before
// the rest spills to temporary files. An unbounded body is a denial-of-service
// surface, so the limit is explicit rather than left to a default.
const maxFormMemory = 8 << 20 // 8 MiB

// parseForm decodes a request body, whichever encoding it arrived in.
//
// This exists because of a genuinely subtle trap in net/http, which produced a
// real bug: r.ParseForm does NOT parse a multipart body, but it does set r.Form.
// r.FormValue only falls back to the multipart parser when r.Form is nil - so
// calling ParseForm first on a multipart request leaves every field silently
// empty, and the handler reports "name is required" for a form the user filled
// in completely.
//
// Handlers therefore call this rather than r.ParseForm directly, and it picks
// the right parser from the content type. A browser sending FormData and a plain
// HTML form post are then handled identically, which is also what keeps the
// no-JavaScript path honest.
func parseForm(r *http.Request) error {
	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxFormMemory); err != nil {
			return fmt.Errorf("%w: could not read the submitted form: %s",
				errBadForm, err)
		}
		return nil
	}

	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("%w: could not read the submitted form: %s", errBadForm, err)
	}
	return nil
}

// errBadForm marks an unreadable request body, which is the caller's mistake
// rather than ours, so it maps onto a 400.
var errBadForm = &formError{}

type formError struct{}

func (e *formError) Error() string { return "malformed form" }
