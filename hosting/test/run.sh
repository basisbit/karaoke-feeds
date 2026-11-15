#!/bin/bash
# Tests hosting/{nginx,caddy,apache} in a distrobox. Run on the host:
#   distrobox create --name kfeed-webtest --image quay.io/fedora/fedora:45
#   distrobox enter kfeed-webtest -- sudo dnf install -y nginx caddy httpd mod_ssl mod_qos python3 openssl curl logrotate
#   distrobox enter kfeed-webtest -- hosting/test/run.sh [nginx] [caddy] [apache]
# Also needs logrotate in the box (dnf install logrotate). Needs bin/kfeed (cd tool && go build -o ../bin/kfeed ./cmd/kfeed) and the songs checkout next to karaoke-feeds.
# The servers run as your user on 127.0.0.1 ports 184xx/180xx; nothing is installed or changed outside $RT.
set -u
HOSTING=$(cd "$(dirname "$0")/.." && pwd); REPO=$(dirname "$HOSTING")
RT=${RT:-$HOME/.cache/karaoke-feeds-webtest}
KFEED=$REPO/bin/kfeed
SERVERS=${*:-nginx caddy apache}
mkdir -p "$RT"/{www/acme,tls,src,log/nginx,log/apache,log/caddy}

# --- certificates: a throw-away CA and a server certificate for localhost ---
if [ ! -f "$RT/tls/fullchain.pem" ]; then
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 -subj /CN=kfeed-test-CA \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -keyout "$RT/tls/ca.key" -out "$RT/tls/ca.pem" 2>/dev/null
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=localhost \
    -keyout "$RT/tls/privkey.pem" -out "$RT/tls/srv.csr" 2>/dev/null
  printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid\n' > "$RT/tls/ext"
  openssl x509 -req -in "$RT/tls/srv.csr" -CA "$RT/tls/ca.pem" -CAkey "$RT/tls/ca.key" -CAcreateserial -days 30 \
    -extfile "$RT/tls/ext" -out "$RT/tls/fullchain.pem" 2>/dev/null
fi

# --- a signed test feed built from the CC songs (hard links, no extra space) ---
[ -f "$RT/src/test.key" ] || "$KFEED" keygen "$RT/src/test.key" >/dev/null
PUB=$("$KFEED" pubkey "$RT/src/test.key")
cat > "$RT/src/feed-config.json" <<JSON
{"name": "Web server test feed", "operator": {"name": "test", "contact": "mailto:test@example.com"},
 "songs": "$(cd "$REPO/../songs" && pwd)", "output": "$RT/www/feed", "keys": ["$PUB"]}
JSON
"$KFEED" build "$RT/src" --key "$RT/src/test.key" | head -2
# decoys that must never be served
FEED=$RT/www/feed
echo secret > "$FEED/.secret"; echo x > "$FEED/README.txt"; echo x > "$FEED/feed.json.tmp"

# --- turn a production config into a test config: host, paths, ports ---
adapt() {
  sed -e "s|feed.example.org|localhost|g" -e "s|/var/www/|$RT/www/|g" \
      -e "s|/etc/ssl/feed/fullchain.pem|$RT/tls/fullchain.pem|" -e "s|/etc/ssl/feed/privkey.pem|$RT/tls/privkey.pem|" \
      -e "/listen \[::\]/d" -e "s|listen 443 |listen 127.0.0.1:$2 |; s|listen 80;|listen 127.0.0.1:$3;|" \
      -e "s|\*:443>|*:$2>|; s|\*:80>|*:$3>|" \
      -e "s|/var/log/nginx/feed/|$RT/log/nginx/|g; s|/var/log/httpd/feed/|$RT/log/apache/|g; s|/var/log/caddy/|$RT/log/caddy/|g" "$1"
}

