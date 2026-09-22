# Release, Code Signing, and Notarisation Guide

This document outlines the desktop packaging, code signing, notarisation, and release verification pipeline for Halp Desktop (`desktop/`), governed by `.github/workflows/release.yml` and Decision 46 in `docs/OPEN-QUESTIONS.md`.

---

## 1. Secrets and CI Configuration

The release workflow is designed to build across macOS, Windows, and Linux on any git tag matching `v*.*.*` or manual `workflow_dispatch`.

When secrets are absent (such as in developer forks, PR builds, or local CI runs), **all signing and notarisation steps skip cleanly with a notice**, producing unsigned artifacts without failing the build.

### Required Repository Secrets

| Secret Name | Platform | Description |
|---|---|---|
| `APPLE_CERTIFICATE` | macOS | Base64-encoded `.p12` containing the Developer ID Application certificate and private key. |
| `APPLE_CERTIFICATE_PASSWORD` | macOS | Password protecting the `.p12` bundle. |
| `APPLE_SIGNING_IDENTITY` | macOS | Common Name of the certificate (e.g., `Developer ID Application: Halp World Inc (XXXXXXXXXX)`). |
| `APPLE_ID` | macOS | Apple ID email address authorised to notarise software. |
| `APPLE_PASSWORD` | macOS | Apple app-specific password generated at appleid.apple.com (or App Store Connect API Key). |
| `APPLE_TEAM_ID` | macOS | 10-character Apple Developer Team ID. |
| `WINDOWS_CERTIFICATE` | Windows | Base64-encoded `.pfx` code-signing certificate (or Azure Trusted Signing identity). |
| `WINDOWS_CERTIFICATE_PASSWORD` | Windows | Password protecting the `.pfx` certificate. |
| `GPG_PRIVATE_KEY` | All (Linux/Checksums) | Armored ASCII export of the GPG private release key for detached signatures. |
| `GPG_PASSPHRASE` | All (Linux/Checksums) | Passphrase protecting the GPG private release key. |

---

## 2. Platform Signing & Verification

### macOS (Universal DMG)
- **Hardened Runtime**: Enabled during `cargo tauri build` (`--options runtime`).
- **Entitlements**: Strict sandbox with outbound network access to `halp.to` and native notification APIs.
- **Notarisation**: Submitted to Apple Notary Service via `notarytool` / Tauri action, ticket stapled directly into the `.dmg`.
- **Verification Command** (on a clean test machine that did not produce the build):
  ```bash
  # Check Gatekeeper assessment
  spctl --assess --type execute --verbose /Volumes/Halp/Halp.app

  # Check staple status
  stapler validate Halp.dmg
  ```

### Windows (MSI / NSIS EXE)
- **Certificate**: Extended Validation (EV) code signing certificate with RFC 3161 timestamping authority (`http://timestamp.digicert.com`).
- **SmartScreen**: EV certificates establish immediate positive reputation in Microsoft Defender SmartScreen, eliminating unknown publisher warning banners upon download.
- **Verification Command**:
  ```powershell
  signtool verify /pa /v Halp-Setup.msi
  ```

### Linux (Debian .deb / AppImage)
- **Integrity**: Standard SHA-256 digest listed in `SHA256SUMS`.
- **Detached GPG Signature**: `SHA256SUMS.asc` generated using Halp's public release key.
- **Verification Command**:
  ```bash
  sha256sum -c SHA256SUMS
  gpg --verify SHA256SUMS.asc SHA256SUMS
  ```

---

## 3. Human Blocker and Lead Time (Issue #25)

Code signing and notarisation require legal entity registration and payments that cannot be automated by software agents:

1. **Apple Developer Program Enrolment**:
   - **Entity**: Organization (requires legal corporate registration and D-U-N-S number from Dun & Bradstreet).
   - **Cost**: \$99 USD / year.
   - **Lead Time**: 2–4 weeks (D-U-N-S generation + Apple legal entity identity check and verification phone call).
2. **Windows Code Signing Certificate (EV)**:
   - **Entity**: Organization verification via DigiCert, Sectigo, or Azure Trusted Signing.
   - **Cost**: \$300–\$450 USD / year (or per-signature billing on Azure).
   - **Lead Time**: 1–2 weeks (business registry verification and hardware token issuance / cloud identity setup).

**Handoff Action Item for Project Maintainer**:
Complete the organization enrolment on [developer.apple.com](https://developer.apple.com) and order the EV certificate. Once issued, export the `.p12` and `.pfx` bundles and paste their base64 strings into the repository secrets listed above. The CI pipeline is fully wired and waiting.

---

## 4. Certificate Renewal Schedule

To prevent unexpected release interruptions, renewals must be scheduled prior to certificate lapse:

- **Apple Developer Program Membership**: Renew annually. Expired membership immediately suspends notarisation requests.
- **Developer ID Application Certificate**: Valid for 5 years from issuance. Existing distributed binaries remain valid past expiration because they are signed with an Apple RFC 3161 timestamp.
- **Windows EV Certificate**: Valid for 1–3 years. Existing distributed installers remain valid because they are timestamped.
- **Automated Reminder**: A recurring calendar notification is maintained for 90, 60, and 30 days prior to expiry.
