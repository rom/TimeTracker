package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Backup and restore.
//
// A backup is a single JSON file. Not a database copy: a copy of the SQLite file
// is opaque, cannot be partial, and cannot be restored into an instance that has
// moved on. JSON can be inspected, diffed, kept in version control if someone
// wants to, and merged rather than replacing everything.
//
// The decision that shapes restore: it **merges**. A record already present is
// skipped rather than duplicated, so restoring the same file twice is harmless
// and restoring an old backup does not delete newer work. The alternative -
// replace everything - turns a mistaken restore into data loss, which is exactly
// the situation someone reaching for a backup is already in.

// BackupFormatVersion identifies the shape of the file. Bumped only when a field
// changes meaning or disappears, never when one is added.
const BackupFormatVersion = 1

// Backup is the file's contents.
type Backup struct {
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
	// Application records which version wrote it, which matters when a restore
	// goes wrong months later.
	Application string `json:"application"`
	// Scope describes what was included, so a partial backup announces itself
	// rather than looking like a complete one that lost most of its data.
	Scope BackupScope `json:"scope"`

	Customers   []domain.Customer   `json:"customers"`
	Projects    []domain.Project    `json:"projects"`
	Assignments []domain.Assignment `json:"assignments"`
	TimeEntries []BackupEntry       `json:"time_entries"`
	Expenses    []domain.Expense    `json:"expenses"`
}

// BackupScope records what a backup covers.
type BackupScope struct {
	Everything bool   `json:"everything"`
	CustomerID int64  `json:"customer_id,omitempty"`
	ProjectID  int64  `json:"project_id,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	// Description is a human-readable summary, for the file listing.
	Description string `json:"description"`
}

// BackupEntry is a time entry in the backup file.
//
// It is a separate shape from domain.TimeEntry because a backup refers to
// catalogue records by *name*, not by database id. Ids are meaningless in
// another instance, and restoring by id would either collide with existing
// records or silently attach work to the wrong customer.
type BackupEntry struct {
	Customer   string     `json:"customer"`
	Project    string     `json:"project"`
	Assignment string     `json:"assignment"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Seconds    int64      `json:"duration_seconds"`
	Note       string     `json:"note,omitempty"`
	Billable   bool       `json:"billable"`
	Status     string     `json:"status"`
	TimeZone   string     `json:"time_zone"`
	// The billing snapshot travels with the entry, so a restored timesheet
	// still says what it was worth rather than being recomputed against
	// today's rates.
	RoundingRule    string `json:"rounding_rule,omitempty"`
	BillableSeconds int64  `json:"billable_seconds,omitempty"`
	RateMinor       int64  `json:"rate_minor,omitempty"`
	AmountMinor     int64  `json:"amount_minor,omitempty"`
	Currency        string `json:"currency,omitempty"`
}

// BackupOptions narrows what a backup covers.
type BackupOptions struct {
	CustomerID int64
	ProjectID  int64
	From       time.Time
	To         time.Time
}

// CreateBackup assembles a backup of the acting user's data.
func (s *Service) CreateBackup(ctx context.Context, opts BackupOptions) (Backup, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return Backup{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, listResource(ctx, "time_entry")); err != nil {
		return Backup{}, err
	}

	backup := Backup{
		FormatVersion: BackupFormatVersion,
		CreatedAt:     s.now().UTC(),
		Application:   "timetracker",
		Scope:         describeScope(opts),
	}

	// The catalogue is included whole where the scope allows, because a backup
	// missing the customer its entries refer to cannot be restored.
	if backup.Customers, err = s.Customers(ctx, true); err != nil {
		return Backup{}, err
	}
	if backup.Projects, err = s.Projects(ctx, opts.CustomerID, true); err != nil {
		return Backup{}, err
	}
	if backup.Assignments, err = s.Assignments(ctx, opts.ProjectID, true); err != nil {
		return Backup{}, err
	}

	// Narrow the catalogue to the scope, so a single-customer backup does not
	// carry every other client's project names.
	if opts.CustomerID != 0 {
		backup.Customers = filterCustomers(backup.Customers, opts.CustomerID)
	}

	entries, err := s.Entries(ctx, EntryFilter{
		From:       opts.From,
		To:         opts.To,
		CustomerID: opts.CustomerID,
		ProjectID:  opts.ProjectID,
		// A backup is not a screen: no limit, because a truncated backup is
		// worse than a slow one.
		Limit: 0,
	})
	if err != nil {
		return Backup{}, err
	}
	for _, entry := range entries {
		backup.TimeEntries = append(backup.TimeEntries, BackupEntry{
			Customer:        entry.CustomerName,
			Project:         entry.ProjectName,
			Assignment:      entry.AssignmentName,
			StartedAt:       entry.StartedAt,
			EndedAt:         entry.EndedAt,
			Seconds:         entry.DurationSeconds,
			Note:            entry.Note,
			Billable:        entry.Billable,
			Status:          string(entry.Status),
			TimeZone:        entry.TimeZone,
			RoundingRule:    entry.RoundingRuleApplied,
			BillableSeconds: entry.BillableSeconds,
			RateMinor:       entry.RateMinor,
			AmountMinor:     entry.AmountMinor,
			Currency:        entry.Currency,
		})
	}

	if backup.Expenses, err = s.Expenses(ctx, ExpenseFilter{
		From: opts.From, To: opts.To,
		CustomerID: opts.CustomerID, ProjectID: opts.ProjectID,
	}); err != nil {
		return Backup{}, err
	}

	s.auditLog(ctx, "backup.create", "backup", 0)
	_ = actor
	return backup, nil
}