PIDFILE=""
# Start a server with the open-file limits a systemd service gets by default (soft 1024, hard 524288),
# not with this container's much higher limits; otherwise file-limit problems would stay hidden.
# SOFT_NOFILE: what a systemd drop-in shipped with the config raises the soft limit to (Apache: limits.conf).
prodlimits() { ( ulimit -Sn "${SOFT_NOFILE:-1024}" && ulimit -Hn 524288 && exec "$@" ); }
# Same for a server that stays in the foreground: run as "prodexec cmd &" so that $! is the server's own PID.
prodexec() { ulimit -Sn "${SOFT_NOFILE:-1024}" && ulimit -Hn 524288 && exec "$@"; }
# Variants for comparisons, e.g. NGINX_MAIN=/dev/null or APACHE_TUNING=/dev/null or APACHE_NOFILE=1024.
NGINX_MAIN=${NGINX_MAIN:-$HOSTING/nginx/main.conf}
APACHE_TUNING=${APACHE_TUNING:-$HOSTING/apache/tuning.conf}
APACHE_NOFILE=${APACHE_NOFILE:-$(sed -n 's/^LimitNOFILE=//p' "$HOSTING/apache/limits.conf")}
children() { for s in /proc/[0-9]*/status; do awk -v p="$1" '/^PPid:/{if($2==p) print FILENAME}' "$s" 2>/dev/null; done | sed 's|/proc/||; s|/status||'; }
nofile() { awk '/^Max open files/{print $4}' "/proc/$1/limits"; }
stop() {
  [ -n "$PIDFILE" ] && [ -f "$PIDFILE" ] && kill "$(cat "$PIDFILE")" 2>/dev/null
  local pat="^(nginx|caddy|httpd).* $RT/"   # only test servers started by this script
  for _ in $(seq 50); do pgrep -f "$pat" >/dev/null || break; sleep 0.2; done   # wait until it is really gone
  pgrep -f "$pat" >/dev/null && { echo "FAIL a test server did not stop"; pkill -f "$pat"; }
  PIDFILE=""
}
trap stop EXIT
port_free() { ! (exec 3<>/dev/tcp/127.0.0.1/"$1") 2>/dev/null; }
wait_port() { for _ in $(seq 50); do curl -sk -o /dev/null "https://localhost:$1/" && return 0; sleep 0.2; done; echo "server did not start"; return 1; }

test_server() { # name https-port http-port rate conn
  [ -n "${PRIVATE_ONLY:-}" ] && { wait_port "$2"; stop; return 0; }   # PRIVATE_ONLY=1: only the private-feed checks
  echo; echo "================ $1 ($(cat "$RT/$1/version")) ================"
  wait_port "$2" || { tail -20 "$RT/$1/error.log"; return 1; }
  echo "--- kfeed verify (as a game sees it)"
  SSL_CERT_FILE=$RT/tls/ca.pem "$KFEED" verify "https://localhost:$2/" --key "$PUB" --full
  sleep 11   # let per-IP rate limits recover: a full verify makes ~190 requests in a few seconds
  echo "--- HTTP/2"
  curl -s -o /dev/null -w '%{http_version}\n' --http2 --cacert "$RT/tls/ca.pem" "https://localhost:$2/feed.json" | sed 's/^/negotiated HTTP version: /'
  echo "--- black-box checks"
  python3 "$HOSTING/test/check.py" "https://localhost:$2" "http://localhost:$3" "$FEED" "$RT/tls/ca.pem" --rate "$4" --conn "$5"
  local rc=$?
  echo "--- many downloads at once"
  # shellcheck disable=SC2086
  python3 "$HOSTING/test/load.py" "https://localhost:$2" "$FEED" "$RT/tls/ca.pem" ${LOAD_ARGS:-} || rc=1
  local main; main=$(cat "$PIDFILE"); local procs=$main
  [ "$1" != caddy ] && procs=$(children "$main")
  local lims; lims=$(for p in $procs; do nofile "$p"; done | sort -u | tr '\n' ' ')
  case $1 in
    nginx) [ "$(for p in $procs; do nofile "$p"; done | sort -n | head -1)" -ge 20000 ] \
             && echo "PASS open-file limit of the $(echo "$procs" | wc -w) workers raised by worker_rlimit_nofile: $lims" \
             || { echo "FAIL open-file limit of the workers: $lims"; rc=1; } ;;
    caddy) [ "$lims" -ge 65536 ] 2>/dev/null && echo "PASS Caddy raised its own open-file limit to the hard limit: $lims" \
             || { echo "FAIL Caddy open-file limit: $lims"; rc=1; } ;;
    apache) [ "$(for p in $procs; do nofile "$p"; done | sort -n | head -1)" -ge 65536 ] \
             && echo "PASS open-file limit of the Apache processes as set by limits.conf: $lims" \
             || { echo "FAIL open-file limit of the Apache processes: $lims"; rc=1; } ;;
  esac
  if grep -hiE "worker_connections are not enough|too many open files|MaxRequestWorkers|accept4?\(\) failed|socket\(\) failed|scoreboard is full" \
       "$RT/log/$1/error.log" "$RT/$1/error.log" 2>/dev/null | head -3 | grep .; then echo "FAIL capacity errors in the error log (above)"; rc=1
  else echo "PASS no capacity errors in the error log (connections, open files, workers)"; fi
  echo "--- logging"
  if [ -n "${MAIN_ACCESS:-}" ]; then
    if [ -s "$MAIN_ACCESS" ]; then echo "FAIL access log: the server's main access log got $(wc -l < "$MAIN_ACCESS") lines"; rc=1
    else echo "PASS access log: nothing written (the server's main access log stays empty)"; fi
  fi
  compgen -G "$RT/log/$1/*access*" >/dev/null && { echo "FAIL an access log file exists in $RT/log/$1"; rc=1; }
  if [ -f "$RT/log/$1/error.log" ]; then echo "PASS error log written to the configured file ($(wc -l < "$RT/log/$1/error.log") lines)"
  else echo "FAIL no error log at $RT/log/$1/error.log"; rc=1; fi
  stop
  [ -f "$HOSTING/$1/logrotate" ] && { rotate_test "$1" || rc=1; }
  return $rc
}

