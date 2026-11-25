<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/design-system/logo/lockup-dark.svg">
  <img src="docs/design-system/logo/lockup-light.svg" alt="Open FIMRA" width="312" height="56">
</picture>

# Open FIMRA

**Fire Information for Monitoring, Reporting and Analysis**

Open FIMRA is an open-source platform for forest fire monitoring, field reporting and geospatial analysis in India. National forest agencies and State Forest Departments can run it on their own servers.

> **Status: planning.** There is no working software yet. This repository currently holds the implementation plan and the design system.

## What it will do

- Collect satellite fire detections automatically, with NASA FIRMS as the default source and connectors for FSI, Bhuvan/NRSC and file uploads.
- Keep only the detections that fall inside the forest layers a department uploads, and attach the department's own hierarchy (for example Circle → Division → Range → Beat) to each one.
- Send each alert to the responsible field staff, who report back through forms the department designs itself, including offline from a phone.
- Show dashboards and maps from beat level to national level.
- Export to GeoPackage, Shapefile, KML and other GIS formats, and offer a live link for QGIS and ArcGIS.
- Analyse fires against other layers such as forest type, roads, settlements, terrain and weather.

## Read the plan

The full plan is in [PLAN.md](PLAN.md): modules, tech stack, deployment, roadmap and risks.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) explains how to start the development stack and the rules for changes, including the rule that all demo data is synthetic.

## Tech stack

| Layer | Choice |
|---|---|
| Database | PostgreSQL with PostGIS |
| Core API | Go |
| Geo worker | Python with GDAL, only for layer import, export and analysis |
| Web app | Angular, Tailwind CSS, Spartan UI |
| Maps and charts | MapLibre GL JS, deck.gl, Apache ECharts |
| Mobile app | Flutter |
| Identity | Keycloak |
| Packaging | Docker Compose |

Details and reasons are in [PLAN.md, section 4.2](PLAN.md#42-tech-stack). The design system is in [docs/design-system](docs/design-system).

## Independence

Open FIMRA is an independent open-source project. It is not an official Government of India system, and it is not affiliated with NASA, the Forest Survey of India or the National Remote Sensing Centre.

This repository must never contain real forest boundaries, staff details or field reports. Demo data is synthetic.

## Licence

[GNU Affero General Public License, version 3](LICENSE).

You may use and change Open FIMRA freely. If you run a changed version for users over a network, you must offer those users the source of your version. Changed versions must keep the attribution described in the [NOTICE](NOTICE) file and be marked as changed.
