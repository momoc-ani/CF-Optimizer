# Packaging resources

The CI and release workflows build packages on native GitHub-hosted runners. Every run exposes exactly one installer for each operating-system and architecture pair: two Windows EXE files, two Linux TAR.GZ bundles, and two macOS DMG files. `SHA256SUMS` is published separately and covers exactly those six files. Regular CI aggregates unsigned commit-validation outputs in the `installers` artifact; version tags publish the same layout with a platform download table and optional signing or notarization.

- `windows/installer.iss` creates per-architecture installers, checks WebView2, and manages the Windows Service.
- `linux/nfpm.yaml` and `linux/package.sh` create one bundle per architecture containing DEB, RPM, and `install.sh`. The installer selects `apt-get`, `dnf`, or `yum`, so Debian/Ubuntu and Fedora/RHEL remain supported without separate release downloads.
- `macos/package.sh` creates and optionally signs/notarizes a PKG in a temporary directory, then publishes only the per-architecture DMG containing that PKG.
- `verify-release-assets.sh` rejects missing, empty, duplicate, or extra packages before checksums and release upload; `release-downloads.sh` generates the six-target download table.
- `wails/` contains reproducible Wails metadata and the shared application icon source.

Upgrades preserve service configuration and state, and repair a missing or stopped service after new files are installed. State schema v2 keeps a versioned `pending_policy` receipt journal so startup and uninstall can recover adapter changes applied before an interrupted commit. Uninstall first recovers that journal and then rolls back the persisted managed-policy receipt chain. Mihomo conflict cleanup rewrites only entries proven to be managed and may finish disk cleanup when the configured controller is offline; reachable-controller protocol or authentication failures still abort. User configuration, logs, history, and cached benchmark data are preserved by default.

The desktop application uses `fyne.io/systray v1.12.2` with the Wails external event loop. Linux uses the desktop session's D-Bus StatusNotifier protocol and does not require native AppIndicator development headers. Closing the window hides it to the tray, while the tray quit action exits only the unprivileged UI process and leaves the service running.

Optional release signing uses these GitHub Secrets:

- Windows: `WINDOWS_CERTIFICATE_BASE64`, `WINDOWS_CERTIFICATE_PASSWORD`.
- macOS signing: `MACOS_CERTIFICATE_BASE64`, `MACOS_CERTIFICATE_PASSWORD`, `MACOS_APP_SIGN_IDENTITY`, `MACOS_INSTALLER_SIGN_IDENTITY`.
- Apple notarization: `MACOS_NOTARY_KEY_BASE64`, `MACOS_NOTARY_KEY_ID`, `MACOS_NOTARY_ISSUER`.

The workflow imports credentials into ephemeral runner keychains and never stores them in build artifacts or the repository.
