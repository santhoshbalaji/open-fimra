// Integration tests of the field app's sign-in and API client against a
// running stack. Run them with `make mobile-test-integration`, which starts
// a core API on a fresh copy of the demo data and sets FIMRA_TEST_API and
// FIMRA_TEST_ISSUER. Without those variables the tests are skipped.
//
// The beat guard signs in through the real open-fimra-mobile client with
// PKCE. A scripted browser fills in the sign-in form, as a person would in
// the phone's browser tab. Other demo users get tokens from the
// development-only password client, which only these tests use.
import 'dart:convert';
import 'dart:io';
import 'dart:math';
import 'dart:typed_data';

import 'package:fimra_field/src/api/api_client.dart';
import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/auth/pkce.dart';
import 'package:fimra_field/src/auth/session.dart';
import 'package:fimra_field/src/auth/tokens.dart';
import 'package:fimra_field/src/config.dart';
import 'package:fimra_field/src/data/alert_repository.dart';
import 'package:fimra_field/src/data/field_store.dart';
import 'package:fimra_field/src/data/offline_repository.dart';
import 'package:fimra_field/src/device/device_manager.dart';
import 'package:fimra_field/src/device/device_store.dart';
import 'package:fimra_field/src/device/local_data.dart';
import 'package:fimra_field/src/device/push.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

final api = Platform.environment['FIMRA_TEST_API'];
final issuer = Platform.environment['FIMRA_TEST_ISSUER'];
const beatPath = 'ds.c1.d1.r1.b01';

