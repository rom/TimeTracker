package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// QuickAdd parses a single line of text into a time entry.
//
// Logging time is a chore, and the interaction that matters is the one that takes
// two seconds. The grammar is deliberately forgiving, and anything ambiguous is
// returned as a pre-filled draft for the full form rather than guessed at - a
// wrong guess that silently becomes billable time is worse than a second click.
//
//	2h acme/migration fixed the login redirect #travel
//	15m standup
//	9:00-10:30 acme/support prepared the release
//
// Grammar, applied in order:
//
//	a leading duration ("2h", "1.5", "90m") or time range ("9:00-10:30")
//	an assignment reference, matched fuzzily against recent and current work
//	#tag words, collected and removed
//	whatever remains is the note
type QuickAddResult struct {
	// Entry is the parsed input, ready to create.
	Entry EntryInput
	// Assignment is the resolved assignment, if one was matched.
	Assignment domain.Assignment
	// Note and Tags are exposed so the UI can show what was understood.
	Tags []string
	// Ambiguous is set when the text could not be resolved confidently. The
	// caller should open the full form pre-filled rather than create anything.
	Ambiguous bool
	// Reason explains an ambiguous result to the user.
	Reason string
	// Candidates are the assignments that matched, when more than one did.
	Candidates []domain.Assignment
}

// ParseQuickAdd interprets a quick-add line for the acting user.
//
// It resolves assignment names against the user's own recent work first, because
// what someone types as "acme" almost always means the acme thing they were on
// yesterday rather than an alphabetically earlier match.
func (s *Service) ParseQuickAdd(ctx context.Context, input string) (QuickAddResult, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return QuickAddResult{Ambiguous: true, Reason: "nothing to parse"}, nil
	}

	result := QuickAddResult{}
	now := s.now()

	// 1. Tags anywhere in the line.
	var remaining []string
	for _, word := range strings.Fields(text) {
		if strings.HasPrefix(word, "#") && len(word) > 1 {
			result.Tags = append(result.Tags, strings.TrimPrefix(word, "#"))
			continue
		}
		remaining = append(remaining, word)
	}

	// 2. A leading duration or time range.
	var durationSeconds int64
	var startedAt, endedAt *time.Time
	if len(remaining) > 0 {
		first := remaining[0]
		if from, to, ok := parseTimeRange(first, now); ok {
			startedAt, endedAt = &from, &to
			remaining = remaining[1:]
		} else if seconds, err := domain.ParseDuration(first); err == nil && seconds > 0 {
			durationSeconds = seconds
			remaining = remaining[1:]
		}
	}

	if durationSeconds == 0 && startedAt == nil {
		result.Ambiguous = true
		result.Reason = "no duration or time range found at the start of the line"
	}

	// 3. An assignment reference, then the note.
	assignments, err := s.RecentAssignments(ctx, 50)
	if err != nil {
		return QuickAddResult{}, err
	}
	if len(assignments) == 0 {
		// Fall back to everything the user can see; on a fresh installation there
		// is no history to match against.
		if assignments, err = s.Assignments(ctx, 0, false); err != nil {
			return QuickAddResult{}, err
		}
	}

	matched, note, candidates := matchAssignment(remaining, assignments)
	result.Candidates = candidates
	switch {
	case matched.ID != 0:
		result.Assignment = matched
	case len(candidates) > 1:
		result.Ambiguous = true
		result.Reason = fmt.Sprintf("%d assignments match", len(candidates))
	default:
		result.Ambiguous = true
		if result.Reason == "" {
			result.Reason = "no assignment matched"
		}
	}

	entry := EntryInput{
		AssignmentID:    matched.ID,
		Note:            note,
		Billable:        matched.BillableDefault,
		DurationSeconds: durationSeconds,
	}
	switch {
	case startedAt != nil:
		entry.StartedAt = *startedAt
		entry.EndedAt = endedAt
	case durationSeconds > 0:
		// A bare duration means "this much work, ending now", which is how people
		// log time they have just finished.
		entry.StartedAt = now.Add(-time.Duration(durationSeconds) * time.Second)
	default:
		entry.StartedAt = now
	}
	result.Entry = entry
	return result, nil
}

