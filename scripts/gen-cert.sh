#!/bin/sh
# gen-cert.sh - create X.509 certificates for TimeTracker's HTTPS listener.
#
# Two modes:
#
#   self-signed  one certificate, signed by itself. Fine for a laptop or a first
#                trial. Every browser will warn, every time, and there is no way
#                to make it stop without adding the certificate itself to a trust
#                store - which is why the CA mode below usually ends up being
#                less work.
#
#   ca           a small local certificate authority, plus a server certificate
#                signed by it. Install the CA certificate once on each machine
#                that will use the service and browsers stop complaining, for
#                this and any future certificate you issue from the same CA.
#
# Neither is a substitute for a publicly trusted certificate on an
# internet-facing host. Use Let's Encrypt (via caddy, certbot or your reverse
# proxy) for anything the public can reach; these are for a private network.
#
# POSIX sh, so it runs on macOS and Linux without bash. Windows: see gen-cert.ps1.

set -eu

usage() {
    cat <<'USAGE'
Usage:
  gen-cert.sh self-signed [-d DIR] [-h HOSTNAME]... [-i IP]... [-D DAYS]
  gen-cert.sh ca          [-d DIR] [-h HOSTNAME]... [-i IP]... [-D DAYS]

Options:
  -d DIR       output directory (default: ./certs)
  -h HOSTNAME  a DNS name the certificate should be valid for; repeatable
               (default: localhost and this machine's hostname)
  -i IP        an IP address the certificate should be valid for; repeatable
               (default: 127.0.0.1 and ::1)
  -D DAYS      validity in days (default: 397 for the server certificate,
               3650 for the CA)

Examples:
  ./scripts/gen-cert.sh self-signed
  ./scripts/gen-cert.sh ca -h timetracker.internal -i 10.0.0.7
USAGE
}

MODE="${1:-}"
case "$MODE" in
    self-signed|ca) shift ;;
    -h|--help|help|"") usage; exit 0 ;;
    *) echo "unknown mode: $MODE" >&2; usage >&2; exit 2 ;;
esac

OUT_DIR="./certs"
DAYS=397          # just over a year: the maximum most browsers now accept
CA_DAYS=3650
HOSTS=""
IPS=""

while getopts "d:h:i:D:" opt; do
    case "$opt" in
        d) OUT_DIR="$OPTARG" ;;
        h) HOSTS="${HOSTS:+$HOSTS }$OPTARG" ;;
        i) IPS="${IPS:+$IPS }$OPTARG" ;;
        D) DAYS="$OPTARG" ;;
        *) usage >&2; exit 2 ;;
    esac
done

command -v openssl >/dev/null 2>&1 || {
    echo "openssl is required but was not found on PATH" >&2
    exit 1
}

# Sensible defaults so the common case needs no arguments at all.
if [ -z "$HOSTS" ]; then
    HOSTS="localhost"
    MACHINE="$(hostname || true)"
    [ -n "$MACHINE" ] && [ "$MACHINE" != "localhost" ] && HOSTS="$HOSTS $MACHINE"
fi
[ -z "$IPS" ] && IPS="127.0.0.1 ::1"

mkdir -p "$OUT_DIR"
# The directory holds private keys; nobody else on the machine needs to read it.
chmod 700 "$OUT_DIR"

# Build the subjectAltName list. Modern browsers ignore the Common Name entirely
# and validate against SANs only, so a certificate without them is rejected no
# matter what CN it carries.
SAN=""
for h in $HOSTS; do SAN="${SAN}DNS:${h},"; done
for i in $IPS;   do SAN="${SAN}IP:${i},";  done
SAN="${SAN%,}"

PRIMARY="$(echo "$HOSTS" | cut -d' ' -f1)"
[ -n "$PRIMARY" ] || { echo "no hostname given" >&2; exit 2; }

CONFIG="$OUT_DIR/openssl.cnf"
cat > "$CONFIG" <<CONF
[req]
distinguished_name = dn
prompt             = no

[dn]
CN = $PRIMARY
O  = TimeTracker

