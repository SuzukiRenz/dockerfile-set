#!/bin/bash

export PUID=${PUID:-'1001'}
export PGID=${PGID:-'1001'}
export UMASK=${UMASK:-'022'}
export RUN_ARIA2=${RUN_ARIA2:-'true'}
export S3_ENABLE=${S3_ENABLE:-'true'}
export S3_PORT=${S3_PORT:-'5246'}

export KOMARI_HOST=${KOMARI_HOST:-${NEZHA_SERVER:-''}}
export KOMARI_TOKEN=${KOMARI_TOKEN:-${NEZHA_KEY:-''}}
export KOMARI_ARGS=${KOMARI_ARGS:-''}
export KOMARI_VERSION=${KOMARI_VERSION:-'1.1.40'}

SUPERVISORD_CONFIG_PATH="/etc/supervisord.conf"
AGENT_DIR="/opt/komari"
AGENT_BIN="$AGENT_DIR/komari-agent"
ARIA2_CONF="/opt/openlist/data/aria2.conf"
ARIA2_DOWNLOAD_DIR="/opt/openlist/data/aria2"

umask ${UMASK}

prepare_aria2() {
    if [ "$RUN_ARIA2" != "true" ] || ! command -v aria2c >/dev/null 2>&1; then
        return 1
    fi

    mkdir -p "$ARIA2_DOWNLOAD_DIR"
    if [ ! -f "$ARIA2_CONF" ]; then
        cat > "$ARIA2_CONF" << EOF
enable-rpc=true
rpc-listen-all=false
rpc-listen-port=6800
rpc-allow-origin-all=false
dir=${ARIA2_DOWNLOAD_DIR}
continue=true
max-concurrent-downloads=3
split=8
max-connection-per-server=8
min-split-size=1M
file-allocation=none
save-session=/opt/openlist/data/aria2.session
input-file=/opt/openlist/data/aria2.session
save-session-interval=60
EOF
        touch /opt/openlist/data/aria2.session
    fi

    return 0
}

prepare_komari() {
    if [ -z "$KOMARI_HOST" ] || [ -z "$KOMARI_TOKEN" ]; then
        echo "Komari: no host/token configured, skipping."
        return 1
    fi

    if [ ! -d "$AGENT_DIR" ]; then
        mkdir -p "$AGENT_DIR"
    fi

    if [ -f "$AGENT_BIN" ]; then
        echo "Komari: agent already exists, skipping download."
        return 0
    fi

    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64) FILE_ARCH="amd64" ;;
        aarch64) FILE_ARCH="arm64" ;;
        *) echo "Komari: unsupported arch $ARCH, skipping."; return 1 ;;
    esac

    DOWNLOAD_URL="https://github.com/komari-monitor/komari-agent/releases/download/${KOMARI_VERSION}/komari-agent-linux-${FILE_ARCH}"
    echo "Komari: downloading agent ${KOMARI_VERSION} (${FILE_ARCH})..."

    if curl -L -o "$AGENT_BIN" "$DOWNLOAD_URL"; then
        chmod +x "$AGENT_BIN"
        echo "Komari: download complete."
        return 0
    else
        echo "Komari: download failed."
        rm -f "$AGENT_BIN"
        return 1
    fi
}

echo "Generating supervisord config..."
prepare_aria2

cat > ${SUPERVISORD_CONFIG_PATH} << EOF
[supervisord]
nodaemon=true
logfile=/var/log/supervisord.log
pidfile=/var/run/supervisord.pid
user=root

[program:openlist]
directory=/opt/openlist
command=su-exec ${PUID}:${PGID} ./openlist server --no-prefix
autorestart=true
priority=20
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
EOF

# aria2 listens on 127.0.0.1:6800 only. OpenList's built-in default
# aria2_uri is http://localhost:6800/jsonrpc with an empty secret.
if [ "$RUN_ARIA2" = "true" ] && command -v aria2c >/dev/null 2>&1; then
    cat >> ${SUPERVISORD_CONFIG_PATH} << EOF

[program:aria2]
command=aria2c --conf-path=/opt/openlist/data/aria2.conf
autorestart=true
priority=10
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
EOF
fi

prepare_komari
if [ $? -eq 0 ] && [ -f "$AGENT_BIN" ]; then
    FINAL_HOST=${KOMARI_HOST%/}
    if [[ "$FINAL_HOST" != http://* ]] && [[ "$FINAL_HOST" != https://* ]]; then
        FINAL_HOST="http://${FINAL_HOST}"
    fi

    echo "Configuring Komari Agent (Endpoint: $FINAL_HOST)..."
    cat >> ${SUPERVISORD_CONFIG_PATH} << EOF

[program:komari-agent]
directory=${AGENT_DIR}
command=${AGENT_BIN} -e ${FINAL_HOST} -t ${KOMARI_TOKEN} ${KOMARI_ARGS}
autorestart=true
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
EOF
fi

if [ -f /etc/os-release ]; then
    sed -i "s/^ID=.*/ID=alpine/" /etc/os-release 2>/dev/null || true
fi

chown -R ${PUID}:${PGID} /opt/openlist/data

if [ "$1" = "version" ]; then
  ./openlist version
else
  echo "Starting services..."
  exec supervisord -n -c ${SUPERVISORD_CONFIG_PATH}
fi
