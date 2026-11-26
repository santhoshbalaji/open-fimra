import 'dart:convert';

import 'package:fimra_field/src/api/api_client.dart';
import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/app.dart';
import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/auth/session.dart';
import 'package:fimra_field/src/auth/tokens.dart';
import 'package:fimra_field/src/config.dart';
import 'package:fimra_field/src/device/device_manager.dart';
import 'package:fimra_field/src/device/device_store.dart';
import 'package:fimra_field/src/device/local_data.dart';
import 'package:fimra_field/src/theme/app_theme.dart';
import 'package:fimra_field/src/ui/alert_map.dart';
import 'package:fimra_field/src/ui/widgets.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support.dart';

/// The app with a fake sign-in server and an in-memory repository. The map
/// tab shows a placeholder, because widget tests have no map view.
class Harness {
  Harness({FakeRepository? repository}) : repository = repository ?? FakeRepository();

  final server = FakeIdentityServer();
  final FakeRepository repository;
  final store = MemoryTokenStore();
  final configs = <AppConfig>[];
  final deviceApi = FakeDeviceApi();
  final push = FakePush();
  final notifier = FakeNotifier();
  final ids = MemoryDeviceStore();
  DeviceManager? devices;

  Widget app({ThemeMode themeMode = ThemeMode.light}) => FimraApp(
    configStore: ConfigStore(testConfig),
    initialConfig: testConfig,
    themeMode: themeMode,
    servicesFactory: (config) {
      configs.add(config);
      final session = Session(
        oidc: OidcClient(config: config, httpClient: server.client),
        store: store,
        browser: FakeBrowser(server),
      );
      final manager = devices = DeviceManager(
        api: deviceApi,
        session: session,
        store: ids,
        push: push,
        notifier: notifier,
        platform: 'android',
        localData: fieldDataOnPhone(
          notifications: notifier.clear,
          pushRegistration: push.stop,
          offlineMaps: () async {},
          deviceId: ids.clear,
          signInTokens: store.clear,
        ),
      );
      repository.onDeviceAction = manager.handleAction;
      return AppServices(config: config, session: session, repository: repository, devices: manager);
    },
    mapBuilder: (context, view, open) => Center(child: Text('Map of ${view.workspace.name}')),
  );
}

