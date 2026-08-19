package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// seedEntries creates n entries for one assignment, spread over consecutive
// days, and returns the server.
func seedForPaging(t *testing.T, n int) *Server {
	t.Helper()
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})

	// Three a day, so the ordering is stable and the range stays short. Dates
	// come from time arithmetic rather than a formatted day number, which ran
	// off the end of January the first time this was written.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		day := start.AddDate(0, 0, i/3).Format("2006-01-02")
		hour := fmt.Sprintf("%02d:00", 8+i%3*3)
		rec := post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {day}, "start": {hour},
			"duration": {"1h"}, "note": {"entry " + strconv.Itoa(i)}, "billable": {"on"},
		})
		if rec.Code >= 400 {
			t.Fatalf("create entry %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	return srv
}

const pagingRange = "from=2026-01-01&to=2026-12-31"

// countRows counts entry rows in a rendered page.
func countRows(body string) int {
	return strings.Count(body, `class="entry-row`)
}

// TestEntriesArePaged: the screen shows a page, and says which.
func TestEntriesArePaged(t *testing.T) {
	srv := seedForPaging(t, 137)

	cases := []struct {
		page      int
		wantRows  int
		wantRange string
	}{
		{1, EntriesPerPage, "1–50"},
		{2, EntriesPerPage, "51–100"},
		{3, 137 - 2*EntriesPerPage, "101–137"},
	}
	for _, c := range cases {
		body := get(t, srv, fmt.Sprintf("/entries?%s&page=%d", pagingRange, c.page)).Body.String()
		if got := countRows(body); got != c.wantRows {
			t.Errorf("page %d has %d rows, want %d", c.page, got, c.wantRows)
		}
		if !strings.Contains(body, c.wantRange) {
			t.Errorf("page %d does not say it is showing %s", c.page, c.wantRange)
		}
		if !strings.Contains(body, "of 137 entries") {
			t.Errorf("page %d does not report the total", c.page)
		}
	}
}

// TestEveryEntryAppearsOnExactlyOnePage. Off-by-one in a page window either
// hides a row or shows it twice, and both are invisible unless counted.
func TestEveryEntryAppearsOnExactlyOnePage(t *testing.T) {
	const total = 137
	srv := seedForPaging(t, total)

	seen := map[string]int{}
	noteOnPage := regexp.MustCompile(`entry (\d+)</span>`)

	for page := 1; page <= 3; page++ {
		body := get(t, srv, fmt.Sprintf("/entries?%s&page=%d", pagingRange, page)).Body.String()
		for _, match := range noteOnPage.FindAllStringSubmatch(body, -1) {
			seen[match[1]]++
		}
	}

	if len(seen) != total {
		t.Errorf("the three pages between them show %d distinct entries, want %d",
			len(seen), total)
	}
	for note, count := range seen {
		if count != 1 {
			t.Errorf("entry %s appears on %d pages, want 1", note, count)
		}
	}
}

// TestPageBeyondTheEndSaysSo rather than looking like an empty database.
func TestPageBeyondTheEndSaysSo(t *testing.T) {
	srv := seedForPaging(t, 10)

	body := get(t, srv, "/entries?"+pagingRange+"&page=9").Body.String()
	if countRows(body) != 0 {
		t.Error("a page past the end should have no rows")
	}
	if !strings.Contains(body, "nothing on this page") {
		t.Error("a page past the end should say so, not look like an empty filter")
	}
	// And never a nonsense range: FirstShown past the total once read "151-137".
	if regexp.MustCompile(`Showing \d+–\d+`).MatchString(body) {
		t.Error("a page past the end must not claim to be showing a range")
	}
}

// TestPagerKeepsTheWholeFilter. A pager that dropped the search box would send
// somebody who clicked "next" into page two of a different result.
func TestPagerKeepsTheWholeFilter(t *testing.T) {
	srv := seedForPaging(t, 137)

	body := get(t, srv, "/entries?"+pagingRange+
		"&customer=1&project=1&assignment=1&kind=work&billable=1&q=entry").Body.String()

	links := regexp.MustCompile(`href="(/entries\?[^"]*page=\d+[^"]*)"`).FindAllStringSubmatch(body, -1)
	if len(links) == 0 {
		t.Fatal("no page links were rendered")
	}
	for _, link := range links {
		target := strings.ReplaceAll(link[1], "&amp;", "&")
		for _, want := range []string{
			"from=2026-01-01", "to=2026-12-31", "customer=1", "project=1",
			"assignment=1", "kind=work", "billable=1", "q=entry",
		} {
			if !strings.Contains(target, want) {
				t.Errorf("the page link %q drops %q", target, want)
			}
		}
	}
}

// TestExportIsNotPaged is the one that matters.
//
// The screen's row limit travelled into every export with the rest of the
// filter, so a range with more entries than the limit was silently truncated -
// oldest first, in a file somebody was about to invoice from. Paging the screen
// makes that failure far easier to reach, so it is pinned here: an export
// covers the filter, whatever page the screen was on.
func TestExportIsNotPaged(t *testing.T) {
	const total = 137
	srv := seedForPaging(t, total)

	for _, query := range []string{
		pagingRange,
		pagingRange + "&page=1",
		pagingRange + "&page=3",
	} {
		rec := get(t, srv, "/export/csv?"+query)
		if rec.Code != 200 {
			t.Fatalf("export = %d", rec.Code)
		}
		// One header row plus one per entry, and the file ends with a newline.
		rows := strings.Count(strings.TrimSpace(rec.Body.String()), "\n")
		if rows != total {
			t.Errorf("/export/csv?%s produced %d entry rows, want %d",
				query, rows, total)
		}
	}
}

// TestPageWindowStaysShort: a filter matching a hundred pages must not render a
// hundred links.
func TestPageWindowStaysShort(t *testing.T) {
	for _, c := range []struct {
		page, total int
		want        string
	}{
		{1, 50, "1"},
		{1, 500, "1 2 3 10"},
		{5, 500, "1 3 4 5 6 7 10"},
		{10, 500, "1 8 9 10"},
		{50, 5000, "1 48 49 50 51 52 100"},
	} {
		form := entryFilterForm{Page: c.page, Total: c.total}
		var got []string
		for _, page := range form.PageNumbers() {
			if page == 0 {
				continue // the gap marker
			}
			got = append(got, strconv.Itoa(page))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("page %d of %d entries: links %v, want %q",
				c.page, c.total, got, c.want)
		}
		if len(form.PageNumbers()) > 9 {
			t.Errorf("page %d of %d renders %d links; the window should be bounded",
				c.page, c.total, len(form.PageNumbers()))
		}
	}
}

// TestPagerIsAbsentWhenEverythingFits.
func TestPagerIsAbsentWhenEverythingFits(t *testing.T) {
	srv := seedForPaging(t, 12)

	body := get(t, srv, "/entries?"+pagingRange).Body.String()
	if strings.Contains(body, `class="pager"`) {
		t.Error("a single page of results should not carry a pager")
	}
	if !strings.Contains(body, "of 12 entries") {
		t.Error("the count is worth showing even on one page")
	}
}

// TestExpenseFilterPagesToo.
//
// "Time on a day its project also had a cost" cannot be a SQL condition - an
// entry's calendar day comes from the zone column, and SQLite cannot convert an
// instant to a named zone's local date - so those rows are narrowed after the
// query and the window has to be applied to the narrowed set. A LIMIT applied
// before the narrowing would return pages with holes in them, which is what
// this pins: the seeded set is large enough that the filtered result spans two
// pages, so an off-by-one is visible rather than hypothetical.
func TestExpenseFilterPagesToo(t *testing.T) {
	// 180 entries over 60 days, three a day.
	srv := seedForPaging(t, 180)

	// An expense on each of the first 30 days, so 90 entries match: two pages.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for day := range 30 {
		rec := post(t, srv, "/expenses", url.Values{
			"project_id": {"1"}, "spent_on": {start.AddDate(0, 0, day).Format("2006-01-02")},
			"description": {"Taxi"}, "amount": {"100.00"}, "billable": {"on"},
		})
		if rec.Code >= 400 {
			t.Fatalf("create expense = %d: %s", rec.Code, rec.Body.String())
		}
	}

	const matching = 90
	seen := map[string]int{}
	noteOnPage := regexp.MustCompile(`entry (\d+)</span>`)

	for page, want := range map[int]int{1: EntriesPerPage, 2: matching - EntriesPerPage} {
		body := get(t, srv, fmt.Sprintf("/entries?%s&expenses=1&page=%d", pagingRange, page)).Body.String()
		if got := countRows(body); got != want {
			t.Errorf("page %d of the expense filter has %d rows, want %d", page, got, want)
		}
		if !strings.Contains(body, fmt.Sprintf("of %d entries", matching)) {
			t.Errorf("page %d should report %d matching entries, not the size of "+
				"the query behind them", page, matching)
		}
		for _, match := range noteOnPage.FindAllStringSubmatch(body, -1) {
			seen[match[1]]++
		}
	}

	if len(seen) != matching {
		t.Errorf("the two pages show %d distinct entries between them, want %d",
			len(seen), matching)
	}
	for note, count := range seen {
		if count != 1 {
			t.Errorf("entry %s appears on %d pages, want 1", note, count)
		}
		// Entries 90 and above fall on days with no expense.
		if number, err := strconv.Atoi(note); err == nil && number >= matching {
			t.Errorf("entry %s is on a day with no expense and should not be listed", note)
		}
	}
}

