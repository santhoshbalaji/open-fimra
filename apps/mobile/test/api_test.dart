import 'dart:convert';
import 'dart:io';

import 'package:fimra_field/src/api/api_client.dart';
import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/config.dart';
import 'package:fimra_field/src/map/map_style.dart';
import 'package:fimra_field/src/theme/tokens.g.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'support.dart';

void main() {
  group('API client', () {
    final seen = <http.Request>[];
    var refreshes = 0;
    late FimraApi api;

    FimraApi make(Future<http.Response> Function(http.Request request) handler) => FimraApi(
      baseUrl: 'https://api.example.test',
      token: ({bool forceRefresh = false}) async {
        if (forceRefresh) refreshes++;
        return 'token-$refreshes';
      },
      httpClient: MockClient((request) async {
        seen.add(request);
        return handler(request);
      }),
    );

    setUp(() {
      seen.clear();
      refreshes = 0;
    });

    test('sends the bearer token and reads alerts with their filters', () async {
      api = make(
        (_) async => http.Response(
          jsonEncode({
            'items': [
              {'id': '41', 'queue': 'unit', 'status': 'new', 'forest_class': 'inside'},
            ],
            'counts': {'unit': 1},
            'canReview': false,
            'nextCursor': 'next',
          }),
          200,
        ),
      );
      final page = await api.alerts('demo', queue: 'unit', hours: 168, limit: 50);
      final request = seen.single;
      expect(request.headers['Authorization'], 'Bearer token-0');
      expect(request.url.path, '/api/v1/workspaces/demo/alerts');
      expect(request.url.queryParameters, {'queue': 'unit', 'hours': '168', 'limit': '50'});
      expect(page.items.single.id, '41');
      expect(page.nextCursor, 'next');
    });

    test('refreshes the token once when the API refuses it', () async {
      api = make(
        (request) async => request.headers['Authorization'] == 'Bearer token-0'
            ? http.Response(jsonEncode({'error': 'Invalid token.'}), 401)
            : http.Response(jsonEncode({'subject': 's', 'username': 'beat.guard', 'roleAssignments': []}), 200),
      );
      final profile = await api.me();
      expect(profile.username, 'beat.guard');
      expect(seen, hasLength(2));
      expect(refreshes, 1);
    });

    test("passes on the server's message", () async {
      api = make((_) async => http.Response(jsonEncode({'error': 'Your role cannot record this step.'}), 403));
      await expectLater(
        api.recordStep('demo', '41', const StepRequest(step: 'closed', clientId: 'c1')),
        throwsA(
          isA<ApiException>()
              .having((e) => e.status, 'status', 403)
              .having((e) => e.message, 'message', 'Your role cannot record this step.'),
        ),
      );
      final body = jsonDecode(seen.single.body) as Map<String, dynamic>;
      expect(body, {'step': 'closed', 'clientId': 'c1'});
      expect(seen.single.headers['Content-Type'], startsWith('application/json'));
    });

    test('reports an unreachable server in plain English', () async {
      api = make((_) async => throw const SocketException('no route'));
      await expectLater(api.me(), throwsA(isA<ApiException>().having((e) => e.offline, 'offline', isTrue)));
    });

    test('an ended session becomes a 401', () async {
      final ended = FimraApi(
        baseUrl: 'https://api.example.test',
        token: ({bool forceRefresh = false}) async => throw const SessionEndedException(),
        httpClient: MockClient((_) async => http.Response('', 500)),
      );
      await expectLater(ended.me(), throwsA(isA<ApiException>().having((e) => e.status, 'status', 401)));
    });

    test('builds tile templates with the map placeholders intact', () {
      api = make((_) async => http.Response('', 204));
      expect(
        api.referenceTiles('demo'),
        'https://api.example.test/api/v1/workspaces/demo/tiles/reference/{z}/{x}/{y}.mvt',
      );
      expect(
        api.detectionTiles('demo', hours: 24),
        'https://api.example.test/api/v1/workspaces/demo/tiles/detections/{z}/{x}/{y}.mvt'
        '?hours=24&classes=inside,near_edge,outside,fixed_heat_source',
      );
    });
  });

  group('models', () {
    test('read an alert as the API sends it', () {
      final alert = sampleAlert(overdue: true);
      expect(alert.forest, ForestClass.inside);
      expect(alert.alertQueue, AlertQueue.unit);
      expect(alert.observedAt, DateTime.utc(2026, 11, 25, 8));
      expect(alert.placeName(sampleMap), 'Beat 01');
      expect(alert.overdue, isTrue);
      expect(sampleMap.levelLabel('territorial', 'beat'), 'Beat');
    });

    test('a step request leaves out empty values', () {
      expect(
        const StepRequest(step: 'reached_site', clientId: 'c', latitude: 16.3, longitude: 86.2, note: 'Here').toJson(),
        {
          'step': 'reached_site',
          'clientId': 'c',
          'note': 'Here',
          'location': {'latitude': 16.3, 'longitude': 86.2},
        },
      );
    });

    test('the profile lists each workspace once', () {
      expect(guardProfile.workspaces.map((w) => w.slug), ['demo']);
      expect(guardProfile.roleAssignments.single.roleLabel, 'Field staff');
      expect(guardProfile.roleAssignments.single.unit.bounds, const Bounds(86.195, 16.302, 86.262, 16.39));
    });
  });

  group('configuration', () {
    test('checks addresses', () {
      expect(AppConfig.checkAddress('https://fimra.example.org'), isNull);
      expect(AppConfig.checkAddress('http://10.0.2.2:18090'), isNull);
      expect(AppConfig.checkAddress('fimra.example.org'), isNotNull);
      expect(AppConfig.checkAddress('ftp://fimra.example.org'), isNotNull);
      expect(AppConfig.checkAddress('https://fimra.example.org/?a=1'), isNotNull);
    });

    test('drops trailing slashes', () {
      expect(testConfig.copyWith(apiBaseUrl: 'https://api.example.test///').apiBaseUrl, 'https://api.example.test');
    });

    test('the default client is the public PKCE client', () {
      final defaults = AppConfig.fromEnvironment();
      expect(defaults.clientId, 'open-fimra-mobile');
      expect(defaults.redirectUri, 'org.openfimra.field:/callback');
      expect(defaults.callbackScheme, 'org.openfimra.field');
    });
  });

  group('map style', () {
    final style = mapStyle(
      referenceTiles: 'https://api.example.test/ref/{z}/{x}/{y}.mvt',
      detectionTiles: 'https://api.example.test/det/{z}/{x}/{y}.mvt',
      info: sampleMap,
      palette: lightPalette,
      jurisdiction: guardProfile.roleAssignments,
    );
    final layers = (style['layers'] as List).cast<Map<String, Object?>>();

    test('loads only the core API tiles and no fonts', () {
      final sources = style['sources'] as Map<String, Object?>;
      final urls = [for (final s in sources.values) ...((s as Map)['tiles'] as List)];
      expect(urls, everyElement(startsWith('https://api.example.test/')));
      expect(style, isNot(contains('glyphs')));
      expect(layers.where((l) => (l['layout'] as Map?)?.containsKey('text-field') ?? false), isEmpty);
    });

    test('draws each forest class with its own shape', () {
      final ids = layers.map((l) => l['id']).toList();
      expect(ids, containsAll(detectionLayers));
      final edge = layers.firstWhere((l) => l['id'] == 'detections-near-edge');
      expect((edge['paint'] as Map)['circle-color'], 'rgba(0,0,0,0)');
      final fixed = layers.firstWhere((l) => l['id'] == 'detections-fixed');
      expect((fixed['layout'] as Map)['icon-image'], squareImage);
      // Detections draw above the reference layers.
      expect(ids.indexOf('detections-outside'), greaterThan(ids.indexOf('my-area')));
    });

    test('outlines the user\'s own unit', () {
      final area = layers.firstWhere((l) => l['id'] == 'my-area');
      expect(jsonEncode(area['filter']), contains('"B01"'));
    });

    test('starts at the user\'s units, or else the workspace', () {
      expect(jurisdictionBounds(guardProfile.roleAssignments, sampleMap), const Bounds(86.195, 16.302, 86.262, 16.39));
      expect(jurisdictionBounds(const [], sampleMap), sampleMap.boundary);
    });
  });
}
