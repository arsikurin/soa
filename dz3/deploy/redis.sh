#!/bin/sh
set -eu
if [ ! -f /data/redis.conf ]; then
  cat > /data/redis.conf <<CONFIG
bind 0.0.0.0
protected-mode no
port 6379
dir /data
save ""
appendonly no
replica-announce-ip $HOSTNAME
replica-announce-port 6379
CONFIG
  if [ "$HOSTNAME" = redis-b ]; then
    echo 'replicaof redis-a 6379' >> /data/redis.conf
  fi
fi
exec redis-server /data/redis.conf
