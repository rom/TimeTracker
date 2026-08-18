package web

import (
	"html"
	"html/template"
	"net/http"
	"strings"

	"github.com/rom/timetracker/internal/i18n"
)

// Context-sensitive help.
//
// The help a person needs depends on what they are looking at, so each screen
// declares its own topics and the help panel shows those rather than a single
// undifferentiated manual. The content lives in the message catalogues, so it is
// translated like everything else.
//
// It is served as a normal page fragment over HTTP, which means it works with
// JavaScript disabled - the help button is a link to /help/today, and the
// browser simply navigates there. With script available the same fragment is
// swapped into a panel without leaving the screen.

// helpTopic names one section of help.
type helpTopic struct {
	// Key is the catalogue prefix; the title is Key+".title" and the body is
	// Key+".body".
	Key string
}

// helpForScreen maps a screen to the topics that are useful there.
//
// The mapping is explicit rather than derived, because usefulness is a judgement:
// the day view needs the quick-add syntax more than it needs the role model, and
// no rule can infer that.
var helpForScreen = map[string][]helpTopic{
	"today":   {{Key: "help.today"}, {Key: "help.quickadd"}, {Key: "help.themes"}},
	"week":    {{Key: "help.week"}, {Key: "help.today"}},
	"entries": {{Key: "help.entries"}, {Key: "help.today"}},
	"admin":   {{Key: "help.admin"}, {Key: "help.themes"}},
	"users":   {{Key: "help.users"}, {Key: "help.admin"}},
	"login":   {{Key: "help.themes"}},
}

// helpSection is one rendered topic.
type helpSection struct {
	Title string
	// Body is rendered from a restricted markup subset; see renderHelpBody.
	Body template.HTML
}

// handleHelp renders the help for a screen.
//
// The screen name comes from the path, and an unknown one falls back to the day
// view's help rather than erroring: someone who has found their way to a help
// URL is asking for help, and an error page is a poor answer to that.
func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	screen := r.PathValue("screen")
	topics, ok := helpForScreen[screen]
	if !ok {
		screen = "today"
		topics = helpForScreen[screen]
	}

	data, err := s.newPageData(r, "", "help")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("help.title")
	data.HelpScreen = screen

	for _, topic := range topics {
		data.Help = append(data.Help, helpSection{
			Title: data.Printer.T(topic.Key + ".title"),
			Body:  renderHelpBody(data.Printer.T(topic.Key + ".body")),
		})
	}

	// An HTMX request wants only the panel; a plain navigation wants a page
	// around it. Both render from the same definitions.
	if isHTMX(r) {
		s.renderFragment(w, r, "page_help.html", "help-panel", data)
		return
	}
	s.render(w, r, "page_help.html", data)
}

// renderHelpBody converts the catalogue's restricted markup into HTML.
//
// The subset is deliberately tiny - blank lines separate paragraphs, `**bold**`
// emphasises, and “ `code` “ marks literal input - because help text is
// written by translators, and a full Markdown parser would be a large dependency
// and a large attack surface for three constructs.
//
// Everything is HTML-escaped first and the markup applied afterwards, so a
// catalogue string can never inject markup even though the result is rendered as
// template.HTML.
func renderHelpBody(body string) template.HTML {
	var out strings.Builder

	for _, paragraph := range strings.Split(body, "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}

		// Escape first. Everything after this point only ever inserts tags of
		// our own choosing around already-safe text.
		escaped := html.EscapeString(paragraph)
		escaped = applyPairedMarkup(escaped, "**", "<strong>", "</strong>")
		escaped = applyPairedMarkup(escaped, "`", "<code>", "</code>")

		out.WriteString("<p>")
		out.WriteString(escaped)
		out.WriteString("</p>\n")
	}
	return template.HTML(out.String())
}

// applyPairedMarkup replaces matched pairs of a delimiter with open and close
// tags. An unmatched trailing delimiter is left as literal text rather than
// producing an unbalanced tag.
func applyPairedMarkup(text, delimiter, open, close string) string {
	parts := strings.Split(text, delimiter)
	if len(parts) < 3 {
		return text
	}

	var out strings.Builder
	for i, part := range parts {
		if i == 0 {
			out.WriteString(part)
			continue
		}
		// Odd indices are inside a pair - but only if a closing delimiter
		// follows, otherwise the delimiter was literal.
		if i%2 == 1 && i+1 < len(parts) {
			out.WriteString(open)
			out.WriteString(part)
			out.WriteString(close)
		} else {
			out.WriteString(part)
		}
	}
	return out.String()
}

// helpAvailable reports whether a screen has its own help, so the chrome can
// hide the button where there is nothing to show.
func helpAvailable(screen string) bool {
	_, ok := helpForScreen[screen]
	return ok
}

// languageOptions returns the languages a user can choose between.
func languageOptions() []i18n.Language { return i18n.Languages() }
