# Control room

Sign in to see detections in your first assigned workspace. The map starts with the past seven days and refreshes every minute. Use the time slider to choose a window from one to 720 hours. Choose a forest class or enter a unit path such as `ds.c1.d1` to narrow the view. All classes are selected at first. The map key explains the age colours and forest shapes.

Click a point or use the **Detections in view** menu to open its details. Times use your local time zone. Press Escape to close the details and return to the menu. Use the arrow keys on the map to pan, plus and minus to zoom, and **Fit workspace to map** to return to the workspace boundary.


The **Alert queue** sits beside the map. On a small screen, switch between **Map** and **Alert queue**. Both use the same time window. Queue counts cover all matching pages. Choose a detection in the table to centre the map and open its details. Selecting a map point shows its row if you have access to its alert. Both views refresh every minute.

Choose **Dashboard** to see detections by forest class, routed alerts by queue and step, and times to acknowledgment, arrival and control. The map, queue and dashboard keep the same filters when you switch views. Today's detections start at midnight in India. A root grant also shows each feed's health.

The chart has a matching data table. **Low bandwidth** keeps the numbers, table and detection points. The map shows only the top-level reference outline, and the dashboard skips the chart. The browser remembers your choice when storage is available.

## Incidents

Choose **Incidents** at the top of the queue panel to see alerts grouped into incidents: fires close together in place and time. **Alerts** returns to the alert queue. The list uses the same time window, forest class and unit path. Each row shows how many alerts you can see, their places, when the fire was last seen and the incident's own step. Open an incident to draw its alerts on the map, read its timeline and record a step. A step on the incident also moves each member alert that you could move the same way. Choose a member alert to open its own details. A merged incident links to the incident that now holds its alerts. The incident screen loads only when you open it, so it adds nothing to the first download.

## Forms

Root administrators and division officers see **Forms** in the top bar. The screen lists national templates, state forms and division forms, with each version's status, the template it extends and how many reports were filled in on it. Published versions open read-only; earlier reports keep showing with the version they used.

- **Add a national template** takes a template file from a national body. It is published at once.
- **Extend** starts a state form from a national template, or a division form from a state form, when the template allows it. Parts the template locks show **Locked by template** and cannot be changed.
- **New state form** starts a blank form, and **Start version 2** copies the latest version.
- **Template file** downloads a published version with its level and template rules, for another workspace to add.

The designer has three views. **Design** shows the sections and questions. Choose a type under **Add** to put it after the selected item, or drag it into place. Drag the handles to move items, or use the arrow buttons or Alt with the up or down arrow on a selected row; **Move to** puts an item in another section or group. Select an item to change its key, question, hint, limits, choices and when it is shown. With **Let states extend this form** (or divisions) switched on in the form settings, each item can be locked for the forms that extend it. **Preview** shows the form read-only as people will fill it in, with its conditions in plain English. **Form file** shows the form in the [form format](../../packages/form-schema/SPEC.md) for stages, shared lists and layout. The shared validator checks the form as it changes and lists each problem in plain English; choose a problem to go to the item. **Publish** saves the draft and freezes the version.

The designer and its validator load only when you open **Forms**, so they add nothing to the first download. The read-only renderer in [`src/forms/form-preview.ts`](src/forms/form-preview.ts) takes a form and, optionally, a submission's answers, so other screens can show a report with the version it used.

## Exports

Choose **Export** beside the alert queue title to save what you can see as a file. Choose alerts or the detections on the map, then a format. GeoPackage is recommended for QGIS. The export uses the shared time window, forest class and unit path, and for alerts the queue you have open or all queues you can see. The dialog shows progress and then a download button. Only you can download the file, until it expires. Press Escape or **Close** to return to the **Export** button.

## Situation reports

Choose **Situation report** to make a printable summary of one unit. Choose the unit, which starts at your highest unit, and one day, a range of days or the past hours, then **Show report**. The report has the key numbers, a map of the unit with the period's alerts and incidents, detections and alerts, response times, incidents, field reports, alerts past their time limit and a table for each unit one level down. The numbers are the dashboard's numbers for the same period and unit, and the report shows only what your role can see. Times are in India Standard Time, so a printed report reads the same everywhere.

