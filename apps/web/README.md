# Control-room map

Sign in to see detections in your first assigned workspace. The map starts with the past seven days and refreshes every minute. Choose a shorter window or turn on **Show outside forest**. The map key explains the age colours and forest shapes.

Click a point or use the **Detections in view** menu to open its details. Times use your local time zone. Press Escape to close the details and return to the menu. Use the arrow keys on the map to pan, plus and minus to zoom, and **Fit state to map** to return to the whole state.

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

The map loads [seed GeoJSON](../../seed/demo-state) directly through the linked asset directories in [angular.json](angular.json). It uses local fonts and a bundled map worker. It needs WebGL. The fictional reference layers stay available if you have no assigned workspace or the API is unavailable. Only the configured API receives your access token.

The two palettes in [src/tokens.css](src/tokens.css) map the shared [design tokens](../../docs/design-system/tokens.json) to Tailwind. Update the mapping when the design tokens change.

## Checks

```sh
make web-lint
make web-test
make web-build
```

The build writes the static app to `apps/web/dist/web/browser/`. Press Ctrl+C to stop the dev server.
