#!/bin/sh
# entrypoint.sh - wait for operator signal via ConfigMap, then start fio

CONFIG_FILE="/etc/initcfg/init_ready"

#echo "Waiting for init_ready condition in ${CONFIG_FILE}..."

while true; do
  if [ -f "$CONFIG_FILE" ]; then
    val=$(cat "$CONFIG_FILE" 2>/dev/null || echo "false")
    if [ "$val" = "true" ]; then
#      echo "init_ready condition met. Starting fio..."
      break
    fi
  fi
#  echo "Still waiting for init_ready=true..."
  sleep 5
done

# Exec fio with the provided arguments
exec fio "$@"
