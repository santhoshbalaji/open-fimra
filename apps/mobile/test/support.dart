// Fakes shared by the unit and widget tests. All data is synthetic.
import 'dart:convert';

import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/config.dart';
import 'package:fimra_field/src/data/alert_repository.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

const testConfig = AppConfig(
  apiBaseUrl: 'https://api.example.test',
  issuer: 'https://id.example.test/realms/open-fimra',
  clientId: 'open-fimra-mobile',
  redirectUri: AppConfig.mobileRedirectUri,
);

/// A JWT with the given claims and no real signature.
String fakeJwt(Map<String, Object?> claims) {
  String part(Object value) => base64Url.encode(utf8.encode(jsonEncode(value))).replaceAll('=', '');
  return '${part({'alg': 'none'})}.${part(claims)}.c2ln';
}

/// A stand-in sign-in server. It records each token request.
class FakeIdentityServer {
  final requests = <Map<String, String>>[];
  var refreshAllowed = true;
  var issued = 0;
  String? nonce;

  late final http.Client client = MockClient((request) async {
    final path = request.url.path;
    if (path.endsWith('/.well-known/openid-configuration')) {
      return http.Response(
        jsonEncode({
          'issuer': testConfig.issuer,
          'authorization_endpoint': '${testConfig.issuer}/protocol/openid-connect/auth',
          'token_endpoint': '${testConfig.issuer}/protocol/openid-connect/token',
          'end_session_endpoint': '${testConfig.issuer}/protocol/openid-connect/logout',
        }),
        200,
      );
    }
    if (path.endsWith('/token')) {
      final form = Uri.splitQueryString(request.body);
      requests.add(form);
      if (form['grant_type'] == 'refresh_token' && !refreshAllowed) {
        return http.Response(jsonEncode({'error': 'invalid_grant'}), 400);
      }
      issued++;
      return http.Response(
        jsonEncode({
          'access_token': fakeJwt({'preferred_username': 'beat.guard', 'n': issued}),
          'expires_in': 300,
          'refresh_token': 'refresh-$issued',
          if (form['grant_type'] == 'authorization_code')
            'id_token': fakeJwt({'preferred_username': 'beat.guard', 'nonce': nonce}),
        }),
        200,
      );
    }
    if (path.endsWith('/logout')) return http.Response('', 204);
    return http.Response('not found', 404);
  });
}

/// Returns to the app at once, as if the user signed in.
class FakeBrowser implements AuthBrowser {
  FakeBrowser(this.server, {this.cancel = false});
  final FakeIdentityServer server;
  final bool cancel;
  Uri? opened;

  @override
  Future<String> authenticate(Uri url, String callbackScheme) async {
    opened = url;
    if (cancel) throw const SignInException('Sign-in was cancelled.', cancelled: true);
    server.nonce = url.queryParameters['nonce'];
    return '$callbackScheme:/callback?state=${url.queryParameters['state']}&code=abc123';
  }
}

Alert sampleAlert({String id = '41', String status = 'new', String label = 'New', bool overdue = false}) =>
    Alert.fromJson({
      'id': id,
      'detection_id': '9$id',
      'queue': 'unit',
      'status': status,
      'step_label': label,
      'step_time': '2026-11-25T09:00:00Z',
      'overdue': overdue,
      'unit_path': 'ds.c1.d1.r1.b01',
      'observed_time': '2026-11-25T08:00:00+00:00',
      'observed_at': 1795680000,
      'latitude': 16.347,
      'longitude': 86.2264,
      'forest_class': 'inside',
      'source': 'demo',
      'product': 'VIIRS_SNPP_NRT',
      'confidence': 'n',
      'frp_mw': 7.4,
      'place_hierarchy': 'territorial',
      'places': {
        'beat': {'code': 'B01', 'name': 'Beat 01'},
        'range': {'code': 'R1', 'name': 'Range 1'},
        'division': {'code': 'D1', 'name': 'Hill Division'},
      },
    });

