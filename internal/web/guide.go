package web

import (
	"html/template"
	"net/http"
)

// The user guide: how to perform the normal actions.
//
// Distinct from the context-sensitive help, and deliberately so. The help
// answers "what am I looking at" for the screen in front of you; the guide
// answers "how do I do X", which is a question you have *before* you know which
// screen to be on. Someone who has been asked to record time for a colleague
// does not know that the answer involves the day view, an @name, and a
// confirmation from the colleague - so a per-screen panel cannot reach them.
//
// The content lives in the message catalogues like everything else, so it is
// translated, and it is rendered through the same restricted markup as the help
// rather than being hand-written HTML.

// guideTopic is one how-to.
type guideTopic struct {
	// Key is both the URL segment and the catalogue prefix: the title is
	// guide.<key>.title, the summary guide.<key>.summary, the body
	// guide.<key>.body.
	Key string
	// Server reports that the topic only makes sense with more than one
	// account. Recording time for a colleague, approving somebody's week and
	// managing users are meaningless on a laptop with one user, and listing
	// them there would send people looking for controls that are not shown.
	Server bool
}

// guideTopics in the order somebody meets them, not alphabetically: record some
// time, fix it, hand it in. Setting up the catalogue comes last because most
// people are handed an instance that already has one.
var guideTopics = []guideTopic{
	{Key: "record"},
	{Key: "correct"},
	{Key: "repeat"},
	{Key: "move"},
	{Key: "find"},
	{Key: "proxy", Server: true},
	{Key: "submit"},
	{Key: "approve", Server: true},
	{Key: "expenses"},
	{Key: "calendar"},
	{Key: "export"},
	{Key: "backup"},
	{Key: "setup"},
}

// guideSection is one rendered topic.
type guideSection struct {
	Key     string
	Title   string
	Summary string
	// Body is rendered from the same restricted markup the help uses; see
	// renderHelpBody.
	Body template.HTML
}

// visibleGuideTopics returns the topics that make sense in this mode.
func (s *Server) visibleGuideTopics() []guideTopic {
	serverMode := s.accounts != nil
	topics := make([]guideTopic, 0, len(guideTopics))
	for _, topic := range guideTopics {
		if topic.Server && !serverMode {
			continue
		}
		topics = append(topics, topic)
	}
	return topics
}

// handleGuide renders the whole guide, or one topic of it.
//
// The whole guide on one page is deliberate: it is one URL to send somebody, it
// is searchable with the browser's own find, and it prints as a manual. Single
// topics keep their own URLs so the context help and a colleague's message can
// link straight to the answer.
func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "guide")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	wanted := r.PathValue("topic")
	for _, topic := range s.visibleGuideTopics() {
		if wanted != "" && topic.Key != wanted {
			continue
		}
		data.Guide = append(data.Guide, guideSection{
			Key:     topic.Key,
			Title:   data.Printer.T("guide." + topic.Key + ".title"),
			Summary: data.Printer.T("guide." + topic.Key + ".summary"),
			Body:    renderHelpBody(data.Printer.T("guide." + topic.Key + ".body")),
		})
	}

	// An unknown topic falls back to the whole guide rather than a 404:
	// somebody who has reached a guide URL is looking for instructions, and an
	// error page is a poor answer to that.
	if len(data.Guide) == 0 {
		for _, topic := range s.visibleGuideTopics() {
			data.Guide = append(data.Guide, guideSection{
				Key:     topic.Key,
				Title:   data.Printer.T("guide." + topic.Key + ".title"),
				Summary: data.Printer.T("guide." + topic.Key + ".summary"),
				Body:    renderHelpBody(data.Printer.T("guide." + topic.Key + ".body")),
			})
		}
		wanted = ""
	}

	data.Title = data.Printer.T("guide.title")
	// One topic titles the page after itself, so a browser tab and a bookmark
	// say which answer they hold.
	data.GuideTopic = wanted
	if wanted != "" && len(data.Guide) == 1 {
		data.Title = data.Guide[0].Title
	}
	s.render(w, r, "page_guide.html", data)
}
