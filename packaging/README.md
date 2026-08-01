# Packaging resources

The CI and release workflows build all packages from native GitHub-hosted runners. Regular CI runs aggregate unsigned commit-validation packages and `SHA256SUMS` in the `installers` artifact. Version tags publish the same package formats through the release workflow, with optional signing and notarization.

- `windows/installer.iss` creates per-architecture installers, checks WebView2, and manages the Windows Service.
- `linux/nfpm.yaml` and `linux/package.sh` create DEB and RPM packages with GTK 3 and WebKitGTK 4.1 runtime dependencies.
- `macos/package.sh` creates per-architecture PKG and DMG files and supports optional Developer ID signing and notarization.
- `wails/` contains reproducible Wails metadata and the shared application icon source.

Upgrades preserve the service configuration and state. Uninstall first rolls back the persisted managed-policy receipt chain. User configuration, logs, history, and cached benchmark data are preserved by default.

The desktop application uses `fyne.io/systray v1.12.2` with the Wails external event loop. Linux uses the desktop session's D-Bus StatusNotifier protocol and does not require native AppIndicator development headers. Closing the window hides it to the tray, while the tray quit action exits only the unprivileged UI process and leaves the service running.

Optional release signing uses these GitHub Secrets:

- Windows: `WINDOWS_CERTIFICATE_BASE64`, `WINDOWS_CERTIFICATE_PASSWORD`.
- macOS signing: `MACOS_CERTIFICATE_BASE64`, `MACOS_CERTIFICATE_PASSWORD`, `MACOS_APP_SIGN_IDENTITY`, `MACOS_INSTALLER_SIGN_IDENTITY`.
- Apple notarization: `MACOS_NOTARY_KEY_BASE64`, `MACOS_NOTARY_KEY_ID`, `MACOS_NOTARY_ISSUER`.

The workflow imports credentials into ephemeral runner keychains and never stores them in build artifacts or the repository.
