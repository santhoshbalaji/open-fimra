import 'dart:async';

import 'package:flutter/foundation.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../auth/session.dart';
import '../config.dart';
import 'device_store.dart';
import 'local_data.dart';
import 'push.dart';

/// Whether this phone receives alert notifications.
enum PushStatus {
  unsupported,
  noPushApp,
  waiting,
  on,
  refused,
  offline;

  String get title => switch (this) {
    unsupported => 'Notifications are not available here',
    noPushApp => 'Install a push app to get notifications',
    waiting => 'Connecting to your push app',
    on => 'Notifications are on',
    refused => 'The server cannot send to this phone',
    offline => 'Could not reach the server',
  };
}

/// This phone's registration with the core API: its ID, push address,
/// alert notifications and the response to a wipe or removal.
///
/// After sign-in the phone registers itself with a new ID. Every API call
/// carries the ID. When the API answers that an administrator wiped the
/// phone, the manager deletes everything in [localData] and returns to the
/// sign-in screen. When the phone was removed from the account, it signs out.
class DeviceManager extends ChangeNotifier {
  DeviceManager({
    required this.api,
    required this.session,
    required this.store,
    required this.push,
    required this.notifier,
    required this.localData,
    String? platform,
    this.appVersion = fieldAppVersion,
  }) : platform = platform ?? _platform();

  final DeviceApi api;
  final Session session;
  final DeviceStore store;
  final PushService push;
  final AlertNotifier notifier;
  final LocalData localData;
  final String platform;
  final String appVersion;

  static const _timeout = Duration(seconds: 8);

  String? _id;
  PushAddress? _address;
  Device? _device;
  PushStatus _status = PushStatus.waiting;
  String? _problem;
  bool _started = false;
  bool _active = false;
  bool _hasPushApp = true;
  Future<void>? _ending;
  Future<void>? _registering;
  bool _again = false;

  /// A notification the user tapped, for the home screen to open. The home
  /// screen sets it back to null once it has opened the alert.
  final openRequest = ValueNotifier<PushNotice?>(null);

  /// The ID sent with every API call, or null before registration.
  String? get deviceId => _id;
  Device? get device => _device;
  PushStatus get pushStatus => _status;

  /// The server's or the push app's problem in plain English, if any.
  String? get pushProblem => _problem;

  static String _platform() {
    if (kIsWeb) return 'web';
    return defaultTargetPlatform == TargetPlatform.iOS ? 'ios' : 'android';
  }

  /// Starts listening. [foreground] is false when the push app started the
  /// app without a screen to deliver a message.
  Future<void> start({bool foreground = true}) async {
    if (_started) return;
    _started = true;
    _id = await store.read();
    try {
      await notifier.init(_openNotice, askPermission: foreground);
      if (foreground) {
        final launch = await notifier.launchNotice();
        if (launch != null) _openNotice(launch);
      }
    } on Object catch (error) {
      debugPrint('Notifications are unavailable: $error');
    }
    session.addListener(_sessionChanged);
    _sessionChanged();
  }

  void _openNotice(PushNotice notice) {
    if (notice.type == 'notification') openRequest.value = notice;
  }

  void _sessionChanged() {
    if (session.status == SessionStatus.signedIn) {
      if (!_active && _ending == null) unawaited(_signedIn());
    } else if (session.status == SessionStatus.signedOut) {
      _active = false;
    }
  }

  Future<void> _signedIn() async {
    _active = true;
    if (_id == null) {
      final id = newDeviceId();
      await store.write(id);
      _id = id;
    }
    await _startPush();
    await register();
  }

  Future<void> _startPush() async {
    if (!push.supported) {
      _status = PushStatus.unsupported;
      notifyListeners();
      return;
    }
    try {
      _hasPushApp = await push.start(onAddress: _addressChanged, onMessage: _received);
    } on Object catch (error) {
      debugPrint('Push did not start: $error');
      _hasPushApp = false;
    }
    _status = _hasPushApp ? PushStatus.waiting : PushStatus.noPushApp;
    notifyListeners();
  }

  /// Looks for a push app again, for example after the user installed one.
  Future<void> retryPush() async {
    if (session.status != SessionStatus.signedIn) return;
    await _startPush();
    await register();
  }