**Print** prints the report alone, in light colours, without the menus and buttons. **Prepare PDF** asks the server to draw the same report as a PDF, then shows a download button. Only you can download it, until it expires. The map is drawn from the workspace's own boundaries and forest, so it needs no outside map service. The report screen loads only when you open it, so it adds nothing to the first download.

## Layers

Sign in as a state administrator at the workspace root and open **Layers**.
Choose an existing layer or create one, select the file, and enter the field
names to import. The screen checks the upload in the background and lists any
problems that need review.

Open an upload and choose **Run preview** to check the last 30 days of
detections. You can change the window to 1–90 days. Open a result group to see
examples. A blocking problem requires a corrected upload. If units disappear,
the preview lists the grants that publishing would remove. Review the list and
confirm the removal before publishing. The screen shows progress as recent
detections are checked again.

## Run locally

Use Node.js 24.15 or later in the 24.x series, with Corepack. Run these commands from the repository root.

```sh
make dev
make web-install
make web-dev
```

Run the [core API](../../services/api/README.md) in a separate terminal for workspace access and detection tiles. Open <http://127.0.0.1:4200>. Keycloak asks you to sign in. For example, use `state.admin` with the password `demo`. The account menu contains **Sign out**. The theme follows your system until you choose a theme with the top bar button.

For a remote development host, forward the web, sign-in and API ports to your computer. For example, run `ssh -L 4200:127.0.0.1:4200 -L 18180:127.0.0.1:18180 -L 18090:127.0.0.1:18090 dev@devhub`, then open the same local address.

## Configuration and assets

[public/config.json](public/config.json) sets the sign-in issuer, public client ID and core API address. The app reads it at startup. Keycloak uses the authorisation code flow with a one-time proof key (PKCE). The callback and sign-out return addresses use the web app's own origin. A static host must serve `index.html` for `/callback` and `/signed-out`.

The map loads the signed-in workspace’s published reference tiles and fits to its boundary. The synthetic GeoJSON in seed/demo-state remains available for seed generation and import tests. The map uses local fonts and a bundled worker and needs WebGL. Only the configured API receives your access token.

The two palettes in [src/tokens.css](src/tokens.css) map the shared [design tokens](../../docs/design-system/tokens.json) to Tailwind. Regenerate the mapping with `node scripts/theme.mjs` when the design tokens change.

## Checks

```sh
make web-lint
make web-test
make web-build
```

The build writes the static app to `apps/web/dist/web/browser/`. Press Ctrl+C to stop the dev server.

The first download should stay under 150 kB compressed, for slow mobile connections in the field. It is about 139 kB. The map library, its styles, charts and every screen except the shell load afterwards. For icons, use the `fimraIcon` directive in [`src/ui/icon.ts`](src/ui/icon.ts), for example `<svg fimraIcon="bell" aria-hidden="true">`. It draws Lucide icons from `@ng-icons/lucide`; add a new icon to its list. The build warns above 620 kB and fails above 700 kB before compression.

**GIS link keys** in the account menu shows the workspace's read-only GIS link address and QGIS steps. Each person can make personal access keys for desktop GIS there, see when each was last used and revoke them. A new key is shown once. See the [GIS link](../../services/api/README.md#gis-link).

The alert queue shows the current step and flags overdue alerts. Select a row or a map point to open its timeline. Choose an available next step, add a note if needed, and save. For a field step, allow the browser to share your location. A supervising officer can enter a reason for a location exception. Ending the workflow requires confirmation. A step with an assigned form, such as the starter pack's first report, needs its submission before the alert can move on, and the timeline shows the reason when a move is refused. The web app cannot fill in forms yet: submissions go through the [core API](../../services/api/README.md#submissions).

## First-run setup

On a fresh install, the first signed-in user sees **Set up Open FIMRA**. Enter the setup code from the core API's log, the workspace name and address, and the state code used in your state boundary file. The person who does this becomes the state administrator. National mode is shown but not available yet.