final sampleMap = WorkspaceMap.fromJson({
  'version': 'v1',
  'boundary': [86.19, 16.29, 86.69, 16.55],
  'levels': [
    for (final (depth, slug, label) in [
      (0, 'state', 'State'),
      (1, 'circle', 'Circle'),
      (2, 'division', 'Division'),
      (3, 'range', 'Range'),
      (4, 'beat', 'Beat'),
    ])
      {
        'hierarchy': 'territorial',
        'depth': depth,
        'slug': slug,
        'labels': {'en': label},
      },
  ],
});

final guardProfile = Profile.fromJson({
  'subject': 'd0000000-0000-4000-8000-0000000000a3',
  'username': 'beat.guard',
  'roleAssignments': [
    {
      'workspace': {'slug': 'demo', 'name': 'Demo State'},
      'hierarchy': 'territorial',
      'role': 'field_staff',
      'unit': {
        'code': 'B01',
        'name': 'Beat 01',
        'levelSlug': 'beat',
        'path': 'ds.c1.d1.r1.b01',
        'bounds': [86.195, 16.302, 86.262, 16.39],
      },
      'beatCount': 1,
    },
  ],
});

/// An in-memory repository with one workflow: New, then Acknowledged.
class FakeRepository implements AlertRepository {
  FakeRepository({Profile? profile, List<Alert>? alerts})
    : profileValue = profile ?? guardProfile,
      items = alerts ?? [sampleAlert(), sampleAlert(id: '42', status: 'acknowledged', label: 'Acknowledged')];

  final Profile profileValue;
  final List<Alert> items;
  final recorded = <StepRequest>[];

  @override
  Future<Profile> profile() async => profileValue;

  @override
  Future<WorkspaceMap> workspace(String slug) async => sampleMap;

  @override
  Future<AlertPage> alerts(String slug, {AlertQueue? queue, String? cursor}) async =>
      AlertPage(items: List.of(items), counts: {'unit': items.length}, canReview: false);

  @override
  Future<Alert?> alertForDetection(String slug, String detectionId) async =>
      items.where((a) => a.detectionId == detectionId).firstOrNull;

  @override
  Future<Alert> alert(String slug, String id) async => items.firstWhere((a) => a.id == id);

  @override
  Future<AlertHistory> history(String slug, String id) async {
    final alert = items.firstWhere((a) => a.id == id);
    return AlertHistory(
      entries: [
        HistoryEntry(step: 'new', label: 'New', at: DateTime.utc(2026, 11, 25, 8)),
        if (alert.status == 'acknowledged')
          HistoryEntry(
            step: 'acknowledged',
            label: 'Acknowledged',
            at: DateTime.utc(2026, 11, 25, 9),
            actor: 'Demo Beat Guard',
          ),
      ],
      moves: alert.status == 'new'
          ? const [
              WorkflowMove(
                step: 'acknowledged',
                label: 'Acknowledged',
                locationRequired: false,
                canOverride: false,
                terminal: false,
              ),
            ]
          : const [
              WorkflowMove(
                step: 'team_sent',
                label: 'Team sent',
                locationRequired: false,
                canOverride: false,
                terminal: false,
              ),
            ],
    );
  }

  @override
  Future<StepReceipt> recordStep(String slug, String alertId, StepRequest request) async {
    recorded.add(request);
    final index = items.indexWhere((a) => a.id == alertId);
    items[index] = sampleAlert(id: alertId, status: request.step, label: 'Acknowledged');
    return StepReceipt(id: '7', step: request.step, at: DateTime.utc(2026, 11, 25, 10));
  }

  @override
  String referenceTiles(String slug) =>
      'https://api.example.test/api/v1/workspaces/$slug/tiles/reference/{z}/{x}/{y}.mvt';

  @override
  String detectionTiles(String slug) =>
      'https://api.example.test/api/v1/workspaces/$slug/tiles/detections/{z}/{x}/{y}.mvt?hours=168';
}
