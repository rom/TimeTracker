package web

import (
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/preview"
	"github.com/rom/timetracker/internal/service"
)

// handleExpenses renders the expenses screen.
func (s *Server) handleExpenses(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "expenses")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("expenses.title")

	filter, form := s.expenseFilter(r)
	data.Filter = form

	if data.Expenses, err = s.svc.Expenses(r.Context(), filter); err != nil {
		s.fail(w, r, err)
		return
	}
	data.ExpenseTotals = s.svc.ExpenseTotals(data.Expenses)

	// Which claims are missing evidence their customer's contract requires.
	// Resolved here rather than in the template, which has no business calling
	// into the service.
	data.NeedsReceipt = make(map[int64]bool, len(data.Expenses))
	for _, expense := range data.Expenses {
		needs, receiptErr := s.svc.NeedsReceipt(r.Context(), expense)
		if receiptErr != nil {
			s.fail(w, r, receiptErr)
			return
		}
		data.NeedsReceipt[expense.ID] = needs
	}

	// The receipts themselves, so a row can show what is attached to it rather
	// than only how many. Loaded per expense that has any, which is the same
	// number of queries the count badge already implies and none for a listing
	// with no receipts on it at all.
	data.AttachmentsByOwner = map[int64][]domain.Attachment{}
	for _, expense := range data.Expenses {
		if expense.AttachmentCount == 0 {
			continue
		}
		attachments, listErr := s.svc.Attachments(r.Context(),
			domain.AttachmentOwnerExpense, expense.ID)
		if listErr != nil {
			s.fail(w, r, listErr)
			return
		}
		data.AttachmentsByOwner[expense.ID] = attachments
		for id, text := range s.svc.TextPreviews(r.Context(), attachments) {
			if data.AttachmentText == nil {
				data.AttachmentText = map[int64]service.TextPreview{}
			}
			data.AttachmentText[id] = text
		}
	}

	if data.Projects, err = s.svc.Projects(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Customers, err = s.svc.Customers(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_expenses.html", data)
}

// expenseFilter reads the date range and narrowing from the query string.
func (s *Server) expenseFilter(r *http.Request) (service.ExpenseFilter, entryFilterForm) {
	now := s.svc.Now()
	form := entryFilterForm{
		From: r.URL.Query().Get("from"),
		To:   r.URL.Query().Get("to"),
	}

	from, err := time.Parse("2006-01-02", form.From)
	if err != nil {
		from = now.AddDate(0, -3, 0)
		form.From = from.Format("2006-01-02")
	}
	to, err := time.Parse("2006-01-02", form.To)
	if err != nil {
		to = now
		form.To = to.Format("2006-01-02")
	}
	form.CustomerID = int64Param(r.URL.Query().Get("customer"))
	form.ProjectID = int64Param(r.URL.Query().Get("project"))

	return service.ExpenseFilter{
		From:       from,
		To:         to.AddDate(0, 0, 1),
		CustomerID: form.CustomerID,
		ProjectID:  form.ProjectID,
		Limit:      1000,
	}, form
}

// handleCreateExpense records a cost.
func (s *Server) handleCreateExpense(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	markup, _ := strconv.ParseInt(r.FormValue("markup"), 10, 64)
	_, err := s.svc.CreateExpense(r.Context(), service.ExpenseInput{
		ProjectID:     int64Param(r.FormValue("project_id")),
		SpentOn:       r.FormValue("spent_on"),
		Category:      r.FormValue("category"),
		Description:   r.FormValue("description"),
		Amount:        r.FormValue("amount"),
		Billable:      r.FormValue("billable") != "",
		Reimbursable:  r.FormValue("reimbursable") != "",
		MarkupPercent: markup,
		// Whether the field was filled in at all, so an explicit 0% is not
		// overwritten by the customer's default markup.
		MarkupGiven: r.FormValue("markup") != "",
		Quantity:    r.FormValue("quantity"),
		Unit:        domain.ExpenseUnit(r.FormValue("unit")),
		OnBehalfOf:  int64Param(r.FormValue("on_behalf_of")),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleUpdateExpense saves an edited cost.
func (s *Server) handleUpdateExpense(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	markup, _ := strconv.ParseInt(r.FormValue("markup"), 10, 64)

	_, err := s.svc.UpdateExpense(r.Context(), int64Param(r.PathValue("id")), service.ExpenseInput{
		ProjectID:     int64Param(r.FormValue("project_id")),
		SpentOn:       r.FormValue("spent_on"),
		Category:      r.FormValue("category"),
		Description:   r.FormValue("description"),
		Amount:        r.FormValue("amount"),
		Billable:      r.FormValue("billable") != "",
		Reimbursable:  r.FormValue("reimbursable") != "",
		MarkupPercent: markup,
		MarkupGiven:   r.FormValue("markup") != "",
		Quantity:      r.FormValue("quantity"),
		Unit:          domain.ExpenseUnit(r.FormValue("unit")),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleDeleteExpense removes a cost and its receipts.
func (s *Server) handleDeleteExpense(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteExpense(r.Context(), int64Param(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// ------------------------------------------------------------ attachments ---

// maxUploadBytes bounds a single upload request. Slightly above the blob store's
// own per-file limit, to leave room for the multipart framing.
const maxUploadBytes = 32 << 20

// handleUpload attaches a file to a time entry or an expense.
//
// This is the one route that must accept multipart, since that is how a browser
// sends a file. Everything else uses URL-encoded bodies so both the JavaScript
// and no-JavaScript paths go through identical parsing.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ownerType := r.PathValue("owner")
	ownerID := int64Param(r.PathValue("id"))

	switch ownerType {
	case domain.AttachmentOwnerTimeEntry, domain.AttachmentOwnerExpense:
	default:
		http.Error(w, "Unknown attachment target.", http.StatusBadRequest)
		return
	}

	// The body limit is applied to the request itself, so an oversized upload is
	// refused before it is read into memory or spilled to disk.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxFormMemory); err != nil {
		http.Error(w, s.tr(r).T("attachments.toolarge"), http.StatusRequestEntityTooLarge)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file was uploaded.", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	filename := "attachment"
	if header != nil && header.Filename != "" {
		filename = header.Filename
	}

	if _, err := s.svc.Attach(r.Context(), ownerType, ownerID, filename, file); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleDownloadAttachment serves an attachment's bytes.
//
// Everything about this response is deliberate: authorisation happens in the
// service layer against the owning record, the type is the one sniffed on
// upload rather than anything the client claimed, and the disposition is always
// "attachment" so a browser saves the file instead of rendering it. A stored
// file rendered inline is a stored cross-site scripting vector.
func (s *Server) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	attachment, reader, err := s.svc.OpenAttachment(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", attachment.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", contentDisposition(attachment.Filename))
	// Attachments are immutable - the path is the content hash - so they can be
	// cached hard. Private, because they are somebody's receipts.
	w.Header().Set("Cache-Control", "private, max-age=86400")

	http.ServeContent(w, r, attachment.Filename, attachment.CreatedAt, reader)
}

// handlePreviewAttachment serves an attachment for display rather than download.
//
// This is the only route in the application that serves stored bytes inline,
// and every header on the response is doing a job:
//
//   - Content-Security-Policy is the whole safety argument for SVG. "sandbox"
//     with no tokens puts the response in a unique origin with scripts, forms,
//     popups and plugins disabled; "default-src 'none'" stops it fetching
//     anything. So even a hostile SVG opened directly at this URL - not through
//     the <img> the page uses, where script never runs anyway - can do nothing.
//     Both are belt and braces on purpose: this replaced a blanket refusal to
//     store SVG at all, and replacing a refusal deserves more than one lock.
//   - X-Content-Type-Options stops a browser sniffing its way to a different
//     type from the one we determined.
//   - Content-Disposition: inline is the only difference in intent from the
//     download route, and it is why the rest of this list exists.
//
// See docs/adr/0031-attachment-previews.md.
func (s *Server) handlePreviewAttachment(w http.ResponseWriter, r *http.Request) {
	attachment, result, err := s.svc.PreviewAttachment(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if result.Kind == preview.KindText {
		// Text is rendered into the page, escaped like any other string, rather
		// than fetched as a resource. There is nothing to serve here.
		http.Error(w, "This file is shown as text on the page it belongs to.",
			http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("inline; filename=%q", previewFilename(attachment, result)))
	// Immutable: the path is derived from an id whose content is addressed by
	// hash. Private, because these are somebody's receipts.
	w.Header().Set("Cache-Control", "private, max-age=86400")

	http.ServeContent(w, r, previewFilename(attachment, result),
		attachment.CreatedAt, bytes.NewReader(result.Body))
}

// previewFilename names the served preview.
//
// A converted TIFF is a PNG and must be called one, or a browser told to save
// it writes a .tiff containing PNG bytes.
func previewFilename(attachment domain.Attachment, result preview.Result) string {
	name := attachment.Filename
	if result.Converted && result.ContentType == "image/png" {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".png"
	}
	return name
}

// handleDeleteAttachment removes an attachment.
func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteAttachment(r.Context(), int64Param(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// ------------------------------------------------------------------ inbox ---

// handleInbox renders what is waiting for the acting user's decision.
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "inbox")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("inbox.title")

	inbox, err := s.svc.Inbox(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Inbox = &inbox

	if data.Proposed, err = s.svc.ProposedByMe(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_inbox.html", data)
}

// handleAcceptEntry confirms a proposal.
func (s *Server) handleAcceptEntry(w http.ResponseWriter, r *http.Request) {
	if _, err := s.svc.AcceptEntry(r.Context(), int64Param(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleRejectEntry declines a proposal, keeping the record.
func (s *Server) handleRejectEntry(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.RejectEntry(r.Context(), int64Param(r.PathValue("id")),
		r.FormValue("reason")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleAcceptExpense and handleRejectExpense mirror the entry handlers.
func (s *Server) handleAcceptExpense(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.AcceptExpense(r.Context(), int64Param(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

func (s *Server) handleRejectExpense(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.RejectExpense(r.Context(), int64Param(r.PathValue("id")),
		r.FormValue("reason")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}
