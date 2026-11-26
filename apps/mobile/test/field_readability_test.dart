import 'dart:convert';

import 'package:fimra_field/src/api/api_client.dart';
import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/app.dart';
import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/auth/session.dart';
import 'package:fimra_field/src/auth/tokens.dart';
import 'package:fimra_field/src/data/field_store.dart';
import 'package:fimra_field/src/data/offline_repository.dart';
import 'package:fimra_field/src/forms/report_form.dart';
import 'package:fimra_field/src/theme/app_theme.dart';
import 'package:fimra_field/src/ui/alert_detail.dart';
import 'package:fimra_field/src/ui/alert_list.dart';
import 'package:fimra_field/src/ui/alert_map.dart';
import 'package:fimra_field/src/ui/home.dart';
import 'package:fimra_field/src/ui/outbox_screen.dart';
import 'package:fimra_field/src/ui/sign_in.dart';
import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'support.dart';

const hindi = 'अग्नि निरीक्षण दल के पहुँचने से पहले वन पथ की स्थिति और प्रवेश का विवरण लिखें';
const bengali = 'আগুন পরিদর্শন দল পৌঁছানোর আগে বনের পথ এবং প্রবেশের অবস্থা সম্পর্কে বিবরণ লিখুন';
const failureReason = 'The report was returned because the location was outside the assigned area.';

class _OfflineView extends OfflineAlertRepository {
  _OfflineView(http.Client client)
    : super(
        FimraApi(
          baseUrl: testConfig.apiBaseUrl,
          token: ({bool forceRefresh = false}) async => 'demo',
          httpClient: client,
        ),
        FieldStore(),
        () => 'demo',
      ) {
    offline = true;
    lastSynced = DateTime.utc(2026, 11, 25, 8);
  }

  final fake = FakeRepository();

  @override
  Future<AlertPage> alerts(String slug, {AlertQueue? queue, String? cursor}) async {
    final page = await fake.alerts(slug, queue: queue, cursor: cursor);
    return AlertPage(items: page.items, counts: page.counts, canReview: true);
  }

  @override
  Future<Alert> alert(String slug, String id) => fake.alert(slug, id);

  @override
  Future<AlertHistory> history(String slug, String id) => fake.history(slug, id);

  @override
  Future<void> queueStep(String slug, String alert, WorkflowMove move, String fromStep, StepRequest request) async {
    await fake.recordStep(slug, alert, request);
  }

  @override
  Future<Json> reportsForAlert(String slug, String id) async => {
    'forms': [
      {'form': 'demo', 'step': 'first_report', 'title': 'Inspection report'},
    ],
  };

  @override
  Future<List<Json>> reportStatus({String? workspace, String? alert}) async => [
    {
      'title': 'Inspection report',
      'body': jsonEncode({'form': 'demo', 'step': 'first_report'}),
      'error': failureReason,
      'file_count': 2,
      'files_sent': 1,
    },
    if (alert == null) {'title': 'Follow-up report'},
    if (alert == null) {'title': 'Earlier report', 'received_at': '2026-11-25T08:00:00Z'},
  ];

  int syncs = 0;

  @override
  Future<void> sync({bool retry = false, bool refresh = false}) async {
    if (retry && refresh) syncs++;
  }
}

class _OfflineSteps extends _OfflineView {
  _OfflineSteps(super.client);

  static const acknowledge = WorkflowMove(
    step: 'acknowledged',
    label: 'Acknowledged',
    locationRequired: false,
    canOverride: false,
    terminal: false,
  );
  static const teamSent = WorkflowMove(
    step: 'team_sent',
    label: 'Team sent',
    locationRequired: false,
    canOverride: false,
    terminal: false,
  );
  static const reached = WorkflowMove(
    step: 'reached_site',
    label: 'Reached site',
    locationRequired: true,
    canOverride: false,
    terminal: false,
  );
  final queued = <Json>[];

  @override
  Future<AlertHistory> history(String slug, String id) async => const AlertHistory(
    entries: [],
    moves: [acknowledge],
    offlineMoves: {
      'acknowledged': [teamSent],
      'team_sent': [reached],
    },
  );

  @override
  Future<Json> reportsForAlert(String slug, String id) async => {};