void main() {
  final skip = api == null || issuer == null ? 'Set FIMRA_TEST_API and FIMRA_TEST_ISSUER' : null;
  final config = AppConfig(
    apiBaseUrl: api ?? '',
    issuer: issuer ?? '',
    clientId: 'open-fimra-mobile',
    redirectUri: AppConfig.mobileRedirectUri,
  );
  sqfliteFfiInit();
  final client = http.Client();
  tearDownAll(client.close);

  OnlineAlertRepository repositoryFor(String token) => OnlineAlertRepository(
    FimraApi(baseUrl: config.apiBaseUrl, token: ({bool forceRefresh = false}) async => token, httpClient: client),
  );

  group('sign-in', skip: skip, () {
    test('the beat guard signs in with PKCE through the field app client', () async {
      final store = MemoryTokenStore();
      final session = Session(
        oidc: OidcClient(config: config, httpClient: client),
        store: store,
        browser: KeycloakFormBrowser('beat.guard', 'demo'),
      );
      await session.restore();
      expect(session.status, SessionStatus.signedOut);
      await session.signIn();
      expect(session.message, isNull);
      expect(session.status, SessionStatus.signedIn);
      expect(session.username, 'beat.guard');
      final claims = TokenSet.unverifiedClaims(await session.accessToken());
      expect(claims['azp'], 'open-fimra-mobile');
      expect(claims['aud'], anyOf('open-fimra-api', contains('open-fimra-api')));
      expect(store.tokens?.refreshToken, isNotNull);

      // A refresh gives a new access token, and the API accepts it.
      final refreshed = await session.accessToken(forceRefresh: true);
      final profile = await repositoryFor(refreshed).profile();
      expect(profile.username, 'beat.guard');

      // Signing out ends the session on the sign-in server too.
      final old = store.tokens!;
      await session.signOut();
      expect(session.status, SessionStatus.signedOut);
      expect(await OidcClient(config: config, httpClient: client).refresh(old), isNull);
    });

    test('the field app client refuses password sign-in', () async {
      final endpoints = await OidcClient(config: config, httpClient: client).endpoints();
      final response = await client.post(
        endpoints.token,
        body: {
          'grant_type': 'password',
          'client_id': 'open-fimra-mobile',
          'username': 'beat.guard',
          'password': 'demo',
        },
      );
      expect(response.statusCode, anyOf(400, 401));
      expect(jsonDecode(response.body)['error'], 'unauthorized_client');
    });

    test('the field app client requires a proof key', () async {
      final oidc = OidcClient(config: config, httpClient: client);
      final request = await oidc.authorizationRequest();
      final params = Map.of(request.url.queryParameters)
        ..remove('code_challenge')
        ..remove('code_challenge_method');
      final callback = await KeycloakFormBrowser(
        'beat.guard',
        'demo',
      ).authenticate(request.url.replace(queryParameters: params), config.callbackScheme);
      expect(() => OidcClient.codeFromCallback(callback, request), throwsA(isA<SignInException>()));
      expect(Uri.parse(callback).queryParameters['error'], 'invalid_request');
    });

    test('a code cannot be exchanged without its verifier', () async {
      final oidc = OidcClient(config: config, httpClient: client);
      final request = await oidc.authorizationRequest();
      final callback = await KeycloakFormBrowser('beat.guard', 'demo').authenticate(request.url, config.callbackScheme);
      final code = OidcClient.codeFromCallback(callback, request);
      await expectLater(oidc.exchangeCode(code, Pkce.generate()), throwsA(isA<SignInException>()));
    });
  });

  group('alerts', skip: skip, () {
    late OnlineAlertRepository guard, officer, admin, nobody;
    setUpAll(() async {
      guard = repositoryFor(await pkceToken(config, client, 'beat.guard'));
      officer = repositoryFor(await passwordToken(client, 'division.officer'));
      admin = repositoryFor(await passwordToken(client, 'state.admin'));
      nobody = repositoryFor(await passwordToken(client, 'no.role'));
    });

    test('each user gets their own workspaces and the extent of their units', () async {
      final profile = await guard.profile();
      expect(profile.workspaces.map((w) => w.slug), ['demo']);
      final unit = profile.roleAssignments.single.unit;
      expect(unit.path, beatPath);
      final bounds = unit.bounds!;
      // The two synthetic detections in B01 are inside the beat's extent.
      for (final (lat, lon) in [(16.347, 86.2264), (16.349, 86.229)]) {
        expect(lon, inInclusiveRange(bounds.west, bounds.east));
        expect(lat, inInclusiveRange(bounds.south, bounds.north));
      }
      expect((await nobody.profile()).roleAssignments, isEmpty);
    });

    test('the beat guard sees only alerts in their own beat', () async {
      final page = await guard.alerts('demo');
      expect(page.items, hasLength(2));
      expect(page.canReview, isFalse);
      for (final alert in page.items) {
        expect(alert.unitPath, beatPath);
        expect(alert.queue, 'unit');
        expect(alert.places['beat']?.code, 'B01');
      }
      final officerPage = await officer.alerts('demo');
      expect(officerPage.items.map((a) => a.unitPath).toSet(), containsAll([beatPath, 'ds.c1.d1.r2.b05']));
      final everything = await admin.alerts('demo');
      expect(everything.items.length, greaterThan(officerPage.items.length));
    });

    test("the beat guard cannot open another beat's alert", () async {
      final other = (await admin.alerts(
        'demo',
        queue: AlertQueue.unit,
      )).items.firstWhere((alert) => alert.unitPath != beatPath);
      await expectLater(
        guard.alert('demo', other.id),
        throwsA(isA<ApiException>().having((e) => e.status, 'status', 404)),
      );
      await expectLater(nobody.alerts('demo'), throwsA(isA<ApiException>().having((e) => e.status, 'status', 403)));
    });

    test('the beat guard acknowledges an alert, and a retry records it once', () async {
      final alert = (await guard.alerts('demo')).items.firstWhere((a) => a.status == 'new');
      final history = await guard.history('demo', alert.id);
      final acknowledge = history.moves.singleWhere((m) => m.step == 'acknowledged');
      expect(acknowledge.locationRequired, isFalse);
      // Field staff cannot end the workflow with another outcome.
      expect(history.moves.where((m) => m.step == 'false_alarm'), isEmpty);

      final request = StepRequest(step: 'acknowledged', clientId: randomToken(18), note: 'On my way.');
      final receipt = await guard.recordStep('demo', alert.id, request);
      expect(receipt.step, 'acknowledged');
      final retry = await guard.recordStep('demo', alert.id, request);
      expect(retry.id, receipt.id);

      final after = await guard.alert('demo', alert.id);
      expect(after.status, 'acknowledged');
      expect(after.stepLabel, isNotEmpty);
      final entries = (await guard.history('demo', alert.id)).entries;
      expect(entries.map((e) => e.step), ['new', 'acknowledged']);
      expect(entries.last.actor, 'Demo Beat Guard');
      expect(entries.last.note, 'On my way.');

      await expectLater(
        guard.recordStep('demo', alert.id, StepRequest(step: 'acknowledged', clientId: randomToken(18))),
        throwsA(isA<ApiException>().having((e) => e.status, 'status', 409)),
      );
    });

    test('a field step needs a location near the fire', () async {
      final alert = (await guard.alerts('demo')).items.firstWhere((a) => a.status == 'acknowledged');
      final moves = (await guard.history('demo', alert.id)).moves;
      expect(moves.map((m) => m.step), contains('team_sent'));
      await guard.recordStep('demo', alert.id, StepRequest(step: 'team_sent', clientId: randomToken(18)));
      final reached = (await guard.history('demo', alert.id)).moves.singleWhere((m) => m.step == 'reached_site');
      expect(reached.locationRequired, isTrue);
      await expectLater(
        guard.recordStep(
          'demo',
          alert.id,
          StepRequest(step: 'reached_site', clientId: randomToken(18), latitude: 16.2, longitude: 86.5),
        ),
        throwsA(isA<ApiException>().having((e) => e.status, 'status', 422)),
      );
      final receipt = await guard.recordStep(
        'demo',
        alert.id,
        StepRequest(
          step: 'reached_site',
          clientId: randomToken(18),
          latitude: alert.latitude,
          longitude: alert.longitude,
        ),
      );
      expect(receipt.step, 'reached_site');
    });

    test('a photo draft survives a restart and follows the queued field steps once', () async {
      final adminToken = await passwordToken(client, 'state.admin');
      const ws = '/api/v1/workspaces/demo';
      const form = 'offline-inspection';
      const definition = {
        'formatVersion': 1,
        'id': form,
        'version': 1,
        'title': {'en': 'Offline inspection'},
        'sections': [
          {
            'key': 'site',
            'title': {'en': 'Site'},
            'items': [
              {
                'key': 'note',
                'type': 'text',
                'label': {'en': 'Site note'},
                'required': true,
              },
              {
                'key': 'photos',
                'type': 'photo',
                'label': {'en': 'Site photo'},
                'required': true,
              },
            ],
          },
        ],
      };
      Future<void> setup(String method, String path, int status, [Json? data]) async {
        final req = http.Request(method, Uri.parse('$api$ws$path'))
          ..headers.addAll({'Authorization': 'Bearer $adminToken', 'Content-Type': 'application/json'});
        if (data != null) req.body = jsonEncode(data);
        final response = await http.Response.fromStream(await client.send(req));
        expect(response.statusCode, status, reason: response.body);
      }

      await setup('POST', '/forms', 201, definition);
      await setup('POST', '/forms/$form/versions/1/publish', 200);
      await setup('PUT', '/forms/$form/assignments/first_report', 200, {'version': 1});

      final directory = await Directory.systemTemp.createTemp('fimra-offline');
      FieldStore openStore() => FieldStore(factory: databaseFactoryFfi, path: '${directory.path}/field.sqlite');
      var store = openStore();
      final token = await pkceToken(config, client, 'beat.guard');
      var connected = true;
      var loseReachedResponse = true;
      final transport = MockClient((request) async {
        if (!connected) throw http.ClientException('No signal');
        final withNetwork = http.Request(request.method, request.url)
          ..headers.addAll(request.headers)
          ..bodyBytes = request.bodyBytes;
        final response = await http.Response.fromStream(await client.send(withNetwork));
        if (loseReachedResponse &&
            request.url.path.endsWith('/steps') &&
            request.body.contains('"step":"reached_site"') &&
            response.statusCode == 200) {
          loseReachedResponse = false;
          throw http.ClientException('Signal lost after arrival was recorded');
        }
        return response;
      });
      final fieldApi = FimraApi(
        baseUrl: config.apiBaseUrl,
        token: ({forceRefresh = false}) async => token,
        httpClient: transport,
      );
      var repository = OfflineAlertRepository(fieldApi, store, () => 'beat.guard');
      addTearDown(() async {
        repository.dispose();
        await store.wipe();
        await directory.delete(recursive: true);
        transport.close();
      });
      final alert = (await repository.alerts('demo')).items.singleWhere((alert) => alert.status == 'new');
      repository.activate('demo');
      await repository.sync(refresh: true);
      expect(repository.lastSynced, isNotNull);

      connected = false;
      await repository.sync();
      expect(repository.offline, isTrue);
      final history = await repository.history('demo', alert.id);
      final assignment = objects((await repository.reportsForAlert('demo', alert.id))['forms']).single;
      final id = newDeviceId();
      final photoId = newDeviceId();
      final photo = ReportFile(
        photoId,
        'site.png',
        'image/png',
        base64Decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jzAAAAABJRU5ErkJggg=='),
      );
      await repository.saveDraft(
        'demo',
        alert.id,
        'Offline inspection',
        {
          'id': id,
          'form': form,
          'step': 'first_report',
          'assignment': assignment,
          'values': {
            'photos': [photo.id],
          },
        },
        [photo],
        store.generation,
      );

      Future<void> take(String from, String to) async {
        final move = history.offlineMoves[from]!.singleWhere((move) => move.step == to);
        await repository.queueStep(
          'demo',
          alert.id,
          move,
          from,
          StepRequest(
            step: to,
            clientId: newDeviceId(),
            clientTime: DateTime.now(),
            note: to == 'reached_site' ? 'At the site' : '',
            latitude: move.locationRequired ? alert.latitude : null,
            longitude: move.locationRequired ? alert.longitude : null,
          ),
        );
      }

      await take('new', 'acknowledged');
      await take('acknowledged', 'team_sent');
      await take('team_sent', 'reached_site');
      await take('reached_site', 'controlled');
      await take('controlled', 'extinguished');
      expect((await guard.alert('demo', alert.id)).status, 'new');

      repository.dispose();
      await store.close();
      store = openStore();
      repository = OfflineAlertRepository(fieldApi, store, () => 'beat.guard');
      final rows = await repository.reportStatus(alert: alert.id);
      final draft = rows.singleWhere((row) => row['kind'] == 'draft');
      expect(draft['id'], id);
      expect(object(jsonDecode(draft['body'] as String)['values'])['photos'], [photoId]);
      final files = await store.draftFiles(id);
      expect(files.single.bytes, photo.bytes);
      repository.activate('demo');
      await repository.sync(refresh: true);
      expect(repository.offline, isTrue);

      await repository.queueReport(
        'demo',
        alert.id,
        'Offline inspection',
        {
          'id': id,
          'form': form,
          'step': 'first_report',
          'version': 1,
          'deviceTime': DateTime.now().toUtc().toIso8601String(),
          'values': {
            'photos': [photoId],
            'note': 'The area is secure.',
          },
        },
        files,
        step: StepRequest(step: 'first_report', clientId: newDeviceId(), clientTime: DateTime.now()),
        fromStep: 'extinguished',
        generation: store.generation,
      );
      expect((await repository.reportStatus(alert: alert.id)).map((row) => row['kind']), [
        'step',
        'step',
        'step',
        'step',
        'step',
        'report',
      ]);

      connected = true;
      await repository.sync(retry: true, refresh: true);
      expect(loseReachedResponse, isFalse);
      expect((await guard.alert('demo', alert.id)).status, 'reached_site');
      await repository.sync(retry: true, refresh: true);
      await repository.sync(retry: true, refresh: true);
      final confirmed = await guard.history('demo', alert.id);
      expect(confirmed.entries.map((entry) => entry.step), [
        'new',
        'acknowledged',
        'team_sent',
        'reached_site',
        'controlled',
        'extinguished',
        'first_report',
      ]);
      expect(confirmed.entries.singleWhere((entry) => entry.step == 'reached_site').note, 'At the site');
      final received = (await guard.api.submissions('demo', alert.id)).single;
      expect(received['id'], id);
      expect(object(received['values'])['photos'], [photoId]);
      expect((await repository.reportStatus(alert: alert.id)).every((row) => row['received_at'] != null), isTrue);
    });

    test('map tiles follow the same grants', () async {
      final guardToken = await pkceToken(config, client, 'beat.guard');
      final nobodyToken = await passwordToken(client, 'no.role');
      // The zoom 12 tile that holds the first B01 detection.
      const z = 12, lat = 16.347, lon = 86.2264;
      final x = ((lon + 180) / 360 * (1 << z)).floor();
      final rad = lat * pi / 180;
      final y = ((1 - log(tan(rad) + 1 / cos(rad)) / pi) / 2 * (1 << z)).floor();
      String fill(String template) =>
          template.replaceFirst('{z}', '$z').replaceFirst('{x}', '$x').replaceFirst('{y}', '$y');
      final detections = Uri.parse(fill(guard.detectionTiles('demo')));
      final reference = Uri.parse(fill(guard.referenceTiles('demo')));
      for (final url in [detections, reference]) {
        final ok = await client.get(url, headers: {'Authorization': 'Bearer $guardToken'});
        expect(ok.statusCode, 200, reason: '$url');
        expect(ok.headers['content-type'], 'application/vnd.mapbox-vector-tile');
        expect(ok.bodyBytes, isNotEmpty);
        final refused = await client.get(url, headers: {'Authorization': 'Bearer $nobodyToken'});
        expect(refused.statusCode, 403, reason: '$url');
        expect((await client.get(url)).statusCode, 401, reason: '$url');
      }
    });
  });

  // Runs last: wiping the phone also refuses its sign-in session.
  group('phone registration and remote wipe', skip: skip, () {
    test('the beat guard registers a phone, an officer wipes it and its next call deletes its data', () async {
      final tokens = MemoryTokenStore();
      final ids = MemoryDeviceStore();
      final push = _NoPushApp();
      final deleted = <String>[];
      final field = FieldStore(factory: databaseFactoryFfi, path: inMemoryDatabasePath);
      await field.saveDraft(
        'guard',
        'demo',
        '1',
        'First report',
        {'id': 'draft', 'form': 'demo', 'step': 'first_report'},
        [],
        generation: field.generation,
      );
      final session = Session(
        oidc: OidcClient(config: config, httpClient: client),
        store: tokens,
        browser: KeycloakFormBrowser('beat.guard', 'demo'),
      );
      late final DeviceManager devices;
      final api = FimraApi(
        baseUrl: config.apiBaseUrl,
        token: ({bool forceRefresh = false}) => session.accessToken(forceRefresh: forceRefresh),
        httpClient: client,
        deviceId: () => devices.deviceId,
        onDeviceAction: (action, message) => devices.handleAction(action, message),
      );
      Future<void> Function() record(String name, Future<void> Function() delete) => () async {
        deleted.add(name);
        await delete();
      };
      devices = DeviceManager(
        api: api,
        session: session,
        store: ids,
        push: push,
        notifier: const NoNotifier(),
        platform: 'android',
        localData: fieldDataOnPhone(
          notifications: record('notifications', () async {}),
          pushRegistration: record('push', push.stop),
          offlineMaps: record('maps', () async {}),
          offlineReports: record('field data', field.wipe),
          deviceId: record('device', ids.clear),
          signInTokens: record('tokens', tokens.clear),
        ),
      );
      await session.restore();
      await devices.start();
      await session.signIn();
      await pumpEventQueue();
      await devices.register();
      final id = ids.id;
      expect(id, isNotNull);
      expect(devices.device?.status, DeviceStatus.active);
      // The test phone has no push app, so it is registered without push.
      expect(devices.pushStatus, PushStatus.noPushApp);
      final mine = await api.devices();
      expect(mine.where((d) => d.current).single.id, id);
      expect(mine.single.appVersion, fieldAppVersion);

      Future<http.Response> asUser(String user, String method, String path) async {
        final request = http.Request(method, Uri.parse('${config.apiBaseUrl}$path'))
          ..headers['Authorization'] = 'Bearer ${await passwordToken(client, user)}';
        return http.Response.fromStream(await client.send(request));
      }

      final listed = await asUser('division.officer', 'GET', '/api/v1/workspaces/demo/devices');
      expect(listed.statusCode, 200, reason: listed.body);
      expect(listed.body, contains(id));
      expect((await asUser('no.role', 'GET', '/api/v1/workspaces/demo/devices')).statusCode, 403);
      expect((await asUser('no.role', 'POST', '/api/v1/workspaces/demo/devices/$id/wipe')).statusCode, 404);
      final wipe = await asUser('division.officer', 'POST', '/api/v1/workspaces/demo/devices/$id/wipe');
      expect(wipe.statusCode, 200, reason: wipe.body);
      expect(wipe.body, contains('wipe_pending'));

      // Any call from the phone now starts the wipe.
      await expectLater(
        OnlineAlertRepository(api).alerts('demo'),
        throwsA(isA<DeviceActionException>().having((e) => e.action, 'action', DeviceAction.wipe)),
      );
      expect(deleted, ['notifications', 'push', 'maps', 'field data', 'device', 'tokens']);
      expect(await field.reports('guard'), isEmpty);
      await field.wipe();
      expect(tokens.tokens, isNull);
      expect(ids.id, isNull);
      expect(session.status, SessionStatus.signedOut);
      expect(session.message, contains('delete its field data'));

      final after = await asUser('division.officer', 'GET', '/api/v1/workspaces/demo/devices');
      final row = (jsonDecode(after.body)['items'] as List).cast<Map<String, dynamic>>().firstWhere(
        (d) => d['id'] == id,
      );
      expect(row['status'], 'wiped');
      expect(row['owner']['username'], 'beat.guard');
    });
  });
}