rotate_test() { # 16 forced daily rotations must leave the live file plus exactly 14 old ones
  local d=$RT/logrotate-$1; rm -rf "$d"; mkdir -p "$d/log"
  sed "s|/var/log/[a-z0-9]*/feed/|$d/log/|" "$HOSTING/$1/logrotate" > "$d/conf"
  logrotate -d -s "$d/state" "$d/conf" > "$d/debug.txt" 2>&1 || { echo "FAIL logrotate rejects the config"; cat "$d/debug.txt"; return 1; }
  for i in $(seq 16); do echo "error $i" >> "$d/log/error.log"; logrotate -f -s "$d/state" "$d/conf" 2>>"$d/err.txt"; done
  local old; old=$(find "$d/log" -name 'error.log.*' | wc -l)
  if [ "$old" = 14 ] && [ ! -s "$d/err.txt" ]; then echo "PASS logrotate: after 16 rotations 14 old error logs are kept ($(find "$d/log" -name '*.gz' | wc -l) compressed)"
  else echo "FAIL logrotate: $old old files kept (want 14)"; cat "$d/err.txt"; return 1; fi
}

# --- "behind a CDN" mode: switch the documented lines on, with a test secret, and check with check_cdn.py ---
CDN_SECRET=test-secret-4f8a2c
cdnmode() { # file -> CDN-mode copy on stdout
  sed -e '/^map \$http_x_feed_origin_auth/d' -e 's/^# map \$http_x_feed_origin_auth/map $http_x_feed_origin_auth/' \
      -e 's/^#Define FEED_\(BEHIND_CDN\|CDN_\)/Define FEED_\1/' -e 's/^\(\s*\)# \(@not_from_cdn\|error @not_from_cdn\)/\1\2/' \
      -e "s/change-me-to-a-long-random-secret/$CDN_SECRET/g" "$1"
}
test_cdn() { # name https-port limits
  [ -n "${PRIVATE_ONLY:-}" ] && { wait_port "$2"; stop; return 0; }
  echo "--- behind a CDN (secret header, client address from X-Real-IP)"
  wait_port "$2" || return 1
  python3 "$HOSTING/test/check_cdn.py" "https://localhost:$2" "$RT/tls/ca.pem" "$CDN_SECRET" X-Real-IP --limits "$3"
  local rc=$?
  stop
  return $rc
}

