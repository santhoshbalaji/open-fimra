# Field app

The Flutter app for forest staff in the field, Android first. Staff sign in with their department account, see the fire alerts for their own area as a list and on a map, open an alert and record workflow steps such as **Acknowledged**.

This first version works online. Keeping alerts, forms and maps on the phone, and sending reports when a signal returns, is the next stage (slice 26 in [PLAN.md](../../PLAN.md#15-next-steps)). Screens already read and write through one repository layer (`lib/src/data/alert_repository.dart`), so offline storage can be added behind it without changing them.

## What it does

- **Sign-in.** The app opens the department's sign-in page in a browser tab and uses the authorisation code flow with a one-time proof key (PKCE). The app never sees the password and has no client secret. It keeps the tokens in the Android Keystore and renews them before they expire. **Sign out** ends the session on the sign-in server too.
- **Workspaces.** The app lists the workspaces where the user has a role. A user with more than one can switch from the account menu or the title.
- **Alerts.** The list shows the past seven days, newest first. Each alert shows its place, forest class, age and its status as an icon and a label, with **Overdue** when a time limit has passed. Officers who review alerts can filter by queue. Pull down to refresh.
- **Map.** The map opens on the user's own units, outlined. It draws the core API's permission-filtered vector tiles: units, forest and exclusions, and detections with the shared symbols (shape for forest class, colour for age). **Map key** explains them in words. Tap a point to open its alert. The map uses MapLibre and loads nothing from outside services, so it needs no licence key.
- **Notifications.** After sign-in the phone registers itself with the core API. New alerts for the user's area arrive as notifications through a push app the user installs, such as ntfy, connected to the department's own push server. A notification says only "New fire alert"; tapping it opens the alert, which the app fetches over the signed-in API. **Notifications and phones** in the account menu shows whether notifications work on this phone and lists the user's phones, so they can remove one they no longer use.
- **Remote wipe.** An administrator can ask a lost phone to delete its field data. The phone does it the next time it contacts the API. See [Remote wipe](#remote-wipe).
- **Alert details and steps.** An alert shows where and when it was seen, its source and its timeline. The buttons offer only the steps the user's role allows next. Field steps such as **Reached site** save the phone's location, which must be near the fire. Supervising officers can record a reason instead. Each step carries a client ID, so pressing **Save** again after a lost answer records it once.

The interface uses the shared [design tokens](../../docs/design-system/tokens.json) in a light and a dark theme that follow the phone's setting, the Inter font bundled with the app, the [project logo](../../docs/design-system/logo) and touch targets of at least 48 pixels.

## Notifications

The app uses [UnifiedPush](https://unifiedpush.org), so it needs no Google service. On the phone:

1. Install a UnifiedPush push app, such as ntfy from F-Droid or the Play Store.
2. In the push app, set the department's push server as the default server, for example `https://push.example.org`. The core API only sends to servers in its `FIMRA_PUSH_SERVERS` setting.
3. Open the field app and sign in. It finds the push app, gets an address from it and registers it with the API. **Notifications and phones** says **Notifications are on** once the API accepts it. Android 13 and later also ask whether the app may show notifications.

If no push app is installed, the app works as before without notifications and says how to turn them on. After installing one, choose **Check again**.

Push messages are encrypted for the phone and hold only IDs and a short generic text. The push app wakes the field app to show the notification, even when the field app is closed. The same notification arriving twice replaces the first. A message asking the phone to check in (sent after a wipe request) makes it call the API, which decides what to do; a push message can never wipe a phone by itself.

The browser test build has no push. It registers as a browser device without notifications.

## Remote wipe

Every call to the API names the phone's registration in the `Fimra-Device` header. When an administrator has wiped the phone, the API refuses its next call, whatever it is, with `device_wiped`. The app then:

1. tells the API it is deleting its data (`POST /api/v1/devices/{id}/wiped`), while it still has a token,
2. deletes the field data listed below,
3. ends the session on the sign-in server, and returns to the sign-in screen with a message saying why.

What a wipe deletes, in order:

| Data | Where it is |
| --- | --- |
| Alert notifications shown on the phone | The Android notification shade |
| The push registration with the push app | The push app, which forgets the phone's address |
| Map areas saved for offline use | MapLibre's offline store (none yet; slice 26 adds offline maps) |
| The phone's registration ID | The Android Keystore |
| Sign-in tokens | The Android Keystore |

The list lives in one place, `fieldDataOnPhone` in `lib/src/device/local_data.dart`. Anything new the app keeps on the phone, such as the offline alerts, forms and queued reports of slice 26, must be added there.

A wipe keeps the server settings, which are not personal data, so the user can sign in again. This version of the app does not save alerts on the phone: it reads them from the API each time, and the API marks its answers and map tiles `no-store` so they are not kept. The sign-in page's cookies belong to the phone's browser; ending the session on the sign-in server makes them useless.

A phone that was removed from the account (`device_revoked`) signs out without the wipe list. Signing out tells the API, stops push and forgets the registration ID. The next sign-in makes a new registration.

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

The integration tests sign the beat guard in through the real `open-fimra-mobile` client with PKCE, with a scripted browser filling in the Keycloak form. They check that the client refuses password sign-in and a request without a proof key, that each demo user sees only their own alerts and tiles, and that the beat guard can acknowledge an alert exactly once. They also register the beat guard's phone, check who can see it, have the division officer wipe it and check that the phone's next call deletes its data and returns it to sign-in. Other demo users get their tokens from the development-only `open-fimra-dev-cli` client.

The walkthrough builds the app for a browser, serves it on `127.0.0.1:4300` (`MOBILE_WEB_PORT`, 4300 to 4303) and signs in as the beat guard at phone size. It sets the server under **Server settings**, checks the list and the map, acknowledges an alert, opens **Notifications and phones**, has the division officer wipe the phone through the API, checks that the next start returns to sign-in with the reason, and saves screenshots to `~/open-fimra-screenshots/field-app` (`MOBILE_SHOTS`). The browser build exists only for these tests. It serves its own copy of MapLibre GL JS, which `make mobile-web` downloads from the npm registry and checks against its published checksum.

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
| `lib/src/device` | Device registration, push, notifications and the wipe list |
| `lib/src/map` | The map style and the token for map requests |
| `lib/src/theme` | The theme, generated from the design tokens |
| `lib/src/ui` | Screens and shared widgets |
| `test` | Unit and widget tests with fakes |
| `test_integration` | Tests against a running stack, with their synthetic fixtures |
| `tool/generate_tokens.dart` | Writes the theme from `docs/design-system/tokens.json` |

The app icon is drawn from the logo mark in `docs/design-system/logo`. Inter is included under the SIL Open Font License (`assets/fonts/OFL.txt`).
