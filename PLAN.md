# Open FIMRA — Implementation Plan

**Open FIMRA** stands for **Fire Information for Monitoring, Reporting and Analysis**. It is an open-source platform for forest fire monitoring, field reporting and geospatial analysis, built for India's national forest agencies and State Forest Departments to run on their own servers.

| | |
|---|---|
| Status | Draft v0.1, 24 November 2026. Planning stage: no code yet. |
| Tech stack | Confirmed (see [section 4.2](#42-tech-stack)) |
| Licence | AGPL-3.0 |

Open FIMRA is an independent open-source project. It is not an official Government of India system, and it is not affiliated with NASA, the Forest Survey of India (FSI) or the National Remote Sensing Centre (NRSC).

---

## 1. What the platform does

1. **Collects fire detections** from satellites automatically. NASA FIRMS is the default source and works with a free key. FSI, Bhuvan/NRSC and file uploads plug in through the same connector interface.
2. **Filters detections against forest layers** that each department uploads (Reserved Forest, Protected Forest, Reserved Land, protected areas and similar). Only points inside those layers, or near their edge, reach the operational queue.
3. **Attaches the department's hierarchy** to every point (for example Circle → Division → Range → Beat), along with attributes from the layers it falls in.
4. **Routes each alert to field staff**, who verify it on the ground and report through forms the department designs itself.
5. **Shows the picture on dashboards and maps** at beat, range, division, state and national level.
6. **Exports to GIS formats** and offers live links so QGIS, ArcGIS and other GIS platforms can use the data.
7. **Analyses fires against other layers**: forest type, roads, settlements, terrain, weather, past burns.

The first release must prove one loop end to end:

> satellite detection → match against forest boundaries → route to the responsible officer → verify in the field, including offline → review the report → see it on the dashboard and export it to QGIS

## 2. Design principles

These rules shape every module.

- **Configuration, not code.** Hierarchy levels, forms, workflow steps, alert rules and roles are settings a department changes in the admin screens. A second state must be able to adopt the platform without a code change.
- **Nothing is deleted at filtering.** Every detection is kept and tagged as inside forest, near the edge, or outside. Views are filtered; raw observations stay for audit and analysis.
- **Three separate records.** A *detection* is what a satellite saw. An *alert* is a detection that met a department's forest and routing rules. An *incident* is the case a team investigates, which can group many detections over time.
- **History does not change when boundaries change.** Every boundary layer is versioned with dates. Each alert stores the boundary version and hierarchy it was matched against.
- **One permission rule everywhere.** A user's role and jurisdiction apply identically to screens, API calls, map tiles, exports, attachments and background jobs.
- **Honest labels.** A satellite hotspot is a possible fire, not a confirmed one, and no detection does not mean no fire. Hotspot counts are never presented as burned area.
- **Runs without outside services.** No licence keys, no paid map services, no data leaving the agency's server unless the agency turns that on.
- **Keep the single-server install small.** A state must be able to run the platform on one virtual machine. Heavier components are optional add-ons.

## 3. Users and deployment models

### 3.1 Users

| User | What they do |
|---|---|
| National administrator | National dashboard, state comparison, national layers, national form templates |
| State administrator | Uploads layers, defines hierarchy, forms, workflows, users and alert rules |
| Supervising officers (circle, division, range) | Monitor their jurisdiction, assign, review and approve reports |
| Field staff (forester, forest guard, fire watcher) | Receive alerts, travel to the point, report from a phone, often offline |
| GIS cell and analysts | Manage layers, run analysis, export, connect desktop GIS |
| Public and researchers (optional) | Read-only aggregated dashboard with no personal data |

### 3.2 Deployment models

One codebase supports all three.

| Model | Description | Who controls the data |
|---|---|---|
| **State standalone** | One state runs its own installation. Expected to be the most common. | The state |
| **National shared installation** | One installation hosts many states as separate workspaces, for states that do not want to run servers. | Each state controls its own layers, forms and staff; national roles see what states grant |
| **National view across state installations** | Separately hosted states send approved summaries to a national hub on a schedule. | Each state approves the measures and detail level it shares |

Every state-owned row carries a workspace (tenant) identifier from day one, so a standalone install is simply one workspace. For the third model, each state and reporting period has one designated source, so no fire is counted twice.

Forest jurisdiction and civil administration are independent hierarchies. A district can cross several forest divisions and a division can cross several districts, so a point can belong to a beat, a village and a tiger reserve at once.

## 4. Architecture

### 4.1 Overview

```
            External sources
   NASA FIRMS · FSI · Bhuvan/NRSC · weather · file uploads
                        │  connectors
                        ▼
┌───────────────────────────────────────────────────────────┐
│ Core API (Go)                                             │
│  ingestion scheduler · spatial matching · alerts and      │
│  incidents · workflow · forms · permissions · exports API │
│  vector tiles · OGC API Features · notifications          │
└───────┬──────────────────────┬──────────────────┬─────────┘
        │                      │ jobs             │
        ▼                      ▼                  ▼
┌────────────────┐   ┌───────────────────┐  ┌──────────────┐
│ PostgreSQL +   │◄──│ Geo worker        │  │ File storage │
│ PostGIS        │   │ (Python + GDAL)   │  │ S3 or disk   │
│ data, jobs,    │   │ layer import,     │  │ uploads,     │
│ permissions    │   │ export, analysis  │  │ photos       │
└────────────────┘   └───────────────────┘  └──────────────┘
        ▲
        │                    Identity: Keycloak (OpenID Connect)
┌───────┴──────────────────────────────────────────────────┐
│ Reverse proxy (TLS)                                      │
└───────┬───────────────────┬──────────────────┬───────────┘
        ▼                   ▼                  ▼
   Web app             Mobile app         QGIS / ArcGIS
   (Angular)           (Flutter)          (files or live link)
```

Optional add-ons, each switched on only when needed: a raster tile server for imagery and terrain analysis, and a monitoring stack.

### 4.2 Tech stack

Every component is open source and self-hostable.

| Layer | Choice | Why |
|---|---|---|
| Database | PostgreSQL 17 or later with PostGIS; `ltree` for hierarchy paths; monthly partitions for detections | The standard open spatial database. Data volume is a few million detections a year nationally, which needs no extra time-series engine. |
| Core API | Go (`pgx`, `chi`, `sqlc`) | One small binary with low memory use on government virtual machines; good at concurrent ingestion during peak season. |
| Background jobs | A job queue stored in PostgreSQL (River for Go jobs; the geo worker claims its jobs from the same database) | No separate message broker to install, secure and monitor. |
| Geo worker | Python with GDAL/OGR, GeoPandas, Shapely, rasterio, exactextract, PySAL | Python has the strongest tools for reading GIS formats, coordinate systems, rasters and spatial statistics. It is confined to layer import, export and analysis. |
| Web app | Angular, TypeScript, Tailwind CSS | Strongly typed and maintainable by long-lived teams. |
| Web components | Spartan UI for controls; TanStack Table for data tables; ngx-formly for forms rendered from JSON Schema; Angular CDK for drag and drop; Lucide icons | All free and open source. The drag-and-drop form designer is built in-house on these, because no free ready-made one exists. |
| Maps and charts | MapLibre GL JS; deck.gl for large point sets and time animation; Apache ECharts | No map licence keys; handles hundreds of thousands of points. |
| Mobile app | Flutter, Android first; SQLite on the device; offline map packages | Works offline with GPS and camera, which browser apps do poorly in the field. |
| Vector tiles | Generated from PostGIS by the core API, with the caller's permissions applied, then cached | Map tiles cannot leak data outside a user's jurisdiction. |
| Basemap | Self-hosted PMTiles with compliant India boundaries; optional Bhuvan WMS | No calls to outside map services. |
| Raster tiles (add-on) | TiTiler serving Cloud Optimized GeoTIFFs | Needed only when terrain, vegetation or imagery analysis is switched on. |
| GIS links | File exports through GDAL; live read-only **OGC API – Features** from the core API | QGIS and ArcGIS Pro both read OGC API – Features. No separate map server is included; WMS/WFS can be added later if an agency needs them. |
| Identity | Keycloak (OpenID Connect, LDAP/Active Directory, multi-factor) or the agency's own OpenID Connect provider | Agencies can plug in their existing sign-on. |
| File storage | Any S3-compatible store, or local disk on a single server | Agencies use what they already run. |
| Notifications | In-app, email (SMTP), SMS through the agency's gateway, mobile push | Push through Google's service is optional, with a self-hosted alternative. |
| Reverse proxy | Caddy | Automatic TLS, simple configuration. |
| Packaging | Docker Compose first; offline install bundle; Helm chart later | One state server is the first target. |
| Monitoring (add-on) | Prometheus, Grafana, Loki | Standard open tools. |
| Documentation | MkDocs | Simple to host and translate. |

A default single-server install is about seven containers: proxy, web, core API, geo worker, PostgreSQL, Keycloak and file storage.

### 4.3 Decisions made

| Decision | Choice | Reason |
|---|---|---|
| Backend | Go core API; Python only where its GIS libraries are needed | Keeps server load low |
| Web | Angular with Spartan UI | Free, open components with little custom work |
| Mobile | Flutter | One codebase, works offline with GPS and camera |
| Map server | None in the first version | Analysis runs in PostGIS and the geo worker, and files plus OGC API – Features cover GIS links. A map server for WMS/WFS can be added later. |
| Licence | AGPL-3.0 | Anyone who runs a changed version for users must share those changes and keep the project's credit |
| Database migrations | goose, plain SQL files in `db/migrations` | Readable SQL that a database administrator can review; runs from the Go binary or the command line |
| Developer commands | One `Makefile` at the repository root | One entry point for every part of the repository, with no extra tool to install |
| Web package manager | pnpm | Fast, strict about dependencies |
| Continuous integration | GitHub Actions | Free for public repositories; every check can also run locally through `make` |

### 4.4 Design system

The interface is deliberately calm so that colour is reserved for data.

- **Themes.** Light for offices and field phones in sunlight; dark for control rooms. Each theme's colours are chosen separately.
- **Map symbols.** Colour shows time since detection. Shape shows forest class (inside, near the edge, outside, fixed heat source), so the map stays readable for colour-blind users.
- **Status.** Always an icon and a label, never colour alone.
- **Charts.** Eight colours used in a fixed order, so a data source keeps its colour on every screen.
- **Type.** Noto Sans, hosted with the app, covering Indian scripts.
- **Field use.** Touch targets of at least 48 px on the phone app.

Tokens are in [docs/design-system/tokens.json](docs/design-system/tokens.json), with a visual reference in [docs/design-system/preview.html](docs/design-system/preview.html).

## 5. Modules

### 5.1 Fire data ingestion

**Connectors.** Each source is a plug-in with four parts: a settings form, a connection test, a fetch step and a step that converts records to one common detection format. Administrators enable sources under *Settings → Data sources*, which shows the last run, records fetched and errors.

| Source | How it connects |
|---|---|
| **NASA FIRMS** (default) | Area API polled every 15 minutes with a two-day rolling window. VIIRS on Suomi-NPP, NOAA-20 and NOAA-21 is primary; MODIS is kept for continuity with history. Standard-processing data is used for multi-year backfill. |
| **FSI** | No open self-service API. Three modes: a feed under an agreement with FSI, a parser for FSI's alert emails (KML/CSV), and manual file upload. Large-fire references are kept where supplied. |
| **Bhuvan / NRSC** | Enabled once the agency has confirmed access and usage terms with NRSC. File import and map overlays until then. |
| **Files and field reports** | CSV, KML, GeoJSON and GeoPackage upload with saved column mappings. Field staff can also report a fire they see before any satellite does; later detections are linked to it. |

**NASA FIRMS setup for an agency.**

1. Request a free MAP_KEY at `https://firms.modaps.eosdis.nasa.gov/api/map_key/` using a department mailbox, not a personal address. The key arrives by email.
2. Paste the key into *Settings → Data sources → NASA FIRMS*. It is stored encrypted and never shown again. The key appears in the request URL, so it is removed from all logs.
3. Choose the satellites and press *Test connection*. The platform works out the bounding box from the agency's boundary, fetches by that box, then clips precisely in the database.
4. Each installation uses its own key. No shared key ships with the code. NASA's limit is 5,000 transactions per 10 minutes per key, far above what 15-minute polling uses.

Before a key is entered, an installation can show recent fires from NASA's public regional files, so a new agency sees live data within minutes of installing.

**Safe to repeat.** Every import can be re-run without creating duplicate detections, alerts or notifications. Detections from different sources are all kept. Where two sources report the same satellite observation, they are linked and shown as "also reported by", not as independent confirmation, because FSI, NRSC and NASA often process the same satellite pass.

**Feed health.** The platform shows the latest successful fetch, the latest observation and the delay for each source. If a source goes quiet during fire season, administrators are alerted. Silent failure in peak season is the largest operational risk.

**Satellite passes.** Predicted overpass times are shown so control rooms know when the next detections are due.

### 5.2 Layers and hierarchy

**Upload.** A GIS officer can turn a dataset into a working layer without a developer.

1. **Import.** GeoPackage (preferred), zipped Shapefile, KML/KMZ, GeoJSON, File Geodatabase and CSV. An existing WFS or ArcGIS feature service can be linked and synced on a schedule.
2. **Check coordinates.** Indian datasets arrive in many coordinate systems and often without a projection file. The officer confirms the system against an on-map preview. Everything is stored in WGS 84; areas are computed in a suitable India projection (EPSG:7755).
3. **Check geometry.** Invalid shapes are repaired, and overlaps or gaps between neighbouring units are listed for review.
4. **Give it a role.** Forest eligibility layer (decides which points count), hierarchy layer (decides who is responsible), exclusion layer (known heat sources such as kilns and factories) or analysis layer.
5. **Map the attributes.** Choose the name, code and parent-code fields, and any other attributes to copy onto each fire point.
6. **Preview, then publish.** Past detections are replayed against the draft so the officer sees what would be included, excluded or reassigned. Publishing creates a dated version.

**Hierarchy.** Each workspace defines its own levels with local-language labels. Presets cover common patterns, such as `State → Circle → Division → Range → Beat` and variants with Sub-Division, Section or Compartment. Several hierarchies run side by side (territorial, revenue, protected area). Where a layer has no parent code, the parent is worked out from the map and conflicts go to a review queue.

**Basemaps.** India's external boundaries must follow Survey of India depiction, and some global map data does not. The default basemap ships with compliant boundaries, and the repository blocks non-compliant boundary files from being committed.

### 5.3 Spatial filtering and enrichment

For each new batch of detections, per workspace:

1. **Area check.** Keep points inside the workspace boundary plus a small margin.
2. **Forest class.** Each detection gets one class:
   - **Inside forest:** the point is inside a published forest layer.
   - **Near the edge:** outside, but within a buffer the department sets. A satellite pixel covers about 375 m (VIIRS) or 1 km (MODIS), so a fire at the forest edge often reports just outside it.
   - **Outside:** kept for analysis, not sent to the operational queue.
   - **Suspected fixed heat source:** inside an exclusion layer, or a spot that repeats outside the fire season.
3. **Hierarchy.** One spatial join adds every level of every hierarchy and stores it as a snapshot on the alert.
4. **Attributes.** Forest name and legal status, compartment, forest type, vulnerability class, distance to the nearest road or settlement, and weather where available.
5. **Routing.** *Inside forest* alerts go straight to the responsible unit. *Near the edge* alerts go to a review queue by default, so field staff are not flooded; a department can change this.
6. **Incident grouping.** Detections close in space and time are suggested as one incident. Officers can merge or split. Large-fire rules are configurable.

Points on a shared boundary, in overlapping units, or inside forest with no mapped unit go to a designated triage queue.

### 5.4 Workflow, forms and field reporting

**Workflow.** A configurable sequence of steps with roles, required forms, time limits and escalation. The default is:

```
New → Acknowledged → Team sent → Reached site → Controlled → Extinguished
    → First report → Final report → Verified → Closed
Other outcomes: false alarm · duplicate · controlled burn · not reachable
```

Every step is logged with user, time and location. Response times come from these logs.

**Form builder.** "Hierarchical" means two things, and both are supported:

- **Hierarchy of ownership.** A national body publishes a template with core fields. A state adopts and extends it. A circle or division adds a section if the state allows. Forms can be limited to parts of the hierarchy, such as a form used only inside tiger reserves.
- **Hierarchy of structure.** Sections, sub-sections, repeating groups (teams, equipment, affected compartments) and multi-stage forms tied to workflow steps.

Field types include text, number with units, date and time, single and multiple choice, cascading lists, GPS point, walked track and drawn polygon (burnt area is computed), photos, signature, file, calculated fields and values filled in from the alert. Forms support conditions, validation and translated labels.

Each form is stored as a JSON Schema plus a layout description. Published versions never change; every submission records the version it used. Each published form gets a flat database view so its fields can be charted and exported like ordinary columns. Forms can be imported from and exported to XLSForm, which many departments know from ODK.

**Starter pack.** The platform ships with a ready set based on common state practice: control-room dispatch, first (flash) report, final report, patrol log and manual fire report. Departments use it as is, edit it or replace it.

**Mobile app.** The Flutter app holds assigned alerts, forms, jurisdiction layers and offline maps on the device. Submissions and photos queue separately and sync when a signal returns. The server decides workflow state; attachments are never overwritten. Offline data can be wiped remotely if a phone is lost.

**Notifications.** Rules decide who is told about what. Channels are in-app, email, SMS and push. SMS templates must be registered under India's DLT rules by the deploying agency.

### 5.5 Dashboards

- **Control room.** Detections today by class, active incidents, alerts past their time limit, pending reports, median response time, feed health, the map with a time slider, and a filterable alert queue. Map, charts and table share the same filters.
- **Seasonal and comparative.** Cumulative season curves against earlier years, breakdowns by unit, source, forest type and hour of day, density and recurrence maps.
- **Operations.** Report completion per division, response-time distribution per range.
- **National.** State comparison with normalised measures, showing which states are reporting and where data is missing.

Counts of detections, routed alerts, incidents and confirmed fires are always shown separately. A low-bandwidth map mode, colour-blind-safe palettes, a keyboard-accessible table view and printable situation reports are included.

### 5.6 Export and GIS links

- **File export** of any filtered view: GeoPackage (recommended), Shapefile, KML/KMZ, GeoJSON, FlatGeobuf, CSV and Excel. Each export includes hierarchy fields, form answers and a note of source, filters, coordinate system and time.
- **Live link:** a read-only OGC API – Features service with scoped keys, tested against the QGIS and ArcGIS versions agencies use. WMS and WFS are not part of the first version.
- **API and webhooks** for state dashboards and other government systems.
- **Scheduled exports** to a file location the agency chooses.

### 5.7 Analysis

Delivered in stages.

| Stage | Tools |
|---|---|
| **First** | Overlay and summarise (fires by forest type, legal status, protected area, unit). Proximity (distance to roads, settlements, water, fire lines). Recurrence (how many seasons each area burned). |
| **Next** | Terrain and vegetation sampling from rasters. Statistically significant hot-spot detection. Weather at the time of each incident. Saved, repeatable analysis recipes. |
| **After 1.0** | Burnt-area mapping from Sentinel-2 imagery compared with field polygons. Risk zoning. Fire-danger outlook from weather forecasts. |

Every analysis run saves its inputs, layer versions and settings so it can be reproduced. Burned hectares come only from field-drawn perimeters or validated burnt-area products.

## 6. Permissions, security and compliance

- **Access.** Roles are granted at a point in the hierarchy. The rule is enforced in the API and again inside the database, and is tested across screens, tiles, exports and attachments.
- **Audit.** Logins, changes, workflow steps and exports are recorded in an append-only log.
- **Personal data.** Staff names and phone numbers are personal data under the Digital Personal Data Protection Act 2023. They are kept to a minimum, access-logged and never included in public or national summaries. Location data is stripped from photos before any public release.
- **Hosting.** Designed for State Data Centres, NIC and approved cloud providers. No usage data is sent anywhere.
- **Government requirements.** The operations guide covers CERT-In directions (180-day log retention, synchronised clocks, six-hour incident reporting), a pre-launch security audit and GIGW 3.0 accessibility. The deploying agency confirms which rules apply to it.
- **Software supply chain.** Signed container images, a published component list for each release and automated dependency scanning.
- **Backup.** Database backups with point-in-time recovery, file storage backup and a rehearsed restore of both together.

## 7. Deployment and operations

| Target | What ships |
|---|---|
| Single server (most states) | Docker Compose bundle, install script and a first-run wizard |
| Restricted networks | Offline bundle with all images and the basemap. Only the ingestion service needs an outbound route, to the data sources alone. |
| National or high availability | Helm chart for Kubernetes, after the first state release |

**First-run wizard.** Choose state or national mode → pick a hierarchy preset and upload layers → connect NASA FIRMS → switch on the starter forms → invite officers.

**Starting sizes**, to be confirmed by load testing: a small state on one server with 8 CPU cores, 32 GB memory and 500 GB of fast disk; a large state on separate application and database servers.

**Releases.** One long-term-support release a year, timed before the fire season, with the previous one still supported. Hierarchy, forms, workflows and notification rules export as configuration packs, so one state can start from another's setup.

## 8. Data model outline

```
workspaces, settings
hierarchies, hierarchy_levels, units (path, geometry, valid from/to)
layers, layer_versions, layer_features, remote_layer_sources
connectors, connector_settings (encrypted), ingest_runs
detections (monthly partitions), detection_links (same observation, other source)
detection_matches (workspace, forest class, unit, hierarchy snapshot, attributes)
alerts, incidents, incident_members
workflow_templates, workflow_steps, workflow_instances, step_log
forms, form_versions, form_assignments, submissions, attachments, approvals
users, roles, role_assignments (role at a hierarchy path)
notification_rules, notifications, devices
analysis_recipes, analysis_runs, exports, api_keys, webhooks
federation_peers, federation_outbox
jobs, audit_log
```

Detections are shared; matches are per workspace. This lets one national installation serve many states from a single copy of the satellite data.

## 9. Repository layout

```
open-fimra/
├── apps/
│   ├── web/            Angular web app
│   └── mobile/         Flutter field app
├── services/
│   ├── api/            Go core API, ingestion, matching, workflow, tiles
│   └── geo-worker/     Python layer import, export and analysis
├── packages/
│   ├── connectors/     connector specifications and fixtures
│   ├── form-schema/    form format and validators
│   └── starter-pack/   default hierarchy presets, forms and workflow
├── deploy/             compose, offline bundle, helm
├── db/                 migrations
├── seed/               synthetic demo state: no real agency data
└── docs/               design system; admin, field user, operations and API guides
```

## 10. Roadmap

The phases run in order. Each one ends when its condition is met.

| Phase | Delivers | Done when |
|---|---|---|
| **0. Foundations** | Repository, build pipeline, sign-in, base data model, install skeleton, synthetic demo data | A fresh install gives a login and an empty map |
| **1. Detect, filter, notify** | FIRMS connector and backfill, layer upload with versions, hierarchy, classification, alert map and queue, default workflow, notifications, basic dashboard, file exports, first-run wizard | A pilot state runs it beside its current system and the two agree on which beat each fire belongs to |
| **2. Field reporting** | Form builder, starter pack, mobile app with offline sync, FSI and Bhuvan connectors, incident grouping, live GIS link, situation reports | Field staff in two or three divisions file reports offline through a full fire season |
| **3. Second state and analysis** | Thematic and raster layers, first two analysis stages, national shared installation, configuration packs | A second state with a different hierarchy goes live with no code change |
| **4. Hardening and 1.0** | Summaries from state installations to a national hub, offline bundle, Helm chart, load tests, security audit, public dashboard | Version 1.0 long-term-support is released |

**Release gates.** No release ships unless:

- replaying a feed creates no duplicate alerts or notifications;
- a reference dataset with holes, shared edges, overlaps, changed boundaries and missing codes classifies as expected;
- permission tests pass for detail views, tiles, exports and attachments;
- an offline submission interrupted mid-sync arrives as one report;
- a restored installation recovers both case history and its photos;
- 95% of newly published FIRMS records are processed within 20 minutes.

## 11. Testing

- **Spatial reference tests:** synthetic polygons and points with known expected results.
- **Connector tests:** recorded responses from each source, plus a nightly live check against FIRMS.
- **Offline tests:** interruption, duplicate retries, conflicting edits, form updates while a device is offline.
- **Load tests:** peak-season ingestion and concurrent dashboard users.
- **Field trials:** each release is tried by forest guards on low-end Android phones with poor signal.

## 12. Open-source governance

- **Licence.** AGPL-3.0 for code. Agencies may use and change the platform freely. Anyone who runs a changed version for users over a network must offer those users the source of that version, and must keep the attribution set out in the [NOTICE](NOTICE) file. Agency data and provider content stay under their own terms.
- **India focus.** The product is designed for India. An open-source licence cannot restrict where others use the code, so "India only" describes the project's focus, not a legal limit.
- **Clean start.** This repository contains no code, data, logos or forms from any existing government system. Anything reused from one needs the owner's permission first.
- **No real data in the repository.** Forest boundaries, staff details and field reports stay on agency servers. Demo data is synthetic.
- **Shared standards.** The detection format, hierarchy pack format and form format are published so other Indian systems can exchange data without using this platform.

## 13. Risks

| Risk | Response |
|---|---|
| FSI or Bhuvan access is delayed | FIRMS, email parsing and file upload keep the platform useful from day one |
| Poor-quality state layers | Import checks, preview before publishing, versioning, and GIS support during onboarding |
| Non-compliant map boundaries | Compliant default basemap and a block on committing other boundary files |
| Weak field connectivity | Offline-first mobile app, SMS, small data transfers |
| Agencies lack server staff | One-command install, offline bundle, national shared installation, yearly long-term releases |
| Silent ingestion failure in peak season | Feed-health alerts and more than one source |
| Too many alerts for field staff | Edge detections go to review by default; exclusion layers; incident grouping |
| Scope growth | A small core plus optional modules. Patrolling, equipment and watcher registers are modules, not core. |
| The name suggests an official system | Clear statement of independence in the README and on the website |
| AGPL-3.0 makes departments or their contractors hesitant | Plain-language explanation of what the licence requires, in the documentation |

## 14. Facts and access to confirm

These are not yet verified and are checked before Phase 1 starts.

- Whether NASA's public regional fire files can be used for the no-key quick start, and under what terms.
- The current state of the FIRMS country endpoint. The plan relies only on the area endpoint.
- FSI and NRSC access routes, data formats and redistribution terms, through a state nodal officer.
- Which S3-compatible store to bundle by default.
- Trademark, domain and organisation-name availability for "Open FIMRA".

## 15. Next steps

Work is built in thin slices. Each slice is a separate pull request and ends when its condition is met.

### Walking skeleton (complete)

A fresh install gives a login and a map with a synthetic forest, and fills that map with classified detections.

| # | Slice | Delivers | Done when |
|---|---|---|---|
| 1 | Repository skeleton | Folder layout from section 9, `Makefile`, development Compose stack (PostGIS and Keycloak), continuous integration, contribution guide | `make dev` starts the stack and continuous integration passes |
| 2 | Base data model | Migrations for workspaces, hierarchy units (`ltree`), layers and versions, partitioned detections, detection matches, jobs and audit log | Migrations apply and roll back cleanly |
| 3 | Synthetic demo state | A generator in `seed/` for a fictional state with circles, divisions, ranges, beats and forest polygons, including holes, shared edges, overlaps and missing codes | Output is reproducible, and it doubles as the spatial reference test set |
| 4 | Core API skeleton | Go service with health check, OpenID Connect token checks, roles at a hierarchy path, and the job queue | A signed-in request returns the caller's units; an anonymous one is refused |
| 5 | Web shell | Angular app with Tailwind, Spartan UI, the design tokens, the logo, sign-in and a MapLibre map of the demo layers | A fresh install gives a login and a map (Phase 0 complete) |
| 6 | Core loop | FIRMS area connector, de-duplication, classification (inside, near the edge, outside, and the beat), permission-filtered vector tiles, and the alert map styled by fire age | Replaying a feed creates no duplicates, and every reference point lands in its expected beat |

### Rest of Phase 1

These slices finish roadmap phase 1 (section 10). They run in three waves. Slices in the same wave are independent of each other.

| # | Wave | Slice | Delivers | Done when |
|---|---|---|---|---|
| 7 | A | Layer import | Geo worker service (Python with GDAL) that turns an uploaded GeoPackage, zipped Shapefile, GeoJSON or KML into a draft layer version: coordinate system check, geometry repair, overlap and gap report, attribute mapping, hierarchy building from parent codes or from the map, and a review list of problems | Uploading the demo state's source files gives draft versions that match `seed/demo-state/load.sql`, and every deliberate defect appears in the review list |
| 8 | B | Layer publishing | Preview that replays past detections against a draft and shows what would be included, excluded or reassigned; publishing; re-matching of recent detections; layer screens in the web app | Publishing a changed beat layer moves the affected detections to their new beat, and past alerts keep the beat they had |
| 9 | A | Alerts and queue | Alerts created from matches: *inside forest* routed to the responsible unit, *near the edge* to a review queue, triage cases to a triage queue; a filterable alert queue beside the map | Each reference detection produces the expected alert and queue, and replaying a feed creates no second alert |
| 10 | B | Default workflow | Workflow templates, steps, instances and step log with roles, time limits and the default sequence and outcomes from section 5.4; actions on an alert in the web app | An alert can be taken from *New* to *Closed* by the right roles only, and every step is logged with user, time and location |
| 11 | C | Notifications | Notification rules; in-app and email channels (a local mail catcher in development); one notification per alert and recipient | A new alert notifies the responsible officers once, and a replayed feed notifies no one again |
| 12 | B | Control-room dashboard | Detections today by class, active alerts, alerts past their time limit, feed health per source, and the map and queue sharing the same filters | The dashboard's numbers match direct database counts for the demo data, for each demo user |
| 13 | B | File exports | Export of any filtered view to GeoPackage, Shapefile, KML, GeoJSON and CSV through the geo worker, with hierarchy fields and a note of source, filters and time; permission checks and audit | An export opens in QGIS with the expected fields, and contains only what the user may see |
| 14 | A | FIRMS backfill and feed health | Multi-year backfill from FIRMS standard-processing data, feed-health checks, and an alert to administrators when a source goes quiet in fire season | A backfill of the demo fixtures is safe to repeat, and a silent source raises a feed-health alert |
| 15 | C | First-run wizard | Encrypted settings (including the FIRMS key), and the wizard from section 7: mode, hierarchy preset, layer upload, FIRMS connection, starter forms switched off for now, and inviting officers | A fresh install reaches a working alert map using only the wizard |

Wave A starts together. Wave B needs slice 7 for slices 8 and 13, and slice 9 for slices 10, 12 and 13. Wave C needs wave B. Each slice adds its own migrations, numbered in the order the slices are merged.

Alongside the build:

- Request a FIRMS key on a department mailbox and start access conversations with FSI and NRSC.
- Confirm the facts in section 14.
- Write the form format specification, using the starter-pack forms as the first examples.
- Agree with a pilot state how the side-by-side comparison with its current system will be run, since that is the condition for finishing phase 1.
