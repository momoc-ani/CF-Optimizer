#!/bin/sh
set -eu

config_path=/etc/cf-optimizer/config.yaml
if [ ! -f "$config_path" ]; then
  /usr/bin/cf-optimizer --config "$config_path" init
  chmod 0600 "$config_path"
fi

if [ -d /run/systemd/system ]; then
  if [ -f /etc/systemd/system/cf-optimizer.service ]; then
    /usr/bin/cf-optimizer --config "$config_path" start
  else
    /usr/bin/cf-optimizer --config "$config_path" install --daemon /usr/bin/cf-optimizerd
  fi
fi
