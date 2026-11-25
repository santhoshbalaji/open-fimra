import 'push.dart';

PushService platformPush() => const NoPushService();

AlertNotifier platformNotifier() => const NoNotifier();

/// Map tiles are not stored by the browser build.
Future<void> clearMapRegions() async {}

/// The browser build is always started by the user.
bool isBackgroundStart(List<String> args) => false;
