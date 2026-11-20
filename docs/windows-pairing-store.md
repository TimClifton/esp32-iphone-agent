# Windows pairing storage

The CLI keeps a pairing key for each board in Windows Credential Manager under the Windows account running the daemon. The generic credential target starts with `ESP32iPhoneAgent/pairing/` and includes a hash of the resolved profile directory and the board ID. Use the same Windows account and `ESP32AGENT_HOME` across daemon restarts. Credentials persist on this device. Moving a profile to another directory, account, or device requires pairing in the destination profile.

The Windows profile's `config.json` keeps the client ID, board IDs, board names and listen preferences. On first load of a legacy profile, the CLI stores each legacy key and verifies that it can read it back before replacing the file. If storage or replacement fails, the existing file is preserved. A successfully stored copy can remain for the next migration attempt. If the key in the file conflicts with a protected key, the CLI stops and preserves both. Older exports, backups and temporary files from a legacy profile still need the same protection as the original.

Pairing needs a Windows logon session that can access Credential Manager. A failure to access the store stops configuration loading or pairing. If a credential was removed externally, use a fresh profile and pair again. Use `unpair` to explicitly remove the protected key from a working profile.

The non-Windows CLI retains the existing JSON pairing format.

Run the credential storage tests from `cli` with `go test ./internal/config`. On Windows, set `ESP32AGENT_TEST_NATIVE_CREDENTIALS=1` for that test process to also exercise Credential Manager. The native test creates a synthetic key in a unique temporary profile, reloads it and verifies that unpairing removes the test credential.