// WriteBackup streams a backup as JSON.
func (s *Service) WriteBackup(ctx context.Context, w io.Writer, opts BackupOptions) error {
	backup, err := s.CreateBackup(ctx, opts)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	// Indented: a backup someone may need to inspect by hand at three in the
	// morning is worth the extra bytes.
	encoder.SetIndent("", "  ")
	return encoder.Encode(backup)
}

// RestoreResult reports what a restore did.
type RestoreResult struct {
	Customers   int
	Projects    int
	Assignments int
	Entries     int
	Expenses    int
	// Skipped counts records that were already present. A high number here is
	// normal and expected when restoring a file that overlaps existing data.
	Skipped int
	// Problems lists what could not be restored, so a partial result is visible.
	Problems []string
}

// Total returns how many records were created.
func (r RestoreResult) Total() int {
	return r.Customers + r.Projects + r.Assignments + r.Entries + r.Expenses
}

// Restore merges a backup file into the acting user's data.
func (s *Service) Restore(ctx context.Context, r io.Reader) (RestoreResult, error) {
	if _, err := auth.MustUser(ctx); err != nil {
		return RestoreResult{}, err
	}

	var backup Backup
	decoder := json.NewDecoder(io.LimitReader(r, 256<<20))
	if err := decoder.Decode(&backup); err != nil {
		return RestoreResult{}, fmt.Errorf("%w: the backup file could not be read: %s",
			ErrValidation, err)
	}
	if backup.FormatVersion == 0 {
		return RestoreResult{}, fmt.Errorf(
			"%w: this does not look like a TimeTracker backup", ErrValidation)
	}
	if backup.FormatVersion > BackupFormatVersion {
		// Refusing is the honest answer: a newer file may carry fields this
		// version would silently drop, and a restore that quietly loses data is
		// the worst possible outcome for a backup.
		return RestoreResult{}, fmt.Errorf(
			"%w: this backup was written by a newer version (format %d, this understands %d); "+
				"upgrade before restoring", ErrValidation, backup.FormatVersion, BackupFormatVersion)
	}

	result := RestoreResult{}

	// Catalogue first, and by name: ids from another instance mean nothing here.
	customerIDs, err := s.restoreCustomers(ctx, backup, &result)
	if err != nil {
		return result, err
	}
	projectIDs, err := s.restoreProjects(ctx, backup, customerIDs, &result)
	if err != nil {
		return result, err
	}
	assignmentIDs, err := s.restoreAssignments(ctx, backup, projectIDs, &result)
	if err != nil {
		return result, err
	}
	if err := s.restoreEntries(ctx, backup, assignmentIDs, &result); err != nil {
		return result, err
	}

	if err := s.recordAudit(ctx, "backup.restore", "backup", 0, map[string]any{
		"customers": result.Customers, "projects": result.Projects,
		"assignments": result.Assignments, "entries": result.Entries,
		"skipped": result.Skipped,
	}); err != nil {
		return result, err
	}
	return result, nil
}

// restoreCustomers creates missing customers and returns a name-to-id map.
func (s *Service) restoreCustomers(ctx context.Context, backup Backup, result *RestoreResult) (map[string]int64, error) {
	existing, err := s.Customers(ctx, true)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, c := range existing {
		ids[strings.ToLower(c.Name)] = c.ID
	}

	for _, c := range backup.Customers {
		key := strings.ToLower(c.Name)
		if _, present := ids[key]; present {
			result.Skipped++
			continue
		}
		created, err := s.CreateCustomer(ctx, domain.Customer{
			Name: c.Name, Code: c.Code, Currency: c.Currency,
			ColourKey: c.ColourKey, Icon: c.Icon, Notes: c.Notes, RateMinor: c.RateMinor,
		})
		if err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("customer %q: %s", c.Name, err))
			continue
		}
		ids[key] = created.ID
		result.Customers++
	}
	return ids, nil
}

