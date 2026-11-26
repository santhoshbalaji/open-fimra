import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';
import 'package:fimra_field/src/api/api_client.dart';
import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/data/field_store.dart';
import 'package:fimra_field/src/data/offline_repository.dart';
import 'package:fimra_field/src/data/outbox.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

void main() {
  sqfliteFfiInit();
  late Directory directory;
  late FieldStore store;
  setUp(() async {
    directory = await Directory.systemTemp.createTemp('fimra-field-test');
    store = FieldStore(factory: databaseFactoryFfi, path: '${directory.path}/field.sqlite');
  });
  tearDown(() async {
    await store.wipe();
    await directory.delete(recursive: true);
  });

  test('an account sees its own saved workspace and wipe deletes queued bytes', () async {
    final generation = store.generation;
    final data = 'x' * (3 << 20);
    await store.write('guard', 'demo', {
      'reference': {'data': data},
    }, generation);
    expect((await store.read('guard', 'demo', 'reference'))?['data'], data);
    expect(await store.read('other', 'demo', 'reference'), isNull);
    final bytes = Uint8List(3 << 20)..[500000] = 49;
    await store.enqueue(
      'guard',
      'demo',
      '42',
      'First report',
      {'id': 'report'},
      [ReportFile('photo', 'site.jpg', 'image/jpeg', bytes)],
      generation: generation,
    );
    expect(await store.reports('other'), isEmpty);
    expect((await store.file('report', 'photo'))?.bytes[500000], 49);
    await store.wipe();
    expect(await File('${directory.path}/field.sqlite').exists(), isFalse);
    await store.write('guard', 'demo', {
      'reference': {'old': true},
    }, generation);
    expect(await store.read('guard', 'demo', 'reference'), isNull);
    expect(await store.reports('guard'), isEmpty);
    expect(await store.file('report', 'photo'), isNull);
  });

  test('a saved alert, timeline, form and map open without a signal', () async {
    var connected = true;
    final api = FimraApi(
      baseUrl: 'http://example.invalid',
      token: ({forceRefresh = false}) async => 'token',
      httpClient: MockClient((request) async {
        if (!connected) throw http.ClientException('Signal lost');
        final path = request.url.path;
        return http.Response(
          jsonEncode(switch (path) {
            '/api/v1/me' => {'subject': 'guard', 'username': 'guard'},
            '/api/v1/workspaces/demo' => {'version': '1'},
            '/api/v1/workspaces/demo/offline-reference' => {'type': 'FeatureCollection', 'features': <Json>[]},
            '/api/v1/workspaces/demo/alerts' => {
              'items': [
                {'id': '42', 'queue': 'unit'},
              ],
            },
            '/api/v1/workspaces/demo/alerts/42' => {'id': '42'},
            '/api/v1/workspaces/demo/alerts/42/history' => {
              'entries': [
                {'step': 'first_report'},
              ],
            },
            '/api/v1/workspaces/demo/alerts/42/forms' => {
              'forms': [
                {'form': 'first-report', 'version': 1},
              ],
            },
            '/api/v1/workspaces/demo/alerts/42/submissions' => <Json>[],
            '/api/v1/workspaces/demo/forms/first-report/versions/1' => {
              'definition': {'id': 'first-report'},
            },
            _ => throw StateError('Unexpected request $path'),
          }),
          200,
        );
      }),
    );
    final repository = OfflineAlertRepository(api, store, () => 'guard');
    addTearDown(repository.dispose);
    expect((await repository.profile()).subject, 'guard');
    repository.activate('demo');
    await repository.sync(refresh: true);
    expect(repository.lastSynced, isNotNull);
    connected = false;
    expect((await repository.profile()).username, 'guard');
    expect((await repository.alerts('demo')).items.single.id, '42');
    expect((await repository.alert('demo', '42')).id, '42');
    expect((await repository.history('demo', '42')).entries.single.step, 'first_report');
    final forms = objects((await repository.reportsForAlert('demo', '42'))['forms']);
    expect(object(forms.single['definition'])['id'], 'first-report');
    expect((await repository.savedReference('demo'))?['type'], 'FeatureCollection');
  });

  test('a refused step retains its input and holds later steps for the same alert', () async {
    final posted = <Json>[];
    final api = FimraApi(
      baseUrl: 'http://example.invalid',
      token: ({forceRefresh = false}) async => 'token',
      httpClient: MockClient((request) async {
        final body = jsonDecode(request.body) as Json;
        posted.add(body);
        if (body['clientId'] == 'refused') {
          return http.Response('{"error":"Record a location near the detection."}', 422);
        }
        return http.Response('{"id":"1","step":"acknowledged","at":"2026-01-01T12:00:00Z"}', 200);
      }),
    );
    final generation = store.generation;
    final arrival = {
      'step': 'reached_site',
      'clientId': 'refused',
      'note': 'At the gate',
      'clientTime': '2026-01-01T12:00:00.000Z',
      'location': {'latitude': 16.347, 'longitude': 86.2264},
    };
    await store.enqueueStep(
      'guard',
      'demo',
      '42',
      'Reached site',
      arrival,
      fromStep: 'team_sent',
      generation: generation,
    );
    await store.enqueueStep(
      'guard',
      'demo',
      '42',
      'Controlled',
      {'step': 'controlled', 'clientId': 'later'},
      fromStep: 'reached_site',
      generation: generation,
    );
    await store.enqueueStep(
      'guard',
      'demo',
      '43',
      'Acknowledged',
      {'step': 'acknowledged', 'clientId': 'other'},
      fromStep: 'new',
      generation: generation,
    );
    final outbox = ReportOutbox(api, store);
    await outbox.send('guard');
    await outbox.send('guard');
    expect(posted.map((body) => body['clientId']), ['refused', 'other']);
    expect(posted.first, arrival);
    final failed = (await store.reports('guard', alert: '42')).first;
    expect(failed['error'], 'Record a location near the detection.');
    expect(failed['retry_at'], isNull);
    expect(object(jsonDecode(failed['body'] as String)['location']), arrival['location']);
    expect(jsonDecode(failed['body'] as String)['note'], 'At the gate');
    expect((await store.reports('other')), isEmpty);
  });

  test('late response updates retry with the same times and send in order', () async {
    final posted = <Json>[];
    final api = FimraApi(
      baseUrl: 'http://example.invalid',
      token: ({forceRefresh = false}) async => 'token',
      httpClient: MockClient((request) async {
        final body = jsonDecode(request.body) as Json;
        posted.add(body);
        if (posted.length == 1) return http.Response('{}', 503);
        return http.Response(
          jsonEncode({
            'id': posted.length.toString(),
            'step': body['step'],
            'at': '2026-01-01T11:30:00Z',
            'syncedAt': '2026-01-01T12:00:00Z',
          }),
          200,
        );
      }),
    );
    final actual = DateTime.utc(2026, 1, 1, 10);
    final estimate = DateTime.utc(2026, 1, 1, 11);
    final requests = [
      (
        'reached_site',
        StepRequest(
          step: 'flash_update',
          clientId: 'one',
          actualAt: actual,
          response: {'status': 'Active', 'threats': 'Access track'},
        ),
      ),
      (
        'flash_update',
        StepRequest(
          step: 'response_update',
          clientId: 'two',
          estimatedAt: estimate,
          response: {'status': 'Crew in place'},
        ),
      ),
      ('flash_update', StepRequest(step: 'controlled', clientId: 'three', actualAt: actual)),
      ('controlled', StepRequest(step: 'extinguished', clientId: 'four', estimatedAt: estimate)),
    ];
    for (final (from, step) in requests) {
      await store.enqueueStep(
        'guard',
        'demo',
        '42',
        step.step,
        step.toJson(),
        fromStep: from,
        generation: store.generation,
      );
    }
    final outbox = ReportOutbox(api, store);
    await outbox.send('guard');
    await outbox.send('guard', retry: true);
    expect(posted, [requests.first.$2.toJson(), for (final (_, step) in requests) step.toJson()]);
    final saved = await store.reports('guard', alert: '42');
    expect(saved.every((row) => row['received_at'] == '2026-01-01T12:00:00.000Z'), isTrue);
    final estimates = saved.map((row) => (jsonDecode(row['body'] as String) as Json)['estimatedAt']);
    expect(estimates.whereType<String>(), [estimate.toIso8601String(), estimate.toIso8601String()]);
  });

  test('lost upload and report responses resume with three files and one report', () async {
    final storedFiles = <String, Json>{};
    final submissions = <String, String>{};
    var photoPuts = 0;
    var reportPosts = 0;
    var loseUpload = true;
    var loseSubmission = true;
    final api = FimraApi(
      baseUrl: 'http://example.invalid',
      token: ({forceRefresh = false}) async => 'token',
      httpClient: MockClient((request) async {
        final id = request.url.pathSegments.last;
        if (request.method == 'GET') {
          final saved = storedFiles[id];
          return http.Response(jsonEncode(saved ?? {}), saved == null ? 404 : 200);
        }
        if (request.method == 'PUT') {
          photoPuts++;
          expect(storedFiles.containsKey(id), isFalse);
          storedFiles[id] = {
            'sha256': sha256.convert(request.bodyBytes).toString(),
            'fileName': request.url.queryParameters['name'],
            'sizeBytes': request.bodyBytes.length,
            'contentType': request.headers['Content-Type'],
          };
          if (loseUpload) {
            loseUpload = false;
            throw http.ClientException('Signal lost after first photo');
          }
          return http.Response(jsonEncode(storedFiles[id]), 201);
        }
        expect(request.method, 'POST');
        reportPosts++;
        final report = jsonDecode(request.body) as Json;
        expect(storedFiles.keys, unorderedEquals(object(report['values'])['photos'] as List));
        final previous = submissions.putIfAbsent(report['id'] as String, () => request.body);
        expect(request.body, previous);
        if (loseSubmission) {
          loseSubmission = false;
          throw http.ClientException('Signal lost after report');
        }
        return http.Response('{"receivedAt":"2026-01-01T12:00:00Z"}', 200);
      }),
    );
    final files = [
      for (var n = 1; n <= 3; n++) ReportFile('photo-$n', 'site-$n.jpg', 'image/jpeg', Uint8List.fromList([n])),
    ];
    await store.enqueue(
      'guard',
      'demo',
      '42',
      'First report',
      {
        'id': 'report',
        'form': 'first-report',
        'step': 'first_report',
        'version': 1,
        'values': {'photos': files.map((file) => file.id).toList()},
      },
      files,
      generation: store.generation,
    );
    var time = DateTime.utc(2026, 1, 1);
    var outbox = ReportOutbox(api, store, clock: () => time);
    expect(await outbox.send('guard'), isEmpty);
    expect(storedFiles, hasLength(1));
    expect(submissions, isEmpty);
    expect((await store.reports('guard')).single['retry_at'], isNotNull);
    expect(await outbox.send('guard'), isEmpty);
    time = time.add(const Duration(minutes: 1));
    outbox = ReportOutbox(api, store, clock: () => time);
    expect(await outbox.send('guard'), isEmpty);
    expect(await store.pendingFiles('report'), isEmpty);
    expect(submissions, hasLength(1));
    time = time.add(const Duration(minutes: 1));
    expect(await outbox.send('guard'), {'demo'});
    expect(photoPuts, 3);
    expect(reportPosts, 2);
    expect(submissions, hasLength(1));
    expect(await store.reports('guard', pendingOnly: true), isEmpty);
  });
}
