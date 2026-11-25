import 'package:flutter/foundation.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:maplibre_gl/maplibre_gl.dart' as maplibre;
import 'package:unifiedpush_android/unifiedpush_android.dart';
import 'package:unifiedpush_platform_interface/unifiedpush_platform_interface.dart';

import '../api/models.dart';
import 'push.dart';

PushService platformPush() =>
    defaultTargetPlatform == TargetPlatform.android ? UnifiedPushService() : const NoPushService();

AlertNotifier platformNotifier() =>
    defaultTargetPlatform == TargetPlatform.android ? SystemNotifier() : const NoNotifier();

/// Deletes any map areas downloaded for offline use.
Future<void> clearMapRegions() async {
  if (defaultTargetPlatform != TargetPlatform.android) return;
  for (final region in await maplibre.getListOfRegions()) {
    await maplibre.deleteOfflineRegion(region.id);
  }
}

/// UnifiedPush starts the app without a screen to deliver a message in the
/// background. It passes this argument to main.
bool isBackgroundStart(List<String> args) => args.contains('--unifiedpush-bg');

/// Receives push messages through the push app the user chose.
///
/// This uses the Android implementation of the UnifiedPush connector
/// directly. The connector's app-facing package also brings a Linux
/// implementation, which a phone does not need.
class UnifiedPushService implements PushService {
  static const _instance = 'fimra-alerts';

  @override
  bool get supported => true;

  @override
  Future<bool> start({
    required void Function(PushAddress? address) onAddress,
    required void Function(Uint8List message) onMessage,
  }) async {
    UnifiedPushAndroid.registerWith();
    final unifiedPush = UnifiedPushPlatform.instance;
    await unifiedPush.initializeCallback(
      onNewEndpoint: (endpoint, _) {
        final keys = endpoint.pubKeySet;
        // Messages are encrypted for the phone, so an address without keys is not used.
        onAddress(
          keys == null
              ? null
              : PushAddress(provider: 'unifiedpush', endpoint: endpoint.url, publicKey: keys.pubKey, auth: keys.auth),
        );
      },
      onRegistrationFailed: (_, _) => onAddress(null),
      onUnregistered: (_) => onAddress(null),
      onMessage: (message, _) {
        if (message.decrypted) onMessage(message.content);
      },
    );
    if (!await unifiedPush.tryUseCurrentOrDefaultDistributor()) {
      final apps = await unifiedPush.getDistributors(const []);
      if (apps.isEmpty) return false;
      await unifiedPush.saveDistributor(apps.first);
    }
    await unifiedPush.register(_instance, const [], null, null);
    return true;
  }

  @override
  Future<void> stop() {
    UnifiedPushAndroid.registerWith();
    return UnifiedPushPlatform.instance.unregister(_instance);
  }
}

/// Shows alert notifications through Android's notification system.
class SystemNotifier implements AlertNotifier {
  final _plugin = FlutterLocalNotificationsPlugin();
  static const _channel = AndroidNotificationDetails(
    'fire-alerts',
    'Fire alerts',
    channelDescription: 'New fire alerts for your area',
    importance: Importance.high,
    priority: Priority.high,
    // The lock screen shows that an alert arrived, not which one.
    visibility: NotificationVisibility.private,
  );

  @override
  Future<void> init(void Function(PushNotice notice) onOpen, {bool askPermission = true}) async {
    await _plugin.initialize(
      settings: const InitializationSettings(android: AndroidInitializationSettings('ic_launcher_monochrome')),
      onDidReceiveNotificationResponse: (response) {
        final notice = PushNotice.fromPayload(response.payload);
        if (notice != null) onOpen(notice);
      },
    );
    if (!askPermission) return;
    // Android 13 and later ask the user before an app may show notifications.
    await _plugin
        .resolvePlatformSpecificImplementation<AndroidFlutterLocalNotificationsPlugin>()
        ?.requestNotificationsPermission();
  }

  @override
  Future<void> show(PushNotice notice) => _plugin.show(
    id: notice.id,
    title: notice.title,
    body: notice.body,
    notificationDetails: const NotificationDetails(android: _channel),
    payload: notice.toPayload(),
  );

  @override
  Future<void> clear() => _plugin.cancelAll();

  @override
  Future<PushNotice?> launchNotice() async {
    final details = await _plugin.getNotificationAppLaunchDetails();
    if (details == null || !details.didNotificationLaunchApp) return null;
    return PushNotice.fromPayload(details.notificationResponse?.payload);
  }
}
