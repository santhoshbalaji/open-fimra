# Field app

The Flutter app for forest staff in the field, Android first. Staff sign in with their department account, see the fire alerts for their own area as a list and on a map, open an alert and record workflow steps such as **Acknowledged**.

This first version works online. Keeping alerts, forms and maps on the phone, and sending reports when a signal returns, is the next stage (slice 26 in [PLAN.md](../../PLAN.md#15-next-steps)). Screens already read and write through one repository layer (`lib/src/data/alert_repository.dart`), so offline storage can be added behind it without changing them.

## What it does

- **Sign-in.** The app opens the department's sign-in page in a browser tab and uses the authorisation code flow with a one-time proof key (PKCE). The app never sees the password and has no client secret. It keeps the tokens in the Android Keystore and renews them before they expire. **Sign out** ends the session on the sign-in server too.
- **Workspaces.** The app lists the workspaces where the user has a role. A user with more than one can switch from the account menu or the title.
- **Alerts.** The list shows the past seven days, newest first. Each alert shows its place, forest class, age and its status as an icon and a label, with **Overdue** when a time limit has passed. Officers who review alerts can filter by queue. Pull down to refresh.
- **Map.** The map opens on the user's own units, outlined. It draws the core API's permission-filtered vector tiles: units, forest and exclusions, and detections with the shared symbols (shape for forest class, colour for age). **Map key** explains them in words. Tap a point to open its alert. The map uses MapLibre and loads nothing from outside services, so it needs no licence key.
- **Alert details and steps.** An alert shows where and when it was seen, its source and its timeline. The buttons offer only the steps the user's role allows next. Field steps such as **Reached site** save the phone's location, which must be near the fire. Supervising officers can record a reason instead. Each step carries a client ID, so pressing **Save** again after a lost answer records it once.

The interface uses the shared [design tokens](../../docs/design-system/tokens.json) in a light and a dark theme that follow the phone's setting, the Inter font bundled with the app, the [project logo](../../docs/design-system/logo) and touch targets of at least 48 pixels.

## Build and test without installing Flutter

Every command runs in a pinned Flutter image through Docker. Rootless Docker works. Flutter packages, Gradle files and the Android NDK are cached in the ignored `.mobile-cache` folder at the repository root, so later runs are faster. Each container has a 4 GB memory limit and 2 CPUs; set `MOBILE_MEMORY` and `MOBILE_CPUS` to change them.

```sh
make mobile-lint    # formatting, analysis and the generated theme
make mobile-test    # unit and widget tests
make mobile-build   # Android test build: apps/mobile/build/app/outputs/flutter-apk/app-debug.apk
```

`make mobile-tokens` regenerates `lib/src/theme/tokens.g.dart` after the design tokens change. `make mobile-lint` fails if it is out of date.

### Tests against a running stack

```sh
make dev                      # the development stack
make mobile-test-integration  # sign-in and API client tests as the demo users
make mobile-walkthrough       # the browser build in headless Chromium, with screenshots
```

Both create their own database, `fimra_mobile_test`, beside the development one and run a core API on it at `127.0.0.1:18099` (set `MOBILE_TEST_API_ADDR` to change it). They load the demo state and detections, then add one synthetic forest patch and two detections in beat B01, because the demo seed gives the beat guard no alerts. The development database is left alone.

The integration tests sign the beat guard in through the real `open-fimra-mobile` client with PKCE, with a scripted browser filling in the Keycloak form. They check that the client refuses password sign-in and a request without a proof key, that each demo user sees only their own alerts and tiles, and that the beat guard can acknowledge an alert exactly once. Other demo users get their tokens from the development-only `open-fimra-dev-cli` client.

The walkthrough builds the app for a browser, serves it on `127.0.0.1:4300` (`MOBILE_WEB_PORT`, 4300 to 4303) and signs in as the beat guard at phone size. It sets the server under **Server settings**, checks the list and the map, acknowledges an alert and saves screenshots to `~/open-fimra-screenshots/field-app` (`MOBILE_SHOTS`). The browser build exists only for these tests. It serves its own copy of MapLibre GL JS, which `make mobile-web` downloads from the npm registry and checks against its published checksum.

## Run it on a phone or an emulator

The Android test build starts with the core API at `http://127.0.0.1:18090` and the sign-in server at `http://127.0.0.1:18180/realms/open-fimra`. Build it for other addresses with, for example:

```sh
make mobile-build MOBILE_API_URL=https://fimra-test.example.org MOBILE_OIDC_ISSUER=https://id.example.org/realms/open-fimra
```

A user can also change the addresses on the phone under **Server settings** on the sign-in screen. Saving signs the user out.

To reach a development stack from a phone connected by USB, forward the two ports with `adb reverse tcp:18090 tcp:18090` and `adb reverse tcp:18180 tcp:18180`, then install the APK with `adb install`. The phone then uses the same `127.0.0.1` addresses as the development machine, so the sign-in server's token issuer matches what the core API expects. If the development machine is remote, forward the ports to your computer first, for example with `ssh -L 18090:127.0.0.1:18090 -L 18180:127.0.0.1:18180`.

The core API only accepts tokens whose issuer matches its `FIMRA_OIDC_ISSUER`. Keycloak writes the address the phone used into each token, so the phone must reach Keycloak by the same address the API trusts. An emulator that uses `10.0.2.2`, or a tunnel with its own host name, needs a stack whose issuer is that address. A test server with one public address for Keycloak avoids the problem.

Debug builds allow plain HTTP so they can reach a development stack. Release builds accept HTTPS only.

### The sign-in client

The development realm has a public client, `open-fimra-mobile`, with the authorisation code flow, a required S256 proof key and no password sign-in. It returns to `org.openfimra.field:/callback`, which the app receives through `flutter_web_auth_2`. It also accepts `http://127.0.0.1:4300/auth.html` to `4303` and the same ports on `localhost`, for the browser test build only. A real installation creates the same client without the loopback addresses. See [CONTRIBUTING.md](../../CONTRIBUTING.md#setting-up).

## Layout

| Path | Contents |
|---|---|
| `lib/src/auth` | PKCE, the OpenID Connect client, the session and token storage |
| `lib/src/api` | The core API client and its data classes |
| `lib/src/data` | The repository the screens use. Offline storage goes here. |
| `lib/src/map` | The map style and the token for map requests |
| `lib/src/theme` | The theme, generated from the design tokens |
| `lib/src/ui` | Screens and shared widgets |
| `test` | Unit and widget tests with fakes |
| `test_integration` | Tests against a running stack, with their synthetic fixtures |
| `tool/generate_tokens.dart` | Writes the theme from `docs/design-system/tokens.json` |

The app icon is drawn from the logo mark in `docs/design-system/logo`. Inter is included under the SIL Open Font License (`assets/fonts/OFL.txt`).
