package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// TestStreamedCSVMatchesCollected.
//
// The collected writer is implemented on the streaming one, so this now pins
// that arrangement rather than comparing two implementations: if somebody
// reintroduces a separate collected path, this is what notices when the two
// drift. Sorting makes the comparison independent of row order.
func TestStreamedCSVMatchesCollected(t *testing.T) {
	report := sampleReport(t)

	var collected, streamed bytes.Buffer
	if err := WriteCSV(&collected, report); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	if err := WriteCSVStream(&streamed, streamOf(report)); err != nil {
		t.Fatalf("WriteCSVStream: %v", err)
	}

	if sortedLines(collected.String()) != sortedLines(streamed.String()) {
		t.Errorf("the two CSV writers disagree.\ncollected:\n%s\nstreamed:\n%s",
			collected.String(), streamed.String())
	}
	// And the stream really is chronological, which the union accumulator needs.
	rows := strings.Split(strings.TrimSpace(streamed.String()), "\n")[1:]
	var previous string
	for _, row := range rows {
		date := strings.Split(row, ",")[1]
		if previous != "" && date < previous {
			t.Errorf("streamed rows are not in ascending order: %s after %s", date, previous)
		}
		previous = date
	}
}

// TestStreamedJSONMatchesCollected, including every total. As above, this pins
// the two paths together rather than comparing rival implementations.
func TestStreamedJSONMatchesCollected(t *testing.T) {
	report := sampleReport(t)

	var collected, streamed bytes.Buffer
	if err := WriteJSON(&collected, report); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if err := WriteJSONStream(&streamed, streamOf(report)); err != nil {
		t.Fatalf("WriteJSONStream: %v", err)
	}

	var want, got map[string]any
	if err := json.Unmarshal(collected.Bytes(), &want); err != nil {
		t.Fatalf("the collected JSON is invalid: %v", err)
	}
	if err := json.Unmarshal(streamed.Bytes(), &got); err != nil {
		t.Fatalf("the streamed JSON is invalid: %v\n%s", err, streamed.String())
	}

	// The entries may be in a different order; everything else must match
	// exactly, and the totals are the reason this test exists - they are
	// computed twice, once by sorting and once in a single pass.
	wantEntries, gotEntries := want["entries"], got["entries"]
	delete(want, "entries")
	delete(got, "entries")

	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("the envelope or totals differ.\ncollected: %s\nstreamed:  %s",
			wantJSON, gotJSON)
	}

	if len(wantEntries.([]any)) != len(gotEntries.([]any)) {
		t.Errorf("entry counts differ: %d collected, %d streamed",
			len(wantEntries.([]any)), len(gotEntries.([]any)))
	}
}

