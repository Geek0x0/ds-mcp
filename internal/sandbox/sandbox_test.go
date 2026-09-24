package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	MaybeRunHelper()
	os.Exit(m.Run())
}

func TestCommand(t *testing.T) {
	got := Command("/bin/ds-mcp", []string{"/a", "/b"}, "bash", "-lc", "echo hi")
	want := []string{"/bin/ds-mcp", HelperArg, "--rw", "/a", "--rw", "/b", "--", "bash", "-lc", "echo hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Command() = %v, want %v", got, want)
	}
}

func TestParseArgsErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--rw"},
		{"--rw", "/a"},
		{"--"},
		{"--bogus", "--", "true"},
	} {
		if _, _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q) error = nil, want error", args)
		}
	}
}

func runHelper(t *testing.T, roots []string, script string) (string, int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := Command(self, roots, "bash", "-c", script)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

func TestHelperRestrictsWrites(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	inside := t.TempDir()
	outside := t.TempDir()

	out, code := runHelper(t, []string{inside}, "touch "+filepath.Join(inside, "ok")+" && echo x > /dev/null && echo y > /dev/stdout && cat /etc/passwd > /dev/null")
	if code != 0 {
		t.Fatalf("inside write exit = %d, output = %q", code, out)
	}

	out, code = runHelper(t, []string{inside}, "touch "+filepath.Join(outside, "bad"))
	if code == 0 {
		t.Fatalf("outside write succeeded, output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "bad")); err == nil {
		t.Fatalf("outside file was created")
	}
}

func TestHelperAllowsCrossDirectoryRenameInsideRoot(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	inside := t.TempDir()

	// ln uses link(2) with no copy fallback, so it fails with EXDEV unless the root grants "refer".
	out, code := runHelper(t, []string{inside}, "cd "+inside+" && mkdir a b && touch a/x && ln a/x b/x")
	if code != 0 {
		t.Fatalf("cross-directory link exit = %d, output = %q", code, out)
	}
}

func TestHelperDeniesDeviceDirectoryWrites(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	if info, err := os.Stat("/dev/shm"); err != nil || !info.IsDir() {
		t.Skip("/dev/shm unavailable")
	}
	probe := filepath.Join("/dev/shm", "ds-mcp-probe-"+filepath.Base(t.TempDir()))
	t.Cleanup(func() { _ = os.Remove(probe) })

	out, code := runHelper(t, []string{t.TempDir()}, "touch "+probe)
	if code == 0 {
		t.Fatalf("/dev/shm write succeeded, output = %q", out)
	}
}

func TestHelperBadArgsExit126(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(self, HelperArg, "--rw").CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 126 || !strings.Contains(string(out), "ds-mcp:") {
		t.Fatalf("helper bad args = (%q, %v), want exit 126 with ds-mcp message", out, err)
	}
}