# --- "private feed" mode: switch the documented lines on, with a test token list, and check with check_private.py ---
TOKEN=test-token-Zk3vQ9xLm2Rt7WpY4sHd
privatemode() { # file -> private-mode copy on stdout
  sed -e '/^map \$feed_token \$feed_token_ok/d' -e 's/^# map \$feed_token \$feed_token_ok/map $feed_token $feed_token_ok/' \
      -e '/^\s*# --- private feed: begin$/,/^\s*# --- private feed: end$/{/--- private feed:/!s/^\(\s*\)# /\1/}' \
      -e 's/^#Define FEED_PRIVATE$/Define FEED_PRIVATE/' \
      -e "s#/etc/nginx/feed-tokens.map#$RT/tokens.nginx#; s#/etc/caddy/feed-tokens#$RT/tokens.caddy#; s#/etc/httpd/feed-tokens.txt#$RT/tokens.apache#" "$1"
}
blobsmode() { # private-mode file -> copy that sends blob downloads on to another address
  sed -e 's/^\(\s*\)# \(rewrite "^\/s\/.*cdn\.example\.org\)/\1\2/' -e 's/^\(\s*\)# \(redir @personal_blob\)/\1\2/' \
      -e 's/^#Define FEED_PRIVATE_BLOBS/Define FEED_PRIVATE_BLOBS/' "$1"
}
tokens() { # server [token]: write the token list in the server's format
  case $1 in nginx) echo "${2:+$2 1;}" ;; *) echo "${2:+$2 1}" ;; esac > "$RT/tokens.$1"
}
start() { # server site-config: start the server with another site config
  case $1 in
    nginx)
      sed "s|$RT/nginx/feed.conf|$2|" "$RT/nginx/nginx.conf" > "$RT/nginx/nginx-variant.conf"
      nginx -t -q -c "$RT/nginx/nginx-variant.conf" -e "$RT/nginx/error.log" || return 1
      prodlimits nginx -c "$RT/nginx/nginx-variant.conf" -e "$RT/nginx/error.log"; PIDFILE=$RT/nginx/nginx.pid ;;
    caddy)
      "$CADDY" validate --config "$2" --adapter caddyfile >"$RT/caddy/variant-validate.log" 2>&1 || { tail -3 "$RT/caddy/variant-validate.log"; return 1; }
      GOMAXPROCS=2 XDG_DATA_HOME=$RT/caddy XDG_CONFIG_HOME=$RT/caddy prodexec "$CADDY" run --config "$2" --adapter caddyfile >>"$RT/caddy/error.log" 2>&1 &
      echo $! > "$RT/caddy/caddy.pid"; PIDFILE=$RT/caddy/caddy.pid ;;
    apache)
      sed "s|$RT/apache/feed.conf|$2|" "$RT/apache/httpd.conf" > "$RT/apache/httpd-variant.conf"
      httpd -t -f "$RT/apache/httpd-variant.conf" 2>&1 | grep -v "Syntax OK"; httpd -t -f "$RT/apache/httpd-variant.conf" >/dev/null 2>&1 || return 1
      SOFT_NOFILE=$APACHE_NOFILE prodlimits httpd -f "$RT/apache/httpd-variant.conf" -k start; PIDFILE=$RT/apache/httpd.pid ;;
  esac
}
test_private() { # name https-port site-config
  echo "--- private feed (personal addresses with a token)"
  local rc=0 base="https://localhost:$2" code out
  privatemode "$3" > "$3-private"; blobsmode "$3-private" > "$3-private-blobs"
  tokens "$1" "$TOKEN"
  start "$1" "$3-private" && wait_port "$2" || return 1
  python3 "$HOSTING/test/check_private.py" "$base" "$RT/tls/ca.pem" "$FEED" "$TOKEN" || rc=1
  out=$(SSL_CERT_FILE=$RT/tls/ca.pem "$KFEED" verify "$base/s/$TOKEN/" --key "$PUB" 2>&1)
  if grep -q "^OK:" <<<"$out" && ! grep -q "$TOKEN" <<<"$out"; then echo "PASS kfeed verify reads the feed through the personal address and doesn't print it"
  else echo "FAIL kfeed verify through the personal address:"; echo "$out" | tail -5; rc=1; fi
  sleep 11   # let per-IP rate limits recover
  # the token is taken off the list: access ends
  tokens "$1"
  case $1 in
    nginx) nginx -s reload -c "$RT/nginx/nginx-variant.conf" -e "$RT/nginx/error.log"; sleep 1 ;;
    caddy) stop; start caddy "$3-private" && wait_port "$2" ;;   # the test Caddy has no admin API for "caddy reload"
    apache) sleep 1 ;;                                              # the token file is re-read by itself
  esac
  code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$RT/tls/ca.pem" "$base/s/$TOKEN/feed.json")
  if [ "$code" = 403 ]; then echo "PASS token removed from the list: 403"; else echo "FAIL token removed from the list: $code"; rc=1; fi
  stop
  echo "--- private feed, big files sent on to another address"
  tokens "$1" "$TOKEN"
  start "$1" "$3-private-blobs" && wait_port "$2" || return 1
  python3 "$HOSTING/test/check_private.py" "$base" "$RT/tls/ca.pem" "$FEED" "$TOKEN" --blobs https://cdn.example.org || rc=1
  stop
  local n; n=$(cat "$RT/log/$1/error.log" "$RT/$1/error.log" 2>/dev/null | grep -c "$TOKEN")
  echo "INFO error log lines that contain the personal address: $n"
  return $rc
}

