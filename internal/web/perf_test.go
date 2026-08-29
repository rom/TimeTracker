//go:build perf

package web

// The ASR-012 budgets, measured.
//
// This file is tagged `perf` and excluded from every ordinary run, because
// seeding a hundred thousand entries takes long enough that nobody would run
// the fast suite if it were included. `make test-perf` runs it.
//
// It measures through the HTTP handler rather than against the store, and that
// is the point rather than convenience: ASR-012's rationale is that it
// "validates the server-rendered choice - a page render must be cheap enough to
// be the response to a button press". A store benchmark would answer a
// different question and leave template rendering unmeasured.
//
// Every figure is logged whether or not it passes, so a run is a trend as well
// as a gate. The budgets are asserted, because a fit criterion nothing checks is
// a sentence rather than a requirement - which is exactly what this file existed
// to fix: `make test-perf` ran `-run TestPerf` against no such test and passed
// in silence.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/service"
	"github.com/rom/timetracker/internal/store"
)

// The dataset and the budgets, from ASR-012.
const (
	perfEntries     = 100_000
	perfInteractive = 100 * time.Millisecond
	perfReport      = 2 * time.Second

	// perfListing was this project's own budget, set at 250 ms when the entries
	// screen rendered up to a thousand rows at once. It is paged at fifty now,
	// which is an interactive screen by any reading, so it is held to the same
	// budget as the others.
	perfListing = perfInteractive

	// Spread over three years, which is the "realistic multi-year dataset" the
	// requirement names. At five assignments a day it is about what a busy
	// consultancy accumulates.
	perfYears = 3
)

// TestPerfBudgets is the whole suite: one expensive fixture, several
// measurements against it.
func TestPerfBudgets(t *testing.T) {
	srv, fixture := newPerfServer(t)

	t.Run("day view", func(t *testing.T) {
		// A day in the middle of the range, so the query is not answered by
		// hitting the end of an index.
		day := fixture.middle.Format("2006-01-02")
		measure(t, "GET /today", perfInteractive, 200, func() {
			mustGet(t, srv, "/today?date="+day)
		})
	})

	t.Run("week view", func(t *testing.T) {
		day := fixture.middle.Format("2006-01-02")
		measure(t, "GET /week", perfInteractive, 200, func() {
			mustGet(t, srv, "/week?date="+day)
		})
	})

	t.Run("entries list", func(t *testing.T) {
		// Not named in the fit criterion, but it is the screen every export is
		// taken from and the one most likely to degrade as the table grows.
		from := fixture.middle.AddDate(0, -1, 0).Format("2006-01-02")
		to := fixture.middle.Format("2006-01-02")
		measure(t, "GET /entries (one month)", perfListing, 100, func() {
			mustGet(t, srv, "/entries?from="+from+"&to="+to)
		})
	})

	t.Run("timer start and stop", func(t *testing.T) {
		// Measured as a pair: a start with no matching stop would leave a
		// running timer on every iteration, and the running-timer bar rendered
		// on every page would then be measuring something else entirely.
		measure(t, "POST /timers/start + stop", perfInteractive, 100, func() {
			rec := post(t, srv, "/timers/start", url.Values{
				"assignment_id": {strconv.FormatInt(fixture.assignmentID, 10)},
			})
			if rec.Code >= 400 {
				t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
			}
			running := runningEntryID(t, srv)
			rec = post(t, srv, "/timers/"+strconv.FormatInt(running, 10)+"/stop", url.Values{})
			if rec.Code >= 400 {
				t.Fatalf("stop = %d: %s", rec.Code, rec.Body.String())
			}
		})
	})

	t.Run("multi-year export streams", func(t *testing.T) {
		// The whole dataset - three years, a hundred thousand entries - in the
		// two formats that stream. What is being measured is not the clock but
		// the heap: a collected export holds every entry and then every report
		// line, so its peak grows with the range. A streamed one should not.
		from := fixture.earliest.Format("2006-01-02")
		to := fixture.latest.AddDate(0, 0, 1).Format("2006-01-02")

		for _, format := range []string{"csv", "json"} {
			peak, bytes, elapsed := measureExport(t, srv,
				"/export/"+format+"?from="+from+"&to="+to)

			t.Logf("%-32s %s of output in %s, peak heap %s",
				"GET /export/"+format+" (3 years)",
				humanBytes(bytes), elapsed.Round(time.Millisecond), humanBytes(int64(peak)))

			if bytes == 0 {
				t.Fatalf("%s produced nothing", format)
			}
			if peak > maxStreamHeap {
				t.Errorf("%s: peak heap grew to %s exporting %d entries.\n"+
					"A streamed export should not hold the range it is exporting; "+
					"the budget is %s.",
					format, humanBytes(int64(peak)), perfEntries, humanBytes(int64(maxStreamHeap)))
			}
		}
	})

	t.Run("one-year report", func(t *testing.T) {
		from := fixture.middle.AddDate(-1, 0, 0).Format("2006-01-02")
		to := fixture.middle.Format("2006-01-02")
		// Five iterations: each one walks a year of entries, and the budget is
		// two seconds rather than a hundred milliseconds.
		measure(t, "GET /export/json (one year)", perfReport, 5, func() {
			mustGet(t, srv, "/export/json?from="+from+"&to="+to)
		})
	})
}

