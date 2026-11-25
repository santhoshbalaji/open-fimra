import 'dart:convert';
import 'dart:io';

import 'package:fimra_field/src/api/api_client.dart';
import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/auth/session.dart';
import 'package:fimra_field/src/auth/tokens.dart';
import 'package:fimra_field/src/config.dart';
import 'package:fimra_field/src/device/device_manager.dart';
import 'package:fimra_field/src/device/device_store.dart';
import 'package:fimra_field/src/device/local_data.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'support.dart';

/// A signed-in phone with fakes for the API, push app and notifications.
class Phone {
  Phone({bool hasPushApp = true, bool pushSupported = true})
    : push = FakePush(hasPushApp: hasPushApp, supported: pushSupported);

  final server = FakeIdentityServer();
  final tokens = MemoryTokenStore();
  final ids = MemoryDeviceStore();
  final api = FakeDeviceApi();
  final FakePush push;
  final notifier = FakeNotifier();
  final wiped = <String>[];
  late final session = Session(
    oidc: OidcClient(config: testConfig, httpClient: server.client),
    store: tokens,
    browser: FakeBrowser(server),
  );
  late final devices = DeviceManager(
    api: api,
    session: session,
    store: ids,
    push: push,
    notifier: notifier,
    platform: 'android',
    localData: fieldDataOnPhone(
      notifications: () async {
        wiped.add('notifications');
        await notifier.clear();
      },
      pushRegistration: () async {
        wiped.add('push');
        await push.stop();
      },
      offlineMaps: () async => wiped.add('maps'),
      deviceId: () async {
        wiped.add('device');
        await ids.clear();
      },
      signInTokens: () async {
        wiped.add('tokens');
        await tokens.clear();
      },
    ),
  );

  Future<void> signIn() async {
    await session.restore();
    await devices.start();
    await session.signIn();
    await pumpEventQueue();
  }
}

