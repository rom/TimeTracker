#!/bin/sh
# harden-check.sh - report which hardening mechanisms are active.
#
# Read-only: it inspects and reports, and changes nothing. Run it after
# deploying to confirm that what you configured actually took effect, which is
# not the same thing as having written the configuration file.

set -u

SERVICE="${1:-timetracker}"
BINARY="${2:-/usr/local/bin/timetracker}"

green()  { printf '  \033[32m✓\033[0m %s\n' "$1"; }
red()    { printf '  \033[31m✗\033[0m %s\n' "$1"; }
yellow() { printf '  \033[33m!\033[0m %s\n' "$1"; }
head_()  { printf '\n\033[1m%s\033[0m\n' "$1"; }

printf '\033[1mTimeTracker hardening report\033[0m\n'
printf 'service: %s   binary: %s\n' "$SERVICE" "$BINARY"

# ---- kernel support ---------------------------------------------------------

head_ 'Kernel'

KERNEL="$(uname -r 2>/dev/null || echo unknown)"
printf '  kernel %s\n' "$KERNEL"

if [ -f /sys/kernel/security/lsm ]; then
    printf '  active LSMs: %s\n' "$(cat /sys/kernel/security/lsm)"
    case "$(cat /sys/kernel/security/lsm)" in
        *landlock*) green 'landlock is available' ;;
        *)          yellow 'landlock is not in the active LSM list (needs Linux 5.13+ and lsm=landlock)' ;;
    esac
else
    yellow 'cannot read /sys/kernel/security/lsm'
fi

# ---- the running process ----------------------------------------------------

head_ 'Process'

PID="$(pgrep -x timetracker 2>/dev/null | head -1)"
if [ -z "$PID" ]; then
    yellow 'timetracker is not running; the process checks are skipped'
else
    printf '  pid %s\n' "$PID"

    USER_NAME="$(ps -o user= -p "$PID" 2>/dev/null | tr -d ' ')"
    if [ "$USER_NAME" = "root" ]; then
        red 'running as root - use a dedicated system account'
    else
        green "running as an unprivileged user ($USER_NAME)"
    fi

    if [ -r "/proc/$PID/status" ]; then
        NNP="$(awk '/^NoNewPrivs:/ {print $2}' "/proc/$PID/status")"
        if [ "$NNP" = "1" ]; then
            green 'no_new_privs is set'
        else
            red 'no_new_privs is NOT set - a setuid binary could regain privilege'
        fi

        SECCOMP="$(awk '/^Seccomp:/ {print $2}' "/proc/$PID/status")"
        case "$SECCOMP" in
            2) green 'seccomp filter active (mode 2)' ;;
            1) yellow 'seccomp in strict mode' ;;
            *) red 'no seccomp filter - set SystemCallFilter= in the unit' ;;
        esac

        CAPS="$(awk '/^CapEff:/ {print $2}' "/proc/$PID/status")"
        if [ "$CAPS" = "0000000000000000" ]; then
            green 'no effective capabilities'
        else
            yellow "effective capabilities: $CAPS (expected none)"
        fi

        BND="$(awk '/^CapBnd:/ {print $2}' "/proc/$PID/status")"
        if [ "$BND" = "0000000000000000" ]; then
            green 'empty capability bounding set'
        else
            yellow "capability bounding set: $BND (set CapabilityBoundingSet= in the unit)"
        fi
    fi

    if [ -r "/proc/$PID/attr/current" ]; then
        CONTEXT="$(tr -d '\0' < "/proc/$PID/attr/current")"
        # "kernel" and "unconfined" both mean no MAC confinement; only a real
        # profile or SELinux label counts.
        case "$CONTEXT" in
            unconfined*|kernel|"") yellow 'process is unconfined by AppArmor/SELinux' ;;
            *)                     green "confined: $CONTEXT" ;;
        esac
    fi
fi

# ---- systemd ----------------------------------------------------------------

head_ 'systemd'

if command -v systemctl >/dev/null 2>&1; then
    if systemctl list-unit-files 2>/dev/null | grep -q "^${SERVICE}.service"; then
        green "unit ${SERVICE}.service is installed"
        if command -v systemd-analyze >/dev/null 2>&1; then
            SCORE="$(systemd-analyze security "$SERVICE" 2>/dev/null | tail -1)"
            [ -n "$SCORE" ] && printf '  %s\n' "$SCORE"
            printf '  full report: systemd-analyze security %s\n' "$SERVICE"
        fi
    else
        yellow "no ${SERVICE}.service unit installed"
    fi
else
    yellow 'systemd is not present'
fi

# ---- mandatory access control ----------------------------------------------

head_ 'Mandatory access control'

if command -v aa-status >/dev/null 2>&1; then
    if aa-status 2>/dev/null | grep -q timetracker; then
        green 'an AppArmor profile is loaded'
        aa-status 2>/dev/null | grep timetracker | sed 's/^/    /'
    else
        yellow 'AppArmor is present but no timetracker profile is loaded'
    fi
elif command -v getenforce >/dev/null 2>&1; then
    printf '  SELinux: %s\n' "$(getenforce)"
    if semodule -l 2>/dev/null | grep -q timetracker; then
        green 'the timetracker SELinux module is installed'
    else
        yellow 'no timetracker SELinux module installed'
    fi
else
    yellow 'neither AppArmor nor SELinux tooling found'
fi

# ---- files ------------------------------------------------------------------

head_ 'Files'

if [ -e "$BINARY" ]; then
    MODE="$(stat -c '%a' "$BINARY" 2>/dev/null || stat -f '%Lp' "$BINARY" 2>/dev/null)"
    OWNER="$(stat -c '%U' "$BINARY" 2>/dev/null || stat -f '%Su' "$BINARY" 2>/dev/null)"
    printf '  binary %s owner=%s mode=%s\n' "$BINARY" "$OWNER" "$MODE"
    case "$MODE" in
        *[2367]) red 'the binary is group- or world-writable' ;;
        *)       green 'the binary is not writable by others' ;;
    esac
fi

for DIR in /var/lib/timetracker /usr/local/var/timetracker; do
    [ -d "$DIR" ] || continue
    MODE="$(stat -c '%a' "$DIR" 2>/dev/null || stat -f '%Lp' "$DIR" 2>/dev/null)"
    printf '  data directory %s mode=%s\n' "$DIR" "$MODE"
    if [ "$MODE" = "700" ]; then
        green 'data directory is private'
    else
        red "data directory should be mode 700, not $MODE - it holds timesheets"
    fi
done

for KEY in /etc/timetracker/*.key /usr/local/etc/timetracker/*.key; do
    [ -e "$KEY" ] || continue
    MODE="$(stat -c '%a' "$KEY" 2>/dev/null || stat -f '%Lp' "$KEY" 2>/dev/null)"
    if [ "$MODE" = "600" ] || [ "$MODE" = "400" ]; then
        green "TLS key $KEY is private"
    else
        red "TLS key $KEY is mode $MODE - run: chmod 600 $KEY"
    fi
done

printf '\n'