RC=0
for s in $SERVERS; do
  for p in 18441 18442 18443 18081 18082 18083; do port_free $p || { echo "port $p is in use: stop the old test server first"; exit 1; }; done
  mkdir -p "$RT/$s"
  rm -f "$RT/$s/error.log" "$RT/log/$s/"*   # each run judges only its own log lines
  case $s in
  nginx)
    nginx -v 2>&1 | sed 's/.*: //' > "$RT/nginx/version"
    mkdir -p "$RT/nginx/tmp"
    adapt "$HOSTING/nginx/feed.conf" 18441 18081 > "$RT/nginx/feed.conf"
    # 2 workers = a small 2-core server; with "auto" this machine's 16 cores would hide per-worker limits
    sed "s|^worker_processes auto;|worker_processes 2;|" "$NGINX_MAIN" > "$RT/nginx/main.conf"
    grep -q "^worker_processes" "$RT/nginx/main.conf" || echo "worker_processes 2; events { worker_connections 4096; }" >> "$RT/nginx/main.conf"
    cat > "$RT/nginx/nginx.conf" <<CONF
pid $RT/nginx/nginx.pid;
error_log $RT/nginx/error.log warn;
include $RT/nginx/main.conf;
http {
  access_log $RT/nginx/main-access.log;   # what a distribution's nginx.conf does; the site must override it
  client_body_temp_path $RT/nginx/tmp/body; proxy_temp_path $RT/nginx/tmp/proxy; fastcgi_temp_path $RT/nginx/tmp/fcgi;
  uwsgi_temp_path $RT/nginx/tmp/uwsgi; scgi_temp_path $RT/nginx/tmp/scgi;
  include $RT/nginx/feed.conf;
}
CONF
    nginx -t -q -c "$RT/nginx/nginx.conf" -e "$RT/nginx/error.log" || { RC=1; continue; }
    prodlimits nginx -c "$RT/nginx/nginx.conf" -e "$RT/nginx/error.log"
    PIDFILE=$RT/nginx/nginx.pid
    : > "$RT/nginx/main-access.log"; MAIN_ACCESS=$RT/nginx/main-access.log
    test_server nginx 18441 18081 yes yes || RC=1
    cdnmode "$RT/nginx/feed.conf" > "$RT/nginx/feed-cdn.conf"
    sed "s|$RT/nginx/feed.conf|$RT/nginx/feed-cdn.conf|" "$RT/nginx/nginx.conf" > "$RT/nginx/nginx-cdn.conf"
    nginx -t -q -c "$RT/nginx/nginx-cdn.conf" -e "$RT/nginx/error.log" || { RC=1; continue; }
    prodlimits nginx -c "$RT/nginx/nginx-cdn.conf" -e "$RT/nginx/error.log"; PIDFILE=$RT/nginx/nginx.pid
    test_cdn nginx 18441 yes || RC=1
    test_private nginx 18441 "$RT/nginx/feed.conf" || RC=1 ;;
  caddy)
    CADDY=${CADDY:-caddy}
    "$CADDY" version | cut -d' ' -f1 > "$RT/caddy/version"
    adapt "$HOSTING/caddy/Caddyfile" 18442 18082 \
      | sed -e "0,/^{/s||{\n\tadmin off\n\tdefault_bind 127.0.0.1\n\thttp_port 18082\n\thttps_port 18442\n\tstorage file_system $RT/caddy/data|" \
            -e "s|^localhost {|localhost {\n\ttls $RT/tls/fullchain.pem $RT/tls/privkey.pem|" > "$RT/caddy/Caddyfile"
    "$CADDY" validate --config "$RT/caddy/Caddyfile" --adapter caddyfile >"$RT/caddy/error.log" 2>&1 || { tail -5 "$RT/caddy/error.log"; RC=1; continue; }
    GOMAXPROCS=2 XDG_DATA_HOME=$RT/caddy XDG_CONFIG_HOME=$RT/caddy prodexec "$CADDY" run --config "$RT/caddy/Caddyfile" --adapter caddyfile >"$RT/caddy/error.log" 2>&1 &
    echo $! > "$RT/caddy/caddy.pid"; PIDFILE=$RT/caddy/caddy.pid
    RATE=no; "$CADDY" list-modules 2>/dev/null | grep -q rate_limit && RATE=yes
    MAIN_ACCESS=""
    test_server caddy 18442 18082 $RATE no || RC=1
    cdnmode "$RT/caddy/Caddyfile" > "$RT/caddy/Caddyfile-cdn"
    "$CADDY" validate --config "$RT/caddy/Caddyfile-cdn" --adapter caddyfile >"$RT/caddy/cdn-validate.log" 2>&1 || { tail -3 "$RT/caddy/cdn-validate.log"; RC=1; continue; }
    GOMAXPROCS=2 XDG_DATA_HOME=$RT/caddy XDG_CONFIG_HOME=$RT/caddy prodexec "$CADDY" run --config "$RT/caddy/Caddyfile-cdn" --adapter caddyfile >>"$RT/caddy/error.log" 2>&1 &
    echo $! > "$RT/caddy/caddy.pid"; PIDFILE=$RT/caddy/caddy.pid
    test_cdn caddy 18442 $RATE || RC=1
    test_private caddy 18442 "$RT/caddy/Caddyfile" || RC=1 ;;
  apache)
    httpd -v | head -1 | sed 's/.*: //' > "$RT/apache/version"
    QOS=$(ls /etc/httpd/modules/mod_qos.so 2>/dev/null)
    adapt "$HOSTING/apache/feed.conf" 18443 18083 > "$RT/apache/feed.conf"
    cat > "$RT/apache/httpd.conf" <<CONF