  @override
  Future<List<Json>> reportStatus({String? workspace, String? alert}) async => List.of(queued);

  @override
  Future<void> queueStep(String slug, String alert, WorkflowMove move, String fromStep, StepRequest request) async {
    queued.add({'title': move.label, 'kind': 'step', 'from_step': fromStep, 'body': jsonEncode(request.toJson())});
    changes.value++;
  }
}

Widget _app(Brightness brightness, Widget home) => MaterialApp(
  theme: fimraTheme(brightness),
  builder: (context, child) => MediaQuery(
    data: MediaQuery.of(context).copyWith(textScaler: TextScaler.linear(2)),
    child: child!,
  ),
  home: home,
);

void _phone(WidgetTester tester) {
  tester.view.physicalSize = const Size(390, 680);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  addTearDown(tester.view.resetViewInsets);
}

Future<void> _guidelines(WidgetTester tester) async {
  expect(tester.takeException(), isNull);
  await expectLater(tester, meetsGuideline(androidTapTargetGuideline));
  await expectLater(tester, meetsGuideline(labeledTapTargetGuideline));
  await expectLater(tester, meetsGuideline(textContrastGuideline));
}

void _order(WidgetTester tester, List<String> parts) {
  final labels = <String>[];
  void visit(SemanticsNode node) {
    labels.addAll(node.label.split('\n'));
    node.debugListChildrenInOrder(DebugSemanticsDumpOrder.traversalOrder).forEach(visit);
  }

  visit(tester.binding.renderViews.single.owner!.semanticsOwner!.rootSemanticsNode!);
  var index = -1;
  for (final part in parts) {
    index = labels.indexWhere((label) => label.contains(part), index + 1);
    expect(index, isNonNegative, reason: 'Expected "$part" in reading order. Labels: $labels');
  }
}

void _wrapped(WidgetTester tester, String label) {
  final finder = find.text(label);
  expect(finder, findsOneWidget);
  expect(tester.getSize(finder).height, greaterThan(48));
  expect(tester.getRect(finder).right, lessThanOrEqualTo(390));
}

