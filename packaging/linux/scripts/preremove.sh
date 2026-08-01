#!/bin/sh
set -eu

case "${1:-remove}" in
  upgrade|1)
    exit 0
    ;;
esac

config_path=/etc/cf-optimizer/config.yaml
if [ -f /etc/systemd/system/cf-optimizer.service ] && [ ! -f "$config_path" ]; then
  echo "CF Optimizer configuration is missing; refusing to remove a service that cannot be cleaned safely." >&2
  exit 1
fi
if [ -x /usr/bin/cf-optimizer ] && [ -f "$config_path" ]; then
  /usr/bin/cf-optimizer --config "$config_path" uninstall
fi
