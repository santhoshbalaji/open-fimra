import 'dart:math';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Keeps this phone's registration ID. A new one is made at each sign-in
/// and deleted at sign-out or a wipe.
abstract interface class DeviceStore {
  Future<String?> read();
  Future<void> write(String id);
  Future<void> clear();
}

class SecureDeviceStore implements DeviceStore {
  SecureDeviceStore([FlutterSecureStorage? storage]) : _storage = storage ?? const FlutterSecureStorage();

  final FlutterSecureStorage _storage;
  static const _key = 'device.id';

  @override
  Future<String?> read() async {
    try {
      final value = await _storage.read(key: _key);
      return value != null && isDeviceId(value) ? value : null;
    } on Exception {
      return null;
    }
  }

  @override
  Future<void> write(String id) => _storage.write(key: _key, value: id);

  @override
  Future<void> clear() => _storage.delete(key: _key);
}

class MemoryDeviceStore implements DeviceStore {
  String? id;

  @override
  Future<String?> read() async => id;

  @override
  Future<void> write(String value) async => id = value;

  @override
  Future<void> clear() async => id = null;
}

/// A random version 4 UUID.
String newDeviceId([Random? random]) {
  final r = random ?? Random.secure();
  final b = List<int>.generate(16, (_) => r.nextInt(256));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  String hex(int from, int to) => b.sublist(from, to).map((v) => v.toRadixString(16).padLeft(2, '0')).join();
  return '${hex(0, 4)}-${hex(4, 6)}-${hex(6, 8)}-${hex(8, 10)}-${hex(10, 16)}';
}

bool isDeviceId(String value) =>
    RegExp(r'^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$').hasMatch(value);