void main() {
  testWidgets('offline steps make the next action available and keep the confirmed status', (tester) async {
    final server = FakeIdentityServer();
    final repo = _OfflineSteps(server.client);
    addTearDown(repo.dispose);
    final view = WorkspaceView(
      services: AppServices(
        config: testConfig,
        session: Session(
          oidc: OidcClient(config: testConfig, httpClient: server.client),
          store: MemoryTokenStore(),
          browser: FakeBrowser(server),
        ),
        repository: repo,
      ),
      profile: guardProfile,
      workspace: guardProfile.workspaces.single,
      map: sampleMap,
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: fimraTheme(Brightness.light),
        home: AlertDetailScreen(
          view: view,
          alert: sampleAlert(),
          locate: () async => (latitude: 16.347, longitude: 86.2264),
        ),
      ),
    );
    await tester.pumpAndSettle();

    for (final action in ['Acknowledge', 'Mark team sent', 'Mark reached']) {
      final button = find.widgetWithText(FilledButton, action);
      expect(button.hitTestable(), findsOneWidget);
      await tester.tap(button);
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(FilledButton, 'Save'));
      await tester.pumpAndSettle();
    }

    expect(find.text('New').hitTestable(), findsOneWidget);
    expect(find.text('3 waiting to send').hitTestable(), findsOneWidget);
    expect(repo.queued.map((row) => row['from_step']), ['new', 'acknowledged', 'team_sent']);
    final arrival = StepRequest.fromJson(jsonDecode(repo.queued.last['body'] as String) as Json);
    expect(arrival.step, 'reached_site');
    expect(arrival.clientTime, isNotNull);
    expect(arrival.latitude, 16.347);
  });

  for (final brightness in Brightness.values) {
    testWidgets('${brightness.name} at 200%: alert list, details, step and outbox', (tester) async {
      _phone(tester);
      final server = FakeIdentityServer();
      final repo = _OfflineView(server.client);
      addTearDown(repo.dispose);
      final services = AppServices(
        config: testConfig,
        session: Session(
          oidc: OidcClient(config: testConfig, httpClient: server.client),
          store: MemoryTokenStore(),
          browser: FakeBrowser(server),
        ),
        repository: repo,
      );
      final view = WorkspaceView(
        services: services,
        profile: guardProfile,
        workspace: guardProfile.workspaces.single,
        map: sampleMap,
      );
      await tester.pumpWidget(
        _app(
          brightness,
          Scaffold(
            body: Builder(
              builder: (context) => AlertListView(
                view: view,
                onOpen: (alert) => Navigator.push(
                  context,
                  MaterialPageRoute<void>(
                    builder: (_) => AlertDetailScreen(view: view, alert: alert),
                  ),
                ),
              ),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      _order(tester, ['Alerts', '2 alerts', 'All (2)', 'Offline']);
      await _guidelines(tester);
      expect(
        tester.widgetList<Scrollable>(find.byType(Scrollable)).map((scroll) => scroll.axisDirection),
        everyElement(AxisDirection.down),
      );
      final failed = find.text('1 could not send');
      await tester.scrollUntilVisible(failed, 140);
      await tester.pumpAndSettle();
      await tester.tap(failed);
      await tester.pumpAndSettle();
      final reason = find.text('Inspection report: $failureReason');
      await tester.ensureVisible(reason);
      await tester.pumpAndSettle();
      expect(reason.hitTestable(), findsOneWidget);
      await _guidelines(tester);
      await tester.ensureVisible(failed);
      await tester.pumpAndSettle();
      await tester.tap(failed);
      await tester.pumpAndSettle();

      final row = find.bySemanticsLabel(RegExp(r'^Beat 01 · Alert #41, New, Inside forest, seen'));
      await tester.scrollUntilVisible(row, 220);
      await tester.pumpAndSettle();
      expect(row.hitTestable(), findsOneWidget);
      _order(tester, ['Report outbox', 'Beat 01 · Alert #41, New, Inside forest']);
      await _guidelines(tester);
      await tester.tap(row);
      await tester.pumpAndSettle();
      _order(tester, ['Alert details', 'Beat 01', 'New', 'Offline', 'Last synced']);
      await _guidelines(tester);

      await tester.scrollUntilVisible(find.text(failureReason), 280);
      await tester.pumpAndSettle();
      _order(tester, ['Inspection report', 'Saved on this phone', 'Could not send', failureReason]);
      await _guidelines(tester);

      final acknowledge = find.widgetWithText(FilledButton, 'Acknowledge');
      await tester.scrollUntilVisible(acknowledge, 200);
      await tester.pumpAndSettle();
      expect(acknowledge.hitTestable(), findsOneWidget);
      await tester.tap(acknowledge);
      await tester.pumpAndSettle();
      _order(tester, ['Acknowledged', 'This is saved with your name', 'Note (optional)', 'Save', 'Cancel']);
      await _guidelines(tester);
      final save = find.widgetWithText(FilledButton, 'Save');
      tester.view.viewInsets = const FakeViewPadding(bottom: 260);
      await tester.pumpAndSettle();
      await tester.ensureVisible(save);
      await tester.pumpAndSettle();
      expect(save.hitTestable(), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.tap(save);
      tester.view.resetViewInsets();
      await tester.pumpAndSettle();
      expect(repo.fake.recorded.single.step, 'acknowledged');

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpWidget(_app(brightness, OutboxScreen(repo: repo)));
      await tester.pumpAndSettle();
      _order(tester, [
        'Report outbox',
        'Offline',
        'Last synced',
        'Sync now',
        'Inspection report',
        'Saved on this phone',
        'Could not send',
        failureReason,
      ]);
      await _guidelines(tester);
      await tester.tap(find.widgetWithText(FilledButton, 'Sync now'));
      expect(repo.syncs, 1);
      await tester.scrollUntilVisible(find.textContaining('Sent '), 200);
      await tester.pumpAndSettle();
      _order(tester, ['Follow-up report', 'Waiting to send', 'Earlier report', 'Sent ']);
      await _guidelines(tester);
    });

    testWidgets('${brightness.name} at 200%: report labels wrap and Save to outbox works', (tester) async {
      _phone(tester);
      Json? saved;
      await tester.pumpWidget(
        _app(
          brightness,
          ReportForm(
            definition: const {
              'title': {'en': 'Inspection report'},
              'sections': [
                {
                  'key': 'inspection',
                  'title': {'en': 'Inspection'},
                  'items': [
                    {
                      'key': 'notes',
                      'type': 'text',
                      'label': {'en': hindi},
                    },
                    {
                      'key': 'access',
                      'type': 'single-choice',
                      'label': {'en': bengali},
                      'choices': [
                        {
                          'key': 'clear',
                          'label': {'en': 'Clear path'},
                        },
                      ],
                    },
                  ],
                },
              ],
            },
            step: 'first_report',
            alert: const {},
            save: (values, _, _) async => saved = values,
          ),
        ),
      );
      await tester.pumpAndSettle();
      _wrapped(tester, hindi);
      _wrapped(tester, bengali);
      _order(tester, ['Field report', 'Inspection report', hindi]);
      await _guidelines(tester);
      await tester.enterText(find.byType(TextFormField), 'Demo inspection');
      tester.testTextInput.hide();
      await tester.ensureVisible(find.byType(DropdownButtonFormField<String>));
      await tester.pumpAndSettle();
      _order(tester, [bengali]);
      final save = find.widgetWithText(FilledButton, 'Save to outbox');
      await tester.scrollUntilVisible(save, 120, scrollable: find.byType(Scrollable).first);
      await tester.pumpAndSettle();
      await _guidelines(tester);
      expect(save.hitTestable(), findsOneWidget);
      await tester.tap(save);
      await tester.pumpAndSettle();
      expect(saved, {'notes': 'Demo inspection'});
    });

    testWidgets('${brightness.name} at 200%: the map key leaves the map visible and Close stays in place', (
      tester,
    ) async {
      _phone(tester);
      await tester.pumpWidget(
        _app(
          brightness,
          Scaffold(
            body: Stack(
              children: [
                Semantics(
                  container: true,
                  sortKey: const OrdinalSortKey(1),
                  label: 'Map of your area with fire detections',
                  child: const SizedBox.expand(),
                ),
                Positioned(top: 12, right: 12, child: AlertMapControls(onFit: () {})),
              ],
            ),
          ),
        ),
      );
      _order(tester, ['Show my area', 'Map key', 'Map of your area']);
      await _guidelines(tester);
      await tester.tap(find.byTooltip('Map key'));
      await tester.pumpAndSettle();
      final close = find.widgetWithText(OutlinedButton, 'Close');
      expect(tester.getTopLeft(find.byType(MapKey)).dy, greaterThan(340));
      final position = tester.getRect(close);
      _order(tester, ['Map key', 'Close', 'Shape shows the forest class', 'Inside forest']);
      await _guidelines(tester);
      await tester.scrollUntilVisible(find.text('3 to 7 days'), 140);
      await tester.pumpAndSettle();
      expect(tester.getRect(close), position);
      _order(tester, ['Close', '1 to 3 days', '3 to 7 days']);
      await _guidelines(tester);
      await tester.tap(close);
      await tester.pumpAndSettle();
      expect(find.byType(MapKey), findsNothing);
    });
  }

  testWidgets('sign-in gives an actionable server error with the response folded away', (tester) async {
    _phone(tester);
    final session = Session(
      oidc: OidcClient(config: testConfig, httpClient: MockClient((_) async => http.Response('', 503))),
      store: MemoryTokenStore(),
      browser: FakeBrowser(FakeIdentityServer()),
    );
    addTearDown(session.dispose);
    await tester.pumpWidget(
      _app(
        Brightness.light,
        ListenableBuilder(
          listenable: session,
          builder: (context, _) => SignInScreen(
            session: session,
            config: testConfig,
            defaults: testConfig,
            onConfigChanged: (_, {reset = false}) async {},
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    final signIn = find.widgetWithText(FilledButton, 'Sign in');
    await tester.ensureVisible(signIn);
    await tester.pumpAndSettle();
    await tester.tap(signIn);
    await tester.pumpAndSettle();
    expect(find.text('Cannot reach the server. Check the address or try again.'), findsOneWidget);
    expect(find.textContaining('503'), findsNothing);
    final details = find.text('Technical details');
    await tester.ensureVisible(details);
    await tester.tap(details);
    await tester.pumpAndSettle();
    expect(find.textContaining('503', findRichText: true), findsOneWidget);
  });
}