ServerRoot /etc/httpd
ServerName localhost
PidFile $RT/apache/httpd.pid
ErrorLog $RT/apache/error.log
LogLevel warn
DefaultRuntimeDir $RT/apache
Listen 127.0.0.1:18083
Listen 127.0.0.1:18443
$(for m in mpm_event unixd authz_core alias mime log_config env setenvif filter deflate headers rewrite reqtimeout ssl socache_shmcb http2; do echo "LoadModule ${m}_module modules/mod_$m.so"; done)
${QOS:+LoadModule qos_module modules/mod_qos.so}
TypesConfig /etc/mime.types
LogFormat "%h %l %u %t \"%r\" %>s %b" common
# what a distribution's httpd.conf does; the site must override it
CustomLog $RT/apache/main-access.log common
SSLSessionCache shmcb:$RT/apache/ssl_scache(512000)
Include $APACHE_TUNING
Include $RT/apache/feed.conf
CONF
    httpd -t -f "$RT/apache/httpd.conf" 2>&1 | grep -v "Syntax OK" ; httpd -t -f "$RT/apache/httpd.conf" >/dev/null 2>&1 || { RC=1; continue; }
    SOFT_NOFILE=$APACHE_NOFILE prodlimits httpd -f "$RT/apache/httpd.conf" -k start
    PIDFILE=$RT/apache/httpd.pid
    QOSFLAG=no; [ -n "$QOS" ] && QOSFLAG=yes
    : > "$RT/apache/main-access.log"; MAIN_ACCESS=$RT/apache/main-access.log
    test_server apache 18443 18083 $QOSFLAG $QOSFLAG || RC=1
    cdnmode "$RT/apache/feed.conf" > "$RT/apache/feed-cdn.conf"
    sed "s|$RT/apache/feed.conf|$RT/apache/feed-cdn.conf|" "$RT/apache/httpd.conf" > "$RT/apache/httpd-cdn.conf"
    httpd -t -f "$RT/apache/httpd-cdn.conf" 2>&1 | grep -v "Syntax OK"; httpd -t -f "$RT/apache/httpd-cdn.conf" >/dev/null 2>&1 || { RC=1; continue; }
    SOFT_NOFILE=$APACHE_NOFILE prodlimits httpd -f "$RT/apache/httpd-cdn.conf" -k start; PIDFILE=$RT/apache/httpd.pid
    test_cdn apache 18443 $QOSFLAG || RC=1
    test_private apache 18443 "$RT/apache/feed.conf" || RC=1 ;;
  esac
done
exit $RC