// restoreProjects creates missing projects, keyed by customer and name.
func (s *Service) restoreProjects(ctx context.Context, backup Backup, customerIDs map[string]int64, result *RestoreResult) (map[string]int64, error) {
	existing, err := s.Projects(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, p := range existing {
		ids[projectKey(p.CustomerName, p.Name)] = p.ID
	}

	for _, p := range backup.Projects {
		key := projectKey(p.CustomerName, p.Name)
		if _, present := ids[key]; present {
			result.Skipped++
			continue
		}
		customerID, ok := customerIDs[strings.ToLower(p.CustomerName)]
		if !ok {
			result.Problems = append(result.Problems,
				fmt.Sprintf("project %q refers to a customer that is not in the backup", p.Name))
			continue
		}
		created, err := s.CreateProject(ctx, domain.Project{
			CustomerID: customerID, Name: p.Name, Code: p.Code,
			ColourKey: p.ColourKey, Icon: p.Icon, BillableDefault: p.BillableDefault,
			RateMinor: p.RateMinor, RoundingRule: p.RoundingRule,
			BudgetSeconds: p.BudgetSeconds, BudgetMinor: p.BudgetMinor,
		})
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("project %q: %s", p.Name, err))
			continue
		}
		ids[key] = created.ID
		result.Projects++
	}
	return ids, nil
}

// restoreAssignments creates missing assignments.
func (s *Service) restoreAssignments(ctx context.Context, backup Backup, projectIDs map[string]int64, result *RestoreResult) (map[string]int64, error) {
	existing, err := s.Assignments(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, a := range existing {
		ids[assignmentKey(a.CustomerName, a.ProjectName, a.Name)] = a.ID
	}

	for _, a := range backup.Assignments {
		key := assignmentKey(a.CustomerName, a.ProjectName, a.Name)
		if _, present := ids[key]; present {
			result.Skipped++
			continue
		}
		projectID, ok := projectIDs[projectKey(a.CustomerName, a.ProjectName)]
		if !ok {
			result.Problems = append(result.Problems,
				fmt.Sprintf("assignment %q refers to a project that is not in the backup", a.Name))
			continue
		}
		created, err := s.CreateAssignment(ctx, domain.Assignment{
			ProjectID: projectID, Name: a.Name, Code: a.Code,
			ColourKey: a.ColourKey, Icon: a.Icon, BillableDefault: a.BillableDefault,
			RateMinor: a.RateMinor, SortOrder: a.SortOrder, Favourite: a.Favourite,
		})
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("assignment %q: %s", a.Name, err))
			continue
		}
		ids[key] = created.ID
		result.Assignments++
	}
	return ids, nil
}

// restoreEntries creates time entries that are not already present.
//
// "Already present" is decided by the subject, the assignment and the start
// instant: two entries at the same second on the same assignment for the same
// person are the same entry, and restoring a file twice must not double
// somebody's week.
func (s *Service) restoreEntries(ctx context.Context, backup Backup, assignmentIDs map[string]int64, result *RestoreResult) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}

	existing, err := s.Entries(ctx, EntryFilter{})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range existing {
		seen[entryKey(e.AssignmentID, e.StartedAt)] = true
	}

	for _, e := range backup.TimeEntries {
		assignmentID, ok := assignmentIDs[assignmentKey(e.Customer, e.Project, e.Assignment)]
		if !ok {
			result.Problems = append(result.Problems, fmt.Sprintf(
				"an entry refers to %s / %s / %s, which is not in the backup",
				e.Customer, e.Project, e.Assignment))
			continue
		}
		if seen[entryKey(assignmentID, e.StartedAt)] {
			result.Skipped++
			continue
		}

		entry := domain.TimeEntry{
			UserID: actor.ID, EnteredBy: actor.ID, AssignmentID: assignmentID,
			StartedAt: e.StartedAt, EndedAt: e.EndedAt, DurationSeconds: e.Seconds,
			Note: e.Note, Billable: e.Billable, TimeZone: e.TimeZone,
			Status:              domain.EntryStatus(e.Status),
			RoundingRuleApplied: e.RoundingRule, BillableSeconds: e.BillableSeconds,
			RateMinor: e.RateMinor, AmountMinor: e.AmountMinor, Currency: e.Currency,
		}
		if entry.Status == "" {
			entry.Status = domain.StatusConfirmed
		}
		if entry.TimeZone == "" {
			entry.TimeZone = "UTC"
		}
		if err := entry.Validate(); err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("an entry was invalid: %s", err))
			continue
		}

		if _, err := s.db.CreateEntry(ctx, entry); err != nil {
			return err
		}
		seen[entryKey(assignmentID, e.StartedAt)] = true
		result.Entries++
	}
	return nil
}