// measure runs an operation repeatedly and asserts its 95th percentile.
func measure(t *testing.T, what string, budget time.Duration, iterations int, run func()) {
	t.Helper()

	// A warm-up outside the measurement. The first call through a handler pays
	// for query planning and whatever the page allocates once; charging that to
	// the percentile would measure start-up rather than steady state.
	run()

	timings := make([]time.Duration, 0, iterations)
	for range iterations {
		start := time.Now()
		run()
		timings = append(timings, time.Since(start))
	}
	slices.Sort(timings)

	p50 := percentile(timings, 0.50)
	p95 := percentile(timings, 0.95)
	worst := timings[len(timings)-1]

	t.Logf("%-32s n=%d  p50=%-8s p95=%-8s max=%-8s budget=%s",
		what, iterations,
		p50.Round(time.Microsecond*100),
		p95.Round(time.Microsecond*100),
		worst.Round(time.Microsecond*100),
		budget)

	if p95 > budget {
		t.Errorf("%s: p95 is %s, over its %s budget.\n"+
			"If this is a slow or shared machine rather than a regression, say so "+
			"explicitly - the budget is 'commodity hardware', and moving it is a "+
			"decision rather than a fix.", what, p95.Round(time.Microsecond*100), budget)
	}
}

// maxStreamHeap is what a streamed export may add to the heap.
//
// Sixty-four megabytes. A hundred thousand entries collected into a slice, and
// then into report lines, is several hundred - so this is not a tight budget,
// it is a bright line between "bounded" and "proportional to the export". It is
// stated in those terms deliberately: a tighter number would fail on a garbage
// collector's whim and teach everyone to raise it.
const maxStreamHeap = 64 << 20