// TestExportCarriesTagsBeyondOneBatch.
//
// The tag lookup batches its bound parameters, because SQLite caps how many one
// statement may carry - a limit that stayed hidden while the only caller was a
// screen capped at a thousand rows, and became a 500 on the download the moment
// the export stopped being truncated.
//
// This is the end-to-end half: six hundred entries is more than one batch, so a
// batching bug that dropped or duplicated a batch shows up here as missing tags.
// It is deliberately *not* the guard for the parameter limit itself - six
// hundred is well under SQLite's ceiling, so this passes unbatched too. That
// guard is TestTagsForEntriesExceedingTheParameterLimit in the store package,
// where forty thousand ids can be passed without creating forty thousand rows.
func TestExportCarriesTagsBeyondOneBatch(t *testing.T) {
	const total = 600
	srv := seedForPaging(t, total)

	// Tags on every entry, so the lookup actually has work to do.
	for id := 1; id <= total; id++ {
		rec := post(t, srv, "/entries/"+strconv.Itoa(id), url.Values{
			"assignment_id": {"1"}, "duration": {"1h"},
			"tags": {"billed,reviewed"}, "billable": {"on"},
		})
		if rec.Code >= 400 {
			t.Fatalf("tag entry %d = %d: %s", id, rec.Code, rec.Body.String())
		}
	}

	rec := get(t, srv, "/export/csv?"+pagingRange)
	if rec.Code != 200 {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if rows := strings.Count(strings.TrimSpace(body), "\n"); rows != total {
		t.Errorf("export produced %d entry rows, want %d", rows, total)
	}
	// And the tags survived the batching rather than being dropped for the
	// entries beyond the first batch.
	if got := strings.Count(body, "billed reviewed"); got != total {
		t.Errorf("%d rows carry their tags, want %d", got, total)
	}
}

// TestDocumentFormatsAreBoundedRatherThanAttempted.
//
// PDF, Word and Markdown group their rows and print a subtotal per group, so
// none of them can write anything until every row has been read - grouping is
// buffering. Rather than discovering that as an out-of-memory, an oversized
// range is refused with a message that says how large it is and what to do
// instead.
func TestDocumentFormatsAreBoundedRatherThanAttempted(t *testing.T) {
	srv := seedForPaging(t, 20)

	// The real bound is far above anything a test should seed, so it is lowered
	// for the duration rather than reached.
	restore := maxCollectedExportRows
	setMaxCollectedExportRows(10)
	t.Cleanup(func() { setMaxCollectedExportRows(restore) })

	for _, format := range []string{"pdf", "docx", "md"} {
		rec := get(t, srv, "/export/"+format+"?"+pagingRange)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("/export/%s = %d, want 413 for a range past the limit", format, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "CSV") {
			t.Errorf("/export/%s: the refusal should point at a format that streams", format)
		}
	}

	// The streaming formats have no such limit, and that is the point of the
	// message: they must still work at the same size.
	for _, format := range []string{"csv", "json"} {
		rec := get(t, srv, "/export/"+format+"?"+pagingRange)
		if rec.Code != http.StatusOK {
			t.Errorf("/export/%s = %d; a streaming format has no row limit", format, rec.Code)
		}
	}
}

// TestStreamedAndCollectedExportsAgree.
//
// The with-expenses filter cannot stream - its condition is applied outside SQL
// - so it takes the collected path while everything else streams. Two paths
// through one handler is the arrangement that drifts, so the same data is
// exported both ways and compared.
func TestStreamedAndCollectedExportsAgree(t *testing.T) {
	srv := seedForPaging(t, 30)

	// An expense on every seeded day, so the with-expenses filter matches
	// everything and the two paths are exporting the same rows.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for day := range 10 {
		if rec := post(t, srv, "/expenses", url.Values{
			"project_id": {"1"}, "spent_on": {start.AddDate(0, 0, day).Format("2006-01-02")},
			"description": {"Taxi"}, "amount": {"100.00"}, "billable": {"on"},
		}); rec.Code >= 400 {
			t.Fatalf("create expense = %d", rec.Code)
		}
	}

	for _, format := range []string{"csv", "json"} {
		streamed := stableExport(get(t, srv, "/export/"+format+"?"+pagingRange).Body.String())
		collected := stableExport(get(t, srv, "/export/"+format+"?"+pagingRange+"&expenses=1").Body.String())

		if streamed != collected {
			t.Errorf("the %s export differs between the streamed and collected paths\n%s",
				format, firstDifference(streamed, collected))
		}
	}
}

