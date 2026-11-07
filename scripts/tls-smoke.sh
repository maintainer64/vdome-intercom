#!/bin/sh
set -eu
image="${1:-vdome-intercom:tls}"
test_dir="$(mktemp -d)"
container="vdome-tls-check-$$"
volume="$container-certs"
cleanup() { docker rm -fv "$container" >/dev/null 2>&1 || true; docker volume rm "$volume" >/dev/null 2>&1 || true; rm -rf "$test_dir"; }
trap cleanup EXIT INT TERM
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 -subj /CN=localhost \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
  -keyout "$test_dir/key.pem" -out "$test_dir/cert.pem" >"$test_dir/openssl.log" 2>&1
docker volume create "$volume" >/dev/null
tar -C "$test_dir" -cf - cert.pem key.pem | docker run --rm -i --user 0 --entrypoint sh \
  -v "$volume:/certs" "$image" -ec 'tar -xf - -C /certs; chown 10001:10001 /certs/*; chmod 0400 /certs/key.pem'
docker run -d --name "$container" --read-only --tmpfs /tmp:rw,size=128m,mode=1777 \
  --cap-drop ALL --security-opt no-new-privileges:true \
  -v "$volume:/run/vdome-tls:ro" -p 127.0.0.1::8080 \
  -e VDOME_PROVIDER=demo -e VDOME_MEMORY_ONLY=1 \
  -e VDOME_TLS_CERT_FILE=/run/vdome-tls/cert.pem -e VDOME_TLS_KEY_FILE=/run/vdome-tls/key.pem \
  "$image" >/dev/null
port="$(docker port "$container" 8080/tcp | sed 's/.*://')"
curl --fail --silent --show-error --retry 10 --retry-connrefused --retry-delay 1 \
  --cacert "$test_dir/cert.pem" "https://127.0.0.1:$port/healthz" >/dev/null
docker exec "$container" vdome healthcheck
curl --fail --silent --show-error --cacert "$test_dir/cert.pem" -D "$test_dir/headers" \
  -X POST -H 'X-VDome-Request: 1' -H 'Content-Type: application/json' \
  --data '{"phone":"79001234567"}' "https://127.0.0.1:$port/api/auth/login" > "$test_dir/login.json"
# A second login completes the demo flow; no live provider or real doors.
challenge="$(sed -n 's/.*"challenge":"\([^"]*\)".*/\1/p' "$test_dir/login.json")"
curl --fail --silent --show-error --cacert "$test_dir/cert.pem" -D "$test_dir/headers" \
  -X POST -H 'X-VDome-Request: 1' -H 'Content-Type: application/json' \
  --data "{\"phone\":\"79001234567\",\"code\":\"000000\",\"challenge\":\"$challenge\"}" \
  "https://127.0.0.1:$port/api/auth/login" > /dev/null
grep -i '^set-cookie:' "$test_dir/headers" | grep -qi 'secure'
echo 'TLS smoke passed: verified certificate, internal healthcheck, Secure cookie, read-only cert mount'
