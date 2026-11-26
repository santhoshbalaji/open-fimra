import 'dart:io';

import 'package:path_provider/path_provider.dart';

/// The picker makes temporary copies. Leave the user's gallery files alone.
Future<void> removePickedCopy(String path) async {
  final cache = await getTemporaryDirectory();
  final file = File(path);
  if (!await file.exists()) return;
  final root = await cache.resolveSymbolicLinks();
  final target = await file.resolveSymbolicLinks();
  if (target.startsWith('$root${Platform.pathSeparator}')) await file.delete();
}

/// The private cache can also hold picker copies left by an interrupted app.
Future<void> clearReportMediaCache() async {
  final cache = await getTemporaryDirectory();
  await for (final entry in cache.list(followLinks: false)) {
    await entry.delete(recursive: true);
  }
}
