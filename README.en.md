# backlog-assistant

[日本語](README.md) | English

A desktop app that extracts and bulk-updates issues and user information in Nulab [Backlog](https://backlog.com/) safely, using a local cache. Open source software under the MIT License.

- Tech stack: [Wails v2](https://wails.io/) + Vue 3 + TypeScript / Go
- Local cache: SQLite ([modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), pure Go)
- Excel I/O: [excelize](https://github.com/qax-os/excelize)
- Distribution targets: Windows / macOS (built automatically by GitHub Actions)

## Download

Download and unzip the latest release for Windows or macOS from [Releases](https://github.com/r404r/p-backlog-assistant/releases). Builds of the latest development code are available as Actions artifacts.

See the [User Guide](docs/USER_GUIDE.en.md) for installation steps, how to use each screen, and the rules for filling in the Excel template.

## Features

### Connection Settings

- Profile management for multiple spaces (add, edit, delete, switch). A successful connection test is required before saving
- **API keys are stored only in the OS keychain** (Windows Credential Manager / macOS Keychain)
- Displays the permission status measured against the API (when administrator features are unavailable, the reduced behavior is stated explicitly)

### Issues

- Issue sync: auto (full on the first run) / full / incremental. Incremental sync uses `updatedSince` plus activity-based deletion detection to save API calls
- Progress (number of issues fetched) is shown while syncing. During a sync, switching projects, searching, exporting to Excel, and bulk updating are blocked on every screen (so that a partially populated local database is never read)
- Local database search: keyword (issue key + summary + description; multiple space-separated words combined with AND / OR; press Enter in the field to search), created/updated date range, status, assignee
- Search results are shown **200 rows per page** (first / previous / page number field / next / last). The number of matches and the range being displayed are shown, and if the data changes because of a sync or a single-issue refresh, the pager is disabled and you are prompted to search again
- Filtering, listing, and exporting by **custom field** (text is a partial match, number and date are ranges, list types are a multi-select of their options)
- **Click an issue key in the result list to open the issue details in a popup** (custom fields, parent issue, and the description body included; copying the URL and opening it in a browser are available there too). **The clipboard icon next to the key copies the issue URL directly**
- **"Get the latest state"** in the detail popup re-fetches just that one issue from Backlog and applies it locally (no need to sync the whole project). It also fetches and displays **comments (up to the most recent 500)** (comments are not retrieved by a normal sync and are covered by neither keyword search nor Excel export)
- **Excel export** (selectable columns, up to 1,000,000 matching rows; above that the export fails with an error instead of producing a file). Custom field columns and the parent issue key column can also be selected

### Bulk Update & Add

- Excel template export (existing issues plus `base_updated` for conflict detection, with the project ID embedded to prevent writing to the wrong project). You can **narrow the exported issues with search conditions** (keyword AND / OR, created/updated date range, status, assignee); with no conditions, all issues are exported
- **Edit by name**: issue type, status, priority, and assignee are chosen from dropdowns that reference the master sheet (assignees use the "Display name (ID)" format to disambiguate identical names)
- Import validation (name columns always win; ID columns are used only on rows where the name column is empty; conflicting ID columns produce a warning and are ignored; required values are checked on new rows) -> **a dry-run preview is mandatory** -> execution with confirmation
- Conflict detection immediately before writing (nothing is written if the remote issue has been updated; overwriting requires an explicit force)
- **Duplicate-creation protection**: the send state is persisted, and rows with an unknown outcome (5xx, missing response, etc.) are held. On resume, they are matched against already created issues so they can be re-sent safely
- Progress display, cancellation, job history (per-row details), and an Excel result report. **Rows where a new issue was created record the key and ID of the created issue** (key and ID in the result report, key in the row details)
- Supports **updating custom fields and the parent issue key** (custom fields use `属性:定義名` (Attribute: definition name) columns; parent-child relationships are one level deep)
- Automatic throttling with the rate limit (update category), plus a minimum 1-second interval between API calls during a bulk run (writes, conflict checks, and the pre-resend match alike)

### User Export

- Extracts the user list along with teams, roles, member projects, and administered projects, and exports it to Excel
- Filters: keyword (partial match on name, user ID, and email address) and role (space-wide permission)
- **Automatically falls back to per-project retrieval** when the API key lacks administrator permission (the retrievable scope is stated explicitly)

### Sync Status and Operation Log

- List of the last sync time per data type, plus manual sync
- Rate limit remaining (remaining / limit and reset time per category, refreshed every 10 seconds). The values are observed from server responses, so refreshing the display consumes no API calls
- Operation logs are written per day to **`logs/` in the same folder as the executable** (retained 14 days). Logs older than 14 days are gzip-compressed in the background at startup and moved to `logs/archive/`, where **archives are retained 90 days**. API keys, issue content, and user names are never recorded, and space names in URLs and home folder names are masked (the logs are designed to be shareable for troubleshooting, but reviewing them before sharing is recommended)

### About

- Shows the version, author, repository (where to report bugs), documentation (README / User Guide), contact, and license
- Where data is stored: the config folder, the local DB per profile (path and size), and the operation log
- **Appearance** switch: Match system (default) / Light / **Dark**. The choice takes effect immediately and persists across restarts (the title bar follows the app theme on Windows only; on macOS it follows the OS appearance setting)
- **言語 / Language** switch: Match system (default) / 日本語 / English. The choice takes effect immediately and persists across restarts ("Match system" follows the language setting of your OS and browser)
  - **Current limitation**: some messages generated by the Go backend (validation errors and the detailed text of processing results), as well as the headers and the `記入方法` (How to fill in) sheet of the Excel exports and bulk update templates, are currently available in Japanese only

## Where data is stored

| Data | Location |
|---|---|
| Configuration (profiles; keys not included) | `<user config dir>/backlog-assistant/config.json` (Windows: `%AppData%\backlog-assistant\config.json`, usually `C:\Users\<user name>\AppData\Roaming\backlog-assistant\config.json`) |
| API keys | OS keychain only |
| Local DB (fetched data, SQLite) | Same location, `data\<space host>_<user ID>.db` (Windows: `%AppData%\backlog-assistant\data\`, usually `C:\Users\<user name>\AppData\Roaming\backlog-assistant\data\`. Permissions are 0600 on Unix-like systems; on Windows the ACL of the user directory applies) |
| Operation log | `logs\` in the same folder as the executable (falls back to the user config directory automatically if it is not writable) |

### Changing the storage location

The folder that holds the configuration and the local DB (the **base folder**) can be changed in two ways. The API keys (OS keychain) and the operation log (`logs\` in the same folder as the executable, falling back to the user config directory automatically if it is not writable) stay where they are.

| Method | Setup | Base folder |
|---|---|---|
| Portable | Put a `portable.txt` file next to the executable (its contents may be empty). **On macOS, put it next to the `.app` bundle** (not inside it) | `userdata\` in the same folder as `portable.txt` (on macOS, `userdata/` next to the `.app` bundle) |
| Environment variable | Set `BACKLOG_ASSISTANT_HOME` to a folder | The folder you specified |

- **The priority is portable > environment variable > default** (if both are set, the portable location wins).
- The environment variable accepts **absolute paths only**. `~` and `%AppData%` are not expanded, and paths containing `?` cannot be used.
- If the location you specified cannot be used (a disconnected USB drive, no write permission, and so on), the app **does not silently fall back to the default location; it stops with a startup error** instead. Falling back would create empty data somewhere else and make it look as if your data had disappeared. The reason is recorded in the operation log, or in `crash.txt` next to the executable (or in the user config directory if that is not writable; that destination is not affected by this customization).
- Switching the location **does not move your data automatically**. To keep using it, quit the app and copy the whole base folder (see ["5.4 Changing the storage location"](docs/USER_GUIDE.en.md#54-changing-the-storage-location) in the User Guide).
- The current location is shown under "Stored Data" on the About screen (the storage location mode and the actual paths).

### Migration example: from environment-variable mode to portable mode

These are the steps for switching a location previously set with the environment variable (`BACKLOG_ASSISTANT_HOME`) to a portable setup you can carry on a USB drive, on the same computer. The layout is the same in both modes: **`config.json` directly in the base folder, and the local DB under `data\` inside it**.

1. **Quit the app.** Copying while it is running can corrupt the local DB.
2. Create an empty **`portable.txt`** in the same folder as the executable. **On macOS, put it next to the `Backlog Assistant.app` bundle, not inside it.**
3. Create a **`userdata`** folder next to it.
4. From the old base folder, copy **`config.json` directly into `userdata\`** and the **`data` folder into `userdata\data\`** (include any `.db-wal` and `.db-shm` files that are present).
5. Start the app and check under "Stored Data" on the About screen that **"Storage location mode" reads "Portable (portable.txt)"** and that the "Config folder" and "Local DB" paths point inside `userdata`.

**On the same computer and the same user account, you do not need to enter the API key again** (API keys live in the OS keychain and are unaffected by the storage location).

**Before (environment-variable mode) — example layout**

```
D:\ba-data\                            <- base folder set in BACKLOG_ASSISTANT_HOME
├─ config.json                         <- configuration (profiles; keys not included)
└─ data\                               <- local DB
   ├─ example.backlog.jp_12345.db
   ├─ example.backlog.jp_12345.db-wal
   └─ example.backlog.jp_12345.db-shm
```

**After (portable mode) — example layout**

```
E:\BacklogAssistant\                   <- folder holding the executable (a USB drive, for example)
├─ backlog-assistant.exe
├─ portable.txt                        <- its presence enables portable mode (contents may be empty)
├─ logs\                               <- operation log (no migration needed; falls back to the user config dir if not writable)
└─ userdata\                           <- base folder
   ├─ config.json                      <- directly in the base folder
   └─ data\                            <- under data\ in the base folder
      ├─ example.backlog.jp_12345.db
      ├─ example.backlog.jp_12345.db-wal
      └─ example.backlog.jp_12345.db-shm
```

- **After switching, we recommend deleting the `BACKLOG_ASSISTANT_HOME` environment variable.** Because the priority is portable > environment variable, leaving it set still works, but removing `portable.txt` would silently send the app back to the old location, which is confusing.
- **Copy the original folder rather than moving it, and delete it only after you have confirmed everything works.**
- **Placing the app somewhere it cannot write, such as under `Program Files`, causes a startup error** (the app never falls back to the default location on its own). Use a USB drive or a folder under your user directory.
- **Moving to a different computer requires entering the API key again** (see ["5.5 Moving to another computer"](docs/USER_GUIDE.en.md#55-moving-to-another-computer) in the User Guide).
- For the full list of caveats (such as why cloud-sync folders are discouraged), see ["5.4 Changing the storage location"](docs/USER_GUIDE.en.md#54-changing-the-storage-location) in the User Guide.

## Security policy

- Only HTTPS connections to `*.backlog.jp` / `*.backlog.com` are allowed, and redirects are not followed
- Rate limits are configured from the actual values returned by `GET /api/v2/rateLimit` on first API use (never hard-coded; if the call fails, operation continues and it is retried on the next API use at least 30 seconds later)
- Configuration and the local DB are stored in the user area (the Excel output location and the log location follow the user's own choice and placement)

## Notes on use

- **Bulk Update & Add modifies data in Backlog.** The first time you use it, we recommend trying it on a test project where you are the only member
- Retrieving the complete user and team lists requires an API key with administrator (or project administrator) permission (otherwise the app falls back automatically)
- `#CLEAR#` (clearing the assignee, due date, description, custom fields, or parent issue) is not documented in the official API specification, so it is still being verified against a live space

## Development

Requirements: Go 1.25+ / Node.js 20.19+ (or 22.13+ / 24+) / Wails CLI v2.13

```sh
# Tests (all packages)
go test ./...

# Frontend lint, test, type check, and build
cd frontend && npm install
npm run lint
npm run test
npm run build

# Development mode (requires a GUI environment)
wails dev

# Distribution build
wails build
```

Distribution artifacts are built automatically by GitHub Actions (on push to main) as a Windows exe and a macOS .app (zipped) and stored as artifacts. The macOS build runs on a macOS runner.

## Development rules

- Follow TDD (Red -> Green -> Refactor)
- Run Codex Review twice before committing
- Never include real data such as issue content or real space information in the repository or in logs
- When updating the user-facing documentation (README / USER GUIDE), update the English versions ([README.en.md](README.en.md) / [docs/USER_GUIDE.en.md](docs/USER_GUIDE.en.md)) in the same change

## License

MIT License. See [LICENSE](LICENSE) for the full text.

- Author: r404r
- Repository and bug reports: <https://github.com/r404r/p-backlog-assistant>