// parseTimeRange reads "9:00-10:30" into two instants on the day of reference.
// A range whose end is before its start is treated as crossing midnight.
func parseTimeRange(s string, reference time.Time) (from, to time.Time, ok bool) {
	fromText, toText, found := strings.Cut(s, "-")
	if !found {
		return time.Time{}, time.Time{}, false
	}
	fromHour, fromMin, ok1 := parseClock(fromText)
	toHour, toMin, ok2 := parseClock(toText)
	if !ok1 || !ok2 {
		return time.Time{}, time.Time{}, false
	}

	loc := reference.Location()
	from = time.Date(reference.Year(), reference.Month(), reference.Day(), fromHour, fromMin, 0, 0, loc)
	to = time.Date(reference.Year(), reference.Month(), reference.Day(), toHour, toMin, 0, 0, loc)
	if !to.After(from) {
		to = to.AddDate(0, 0, 1)
	}
	return from, to, true
}

// parseClock reads "9:00" or "0930" into an hour and minute.
func parseClock(s string) (hour, minute int, ok bool) {
	s = strings.TrimSpace(s)
	if h, m, found := strings.Cut(s, ":"); found {
		if _, err := fmt.Sscanf(h, "%d", &hour); err != nil {
			return 0, 0, false
		}
		if _, err := fmt.Sscanf(m, "%d", &minute); err != nil {
			return 0, 0, false
		}
	} else if len(s) == 4 {
		if _, err := fmt.Sscanf(s, "%2d%2d", &hour, &minute); err != nil {
			return 0, 0, false
		}
	} else {
		return 0, 0, false
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

// matchAssignment finds the assignment the words refer to and returns the rest of
// the line as the note.
//
// It tries progressively looser strategies, and stops at the first that yields
// exactly one match:
//
//  1. a "customer/assignment" style path in one word
//  2. an exact assignment code
//  3. a prefix of the assignment name
//  4. a substring anywhere in the customer/project/assignment path
//
// Several matches at the same level means ambiguity, which is reported rather
// than resolved by picking one.
func matchAssignment(words []string, assignments []domain.Assignment) (domain.Assignment, string, []domain.Assignment) {
	if len(words) == 0 || len(assignments) == 0 {
		return domain.Assignment{}, strings.Join(words, " "), nil
	}

	// The reference is at most the first two words: "acme/migration" as one word,
	// or "acme migration" as two. Beyond that we would start eating the note.
	for wordCount := min(2, len(words)); wordCount >= 1; wordCount-- {
		phrase := strings.ToLower(strings.Join(words[:wordCount], " "))
		note := strings.TrimSpace(strings.Join(words[wordCount:], " "))

		if matches := findMatches(phrase, assignments); len(matches) == 1 {
			return matches[0], note, matches
		} else if len(matches) > 1 && wordCount == 1 {
			// Report the ambiguity from the shortest phrase, which is the one the
			// user most likely intended.
			return domain.Assignment{}, note, matches
		}
	}
	return domain.Assignment{}, strings.Join(words, " "), nil
}

// findMatches applies the matching strategies in order of decreasing confidence.
func findMatches(phrase string, assignments []domain.Assignment) []domain.Assignment {
	// Treat "/" as a separator so "acme/migration" and "acme migration" behave
	// identically.
	normalised := strings.ReplaceAll(phrase, "/", " ")

	var byCode, byPrefix, bySubstring []domain.Assignment
	for _, a := range assignments {
		label := strings.ToLower(a.Label())
		name := strings.ToLower(a.Name)
		code := strings.ToLower(a.Code)

		switch {
		case code != "" && code == normalised:
			byCode = append(byCode, a)
		case strings.HasPrefix(name, normalised):
			byPrefix = append(byPrefix, a)
		case containsAllWords(label, normalised):
			bySubstring = append(bySubstring, a)
		}
	}

	for _, matches := range [][]domain.Assignment{byCode, byPrefix, bySubstring} {
		if len(matches) > 0 {
			return matches
		}
	}
	return nil
}

// containsAllWords reports whether every word of the query appears in the label,
// so "acme mig" matches "Acme AB / Migration / Development".
func containsAllWords(label, query string) bool {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return false
	}
	for _, word := range fields {
		if !strings.Contains(label, word) {
			return false
		}
	}
	return true
}

// QuickAdd parses a line and, if it is unambiguous, creates the entry.
func (s *Service) QuickAdd(ctx context.Context, input string) (domain.TimeEntry, QuickAddResult, error) {
	parsed, err := s.ParseQuickAdd(ctx, input)
	if err != nil {
		return domain.TimeEntry{}, QuickAddResult{}, err
	}
	if parsed.Ambiguous {
		return domain.TimeEntry{}, parsed, nil
	}
	entry, err := s.CreateEntry(ctx, parsed.Entry)
	return entry, parsed, err
}