[server_ext]
basicConstraints       = critical, CA:FALSE
# A server certificate should be usable for exactly one thing. Restricting the
# key usage limits what a stolen key can be repurposed for.
keyUsage               = critical, digitalSignature, keyEncipherment
extendedKeyUsage       = serverAuth
subjectAltName         = $SAN
subjectKeyIdentifier   = hash

[ca_ext]
basicConstraints       = critical, CA:TRUE, pathlen:0
keyUsage               = critical, keyCertSign, cRLSign
subjectKeyIdentifier   = hash
CONF

# ECDSA P-256 rather than RSA: much smaller keys and faster handshakes for the
# same security level, and universally supported for well over a decade.
KEY_ALGO="-algorithm EC -pkeyopt ec_paramgen_curve:prime256v1"

case "$MODE" in
self-signed)
    echo "Creating a self-signed certificate for: $SAN"

    openssl genpkey $KEY_ALGO -out "$OUT_DIR/server.key"
    chmod 600 "$OUT_DIR/server.key"

    openssl req -new -x509 \
        -key "$OUT_DIR/server.key" \
        -out "$OUT_DIR/server.crt" \
        -days "$DAYS" \
        -config "$CONFIG" \
        -extensions server_ext

    echo
    echo "  certificate: $OUT_DIR/server.crt"
    echo "  private key: $OUT_DIR/server.key  (mode 600)"
    ;;

ca)
    echo "Creating a local CA and a server certificate for: $SAN"

    if [ ! -f "$OUT_DIR/ca.key" ]; then
        openssl genpkey $KEY_ALGO -out "$OUT_DIR/ca.key"
        chmod 600 "$OUT_DIR/ca.key"
        openssl req -new -x509 \
            -key "$OUT_DIR/ca.key" \
            -out "$OUT_DIR/ca.crt" \
            -days "$CA_DAYS" \
            -subj "/CN=TimeTracker Local CA/O=TimeTracker" \
            -config "$CONFIG" \
            -extensions ca_ext
        echo "  created a new CA: $OUT_DIR/ca.crt"
    else
        echo "  reusing the existing CA: $OUT_DIR/ca.crt"
    fi

    openssl genpkey $KEY_ALGO -out "$OUT_DIR/server.key"
    chmod 600 "$OUT_DIR/server.key"

    openssl req -new \
        -key "$OUT_DIR/server.key" \
        -out "$OUT_DIR/server.csr" \
        -config "$CONFIG"

    # A fresh serial each time, so reissuing does not collide with a previous
    # certificate in a browser's or a client's cache.
    openssl x509 -req \
        -in "$OUT_DIR/server.csr" \
        -CA "$OUT_DIR/ca.crt" -CAkey "$OUT_DIR/ca.key" \
        -CAcreateserial \
        -out "$OUT_DIR/server.crt" \
        -days "$DAYS" \
        -extfile "$CONFIG" -extensions server_ext

    rm -f "$OUT_DIR/server.csr"

    echo
    echo "  CA certificate: $OUT_DIR/ca.crt   (install this on client machines)"
    echo "  certificate:    $OUT_DIR/server.crt"
    echo "  private key:    $OUT_DIR/server.key  (mode 600)"
    echo
    echo "  Trust the CA:"
    echo "    Linux  (Debian/Ubuntu): sudo cp $OUT_DIR/ca.crt /usr/local/share/ca-certificates/timetracker.crt && sudo update-ca-certificates"
    echo "    Linux  (Fedora/RHEL):   sudo cp $OUT_DIR/ca.crt /etc/pki/ca-trust/source/anchors/ && sudo update-ca-trust"
    echo "    macOS:                  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain $OUT_DIR/ca.crt"
    echo "    Firefox keeps its own store: Settings -> Privacy & Security -> Certificates -> View Certificates -> Authorities -> Import"
    ;;
esac

rm -f "$CONFIG"

echo
echo "Verify:"
echo "  openssl x509 -in $OUT_DIR/server.crt -noout -text | head -20"
echo
echo "Run TimeTracker with it:"
echo "  ./bin/timetracker --mode=server --addr=0.0.0.0:8443 \\"
echo "      --tls-cert $OUT_DIR/server.crt --tls-key $OUT_DIR/server.key"
