//go:build !linux

package hardening

import "runtime"

// apply is the fallback for platforms with no in-process sandboxing that this
// application can invoke for itself.
//
// This is not a gap to be apologetic about: on macOS the sandbox is applied by
// launchd from a profile, and on Windows the restrictions come from the service
// account, the data directory ACL, and WDAC or AppLocker policy. In both cases
// the mechanism is external by design, and applying it belongs to the deployment
// rather than to the process. See deploy/macos/ and deploy/windows/.
func apply(_ Policy) (Result, error) {
	return Result{
		Unavailable: []string{
			"no in-process sandboxing on " + runtime.GOOS +
				"; hardening is applied externally (see deploy/" + runtime.GOOS + "/)",
		},
	}, nil
}