/// Signs in through the field app client with PKCE and returns the token.
Future<String> pkceToken(AppConfig config, http.Client client, String username) async {
  final tokens = await OidcClient(config: config, httpClient: client).signIn(KeycloakFormBrowser(username, 'demo'));
  return tokens.accessToken;
}

/// A token from the development-only password client, for the other users.
Future<String> passwordToken(http.Client client, String username) async {
  final response = await client.post(
    Uri.parse('$issuer/protocol/openid-connect/token'),
    body: {'grant_type': 'password', 'client_id': 'open-fimra-dev-cli', 'username': username, 'password': 'demo'},
  );
  expect(response.statusCode, 200, reason: response.body);
  return jsonDecode(response.body)['access_token'] as String;
}

/// Plays the part of the phone's browser tab: opens the sign-in page, fills
/// in the Keycloak form and returns the address Keycloak sends it back to.
class KeycloakFormBrowser implements AuthBrowser {
  KeycloakFormBrowser(this.username, this.password);
  final String username;
  final String password;
  final _cookies = <String, String>{};

  @override
  Future<String> authenticate(Uri url, String callbackScheme) async {
    final http = HttpClient();
    try {
      var response = await _send(http, 'GET', url);
      var location = response.headers.value('location');
      // Keycloak answers some errors straight away with a redirect.
      if (location != null && location.startsWith('$callbackScheme:')) return location;
      final page = await response.transform(utf8.decoder).join();
      final action = RegExp(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"').firstMatch(page)?.group(1);
      if (action == null) throw StateError('No sign-in form on the page: ${response.statusCode}');
      final body = Uri(queryParameters: {'username': username, 'password': password, 'credentialId': ''}).query;
      response = await _send(http, 'POST', Uri.parse(action.replaceAll('&amp;', '&')), body: body);
      location = response.headers.value('location');
      await response.drain<void>();
      if (location == null || !location.startsWith('$callbackScheme:')) {
        throw StateError('Sign-in did not return to the app: ${response.statusCode} $location');
      }
      return location;
    } finally {
      http.close(force: true);
    }
  }

  Future<HttpClientResponse> _send(HttpClient http, String method, Uri url, {String? body}) async {
    final request = await http.openUrl(method, url);
    request.followRedirects = false;
    if (_cookies.isNotEmpty) {
      request.headers.set('Cookie', _cookies.entries.map((c) => '${c.key}=${c.value}').join('; '));
    }
    if (body != null) {
      request.headers.contentType = ContentType('application', 'x-www-form-urlencoded', charset: 'utf-8');
      request.write(body);
    }
    final response = await request.close();
    for (final cookie in response.cookies) {
      _cookies[cookie.name] = cookie.value;
    }
    return response;
  }
}

/// A phone with no push app installed.
class _NoPushApp implements PushService {
  @override
  bool get supported => true;

  @override
  Future<bool> start({
    required void Function(PushAddress? address) onAddress,
    required void Function(Uint8List message) onMessage,
  }) async => false;

  @override
  Future<void> stop() async {}
}
