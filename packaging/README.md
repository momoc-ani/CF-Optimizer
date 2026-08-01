# Packaging resources

The release workflow builds all packages from native GitHub-hosted runners.

- `windows/installer.iss` creates per-architecture installers, checks WebView2, and manages the Windows Service.
- `linux/nfpm.yaml` and `linux/package.sh` create DEB and RPM packages with GTK 3, WebKitGTK 4.1, and Ayatana AppIndicator runtime dependencies.
- `macos/package.sh` creates per-architecture PKG and DMG files and supports optional Developer ID signing and notarization.
- `wails/` contains reproducible Wails metadata and the shared application icon source.

Upgrades preserve the service configuration and state. Uninstall first rolls back the persisted managed-policy receipt chain. User configuration, logs, history, and cached benchmark data are preserved by default.

The desktop application uses `getlantern/systray v1.2.2`. Linux native builds therefore require `libayatana-appindicator3-dev`; the packaged application declares the matching runtime library. Closing the window hides it to the tray, while the tray quit action exits only the unprivileged UI process and leaves the service running.

Optional release signing uses these GitHub Secrets:

- Windows: `WINDOWS_CERTIFICATE_BASE64`, `WINDOWS_CERTIFICATE_PASSWORD`.
- macOS signing: `MACOS_CERTIFICATE_BASE64`, `MACOS_CERTIFICATE_PASSWORD`, `MACOS_APP_SIGN_IDENTITY`, `MACOS_INSTALLER_SIGN_IDENTITY`.
- Apple notarization: `MACOS_NOTARY_KEY_BASE64`, `MACOS_NOTARY_KEY_ID`, `MACOS_NOTARY_ISSUER`.

The workflow imports credentials into ephemeral runner keychains and never stores them in build artifacts or the repository.
