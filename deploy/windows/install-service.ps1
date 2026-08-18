<#
.SYNOPSIS
    Install TimeTracker as a hardened Windows service.

.DESCRIPTION
    Windows has no direct equivalent of Landlock or AppArmor. Confinement comes
    from four places, and this script sets up the first three:

      1. A virtual service account with no interactive logon, no network
         identity, and no membership of any group. This is the closest analogue
         to the unprivileged system user a Linux service runs as.
      2. ACLs on the data directory granting that account and Administrators
         only, with inheritance disabled so a permissive parent cannot widen it.
      3. A firewall rule scoped to the port and profile you choose, rather than
         the "allow everything" rule Windows offers when a program first listens.
      4. WDAC or AppLocker policy, which is organisation-wide and cannot
         sensibly be configured from a per-application script. See the notes at
         the end.

    Run as Administrator.

.PARAMETER InstallDir
    Where the binary lives. Default: C:\Program Files\TimeTracker

.PARAMETER DataDir
    Where the database and attachments live. Default: C:\ProgramData\TimeTracker

.PARAMETER Port
    TCP port to listen on. Default: 8420

.PARAMETER FirewallProfile
    Which network profiles the firewall rule applies to. Default: Domain,Private
    - deliberately not Public.

.EXAMPLE
    .\deploy\windows\install-service.ps1 -Port 8443
#>

[CmdletBinding()]
param(
    [string]$InstallDir = "$env:ProgramFiles\TimeTracker",
    [string]$DataDir    = "$env:ProgramData\TimeTracker",
    [int]$Port          = 8420,
    [string[]]$FirewallProfile = @('Domain', 'Private'),
    [string]$ServiceName = 'TimeTracker'
)

$ErrorActionPreference = 'Stop'

if (-not ([Security.Principal.WindowsPrincipal] `
          [Security.Principal.WindowsIdentity]::GetCurrent()
         ).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'This script must be run as Administrator.'
}

Write-Host "Installing $ServiceName"

# ---- 1. directories --------------------------------------------------------

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $DataDir    | Out-Null

$binarySource = Join-Path (Get-Location) 'bin\timetracker.exe'
if (-not (Test-Path $binarySource)) {
    throw "Could not find $binarySource. Run 'make build' first, or copy the binary here."
}
Copy-Item $binarySource (Join-Path $InstallDir 'timetracker.exe') -Force

# ---- 2. the service --------------------------------------------------------

# A virtual account, "NT SERVICE\<name>", is created implicitly by the service
# control manager. It has no password to manage or leak, cannot log on
# interactively, and is a member of no group - so it starts with essentially
# nothing and is granted only what the ACLs below give it.
$account = "NT SERVICE\$ServiceName"

$binaryPath = '"{0}\timetracker.exe" --mode=server --addr=0.0.0.0:{1} --data-dir "{2}"' `
              -f $InstallDir, $Port, $DataDir

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Write-Host '  stopping the existing service'
    Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
    & sc.exe delete $ServiceName | Out-Null
    Start-Sleep -Seconds 2
}

& sc.exe create $ServiceName binPath= $binaryPath start= auto obj= $account `
    DisplayName= 'TimeTracker' | Out-Null
& sc.exe description $ServiceName 'Time tracking for billable work' | Out-Null

# Restart on failure rather than leaving the service down after a transient
# problem: 5s, 10s, then every 60s, with the counter resetting after a day.
& sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/10000/restart/60000 | Out-Null

# Remove every privilege the service does not need. This is the closest Windows
# equivalent of CapabilityBoundingSet= on Linux: the service can hold no
# privilege beyond what is listed, and this one needs none at all.
& sc.exe privs $ServiceName '' | Out-Null

# Run with a write-restricted token: the account may write only where its SID is
# explicitly granted access, which is the data directory and nothing else.
& sc.exe sidtype $ServiceName restricted | Out-Null

Write-Host "  service created, running as $account"

# ---- 3. ACLs ---------------------------------------------------------------

function Set-StrictAcl {
    param([string]$Path, [string[]]$FullControl, [string[]]$ReadOnly = @())

    $acl = Get-Acl $Path
    # Stop inheriting: a permissive ACL on the parent must not widen this one.
    $acl.SetAccessRuleProtection($true, $false)
    $acl.Access | ForEach-Object { [void]$acl.RemoveAccessRule($_) }

    foreach ($identity in $FullControl) {
        $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule(
            $identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')))
    }
    foreach ($identity in $ReadOnly) {
        $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule(
            $identity, 'ReadAndExecute', 'ContainerInherit,ObjectInherit', 'None', 'Allow')))
    }
    Set-Acl -Path $Path -AclObject $acl
}

# The data directory holds timesheets: who works for whom, at what rate. Only
# the service account and Administrators.
Set-StrictAcl -Path $DataDir -FullControl @($account, 'BUILTIN\Administrators', 'NT AUTHORITY\SYSTEM')

# The install directory is read-and-execute for the service: it must run the
# binary, and must not be able to replace it.
Set-StrictAcl -Path $InstallDir `
    -FullControl @('BUILTIN\Administrators', 'NT AUTHORITY\SYSTEM') `
    -ReadOnly    @($account)

Write-Host '  ACLs restricted (inheritance disabled)'

# ---- 4. firewall -----------------------------------------------------------

Remove-NetFirewallRule -DisplayName "$ServiceName inbound" -ErrorAction SilentlyContinue

New-NetFirewallRule `
    -DisplayName "$ServiceName inbound" `
    -Direction Inbound `
    -Action Allow `
    -Protocol TCP `
    -LocalPort $Port `
    -Profile $FirewallProfile `
    -Program (Join-Path $InstallDir 'timetracker.exe') | Out-Null

Write-Host "  firewall: TCP $Port allowed on $($FirewallProfile -join ', ') profiles only"

# ---- done ------------------------------------------------------------------

Write-Host ''
Write-Host 'Before starting, set the environment for the service:'
Write-Host "  New-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName' ``"
Write-Host "      -Name Environment -PropertyType MultiString -Value @('TT_ADMIN_EMAIL=you@example.com','TT_ADMIN_PASSWORD=...')"
Write-Host ''
Write-Host 'Then:'
Write-Host "  Start-Service $ServiceName"
Write-Host "  Get-Service $ServiceName"
Write-Host ''
Write-Host @'
Remaining hardening, which is organisation-wide rather than per-application:

  WDAC (Windows Defender Application Control) is the strongest option. A policy
  in enforcement mode allows only signed, approved binaries to run at all, so a
  dropped executable cannot execute even with write access. Generate a policy
  from a known-good machine with New-CIPolicy, audit it, then enforce.

  AppLocker is the older, lighter alternative: publisher or path rules under
  Computer Configuration -> Windows Settings -> Security Settings ->
  Application Control Policies.

  Neither is meaningful without code signing. Sign timetracker.exe with your
  organisation's certificate and write publisher rules; path rules are
  bypassable by anyone who can write to the path.

  Attack Surface Reduction rules (Set-MpPreference -AttackSurfaceReductionRules_Ids)
  add process-creation and credential-theft protections that apply regardless.
'@
