#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "CF Optimizer uninstall must run as root." >&2
  exit 1
fi

cli=/usr/local/bin/cf-optimizer
config_path='/Library/Application Support/CF Optimizer/config.yaml'
service_path=/Library/LaunchDaemons/com.cfoptimizer.daemon.plist
if [ -f "$service_path" ] && [ ! -f "$config_path" ]; then
  echo "CF Optimizer configuration is missing; refusing to remove a service that cannot be cleaned safely." >&2
  exit 1
fi
if [ -x "$cli" ] && [ -f "$config_path" ]; then
  "$cli" --config "$config_path" uninstall
fi

rm -f /usr/local/bin/cf-optimizer /usr/local/bin/cf-optimizerd
rm -rf '/Applications/CF Optimizer.app'
rm -f /usr/local/share/cf-optimizer/config.example.yaml
rm -f /usr/local/share/cf-optimizer/uninstall.sh
rmdir /usr/local/share/cf-optimizer 2>/dev/null || true

echo "CF Optimizer binaries and managed service resources were removed."
echo "Configuration, logs, and history remain in /Library/Application Support/CF Optimizer."