The wizard then opens for root administrators until it is finished. Its steps are listed on the left, or across the top on a phone:

1. **Mode** and 2. **Workspace** show the choices made when setup started.
3. **Hierarchy**: choose a preset, such as State → Circle → Division → Range → Beat, and edit the English and Hindi labels.
4. **Layers**: upload one file per level from the state down, then the forest layer and, optionally, known heat sources. Each row shows the import status and any blocking problems. Publish each one when it is ready. The **Layers screen** has the full review and preview.
5. **NASA FIRMS**: paste the map key. It is saved encrypted and tested. The page shows only when it was set and its last four characters. Skipping asks you to confirm, because the map then gets no new detections.
6. **Starter forms**: lists the five starter forms and where each is filled in. Switch them on to add and publish them and put them on their workflow steps. Forms the workspace already has with the same names are left as they are. You can also skip this step and switch them on later by reopening the wizard.
7. **Invite officers**: enter an email, role and unit for each officer. Officers claim the role the first time they sign in with that verified address.
8. **Finish** shows a summary and opens the alert map.

**Leave setup for now** returns to the control room. **Finish setup** in the top bar opens the wizard again. After it is finished, reopen it from the account menu with **Settings: first-run setup**.

`make wizard-walkthrough` drives these screens in headless Chromium on a fresh database, with a fake FIRMS server, and saves screenshots to `~/open-fimra-screenshots/first-run-wizard`. It uses rootless Docker, port 4200 and your stack's ports from `.env`. Set `WEB_PORT` to use another web port that your sign-in realm allows, such as `make wizard-walkthrough WEB_PORT=4203`.

## Notifications

The bell in the top bar shows the unread count. Open it to see unread
notifications first. Choose an alert to mark the notification as read and
select the alert on the map and in its queue. Alert links also work after
sign-in and can open older alerts. Feed notices open the feed health section
of the dashboard. Press Escape to close the list and return
focus to the bell.

## How to use the design system

Open `/design-system` on your development server. On a built copy, open
`/design-system?preview`. The old HTML preview links to this page. It uses
the same components, map key, colours and theme switch as the app.

| When you need | Use |
|---|---|
| An action | A button with `hlmBtn`. Choose primary for the main action, destructive for removal, or outline, secondary or ghost for other actions. Give icon buttons an accessible name. |
| Form controls | Group a label, control, help text and error in `hlm-field`. Use Helm input, textarea, select, checkbox, radio group or switch. Put a radio indicator inside each radio. |
| Extra detail or actions | Dialog for a decision, sheet for a side panel, dropdown menu for actions, tooltip for a short hint. Give dialogs and sheets a title. |
| Grouped data | Card, tabs, table and separator. Use tabular numbers for counts and times. |
| Progress and feedback | Skeleton while loading, alert for a lasting message, toast for brief feedback, and `fimra-empty-state` for empty results. |
| A workflow state | `<fimra-status-badge icon="step" label="Team sent" />`. Use `overdue` for a late step and `complete` for a finished step. Supply visible text with every icon. Use plain Helm badges for categories. |

Import Helm controls from `@spartan-ng/helm/<component>`. Their source is in
`src/ui`. Add new controls with `corepack pnpm exec ng g @spartan-ng/cli:ui <component>`
from this directory. Check and pin any packages the generator adds.

Use Tailwind utilities such as `bg-card`, `text-body` and `rounded-control`.
Use petrol for interface actions and reserve copper for the logo. Keep web
targets at least 32 px. The app bundles Inter only. Hindi and other Indian
scripts use device fonts.

Change colours, spacing and type in the shared design tokens. From this
directory run `node scripts/theme.mjs` to rebuild the CSS variables.
MapLibre and the charts read the same tokens. Web lint checks the generated
file and reports colour literals in templates, styles and TypeScript.

For a browser check, run `scripts/design-system-visual.mjs` in the Playwright
container with Playwright 1.63.0 installed beside the script. Set `WEB` to
your seeded development app and `SHOTS` to a mounted folder outside the
checkout. The check opens the guide, uses the overlays and signs in as the
demo division officer to capture the map.
