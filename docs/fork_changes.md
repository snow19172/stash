# Fork changes

This file records changes made specifically for this Stash fork. Add future
entries using the same dated, task-based format.

## 2026-11-15

### Task 1: Automatic library watching

- Added recursive filesystem watching for configured library directories.
- Newly written video, image, and supported archive files are debounced and
  scanned only after the file has remained stable.
- Watch-triggered scans reuse the scan options configured in
  **Settings > Tasks > Library > Scan**, including covers, previews, sprites,
  and perceptual hashes.
- Scans and their generation work run as manager background jobs. The watcher
  does not invoke or share the plugin task runner.
- Added the **Watch library folders for new files** toggle to
  **Settings > Library**. Watching is disabled by default.

### Task 2: Automatic scene identification

- Added the **Auto-identification** settings page under **Settings > Library**,
  before **Interface**.
- Automatic identification can be enabled independently and configured with
  an ordered list of fragment-capable scrapers and Stash-Box instances for each
  configured library path.
- Higher-priority sources keep their values; lower-priority results fill
  missing metadata. Existing metadata is preserved by default. The optional
  overwrite setting allows scraped metadata to replace it.
- Video perceptual hash generation must be enabled in the existing scan
  settings before automatic identification runs. The settings page warns when
  this prerequisite is disabled, and the background task skips identification
  in that case.
- Identification runs after the file scan and its configured generation work
  finish. It currently applies to video scenes; images and archives are
  scanned but are not automatically identified.

### Configuration keys

- `auto_identify_libraries`

### Task 3: Per-library auto-scan and identification

- Moved the controls into the **Auto-Scan/identify** page under
  **Settings > Library**. Each configured library has independent auto-scan,
  auto-identification, overwrite, and ordered source settings; all are off by
  default for a library without saved settings.
- Existing global automation settings are migrated to the libraries configured
  at upgrade time; libraries added afterward remain off until enabled.
- Automatic identification now runs after watcher scans and manual scans, and
  only for video scenes processed by that scan.
- Phash generation is optional. The settings page recommends enabling it for
  more accurate Stash-Box identification; when enabled as part of a scan,
  background generation completes before identification starts.
- Recursive watch registration now runs asynchronously so large libraries do
  not block server startup. Directory-registration errors are reported without
  aborting registration of the rest of the library.
- Improved identification-source contrast and labeled the selected library
  and source in the priority list.

### Task 4: Identification source fallback

- Automatic identification now continues to later configured sources when an
  earlier source returns multiple matches, while still skipping ambiguous
  results. If no source returns an unambiguous result, the existing
  multiple-match handling is retained.
- Identification logs now list every source that returned data for the
  combined result.

### Task 5: Watcher queue and source diagnostics

- Watcher scans now use a dedicated sequential worker and start immediately as
  background jobs, rather than waiting behind unrelated queued jobs. Repeated
  changes to a file while it is pending trigger a follow-up scan.
- Identification debug logs now report each source's result count, and source
  errors name the configured source.

### Task 6: Per-library identification options

- Added a per-library **Ignore organized scenes** setting. It defaults off to
  preserve current behavior and applies to automatic identification after both
  watcher and manual scans.
- Added an options editor for every configured identification source, reusing
  the identify task's field strategies and scraper options.
- Moved overwrite behavior from a library-wide toggle to each source's field
  strategies, including cover images. Existing library overwrite settings are
  migrated to per-source `OVERWRITE` strategies.
