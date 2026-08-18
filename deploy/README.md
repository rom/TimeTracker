# Deployment and hardening

TimeTracker is one binary with no runtime dependencies, so deploying it is
copying a file. Hardening it is where the work is, and this directory holds the
platform configuration that does it.

The principle throughout: **the application should be able to reach its own data
and nothing else.** Everything below is a different operating system's way of
expressing that.

## What applies where

| Mechanism | Platform | Applied by | What it removes |
|---|---|---|---|
| [Landlock](#landlock) | Linux 5.13+ | the process itself, `--hardening` | all filesystem access outside the data directory and a read-only system set |
| [systemd](#systemd) | Linux | the unit file | seccomp-filtered syscalls, all capabilities, namespaces, a read-only root filesystem, W^X memory |
| [AppArmor](#apparmor) | Debian, Ubuntu, SUSE | the kernel, from a path-based profile | the same, enforced whoever starts the process |
| [SELinux](#selinux) | Fedora, RHEL | the kernel, from a label-based policy | the same, and it survives files being moved |
| [launchd + sandbox](#macos) | macOS | launchd, from a `.sb` profile | filesystem and network access outside the profile |
| [service account + ACL + WDAC](#windows) | Windows | the service control manager and policy | privileges, write access, and unsigned code execution |

They overlap deliberately. Landlock still applies when someone runs the binary
by hand outside systemd; the systemd unit still applies where Landlock is
unavailable; AppArmor and SELinux apply to both.

## Landlock

The only mechanism the application applies to *itself*. It needs no privilege
and no configuration file: the process knows which directories it needs and
tells the kernel to deny the rest, permanently, before serving a request.

```sh
timetracker --mode=server --hardening=auto      # apply what the kernel supports
timetracker --mode=server --hardening=enforce   # refuse to start without it
```

**The default is `off`**, deliberately. Silently sandboxing a process and
breaking its file access in a way nobody can diagnose is worse than requiring a
flag; the systemd unit turns it on, which is where hardening belongs. See
[ADR-0017](../docs/adr/0017-defence-in-depth-hardening.md).

What the process keeps: read and write beneath the data directory and the
temporary directory, read-only access to `/usr`, `/etc`, `/proc`, `/sys`, `/dev`
and `/run`, plus the directories holding the TLS certificate and key. Everything
else — every home directory, `/etc/shadow`, any other service's data — becomes
inaccessible for the life of the process.

Check what took effect: the process logs `hardening applied` at start-up, and
`/healthz` reports it.

## systemd

```sh
sudo useradd --system --home-dir /var/lib/timetracker --shell /usr/sbin/nologin timetracker
sudo install -m 0755 bin/timetracker /usr/local/bin/timetracker
sudo install -m 0644 systemd/timetracker.service /etc/systemd/system/
sudo install -m 0600 systemd/timetracker.env /etc/timetracker.env   # then edit
sudo systemctl daemon-reload && sudo systemctl enable --now timetracker
```

Verify what actually took effect — this scores the unit and lists every
directive you have *not* set:

```sh
systemd-analyze security timetracker
```

The unit sets an empty `CapabilityBoundingSet` (the service binds a port above
1024 and needs no capability at all), `ProtectSystem=strict` with a single
`ReadWritePaths`, a seccomp filter, `MemoryDenyWriteExecute`, and
`RestrictAddressFamilies` to the three families it uses. Every directive carries
a comment saying what it removes, because a hardening block nobody understands
is a hardening block someone deletes the first time something breaks.

## AppArmor

```sh
sudo install -m 0644 apparmor/usr.local.bin.timetracker /etc/apparmor.d/
sudo apparmor_parser -r /etc/apparmor.d/usr.local.bin.timetracker
```

Develop in complain mode first, which logs violations instead of blocking:

```sh
sudo aa-complain /usr/local/bin/timetracker
# exercise the application, then:
sudo journalctl -k | grep apparmor
sudo aa-enforce /usr/local/bin/timetracker
```

## SELinux

```sh
cd selinux
make -f /usr/share/selinux/devel/Makefile timetracker.pp
sudo semodule -i timetracker.pp
sudo semanage fcontext -a -t timetracker_exec_t '/usr/local/bin/timetracker'
sudo semanage fcontext -a -t timetracker_var_lib_t '/var/lib/timetracker(/.*)?'
sudo restorecon -Rv /usr/local/bin/timetracker /var/lib/timetracker
sudo semanage port -a -t timetracker_port_t -p tcp 8420
```

Develop in permissive mode and let the audit log tell you what the policy is
missing:

```sh
sudo semanage permissive -a timetracker_t
sudo ausearch -m AVC -c timetracker | audit2allow -R
sudo semanage permissive -d timetracker_t
```

## macOS

```sh
sudo install -m 0755 bin/timetracker /usr/local/bin/timetracker
sudo install -d -m 0755 /usr/local/etc/timetracker
sudo install -m 0644 macos/timetracker.sb /usr/local/etc/timetracker/
sudo install -m 0644 macos/com.timetracker.server.plist /Library/LaunchDaemons/
sudo launchctl load -w /Library/LaunchDaemons/com.timetracker.server.plist
```

launchd runs the job as an unprivileged `_timetracker` account, and
`sandbox-exec` applies the profile before the binary starts. The profile denies
everything by default and allows back the data directory, the system libraries,
and outbound TCP for single sign-on.

`sandbox-exec` is formally deprecated by Apple but remains the only way to
sandbox an executable not distributed through the App Store, and Apple's own
daemons still use it. Test the profile before relying on it:

```sh
sandbox-exec -f macos/timetracker.sb ./bin/timetracker --data-dir /tmp/tt
log stream --predicate 'senderImagePath contains "Sandbox"' --info
```

For distribution rather than self-hosting, sign the binary with a Developer ID,
enable the hardened runtime, and notarise it — otherwise Gatekeeper will refuse
to run it on anyone else's machine.

## Windows

```powershell
.\windows\install-service.ps1 -Port 8443
```

The script creates the service under a **virtual service account**
(`NT SERVICE\TimeTracker`) — no password to manage, no interactive logon, no
group membership — strips every privilege with `sc.exe privs`, sets a
write-restricted token with `sc.exe sidtype restricted`, locks the data
directory's ACL with inheritance disabled, and adds a firewall rule scoped to
the Domain and Private profiles only.

What it deliberately does not do is configure **WDAC** or **AppLocker**. Those
are organisation-wide policy, not per-application settings, and they are the
part that stops unsigned code running at all. Sign the binary and write
publisher rules; path rules are bypassable by anyone who can write to the path.

## Reverse proxy

Any of these can sit behind nginx, Caddy or Traefik instead of terminating TLS
itself. If you do:

* set `TT_SECURE_COOKIES=1`, so the session cookie still carries `Secure`;
* set `TT_TRUSTED_PROXIES` to the proxy's address — otherwise
  `X-Forwarded-For` is attacker-controlled and **your audit trail records
  fiction**;
* leave `TT_TLS_CERT` and `TT_TLS_KEY` unset.

## Checking your work

```sh
./scripts/harden-check.sh          # what is active for a running instance
systemd-analyze security timetracker
sudo aa-status | grep timetracker
sudo semodule -l | grep timetracker
curl -s localhost:8420/healthz     # reports the in-process hardening
```
