<#
.SYNOPSIS
    Create X.509 certificates for TimeTracker's HTTPS listener on Windows.

.DESCRIPTION
    The Windows counterpart of gen-cert.sh. Two modes:

      SelfSigned  one certificate signed by itself. Fine for a laptop or a first
                  trial; browsers will warn every time.

      Ca          a small local certificate authority plus a server certificate
                  signed by it. Install the CA certificate once per machine and
                  the warnings stop, for this and any future certificate from
                  the same CA.

    Neither replaces a publicly trusted certificate on an internet-facing host.

    Uses New-SelfSignedCertificate where available (Windows 10/Server 2016 and
    later), and falls back to openssl if it is on PATH - which is the case under
    Git for Windows, WSL, or a manual install.

.PARAMETER Mode
    SelfSigned or Ca.

.PARAMETER OutDir
    Where to write the files. Default: .\certs

.PARAMETER Hostnames
    DNS names the certificate is valid for. Default: localhost and this machine.

.PARAMETER IPAddresses
    IP addresses the certificate is valid for. Default: 127.0.0.1 and ::1

.PARAMETER Days
    Validity of the server certificate in days. Default: 397.

.EXAMPLE
    .\scripts\gen-cert.ps1 -Mode SelfSigned

.EXAMPLE
    .\scripts\gen-cert.ps1 -Mode Ca -Hostnames timetracker.internal -IPAddresses 10.0.0.7
#>

[CmdletBinding()]
param(
    [ValidateSet('SelfSigned', 'Ca')]
    [string]$Mode = 'SelfSigned',

    [string]$OutDir = '.\certs',

    [string[]]$Hostnames,

    [string[]]$IPAddresses = @('127.0.0.1', '::1'),

    [int]$Days = 397,

    [int]$CaDays = 3650
)

$ErrorActionPreference = 'Stop'

# Sensible defaults so the common case needs no arguments.
if (-not $Hostnames) {
    $Hostnames = @('localhost')
    $machine = $env:COMPUTERNAME
    if ($machine -and $machine -ne 'localhost') { $Hostnames += $machine }
}

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$primary  = $Hostnames[0]
$dnsNames = $Hostnames + $IPAddresses

$certPath = Join-Path $OutDir 'server.crt'
$keyPath  = Join-Path $OutDir 'server.key'
$caPath   = Join-Path $OutDir 'ca.crt'
$caKeyPath = Join-Path $OutDir 'ca.key'

# Restrict the directory to the current user and administrators. The private key
# lives here, and Windows inherits permissive ACLs from the parent by default.
function Protect-Directory([string]$Path) {
    $acl = Get-Acl $Path
    $acl.SetAccessRuleProtection($true, $false)   # stop inheriting
    $acl.Access | ForEach-Object { [void]$acl.RemoveAccessRule($_) }

    foreach ($identity in @($env:USERNAME, 'BUILTIN\Administrators', 'NT AUTHORITY\SYSTEM')) {
        try {
            $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
                $identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
            $acl.AddAccessRule($rule)
        } catch {
            Write-Warning "Could not grant access to $identity : $_"
        }
    }
    Set-Acl -Path $Path -AclObject $acl
}

function Use-OpenSsl {
    return [bool](Get-Command openssl -ErrorAction SilentlyContinue)
}

Write-Host "Creating certificates in $OutDir for: $($dnsNames -join ', ')"