void main() {
  group('push messages', () {
    test('an alert message holds IDs and a generic text, and a repeat keeps its ID', () {
      final notice = PushNotice.parse(utf8.encode(jsonEncode(alertPush())))!;
      expect(notice.type, 'notification');
      expect((notice.workspace, notice.alert, notice.id), ('demo', '41', 12));
      expect(notice.title, 'New fire alert');
      expect(PushNotice.fromPayload(notice.toPayload())!.alert, '41');
      expect(PushNotice.parse(utf8.encode(jsonEncode(alertPush())))!.id, notice.id);
    });

    test('unknown or damaged messages are ignored', () {
      for (final body in [
        'not json',
        jsonEncode({'v': 2, 'type': 'notification'}),
        jsonEncode({'v': 1, 'type': 'wipe'}),
        jsonEncode({...alertPush(), 'notification': 'x'}),
        jsonEncode({...alertPush(), 'workspace': null}),
      ]) {
        expect(PushNotice.parse(utf8.encode(body)), isNull, reason: body);
      }
      expect(PushNotice.parse(utf8.encode(jsonEncode({'v': 1, 'type': 'device_check'})))!.type, 'device_check');
      // A long or empty text falls back to the generic one.
      final long = PushNotice.parse(utf8.encode(jsonEncode({...alertPush(), 'title': 'x' * 200, 'body': ''})))!;
      expect((long.title, long.body), ('New fire alert', 'Open the field app to see it.'));
    });
  });

  group('API client device calls', () {
    final seen = <http.Request>[];
    var refreshes = 0;
    final actions = <(DeviceAction, String)>[];
    late http.Response Function(http.Request) answer;
    final api = FimraApi(
      baseUrl: 'https://api.example.test',
      token: ({bool forceRefresh = false}) async {
        if (forceRefresh) refreshes++;
        return 'token';
      },
      httpClient: MockClient((request) async {
        seen.add(request);
        return answer(request);
      }),
      deviceId: () => '6f1c4a52-1b2c-4d3e-8f90-0123456789ab',
      onDeviceAction: (action, message) async => actions.add((action, message)),
    );
    setUp(() {
      seen.clear();
      actions.clear();
      refreshes = 0;
    });

    test('every call names the device, and registration sends the push address', () async {
      answer = (_) => http.Response(
        jsonEncode({
          'device': {'id': 'x', 'platform': 'android', 'push': true, 'status': 'active', 'current': true},
          'pushProviders': ['unifiedpush'],
        }),
        200,
      );
      final receipt = await api.registerDevice(
        'x',
        const DeviceRegistration(platform: 'android', appVersion: fieldAppVersion, push: testAddress),
      );
      final request = seen.single;
      expect(request.method, 'PUT');
      expect(request.url.path, '/api/v1/devices/x');
      expect(request.headers['Fimra-Device'], '6f1c4a52-1b2c-4d3e-8f90-0123456789ab');
      expect(jsonDecode(request.body), {
        'platform': 'android',
        'appVersion': fieldAppVersion,
        'push': {'provider': 'unifiedpush', 'endpoint': testAddress.endpoint, 'publicKey': 'BKey', 'auth': 'Auth'},
      });
      expect(receipt.device.push, isTrue);
      expect(receipt.device.status, DeviceStatus.active);
    });

    test('a wiped device is told once, without a token refresh', () async {
      answer = (_) => http.Response(
        jsonEncode({'code': 'device_wiped', 'error': 'An administrator asked this phone to delete its field data.'}),
        401,
      );
      await expectLater(
        api.alerts('demo'),
        throwsA(isA<DeviceActionException>().having((e) => e.action, 'action', DeviceAction.wipe)),
      );
      expect(seen, hasLength(1));
      expect(refreshes, 0);
      expect(actions.single.$1, DeviceAction.wipe);
      expect(actions.single.$2, startsWith('An administrator'));
    });

    test('a removed device is told so; other refusals refresh the token first', () async {
      answer = (_) => http.Response(jsonEncode({'code': 'device_revoked', 'error': 'Removed.'}), 401);
      await expectLater(api.me(), throwsA(isA<DeviceActionException>()));
      expect(actions.single.$1, DeviceAction.removed);
      answer = (_) => http.Response(jsonEncode({'error': 'The bearer token is invalid.'}), 401);
      await expectLater(api.me(), throwsA(isA<ApiException>().having((e) => e.status, 'status', 401)));
      expect(refreshes, 1);
      expect(actions, hasLength(1));
    });

    test('sign-out and wipe confirmation accept an empty answer', () async {
      answer = (_) => http.Response('', 204);
      await api.revokeDevice('x', signOut: true);
      await api.confirmWiped('x');
      expect(seen.map((r) => '${r.method} ${r.url.path}?${r.url.query}'), [
        'DELETE /api/v1/devices/x?reason=sign_out',
        'POST /api/v1/devices/x/wiped?',
      ]);
    });
  });

  group('device manager', () {
    test('registers after sign-in, then again with the push address', () async {
      final phone = Phone();
      await phone.signIn();
      final first = phone.api.registrations.single;
      expect(isDeviceId(first.$1), isTrue);
      expect(phone.ids.id, first.$1);
      expect(first.$2.platform, 'android');
      expect(first.$2.appVersion, fieldAppVersion);
      expect(first.$2.push, isNull);
      expect(phone.devices.pushStatus, PushStatus.waiting);

      phone.push.onAddress!(testAddress);
      await pumpEventQueue();
      expect(phone.api.registrations.last.$2.push, testAddress);
      expect(phone.api.registrations.last.$1, first.$1);
      expect(phone.devices.pushStatus, PushStatus.on);
    });

    test('explains a missing push app or a refused push server', () async {
      final none = Phone(hasPushApp: false);
      await none.signIn();
      expect(none.devices.pushStatus, PushStatus.noPushApp);

      final refused = Phone();
      refused.api
        ..pushAccepted = false
        ..pushProblem = 'Set your push app to use https://push.example.test.';
      await refused.signIn();
      refused.push.onAddress!(testAddress);
      await pumpEventQueue();
      expect(refused.devices.pushStatus, PushStatus.refused);
      expect(refused.devices.pushProblem, contains('https://push.example.test'));

      final browser = Phone(pushSupported: false);
      await browser.signIn();
      expect(browser.devices.pushStatus, PushStatus.unsupported);
      expect(browser.push.starts, 0);
    });

    test('shows each alert notification once, and a device check calls the API', () async {
      final phone = Phone();
      await phone.signIn();
      phone.push.deliver(alertPush());
      phone.push.deliver(alertPush());
      phone.push.deliver(alertPush(notification: '13', alert: '42'));
      await pumpEventQueue();
      expect(phone.notifier.shown.keys, [12, 13]);
      expect(phone.notifier.shown[12]!.alert, '41');

      final before = phone.api.registrations.length;
      phone.push.deliver({'v': 1, 'type': 'device_check'});
      await pumpEventQueue();
      expect(phone.api.registrations.length, before + 1);
      expect(phone.notifier.shown, hasLength(2));
    });

    test('a tapped notification asks the home screen to open its alert', () async {
      final phone = Phone();
      await phone.signIn();
      phone.notifier.onOpen!(PushNotice.parse(utf8.encode(jsonEncode(alertPush())))!);
      expect(phone.devices.openRequest.value?.alert, '41');
    });

    test('a wipe confirms, deletes everything on the list and returns to sign-in', () async {
      final phone = Phone();
      await phone.signIn();
      phone.push.deliver(alertPush());
      await pumpEventQueue();
      final id = phone.ids.id!;
      await phone.devices.handleAction(
        DeviceAction.wipe,
        'An administrator asked this phone to delete its field data.',
      );

      expect(phone.api.confirmed, [id]);
      expect(phone.wiped, ['notifications', 'push', 'maps', 'device', 'tokens']);
      expect(phone.notifier.shown, isEmpty);
      expect(phone.ids.id, isNull);
      expect(phone.tokens.tokens, isNull);
      expect(phone.devices.deviceId, isNull);
      expect(phone.session.status, SessionStatus.signedOut);
      expect(phone.session.message, startsWith('An administrator'));

      // Signing in again makes a new device.
      await phone.session.signIn();
      await pumpEventQueue();
      expect(phone.api.registrations.last.$1, isNot(id));
    });

    test('a removed phone signs out without the wipe list', () async {
      final phone = Phone();
      await phone.signIn();
      await phone.devices.handleAction(DeviceAction.removed, 'This phone was removed from your account.');
      expect(phone.api.confirmed, isEmpty);
      expect(phone.wiped, isEmpty);
      expect(phone.push.stops, 1);
      expect(phone.ids.id, isNull);
      expect(phone.session.message, 'This phone was removed from your account.');
    });

    test('signing out tells the API, stops push and forgets the device', () async {
      final phone = Phone();
      await phone.signIn();
      final id = phone.ids.id!;
      await phone.devices.signOut();
      expect(phone.api.revoked, [(id, true)]);
      expect(phone.push.stops, 1);
      expect(phone.notifier.clears, 1);
      expect(phone.ids.id, isNull);
      expect(phone.session.status, SessionStatus.signedOut);
      expect(phone.session.message, isNull);
    });
  });

  test('a wipe carries on past a step that fails and names it', () async {
    final done = <String>[];
    final data = LocalData([
      WipeStep('First', () async => done.add('first')),
      WipeStep('Broken', () async => throw StateError('locked')),
      WipeStep('Last', () async => done.add('last')),
    ]);
    expect(await data.wipe(), ['Broken']);
    expect(done, ['first', 'last']);
  });

  test('the field data list names each kind of data in plain words', () {
    Future<void> none() async {}
    final data = fieldDataOnPhone(
      notifications: none,
      pushRegistration: none,
      offlineMaps: none,
      deviceId: none,
      signInTokens: none,
    );
    expect(data.steps.map((s) => s.name), [
      'Alert notifications shown on the phone',
      'The push registration with the push app',
      'Map areas saved for offline use',
      "This phone's registration ID",
      'Sign-in tokens',
    ]);
  });

  test('device IDs are random version 4 UUIDs', () {
    final ids = {for (var i = 0; i < 50; i++) newDeviceId()};
    expect(ids, hasLength(50));
    expect(ids.every(isDeviceId), isTrue);
  });

  test('the app version matches pubspec.yaml', () {
    final pubspec = File('pubspec.yaml').readAsStringSync();
    expect(pubspec, contains('\nversion: $fieldAppVersion\n'));
  });
}