// generatedAt matches the moment a report says it was produced - that field
// alone, not every timestamp, so an entry whose start differs between the two
// paths still shows up as a difference.
var generatedAt = regexp.MustCompile(`"generated_at": "[^"]*"`)

// stableExport blanks the one field that is expected to differ.
//
// The two requests are issued a moment apart, so their generated_at stamps are
// a moment apart too. Everything else about them must be identical, and that is
// what is being compared.
func stableExport(document string) string {
	return generatedAt.ReplaceAllString(document, `"generated_at": "<generated>"`)
}

// firstDifference reports where two documents diverge, because "they differ" is
// not enough to act on when the documents are thousands of lines long.
func firstDifference(a, b string) string {
	left, right := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(left), len(right)) {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l != r {
			return fmt.Sprintf("line %d:\n  streamed:  %q\n  collected: %q", i+1, l, r)
		}
	}
	return fmt.Sprintf("no differing line; %d vs %d bytes", len(a), len(b))
}

// setMaxCollectedExportRows lowers the document-format limit for a test.
//
// A variable rather than a constant only so this can exist; nothing in the
// running application changes it.
func setMaxCollectedExportRows(rows int) { maxCollectedExportRows = rows }

// TestStreamedExportReportsAnEarlyFailureAsAnError.
//
// A streamed response commits to its status code with its first byte, so the
// handler pulls one row before setting a single header. Without that, a query
// SQLite or Go refuses - a malformed regular expression is the one a person
// actually types - is answered with 200 and a file containing nothing but a
// header row, which reads as "no results" rather than as "your search was
// wrong".
func TestStreamedExportReportsAnEarlyFailureAsAnError(t *testing.T) {
	srv := seedForPaging(t, 5)

	for _, format := range []string{"csv", "json"} {
		rec := get(t, srv, "/export/"+format+"?"+pagingRange+"&q=%5B&regexp=1")

		if rec.Code != http.StatusBadRequest {
			t.Errorf("/export/%s with a broken pattern = %d, want 400; body: %.120q",
				format, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Content-Type"), format) {
			t.Errorf("/export/%s: a refusal should not be typed as the download it refused (%q)",
				format, rec.Header().Get("Content-Type"))
		}
	}

	// The same query without the regexp flag is an ordinary literal search, and
	// must still stream - the point is to reject what is broken, not to reject
	// a bracket.
	if rec := get(t, srv, "/export/csv?"+pagingRange+"&q=%5B"); rec.Code != http.StatusOK {
		t.Errorf("/export/csv with a literal bracket = %d, want 200", rec.Code)
	}
}