if (Use-OpenSsl) {
    # openssl produces the PEM pair TimeTracker wants directly, with no export
    # or conversion step, so it is preferred when available.
    Write-Host '  using openssl'

    $san = (($Hostnames | ForEach-Object { "DNS:$_" }) +
            ($IPAddresses | ForEach-Object { "IP:$_" })) -join ','

    $config = Join-Path $OutDir 'openssl.cnf'
    @"
[req]
distinguished_name = dn
prompt             = no

[dn]
CN = $primary
O  = TimeTracker

[server_ext]
basicConstraints = critical, CA:FALSE
keyUsage         = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName   = $san

[ca_ext]
basicConstraints = critical, CA:TRUE, pathlen:0
keyUsage         = critical, keyCertSign, cRLSign
"@ | Set-Content -Path $config -Encoding ASCII

    & openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:prime256v1 -out $keyPath

    if ($Mode -eq 'SelfSigned') {
        & openssl req -new -x509 -key $keyPath -out $certPath -days $Days `
            -config $config -extensions server_ext
    } else {
        if (-not (Test-Path $caKeyPath)) {
            & openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:prime256v1 -out $caKeyPath
            & openssl req -new -x509 -key $caKeyPath -out $caPath -days $CaDays `
                -subj '/CN=TimeTracker Local CA/O=TimeTracker' `
                -config $config -extensions ca_ext
            Write-Host "  created a new CA: $caPath"
        } else {
            Write-Host "  reusing the existing CA: $caPath"
        }

        $csrPath = Join-Path $OutDir 'server.csr'
        & openssl req -new -key $keyPath -out $csrPath -config $config
        & openssl x509 -req -in $csrPath -CA $caPath -CAkey $caKeyPath -CAcreateserial `
            -out $certPath -days $Days -extfile $config -extensions server_ext
        Remove-Item $csrPath -Force
    }

    Remove-Item $config -Force

} else {
    # No openssl: use the built-in cmdlet and export to PEM. This path needs
    # Windows 10 / Server 2016 or later.
    Write-Host '  using New-SelfSignedCertificate'

    if ($Mode -eq 'Ca') {
        $ca = New-SelfSignedCertificate `
            -Subject 'CN=TimeTracker Local CA, O=TimeTracker' `
            -KeyUsage CertSign, CRLSign `
            -KeyAlgorithm ECDSA_nistP256 `
            -CertStoreLocation 'Cert:\CurrentUser\My' `
            -NotAfter (Get-Date).AddDays($CaDays) `
            -TextExtension @('2.5.29.19={critical}{text}CA=true&pathlength=0')

        Export-Certificate -Cert $ca -FilePath $caPath -Type CERT | Out-Null
        Write-Host "  created a new CA in Cert:\CurrentUser\My and exported to $caPath"

        $cert = New-SelfSignedCertificate `
            -Subject "CN=$primary, O=TimeTracker" `
            -DnsName $dnsNames `
            -KeyAlgorithm ECDSA_nistP256 `
            -KeyExportPolicy Exportable `
            -CertStoreLocation 'Cert:\CurrentUser\My' `
            -NotAfter (Get-Date).AddDays($Days) `
            -Signer $ca `
            -TextExtension @('2.5.29.37={text}1.3.6.1.5.5.7.3.1')
    } else {
        $cert = New-SelfSignedCertificate `
            -Subject "CN=$primary, O=TimeTracker" `
            -DnsName $dnsNames `
            -KeyAlgorithm ECDSA_nistP256 `
            -KeyExportPolicy Exportable `
            -CertStoreLocation 'Cert:\CurrentUser\My' `
            -NotAfter (Get-Date).AddDays($Days) `
            -TextExtension @('2.5.29.37={text}1.3.6.1.5.5.7.3.1')
    }

    # Go wants PEM on disk, so export a PFX and convert. The password is
    # ephemeral and never written down: it exists only to satisfy the PFX
    # format, and the resulting PEM key is what actually needs protecting.
    $pfxPath  = Join-Path $OutDir 'server.pfx'
    $password = [System.Guid]::NewGuid().ToString('N')
    $secure   = ConvertTo-SecureString -String $password -Force -AsPlainText
    Export-PfxCertificate -Cert $cert -FilePath $pfxPath -Password $secure | Out-Null

    Export-Certificate -Cert $cert -FilePath $certPath -Type CERT | Out-Null

    Write-Warning @"
The private key was exported to $pfxPath (PKCS#12).
TimeTracker needs a PEM key. Convert it with openssl:

    openssl pkcs12 -in "$pfxPath" -nocerts -nodes -out "$keyPath" -passin pass:$password
    openssl x509 -inform DER -in "$certPath" -out "$certPath"

Then delete the PFX. Installing Git for Windows provides openssl, after which
re-running this script takes the openssl path and produces PEM files directly.
"@
}

Protect-Directory $OutDir

Write-Host ''
Write-Host "  certificate: $certPath"
Write-Host "  private key: $keyPath"
if ($Mode -eq 'Ca') {
    Write-Host "  CA certificate: $caPath"
    Write-Host ''
    Write-Host '  Trust the CA on this machine (as Administrator):'
    Write-Host "    Import-Certificate -FilePath `"$caPath`" -CertStoreLocation Cert:\LocalMachine\Root"
    Write-Host '  Firefox keeps its own store and needs a separate import.'
}

Write-Host ''
Write-Host 'Run TimeTracker with it:'
Write-Host "  .\bin\timetracker.exe --mode=server --addr=0.0.0.0:8443 ``"
Write-Host "      --tls-cert `"$certPath`" --tls-key `"$keyPath`""
