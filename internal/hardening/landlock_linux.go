//go:build linux

package hardening

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Landlock is a Linux kernel feature (5.13 and later) that lets an unprivileged
// process permanently restrict its own filesystem access.
//
// It is a good fit here because it needs no privilege, no helper daemon and no
// configuration file: the application knows exactly which directories it needs -
// its data directory, its certificates, the system libraries - and can say so.
// Once applied, a defect that yields arbitrary file access still cannot read
// /etc/shadow or write outside the data directory.
//
// It is implemented against the raw syscalls because golang.org/x/sys/unix
// exposes the structures but not the wrappers, and pulling in a dependency for
// three system calls is a poor trade.

// Syscall numbers. Landlock was added to the generic syscall table, so these are
// the same on every architecture that has it.
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446
)

// Filesystem access rights. The kernel denies any handled right for which no
// rule grants access, and ignores rights the running ABI does not know, so the
// handled set is trimmed to the detected ABI below.
const (
	accessFSExecute    = 1 << 0
	accessFSWriteFile  = 1 << 1
	accessFSReadFile   = 1 << 2
	accessFSReadDir    = 1 << 3
	accessFSRemoveDir  = 1 << 4
	accessFSRemoveFile = 1 << 5
	accessFSMakeChar   = 1 << 6
	accessFSMakeDir    = 1 << 7
	accessFSMakeReg    = 1 << 8
	accessFSMakeSock   = 1 << 9
	accessFSMakeFifo   = 1 << 10
	accessFSMakeBlock  = 1 << 11
	accessFSMakeSym    = 1 << 12
	accessFSRefer      = 1 << 13 // ABI 2
	accessFSTruncate   = 1 << 14 // ABI 3
	accessFSIoctlDev   = 1 << 15 // ABI 5

	ruleTypePathBeneath = 1

	// O_PATH opens a directory as a reference without granting access to its
	// contents, which is exactly what a Landlock rule needs. Go's syscall
	// package does not define it.
	oPath = 0x200000

	// createRulesetVersion asks the kernel which ABI it implements instead of
	// creating anything.
	createRulesetVersion = 1 << 0
)

// rulesetAttr mirrors struct landlock_ruleset_attr.
type rulesetAttr struct {
	HandledAccessFS  uint64
	HandledAccessNet uint64
}

// pathBeneathAttr mirrors struct landlock_path_beneath_attr.
type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFD      int32
	_             [4]byte // explicit padding to match the kernel's alignment
}

// apply restricts the process to the paths in the policy.
func apply(policy Policy) (Result, error) {
	abi, err := landlockABI()
	if err != nil {
		return Result{}, fmt.Errorf("landlock is not available on this kernel: %w", err)
	}

	// Everything the process may ever want to do to a file. Anything in this set
	// without a matching rule below becomes impossible.
	handled := uint64(
		accessFSExecute | accessFSWriteFile | accessFSReadFile | accessFSReadDir |
			accessFSRemoveDir | accessFSRemoveFile | accessFSMakeChar |
			accessFSMakeDir | accessFSMakeReg | accessFSMakeSock |
			accessFSMakeFifo | accessFSMakeBlock | accessFSMakeSym)

	// Rights added in later ABI versions must not be requested on an older
	// kernel: an unknown bit makes the whole call fail with EINVAL.
	if abi >= 2 {
		handled |= accessFSRefer
	}
	if abi >= 3 {
		handled |= accessFSTruncate
	}
	if abi >= 5 {
		handled |= accessFSIoctlDev
	}

	attr := rulesetAttr{HandledAccessFS: handled}
	rulesetFD, _, errno := syscall.Syscall(sysLandlockCreateRuleset,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return Result{}, fmt.Errorf("create landlock ruleset: %w", errno)
	}
	defer func() { _ = syscall.Close(int(rulesetFD)) }()

	// Read-write access, for the directories the application owns.
	readWrite := uint64(
		accessFSReadFile | accessFSReadDir | accessFSWriteFile |
			accessFSMakeReg | accessFSMakeDir | accessFSRemoveFile | accessFSRemoveDir)
	if abi >= 2 {
		readWrite |= accessFSRefer
	}
	if abi >= 3 {
		readWrite |= accessFSTruncate
	}

	readOnly := uint64(accessFSReadFile | accessFSReadDir)
	// The system directories hold the shared libraries and the loader, which the
	// process must be able to execute even though the binary itself is static.
	readExec := readOnly | accessFSExecute

	writable := []string{policy.DataDir, policy.TempDir}
	for _, path := range writable {
		if path == "" {
			continue
		}
		if err := addRule(int(rulesetFD), path, readWrite); err != nil {
			return Result{}, err
		}
	}

	for _, path := range policy.ReadOnlyPaths {
		if path == "" {
			continue
		}
		access := readOnly
		if isSystemPath(path) {
			access = readExec
		}
		// A path that does not exist on this system is skipped rather than
		// fatal: the read-only list is deliberately generous and covers several
		// distributions' conventions at once.
		if err := addRule(int(rulesetFD), path, access); err != nil && !os.IsNotExist(err) {
			return Result{}, err
		}
	}

	// no_new_privs is mandatory before restricting: without it a setuid binary
	// executed later could regain what was just given up, which would make the
	// whole exercise pointless.
	if err := setNoNewPrivs(); err != nil {
		return Result{}, fmt.Errorf("set no_new_privs: %w", err)
	}

	if _, _, errno := syscall.Syscall(sysLandlockRestrictSelf, rulesetFD, 0, 0); errno != 0 {
		return Result{}, fmt.Errorf("apply landlock ruleset: %w", errno)
	}

	return Result{Applied: []string{fmt.Sprintf("landlock (ABI %d)", abi)}}, nil
}

// addRule grants access beneath one directory.
func addRule(rulesetFD int, path string, access uint64) error {
	fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == syscall.ENOENT {
			return os.ErrNotExist
		}
		return fmt.Errorf("open %s for landlock rule: %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()

	attr := pathBeneathAttr{AllowedAccess: access, ParentFD: int32(fd)}
	_, _, errno := syscall.Syscall6(sysLandlockAddRule,
		uintptr(rulesetFD), ruleTypePathBeneath,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("add landlock rule for %s: %w", path, errno)
	}
	return nil
}

// landlockABI asks the kernel which Landlock version it implements.
func landlockABI() (int, error) {
	version, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, createRulesetVersion)
	if errno != 0 {
		return 0, errno
	}
	return int(version), nil
}

// setNoNewPrivs stops the process and its children from gaining privileges
// through a setuid or file-capability binary.
func setNoNewPrivs() error {
	const prSetNoNewPrivs = 38
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

// isSystemPath reports whether a path holds executables or libraries, which need
// execute permission as well as read.
func isSystemPath(path string) bool {
	switch path {
	case "/usr", "/bin", "/sbin", "/lib", "/lib64", "/usr/lib", "/usr/lib64", "/usr/bin":
		return true
	default:
		return false
	}
}
