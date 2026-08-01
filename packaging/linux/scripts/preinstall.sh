#!/bin/sh
set -eu

config_path=/etc/cf-optimizer/config.yaml
if [ -x /usr/bin/cf-optimizer ] && [ -f /etc/systemd/system/cf-optimizer.service ]; then
  /usr/bin/cf-optimizer --config "$config_path" stop || exit 1
fi