// --------------------------------------------------------------- files ------

// BackupFile describes a backup on disk.
type BackupFile struct {
	Name      string
	Path      string
	SizeBytes int64
	CreatedAt time.Time
}

// WriteBackupFile writes a backup into the backup directory and returns it.
func (s *Service) WriteBackupFile(ctx context.Context, dir string, opts BackupOptions) (BackupFile, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BackupFile{}, fmt.Errorf("create backup directory: %w", err)
	}

	name := fmt.Sprintf("timetracker-%s.json", s.now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)

	// Written to a temporary file and renamed, so an interrupted run never
	// leaves a truncated file that looks like a usable backup.
	temp, err := os.CreateTemp(dir, ".backup-*")
	if err != nil {
		return BackupFile{}, fmt.Errorf("create backup file: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}()

	if err := s.WriteBackup(ctx, temp, opts); err != nil {
		return BackupFile{}, err
	}
	// A backup still in the page cache is no protection against the crash it
	// exists for.
	if err := temp.Sync(); err != nil {
		return BackupFile{}, fmt.Errorf("flush backup: %w", err)
	}
	if err := temp.Close(); err != nil {
		return BackupFile{}, fmt.Errorf("close backup: %w", err)
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return BackupFile{}, err
	}
	if err := os.Rename(tempName, path); err != nil {
		return BackupFile{}, fmt.Errorf("store backup: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return BackupFile{}, err
	}
	return BackupFile{
		Name: name, Path: path, SizeBytes: info.Size(), CreatedAt: info.ModTime(),
	}, nil
}

// ListBackupFiles returns the backups on disk, newest first.
func (s *Service) ListBackupFiles(dir string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []BackupFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, BackupFile{
			Name: entry.Name(), Path: filepath.Join(dir, entry.Name()),
			SizeBytes: info.Size(), CreatedAt: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].CreatedAt.After(files[j].CreatedAt) })
	return files, nil
}

// PruneBackups keeps the newest n backups and removes the rest.
func (s *Service) PruneBackups(dir string, keep int) (int, error) {
	if keep < 1 {
		// Never interpret a misconfiguration as "delete everything".
		return 0, nil
	}
	files, err := s.ListBackupFiles(dir)
	if err != nil || len(files) <= keep {
		return 0, err
	}
	removed := 0
	for _, file := range files[keep:] {
		if err := os.Remove(file.Path); err == nil {
			removed++
		}
	}
	return removed, nil
}

// --------------------------------------------------------------- helpers ----

func describeScope(opts BackupOptions) BackupScope {
	scope := BackupScope{
		CustomerID: opts.CustomerID,
		ProjectID:  opts.ProjectID,
	}
	var parts []string
	if opts.CustomerID != 0 {
		parts = append(parts, fmt.Sprintf("customer %d", opts.CustomerID))
	}
	if opts.ProjectID != 0 {
		parts = append(parts, fmt.Sprintf("project %d", opts.ProjectID))
	}
	if !opts.From.IsZero() {
		scope.From = opts.From.Format("2006-01-02")
		parts = append(parts, "from "+scope.From)
	}
	if !opts.To.IsZero() {
		scope.To = opts.To.Format("2006-01-02")
		parts = append(parts, "to "+scope.To)
	}
	if len(parts) == 0 {
		scope.Everything = true
		scope.Description = "everything"
	} else {
		scope.Description = strings.Join(parts, ", ")
	}
	return scope
}

func filterCustomers(customers []domain.Customer, id int64) []domain.Customer {
	for _, c := range customers {
		if c.ID == id {
			return []domain.Customer{c}
		}
	}
	return nil
}

func projectKey(customer, project string) string {
	return strings.ToLower(customer) + "\x00" + strings.ToLower(project)
}

func assignmentKey(customer, project, assignment string) string {
	return projectKey(customer, project) + "\x00" + strings.ToLower(assignment)
}

func entryKey(assignmentID int64, started time.Time) string {
	return fmt.Sprintf("%d\x00%d", assignmentID, started.UTC().Unix())
}