// TestStreamedJSONIsValidWhenEmpty: an empty array is the case a hand-assembled
// document gets wrong, because the separator logic has nothing to separate.
func TestStreamedJSONIsValidWhenEmpty(t *testing.T) {
	empty := Stream{
		Meta:  Meta{Title: "Nothing", GeneratedAt: time.Now().UTC()},
		Lines: func(yield func(Line, error) bool) {},
	}

	var buf bytes.Buffer
	if err := WriteJSONStream(&buf, empty); err != nil {
		t.Fatalf("WriteJSONStream: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("an empty streamed report is not valid JSON: %v\n%s", err, buf.String())
	}
	if entries, ok := doc["entries"].([]any); !ok || len(entries) != 0 {
		t.Errorf("entries = %v, want an empty array", doc["entries"])
	}
	if totals, ok := doc["totals"].(map[string]any); !ok || totals["entry_count"].(float64) != 0 {
		t.Errorf("totals = %v", doc["totals"])
	}
}

// TestStreamedJSONEscapesMetadata: the envelope is assembled by hand, so a
// title containing a quote is the way to break it.
func TestStreamedJSONEscapesMetadata(t *testing.T) {
	stream := Stream{
		Meta: Meta{
			Title: `Acme "Q3" report \ 2026`,
			User:  "O'Brien <ob@example.com>",
		},
		Lines: func(yield func(Line, error) bool) {},
	}

	var buf bytes.Buffer
	if err := WriteJSONStream(&buf, stream); err != nil {
		t.Fatalf("WriteJSONStream: %v", err)
	}

	var doc struct {
		Title string `json:"title"`
		User  string `json:"user"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("a quoted title broke the document: %v\n%s", err, buf.String())
	}
	if doc.Title != stream.Meta.Title || doc.User != stream.Meta.User {
		t.Errorf("metadata round-tripped as %q / %q", doc.Title, doc.User)
	}
}

// TestStreamPropagatesErrors: a database failure halfway through must stop the
// write rather than produce a document that looks complete.
func TestStreamPropagatesErrors(t *testing.T) {
	failing := Stream{
		Meta: Meta{Title: "Doomed"},
		Lines: func(yield func(Line, error) bool) {
			yield(Line{ID: 1, Status: string(domain.StatusConfirmed)}, nil)
			yield(Line{}, errBoom)
		},
	}

	for name, write := range map[string]func(*bytes.Buffer, Stream) error{
		"csv":  func(b *bytes.Buffer, s Stream) error { return WriteCSVStream(b, s) },
		"json": func(b *bytes.Buffer, s Stream) error { return WriteJSONStream(b, s) },
	} {
		var buf bytes.Buffer
		if err := write(&buf, failing); err == nil {
			t.Errorf("%s: a failing stream wrote successfully", name)
		}
	}
}

var errBoom = errBoomType{}

type errBoomType struct{}

func (errBoomType) Error() string { return "the database went away" }

// TestStreamRefusesOutOfOrderEntries.
//
// The elapsed total is folded in one pass, which is only correct for intervals
// in ascending order. If the query behind it ever stops ordering that way the
// total is silently wrong, so the writer refuses instead.
func TestStreamRefusesOutOfOrderEntries(t *testing.T) {
	base := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	backwards := Stream{
		Meta: Meta{Title: "Backwards"},
		Lines: func(yield func(Line, error) bool) {
			confirmed := string(domain.StatusConfirmed)
			yield(Line{ID: 1, Start: base.Add(4 * time.Hour), Seconds: 3600, Status: confirmed}, nil)
			yield(Line{ID: 2, Start: base, Seconds: 3600, Status: confirmed}, nil)
		},
	}

	var buf bytes.Buffer
	if err := WriteJSONStream(&buf, backwards); err == nil {
		t.Error("entries out of order should be refused, not totalled wrongly")
	}
}

// TestPrimedSeparatesAnEarlyFailureFromAnEmptyRange.
//
// Priming exists so a handler can still choose a status code. The three cases
// it has to tell apart are a failure before any row, no rows at all, and rows -
// and the second must not be reported as the first, because an empty week is a
// valid answer rather than an error.
func TestPrimedSeparatesAnEarlyFailureFromAnEmptyRange(t *testing.T) {
	failure := errors.New("no such column")
	if _, err := Primed(func(yield func(Line, error) bool) {
		yield(Line{}, failure)
	}); !errors.Is(err, failure) {
		t.Errorf("an error on the first row should be returned, got %v", err)
	}

	empty, err := Primed(func(func(Line, error) bool) {})
	if err != nil {
		t.Fatalf("an empty range is not a failure: %v", err)
	}
	for range empty {
		t.Error("an empty range yielded a line")
	}

	full, err := Primed(func(yield func(Line, error) bool) {
		for id := int64(1); id <= 3; id++ {
			if !yield(Line{ID: id}, nil) {
				return
			}
		}
	})
	if err != nil {
		t.Fatalf("Primed: %v", err)
	}
	var ids []int64
	for line, err := range full {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		ids = append(ids, line.ID)
	}
	// The pulled row has to be yielded again rather than consumed, and in its
	// original place.
	if len(ids) != 3 || ids[0] != 1 || ids[2] != 3 {
		t.Errorf("lines = %v, want the whole sequence in order", ids)
	}
}

// TestPrimedStopsTheSourceWhenTheConsumerDoes.
//
// The wrapped sequence is pulled, so it is running independently and has to be
// stopped: a client that abandons a download halfway must not leave a database
// cursor open for the life of the process.
func TestPrimedStopsTheSourceWhenTheConsumerDoes(t *testing.T) {
	released := false
	lines, err := Primed(func(yield func(Line, error) bool) {
		defer func() { released = true }()
		for id := int64(1); ; id++ {
			if !yield(Line{ID: id}, nil) {
				return
			}
		}
	})
	if err != nil {
		t.Fatalf("Primed: %v", err)
	}

	for range lines {
		break
	}
	if !released {
		t.Error("abandoning the stream left the source running")
	}
}

// sortedLines makes a comparison independent of row order.
func sortedLines(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := range lines {
		for j := i + 1; j < len(lines); j++ {
			if lines[j] < lines[i] {
				lines[i], lines[j] = lines[j], lines[i]
			}
		}
	}
	return strings.Join(lines, "\n")
}
