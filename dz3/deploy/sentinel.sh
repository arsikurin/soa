#!/bin/sh
set -eu
if [ ! -f /data/sentinel.conf ]; then
  cat > /data/sentinel.conf <<CONFIG
bind 0.0.0.0
protected-mode no
port 26379
dir /data
sentinel resolve-hostnames yes
sentinel announce-hostnames yes
sentinel announce-ip $HOSTNAME
sentinel announce-port 26379
sentinel monitor flight-cache redis-a 6379 2
sentinel down-after-milliseconds flight-cache 3000
sentinel failover-timeout flight-cache 10000
sentinel parallel-syncs flight-cache 1
CONFIG
fi
exec redis-server /data/sentinel.conf --sentinel
