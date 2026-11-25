import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/app.dart';
import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/auth/session.dart';
import 'package:fimra_field/src/auth/tokens.dart';
import 'package:fimra_field/src/config.dart';
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

  Widget app({ThemeMode themeMode = ThemeMode.light}) => FimraApp(
    configStore: ConfigStore(testConfig),
    initialConfig: testConfig,
    themeMode: themeMode,
    servicesFactory: (config) {
      configs.add(config);
      return AppServices(
        config: config,
        session: Session(
          oidc: OidcClient(config: config, httpClient: server.client),
          store: store,
          browser: FakeBrowser(server),
        ),
        repository: repository,
      );
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
    await tester.tap(find.widgetWithText(FilledButton, 'Acknowledged'));
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