Future<void> signIn(WidgetTester tester, Harness harness) async {
  await tester.pumpWidget(harness.app());
  await tester.pumpAndSettle();
  await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
  await tester.pumpAndSettle();
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  testWidgets('a beat guard signs in and sees their alerts with status icons and labels', (tester) async {
    final harness = Harness();
    await tester.pumpWidget(harness.app());
    await tester.pumpAndSettle();
    expect(find.text('Field app'), findsOneWidget);
    expect(find.text('Server: api.example.test'), findsOneWidget);

    await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
    await tester.pumpAndSettle();
    expect(harness.server.requests.single['grant_type'], 'authorization_code');
    expect(find.text('Demo State'), findsOneWidget);
    expect(find.text('2 alerts in the past 7 days · Beat 01'), findsOneWidget);
    expect(find.text('Beat 01'), findsNWidgets(2));

    // Every status is an icon and a label.
    final badges = tester.widgetList<StatusBadge>(find.byType(StatusBadge)).toList();
    expect(badges.map((b) => b.label), containsAll(['New', 'Acknowledged']));
    for (final badge in badges) {
      expect(find.descendant(of: find.byWidget(badge), matching: find.byIcon(badge.icon)), findsOneWidget);
    }
    // Field staff see no review queues.
    expect(find.byType(ChoiceChip), findsNothing);

    await tester.tap(find.text('Map'));
    await tester.pumpAndSettle();
    expect(find.text('Map of Demo State'), findsOneWidget);
  });

  testWidgets('a beat guard acknowledges an alert', (tester) async {
    final harness = Harness();
    await signIn(tester, harness);
    await tester.tap(find.widgetWithText(Card, 'New'));
    await tester.pumpAndSettle();

    expect(find.text('Hill Division (D1)'), findsOneWidget);
    expect(find.text('Inside forest'), findsOneWidget);
    final acknowledge = find.widgetWithText(FilledButton, 'Acknowledged');
    await tester.scrollUntilVisible(acknowledge, 180);
    await tester.pumpAndSettle();
    await tester.tap(acknowledge);
    await tester.pumpAndSettle();
    expect(find.text('This is saved with your name and the time.'), findsOneWidget);
    await tester.enterText(find.byType(TextField), 'On my way.');
    await tester.tap(find.widgetWithText(FilledButton, 'Save'));
    await tester.pumpAndSettle();

    final request = harness.repository.recorded.single;
    expect(request.step, 'acknowledged');
    expect(request.note, 'On my way.');
    expect(request.clientId, isNotEmpty);
    expect(request.latitude, isNull);
    expect(find.text('Acknowledged recorded.'), findsOneWidget);
    // The timeline shows the new step with who took it.
    await tester.scrollUntilVisible(find.textContaining('Demo Beat Guard'), 200);
    expect(find.textContaining('Demo Beat Guard'), findsOneWidget);

    await tester.pageBack();
    await tester.pumpAndSettle();
    expect(find.text('New'), findsNothing);
  });

  testWidgets('a user without a role is told to ask for one', (tester) async {
    final harness = Harness(
      repository: FakeRepository(
        profile: const Profile(subject: 's', username: 'no.role', roleAssignments: []),
      ),
    );
    await signIn(tester, harness);
    expect(find.text('You have no access to a workspace yet'), findsOneWidget);
    expect(find.byType(NavigationBar), findsNothing);
  });

  testWidgets('an empty area says so', (tester) async {
    final harness = Harness(repository: FakeRepository(alerts: []));
    await signIn(tester, harness);
    expect(find.text('No alerts in the past 7 days'), findsOneWidget);
  });

  testWidgets('server settings check addresses and sign out when saved', (tester) async {
    final harness = Harness();
    await tester.pumpWidget(harness.app());
    await tester.pumpAndSettle();
    await tester.tap(find.text('Server settings'));
    await tester.pumpAndSettle();

    await tester.enterText(find.widgetWithText(TextFormField, 'Core API address'), 'not an address');
    await tester.tap(find.widgetWithText(FilledButton, 'Save'));
    await tester.pumpAndSettle();
    expect(find.text('Enter a full address that starts with http:// or https://'), findsOneWidget);

    await tester.enterText(find.widgetWithText(TextFormField, 'Core API address'), 'http://10.0.2.2:18090/');
    await tester.tap(find.widgetWithText(FilledButton, 'Save'));
    await tester.pumpAndSettle();
    expect(harness.configs.last.apiBaseUrl, 'http://10.0.2.2:18090');
    expect(find.text('Server: 10.0.2.2:18090'), findsOneWidget);
    final saved = await ConfigStore(testConfig).load();
    expect(saved.apiBaseUrl, 'http://10.0.2.2:18090');
  });

  testWidgets('a cancelled sign-in stays on the sign-in screen without an error', (tester) async {
    final harness = Harness();
    final cancelled = FimraApp(
      configStore: ConfigStore(testConfig),
      initialConfig: testConfig,
      servicesFactory: (config) => AppServices(
        config: config,
        session: Session(
          oidc: OidcClient(config: config, httpClient: harness.server.client),
          store: harness.store,
          browser: FakeBrowser(harness.server, cancel: true),
        ),
        repository: harness.repository,
      ),
    );
    await tester.pumpWidget(cancelled);
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
    await tester.pumpAndSettle();
    expect(find.widgetWithText(FilledButton, 'Sign in'), findsOneWidget);
    expect(find.byIcon(Icons.error_outline), findsNothing);
  });

  for (final mode in [ThemeMode.light, ThemeMode.dark]) {
    testWidgets('touch targets and text contrast meet the guidelines in ${mode.name} mode', (tester) async {
      final handle = tester.ensureSemantics();
      final harness = Harness();
      await tester.pumpWidget(harness.app(themeMode: mode));
      await tester.pumpAndSettle();
      await expectLater(tester, meetsGuideline(androidTapTargetGuideline));
      await expectLater(tester, meetsGuideline(textContrastGuideline));
      await tester.tap(find.widgetWithText(FilledButton, 'Sign in'));
      await tester.pumpAndSettle();
      await expectLater(tester, meetsGuideline(androidTapTargetGuideline));
      await expectLater(tester, meetsGuideline(labeledTapTargetGuideline));
      await expectLater(tester, meetsGuideline(textContrastGuideline));
      await tester.tap(find.widgetWithText(Card, 'New'));
      await tester.pumpAndSettle();
      await expectLater(tester, meetsGuideline(androidTapTargetGuideline));
      await expectLater(tester, meetsGuideline(textContrastGuideline));
      handle.dispose();
    });
  }

  testWidgets('tapping an alert notification opens the alert from the API', (tester) async {
    final harness = Harness();
    await signIn(tester, harness);
    expect(harness.deviceApi.registrations, hasLength(1));
    harness.notifier.onOpen!(PushNotice.parse(utf8.encode(jsonEncode(alertPush())))!);
    await tester.pumpAndSettle();
    expect(find.text('Hill Division (D1)'), findsOneWidget);
    expect(harness.devices!.openRequest.value, isNull);
  });

  testWidgets('a notification that started the app opens its alert after sign-in', (tester) async {
    final harness = Harness();
    harness.notifier.launch = PushNotice.parse(utf8.encode(jsonEncode(alertPush())));
    await signIn(tester, harness);
    expect(find.text('Hill Division (D1)'), findsOneWidget);
  });

  testWidgets('a phone wiped while in use deletes its data and returns to sign-in', (tester) async {
    final harness = Harness();
    await signIn(tester, harness);
    harness.push.onAddress!(testAddress);
    harness.push.deliver(alertPush());
    await tester.pumpAndSettle();
    expect(harness.notifier.shown, hasLength(1));
    final id = harness.ids.id;

    harness.repository.refuseWith = DeviceAction.wipe;
    await tester.drag(find.byType(Scrollable).first, const Offset(0, 400));
    await tester.pumpAndSettle();

    expect(find.widgetWithText(FilledButton, 'Sign in'), findsOneWidget);
    expect(find.textContaining('An administrator asked this phone to delete its field data'), findsOneWidget);
    expect(harness.deviceApi.confirmed, [id]);
    expect(harness.store.tokens, isNull);
    expect(harness.ids.id, isNull);
    expect(harness.notifier.shown, isEmpty);
    expect(harness.push.stops, 1);
  });

  testWidgets('the phones screen shows notifications and removes another phone', (tester) async {
    final handle = tester.ensureSemantics();
    final harness = Harness();
    await signIn(tester, harness);
    harness.push.onAddress!(testAddress);
    await tester.pumpAndSettle();
    final id = harness.ids.id!;
    harness.deviceApi.listed = [
      Device(
        id: id,
        platform: 'android',
        appVersion: fieldAppVersion,
        push: true,
        status: DeviceStatus.active,
        registeredAt: DateTime.utc(2026, 11, 25, 8),
        lastSeenAt: DateTime.utc(2026, 11, 25, 9),
        current: true,
      ),
      Device(
        id: newDeviceId(),
        platform: 'android',
        appVersion: '0.1.0+1',
        push: false,
        status: DeviceStatus.active,
        registeredAt: DateTime.utc(2026, 11, 20, 8),
        lastSeenAt: DateTime.utc(2026, 11, 21, 9),
      ),
      Device(
        id: newDeviceId(),
        platform: 'android',
        appVersion: '0.1.0+1',
        push: false,
        status: DeviceStatus.wiped,
        registeredAt: DateTime.utc(2026, 11, 1, 8),
        lastSeenAt: DateTime.utc(2026, 11, 2, 9),
      ),
    ];
    await tester.tap(find.byTooltip('Account'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Notifications and phones'));
    await tester.pumpAndSettle();

    expect(find.text('Notifications are on'), findsOneWidget);
    expect(find.text('Android phone (this phone)'), findsOneWidget);
    expect(find.text('Data deleted'), findsOneWidget);
    expect(find.text('No notifications'), findsOneWidget);
    await expectLater(tester, meetsGuideline(androidTapTargetGuideline));
    await expectLater(tester, meetsGuideline(labeledTapTargetGuideline));
    await expectLater(tester, meetsGuideline(textContrastGuideline));
    // Only the other signed-in phone can be removed here.
    expect(find.widgetWithText(TextButton, 'Remove'), findsOneWidget);
    await tester.tap(find.widgetWithText(TextButton, 'Remove'));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Remove'));
    await tester.pumpAndSettle();
    expect(harness.deviceApi.revoked, [(harness.deviceApi.listed[1].id, false)]);
    expect(find.text('The phone was removed.'), findsOneWidget);
    handle.dispose();
  });

  testWidgets('signing out tells the API this phone signed out', (tester) async {
    final harness = Harness();
    await signIn(tester, harness);
    final id = harness.ids.id!;
    await tester.tap(find.byTooltip('Account'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Sign out'));
    await tester.pumpAndSettle();
    expect(harness.deviceApi.revoked, [(id, true)]);
    expect(harness.ids.id, isNull);
    expect(find.widgetWithText(FilledButton, 'Sign in'), findsOneWidget);
  });

  testWidgets('the map key explains shape and colour in words', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: fimraTheme(Brightness.light),
        home: const Scaffold(body: MapKey()),
      ),
    );
    for (final forest in ForestClass.values) {
      expect(find.text(forest.label), findsOneWidget);
    }
    expect(find.text('Under 6 hours'), findsOneWidget);
    expect(find.text('3 to 7 days'), findsOneWidget);
  });
}
