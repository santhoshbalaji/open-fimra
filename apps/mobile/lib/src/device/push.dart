import 'dart:typed_data';

import '../api/models.dart';

/// How the phone receives push messages.
///
/// The field app uses UnifiedPush: the user installs a push app (a
/// "distributor") such as ntfy and connects it to the department's own push
/// server. No Google service is needed. The browser test build has no push.
abstract interface class PushService {
  /// False on platforms without push, such as the browser test build.
  bool get supported;

  /// Starts receiving. [onAddress] is called whenever the push address
  /// changes, with null when the push app drops it. [onMessage] gets each
  /// decrypted message. Returns false when no push app is installed.
  Future<bool> start({
    required void Function(PushAddress? address) onAddress,
    required void Function(Uint8List message) onMessage,
  });

  /// Stops receiving and asks the push app to forget this phone's address.
  Future<void> stop();
}

/// Shows alert notifications on the phone.
abstract interface class AlertNotifier {
  /// Prepares the notifier. [onOpen] is called when the user taps a
  /// notification. [askPermission] is false when the app runs without a
  /// screen, which cannot ask the user.
  Future<void> init(void Function(PushNotice notice) onOpen, {bool askPermission = true});

  /// Shows a notice. The same notice ID replaces an earlier one.
  Future<void> show(PushNotice notice);

  /// Removes every notification this app has shown.
  Future<void> clear();

  /// The notice whose tap started the app, if any.
  Future<PushNotice?> launchNotice();
}

/// No push, for the browser test build and for tests.
class NoPushService implements PushService {
  const NoPushService();

  @override
  bool get supported => false;

  @override
  Future<bool> start({
    required void Function(PushAddress? address) onAddress,
    required void Function(Uint8List message) onMessage,
  }) async => false;

  @override
  Future<void> stop() async {}
}

/// Shows nothing, for the browser test build.
class NoNotifier implements AlertNotifier {
  const NoNotifier();

  @override
  Future<void> init(void Function(PushNotice notice) onOpen, {bool askPermission = true}) async {}

  @override
  Future<void> show(PushNotice notice) async {}

  @override
  Future<void> clear() async {}

  @override
  Future<PushNotice?> launchNotice() async => null;
}