// measureExport runs one export and reports the peak heap it added.
//
// Sampled from another goroutine rather than read before and after, because the
// interesting number is the high-water mark during the response and a reading
// taken afterwards has already had the garbage collected out of it.
func measureExport(t *testing.T, srv *Server, path string) (peak uint64, written int64, elapsed time.Duration) {
	t.Helper()

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	// Two channels, not one: stop is closed by the caller to ask the sampler to
	// finish, and stopped is closed by the sampler to say it has. Sharing one
	// meant closing an already-closed channel, which panics.
	stop := make(chan struct{})
	stopped := make(chan struct{})
	var highest atomic.Uint64
	go func() {
		defer close(stopped)
		var stats runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.ReadMemStats(&stats)
			if stats.HeapAlloc > baseline.HeapAlloc {
				if grown := stats.HeapAlloc - baseline.HeapAlloc; grown > highest.Load() {
					highest.Store(grown)
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()

	started := time.Now()
	// The body is counted and discarded rather than recorded: keeping a
	// multi-megabyte response would be the test doing the buffering it is
	// checking for.
	recorder := &countingWriter{header: http.Header{}}
	srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	elapsed = time.Since(started)

	close(stop)
	<-stopped

	if recorder.status != 0 && recorder.status != http.StatusOK {
		t.Fatalf("GET %s = %d", path, recorder.status)
	}
	return highest.Load(), recorder.written, elapsed
}

// countingWriter is an http.ResponseWriter that counts and drops the body.
type countingWriter struct {
	header  http.Header
	status  int
	written int64
}

func (c *countingWriter) Header() http.Header { return c.header }
func (c *countingWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *countingWriter) Write(p []byte) (int, error) {
	c.written += int64(len(p))
	return len(p), nil
}

// percentile returns the value at a fraction through a sorted slice.
func percentile(sorted []time.Duration, fraction float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	// Nearest-rank: the smallest value at or above the fraction. For 200
	// samples at 0.95 that is the 190th, which is what "95th percentile"
	// ordinarily means outside a statistics textbook.
	index := int(float64(len(sorted))*fraction+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

// --------------------------------------------------------------- fixture ----

type perfFixture struct {
	assignmentID int64
	// middle is a date in the centre of the seeded range.
	middle time.Time
	// actor is a context carrying the seeded user, and svc the service behind
	// the handlers, for measurements that call it directly.
	actor context.Context
	svc   *service.Service
	// earliest and latest bound the seeded range, for the whole-dataset export.
	earliest, latest time.Time
}

// newPerfServer builds a server over a database holding perfEntries entries.
func newPerfServer(t *testing.T) (*Server, perfFixture) {
	t.Helper()

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "perf.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(db, auth.SingleUserAuthorizer{}, logger, time.Now)

	user, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Perf User", Role: domain.RoleAdmin,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	actor := auth.WithUser(ctx, user)

	// Reminders from midnight, so the end-of-day window is open whatever time
	// the suite runs and the day view is measured doing its most expensive
	// work. Left at the default of four in the afternoon, this suite would have
	// measured the cheap version of the screen every morning and the real one
	// every evening, which is a budget that passes or fails by the clock.
	settings, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	settings.RemindersEnabled = true
	settings.ReminderHour = 0
	if err := db.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("set the reminder hour: %v", err)
	}

	customer, err := svc.CreateCustomer(actor, domain.Customer{
		Name: "Acme AB", Currency: "SEK", RateMinor: 125000,
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	project, err := svc.CreateProject(actor, domain.Project{
		CustomerID: customer.ID, Name: "Migration", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Five assignments, because a realistic day is split across about that many
	// and a single one would let the day view's join hit one row per lookup.
	var assignments []domain.Assignment
	for _, name := range []string{"Development", "Support", "Meetings", "Review", "Admin"} {
		assignment, err := svc.CreateAssignment(actor, domain.Assignment{
			ProjectID: project.ID, Name: name, BillableDefault: true,
		})
		if err != nil {
			t.Fatalf("create assignment: %v", err)
		}
		assignments = append(assignments, assignment)
	}

	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(-perfYears, 0, 0)
	seedEntries(t, db, user.ID, assignments, start, end)

	srv, err := New(svc, config.Config{Mode: config.ModeLocal}, logger, Options{
		Identity: func(r *http.Request) (domain.User, error) {
			return db.GetUser(r.Context(), user.ID)
		},
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}

	return srv, perfFixture{
		assignmentID: assignments[0].ID,
		middle:       start.AddDate(0, 0, int(end.Sub(start).Hours()/24/2)),
		actor:        actor,
		svc:          svc,
		earliest:     start,
		latest:       end,
	}
}

// seedEntries writes the dataset directly, in batched transactions.
//
// Not through the service: a hundred thousand service calls would each write an
// audit row and re-read the catalogue, which would take long enough that nobody
// would run this, and would measure the seeding rather than the screens.
func seedEntries(t *testing.T, db *store.DB, userID int64, assignments []domain.Assignment, start, end time.Time) {
	t.Helper()

	began := time.Now()
	days := int(end.Sub(start).Hours() / 24)
	if days <= 0 {
		t.Fatal("the seeded range is empty")
	}

	// SQLite has one writer, and a transaction held open too long blocks
	// everything else - so the work is batched rather than done in one.
	const batch = 5_000
	written := 0

	for written < perfEntries {
		remaining := perfEntries - written
		size := min(batch, remaining)

		err := db.InTx(context.Background(), func(tx *sql.Tx) error {
			for i := range size {
				n := written + i
				// Spread evenly across the range and across the day, so no
				// single day holds an implausible number and the day view is
				// measured against a realistic one.
				day := start.AddDate(0, 0, n%days)
				hour := 8 + (n/days)%9
				started := day.Add(time.Duration(hour) * time.Hour)
				ended := started.Add(time.Hour)

				entry := domain.TimeEntry{
					UserID:       userID,
					EnteredBy:    userID,
					AssignmentID: assignments[n%len(assignments)].ID,
					StartedAt:    started,
					EndedAt:      &ended,
					// Whole hours: the arithmetic is not what is being measured.
					DurationSeconds: 3600,
					Note:            "seeded entry " + strconv.Itoa(n),
					Billable:        true,
					Status:          domain.StatusConfirmed,
					TimeZone:        "UTC",
					BillableSeconds: 3600,
					RateMinor:       125000,
					AmountMinor:     125000,
					Currency:        "SEK",
				}
				if _, err := store.CreateEntryTx(context.Background(), tx, entry); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("seed entries: %v", err)
		}
		written += size
	}

	t.Logf("seeded %d entries across %d days in %s",
		written, days, time.Since(began).Round(time.Millisecond))
}

// ------------------------------------------------------------- utilities ----

func mustGet(t *testing.T, srv *Server, path string) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
}

// runningEntryID finds the timer just started, so it can be stopped again.
func runningEntryID(t *testing.T, srv *Server) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/today", nil))
	body := rec.Body.String()

	marker := `action="/timers/`
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatal("no running timer on the page after starting one")
	}
	rest := body[index+len(marker):]
	end := strings.Index(rest, "/stop")
	if end < 0 {
		t.Fatal("a timer form with no stop action")
	}
	id, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		t.Fatalf("timer id %q: %v", rest[:end], err)
	}
	return id
}
