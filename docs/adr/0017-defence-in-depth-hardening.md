# ADR-0017: Layered platform hardening, opt-in in the process

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-002, ASR-005, ASR-015

## Context

The application holds commercially sensitive data and, in server mode, listens
on a network. Application-level controls - authentication, RBAC, parameterised
SQL - are the first line, and they are the line most likely to have a defect in
it. What limits the damage of that defect is the operating system.

Every supported platform offers something, and they are not alike:

* **Linux** has the richest set, and much of it applies from *outside* the
  process (systemd's seccomp filter, capability bounding, namespaces, read-only
  mounts) with one mechanism, **Landlock**, that a process can apply to itself
  with no privilege at all.
* **macOS** confines from outside only: launchd sets the account, `sandbox-exec`
  applies a profile. There is no unprivileged self-sandboxing API for a binary
  distributed outside the App Store.
* **Windows** confines through the service account, the token, ACLs, and
  organisation-wide code-integrity policy (WDAC/AppLocker). Again, all external.

Two questions follow. Which layers do we ship? And does the process restrict
itself by default?

## Decision

**Ship all of them, and be explicit that they overlap.**

`deploy/` carries a hardened systemd unit, an AppArmor profile, an SELinux
policy module, a launchd job with a sandbox profile, and a Windows service
installer. Each is annotated with what it removes, because a hardening block
nobody understands is a hardening block someone deletes the first time something
breaks.

The overlap is deliberate, not redundant: Landlock still applies when someone
runs the binary by hand outside systemd; the systemd unit still applies on a
kernel too old for Landlock; AppArmor and SELinux apply to both, and survive the
process being started by something else entirely.

**Landlock is applied by the process itself, and defaults to off.**

`--hardening=off|auto|enforce`. The default is `off`. This is the decision most
likely to be questioned, so the reasoning is stated plainly: a sandbox that
silently denies a file access produces a failure with no obvious cause, often
far from the code that triggered it. A user running the binary on a laptop, with
their database in an unusual place, would meet an error nobody could diagnose
from the message. Requiring one flag costs an operator nothing; the shipped
systemd unit sets `--hardening=enforce`, which is where hardening belongs.

`auto` applies what the kernel supports and continues quietly when it supports
nothing - the same binary runs on three operating systems and many kernel
versions, so "unsupported" is an ordinary fact rather than a failure. `enforce`
refuses to start, for an operator who has decided the sandbox is not optional.

**seccomp is delegated to systemd rather than implemented in-process.**

A seccomp filter is a BPF program, and applying one from Go means either cgo and
libseccomp - which would break the no-cgo rule that makes cross-compilation work
([ADR-0003](0003-pure-go-sqlite.md)) - or hand-assembling BPF against a syscall
set the Go runtime may change between releases. systemd's `SystemCallFilter=`
gets the same result, is maintained by people who track that surface, and is
inspectable with `systemd-analyze security`.

## Consequences

**Positive**

* An exploited defect in the application still cannot read `/etc/shadow`, write
  outside the data directory, load a kernel module or execute another program.
* Each platform gets its idiomatic mechanism, so an operator configures it the
  way they already configure everything else.
* `scripts/harden-check.sh` reports what is *actually* active, which is not the
  same thing as what was configured.

**Negative / accepted costs**

* Five deployment configurations to maintain, in five different languages, none
  of which the test suite can meaningfully exercise. They will drift, and the
  drift will be found by an operator rather than by CI.
* Off-by-default means a plain `--mode=server` run has no in-process sandbox.
  An operator who ignores `deploy/` gets no Landlock.
* Landlock is implemented against raw syscalls, since `golang.org/x/sys/unix`
  exposes the structures but not the wrappers. That is three syscall numbers and
  two structs we now own and must keep correct.
* The read-only path list is deliberately generous (`/usr`, `/etc`, `/proc`,
  `/sys`, `/dev`, `/run`), which weakens the confinement in exchange for not
  breaking on distributions we have not tried. A tighter list would be better
  and would need per-distribution testing we cannot currently do.

## Alternatives considered

**Apply Landlock by default** - stronger out of the box. Rejected on the
diagnosis problem above: the failure mode is a mysterious permission error far
from its cause, and the population most likely to hit it is the laptop user who
has no operator to help them.

**In-process seccomp** - would apply without systemd. Rejected: it needs cgo or
hand-written BPF, and the Go runtime's syscall usage is not a stable contract.

**Ship only the systemd unit** - much less to maintain, and covers most Linux
deployments. Rejected because it leaves macOS and Windows with nothing at all,
against ASR-002's premise that all three are first-class.

**Drop privileges in-process (setuid/setgid)** - the traditional daemon
approach. Rejected: it is error-prone in a multi-threaded Go runtime, and the
service manager does it correctly on every platform.

## Related

* ADR-0003 (no cgo), ADR-0018 (TLS termination)
