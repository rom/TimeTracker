package repocheck

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// ASR-003: one binary, no runtime dependencies, no installation step.
//
// "Download it, run it, it works" is the requirement, and until now nothing
// tested it. The store and service suites apply the migrations from empty
// against a fresh database on every run, which is the substance of the claim -
// but they do it from inside the test binary, with the module's dependencies
// already resolved and the working directory full of source. None of them would
// notice a template that failed to embed, an asset served from disk, a
// configuration file the binary refused to start without, or a data directory it
// expected somebody to create first.
//
// This one builds the real binary and runs it the way a person would: in an
// empty directory, with no configuration, no arguments beyond where to listen,
// and an environment stripped of everything the application knows how to read.
// Then it asks the running server for a page and shuts it down.
//
// It costs a compile and about a second, so it is skipped under -short.

// TestTheBinaryStartsInAnEmptyDirectory.
func TestTheBinaryStartsInAnEmptyDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	binary := buildBinary(t)

	// An empty directory and nothing else. The environment is stripped rather
	// than inherited: every TT_ variable is a way for the machine running the
	// test to configure the binary, and a test that passed only because the
	// developer's shell had TT_DATA_DIR set would prove nothing.
	dataDir := t.TempDir()
	address := freeAddress(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, binary,
		"--mode=local", "--addr="+address, "--data-dir="+dataDir)
	command.Dir = t.TempDir() // not the source tree, and not the data directory
	command.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=", // nothing to shell out to
	}
	var output strings.Builder
	command.Stdout = &output
	command.Stderr = &output

	if err := command.Start(); err != nil {
		t.Fatalf("start the binary: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- command.Wait() }()

	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})

	// The first request is the assertion. A binary that starts and then fails to
	// render is not a working binary.
	client := &http.Client{Timeout: 5 * time.Second}
	body, status := waitForServer(t, client, "http://"+address+"/healthz", stopped, &output)
	if status != http.StatusOK {
		t.Fatalf("/healthz = %d in an empty directory\n%s", status, output.String())
	}

	// The health endpoint reports the version and the schema, so it is also the
	// proof that the migrations ran.
	var health map[string]any
	if err := json.Unmarshal([]byte(body), &health); err != nil {
		t.Errorf("/healthz is not JSON: %v\n%s", err, body)
	}

	// A real page, because /healthz could plausibly be served by a binary whose
	// templates or static assets failed to embed (ADR-0009).
	page := requestBody(t, client, "http://"+address+"/")
	for _, expected := range []string{"<html", "</html>"} {
		if !strings.Contains(page, expected) {
			t.Errorf("the day screen is not a page in a fresh install:\n%s", truncate(page))
		}
	}

	// The stylesheet is embedded, not read from the source tree the binary was
	// built in - which is where it would still be found if the dev build tag had
	// been compiled in by accident.
	if css := requestBody(t, client, "http://"+address+"/static/css/app.css"); !strings.Contains(css, "--") {
		t.Errorf("the stylesheet did not come back from a binary running outside "+
			"the source tree:\n%s", truncate(css))
	}

	// What it created, and only that. A binary that needs a directory somebody
	// else made, or that scattered files into the working directory, fails here.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read the data directory: %v", err)
	}
	var created []string
	for _, entry := range entries {
		created = append(created, entry.Name())
	}
	sort.Strings(created)
	if len(created) == 0 {
		t.Error("the binary created nothing in its data directory")
	}
	if !contains(created, "timetracker.db") {
		t.Errorf("no database in the data directory, only %v", created)
	}

	working, err := os.ReadDir(command.Dir)
	if err != nil {
		t.Fatalf("read the working directory: %v", err)
	}
	if len(working) != 0 {
		var names []string
		for _, entry := range working {
			names = append(names, entry.Name())
		}
		t.Errorf("the binary wrote %v into the directory it was run from; "+
			"everything belongs under --data-dir", names)
	}

	// And it stops when asked. A graceful shutdown is the other half of "no
	// installation step": a process that has to be killed leaves a database
	// somebody has to recover.
	if runtime.GOOS == "windows" {
		return
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal the binary: %v", err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("the binary exited badly on SIGINT: %v\n%s", err, output.String())
		}
	case <-time.After(20 * time.Second):
		t.Errorf("the binary did not shut down within 20s of SIGINT\n%s", output.String())
	}
}

// TestTheBinaryReportsItsVersion.
//
// `timetracker version` before anything else is what somebody runs to find out
// what they have. It has to work with no data directory, no configuration and no
// database - which is why main handles it before parsing flags, and why this
// runs it in a directory that contains nothing.
func TestTheBinaryReportsItsVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	binary := buildBinary(t)

	command := exec.Command(binary, "version")
	command.Dir = t.TempDir()
	command.Env = []string{"HOME=" + t.TempDir()}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("timetracker version: %v\n%s", err, output)
	}
	if !strings.HasPrefix(string(output), "timetracker ") {
		t.Errorf("version output does not name the program: %q", output)
	}
	if !strings.Contains(string(output), "commit") {
		t.Errorf("version output does not carry the build metadata: %q", output)
	}
}

// buildBinary compiles the command into a temporary directory.
//
// With cgo explicitly disabled, so this is also the smallest possible check that
// the tree still builds the way it ships (ASR-002). The build cache makes the
// second call cheap, but each test gets its own copy rather than sharing state.
func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "timetracker")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	build := exec.Command("go", "build", "-o", binary, "./cmd/timetracker")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the binary: %v\n%s", err, output)
	}
	return binary
}

// freeAddress reserves a loopback port and releases it.
//
// A short race against anything else on the machine that binds in the same
// instant, and the alternative - a fixed port - races against every other test
// run and against the developer's own running copy.
func freeAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return address
}

// waitForServer polls until the server answers, the process dies, or time runs
// out.
func waitForServer(t *testing.T, client *http.Client, url string, stopped <-chan error, output *strings.Builder) (string, int) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-stopped:
			t.Fatalf("the binary exited before serving anything: %v\n%s", err, output.String())
		default:
		}

		response, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			return string(body), response.StatusCode
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the binary never answered %s\n%s", url, output.String())
	return "", 0
}

// requestBody fetches a URL and returns the body, failing the test on anything
// other than 200.
func requestBody(t *testing.T, client *http.Client, url string) string {
	t.Helper()

	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, response.StatusCode)
	}
	return string(body)
}

func contains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