  void _addressChanged(PushAddress? address) {
    if (address == _address) return;
    _address = address;
    unawaited(register());
  }

  void _received(Uint8List bytes) {
    final notice = PushNotice.parse(bytes);
    if (notice == null) return;
    // A device check only asks the phone to call in. Whether it must wipe is
    // decided by the API's answer, never by a push message.
    if (notice.type == 'device_check') {
      unawaited(register());
    } else {
      unawaited(notifier.show(notice));
    }
  }

  /// Registers this phone, or records its contact and current push address.
  /// Safe to call often; overlapping calls run one after another.
  Future<void> register() {
    if (_registering != null) {
      _again = true;
      return _registering!;
    }
    return _registering = _register().whenComplete(() => _registering = null);
  }

  Future<void> _register() async {
    do {
      _again = false;
      final id = _id;
      if (id == null || session.status != SessionStatus.signedIn || _ending != null) return;
      try {
        final receipt = await api.registerDevice(
          id,
          DeviceRegistration(platform: platform, appVersion: appVersion, push: _address),
        );
        _device = receipt.device;
        _problem = receipt.pushProblem;
        _status = switch ((push.supported, _hasPushApp, _address)) {
          (false, _, _) => PushStatus.unsupported,
          (_, false, _) => PushStatus.noPushApp,
          (_, _, null) => PushStatus.waiting,
          _ when receipt.device.push => PushStatus.on,
          _ => PushStatus.refused,
        };
      } on DeviceActionException {
        return;
      } on ApiException catch (error) {
        if (error.status == 401) return;
        _status = PushStatus.offline;
        _problem = error.message;
      }
      notifyListeners();
    } while (_again);
  }

  /// The user's phones, for the notifications screen.
  Future<List<Device>> devices() => api.devices();

  /// Removes another of the user's phones.
  Future<void> remove(Device device) async {
    if (device.id == _id) return signOut();
    await api.revokeDevice(device.id);
  }

  /// Signs out: tells the API this phone signed out, stops push, forgets the
  /// device ID and ends the session.
  Future<void> signOut() async {
    final id = _id;
    if (id != null && session.status == SessionStatus.signedIn) {
      try {
        await api.revokeDevice(id, signOut: true).timeout(_timeout);
      } on DeviceActionException {
        // The phone was wiped or removed, and that has been handled.
        return;
      } on ApiException {
        // Offline: the phone's push address is dropped below, so the API's
        // next push to it fails and the API stops using it.
      } on TimeoutException {
        // As above.
      }
    }
    await _end(wipe: false);
  }

  /// Ends the session after the server refused the user's token.
  Future<void> sessionEnded() => _end(wipe: false, message: 'Your session has ended. Sign in again.');

  /// Follows the API's instruction to wipe this phone or sign it out.
  Future<void> handleAction(DeviceAction action, String message) =>
      _end(wipe: action == DeviceAction.wipe, message: message);

  Future<void> _end({required bool wipe, String? message}) =>
      _ending ??= _finish(wipe, message).whenComplete(() => _ending = null);

  Future<void> _finish(bool wipe, String? message) async {
    final id = _id;
    if (wipe && id != null) {
      // Tell the API first, while the tokens still exist. The wipe goes ahead
      // even if this fails.
      try {
        await api.confirmWiped(id).timeout(_timeout);
      } on Object catch (error) {
        debugPrint('Could not confirm the wipe: $error');
      }
    }
    _active = false;
    _id = null;
    _address = null;
    _device = null;
    _problem = null;
    openRequest.value = null;
    if (wipe) {
      final failed = await localData.wipe();
      if (failed.isNotEmpty) debugPrint('Some data could not be deleted: ${failed.join(', ')}');
    } else {
      for (final step in [push.stop, notifier.clear, store.clear]) {
        try {
          await step();
        } on Object catch (error) {
          debugPrint('Sign-out clean-up failed: $error');
        }
      }
    }
    await session.signOut(message: message);
    notifyListeners();
  }

  @override
  void dispose() {
    session.removeListener(_sessionChanged);
    openRequest.dispose();
    super.dispose();
  }
}
