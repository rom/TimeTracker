# scripts

Helper scripts for running TimeTracker. Nothing here is required to *build* the
application — `make build` needs only Go.

| Script | Platform | Purpose |
|---|---|---|
| [`gen-cert.sh`](gen-cert.sh) | Linux, macOS, any POSIX shell | Create X.509 certificates for the HTTPS listener |
| [`gen-cert.ps1`](gen-cert.ps1) | Windows | The same, using `New-SelfSignedCertificate` or `openssl` |
| [`harden-check.sh`](harden-check.sh) | Linux | Report which hardening mechanisms are active for a running instance |

## Certificates

```sh
# A self-signed certificate: quickest, and every browser will warn.
./scripts/gen-cert.sh self-signed

# A local CA plus a server certificate: install the CA once per machine and the
# warnings stop, for this and any future certificate from the same CA.
./scripts/gen-cert.sh ca -h timetracker.internal -i 10.0.0.7
```

```powershell
.\scripts\gen-cert.ps1 -Mode Ca -Hostnames timetracker.internal
```

Then:

```sh
./bin/timetracker --mode=server --addr=0.0.0.0:8443 \
    --tls-cert certs/server.crt --tls-key certs/server.key \
    --redirect-http-from :8080
```

**Neither mode is a substitute for a publicly trusted certificate.** For a host
the public can reach, use Let's Encrypt — through your reverse proxy, `certbot`,
or Caddy, which does it automatically. These scripts are for a private network,
a lab, or a laptop.

### What the certificates contain

* **ECDSA P-256** keys. Smaller and faster than RSA at equivalent strength, and
  universally supported.
* **Subject Alternative Names** for every hostname and IP given. Modern browsers
  ignore the Common Name entirely and validate against SANs only, so a
  certificate without them is rejected however its CN reads.
* **Constrained key usage**: `serverAuth` only, so a stolen key cannot be
  repurposed for client authentication or code signing.
* **397 days** validity, the maximum most browsers accept.

### Key protection

The scripts set the key to mode `600` (POSIX) or an inherited-permissions-off
ACL granting only the current user, Administrators and SYSTEM (Windows).

TimeTracker **refuses to start** if the key is group- or world-readable on a
POSIX system. That check is skipped on Windows, where Go's reported permission
bits are synthesised and say nothing about the real ACL.

## Renewal

These are not auto-renewing. Re-run the script before expiry and restart the
service. For a CA-issued certificate the CA stays in place, so clients do not
need to trust anything new:

```sh
./scripts/gen-cert.sh ca -h timetracker.internal   # reuses the existing CA
sudo systemctl restart timetracker
```

If you want automatic renewal, put a reverse proxy in front and let it handle
TLS — see [`deploy/`](../deploy/README.md).
