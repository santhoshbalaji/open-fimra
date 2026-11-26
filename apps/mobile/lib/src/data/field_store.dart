import 'dart:convert';
import 'dart:math';
import 'dart:typed_data' show BytesBuilder;

import 'package:flutter/foundation.dart';
import 'package:sqflite/sqflite.dart';

typedef Json = Map<String, dynamic>;

List<Json> objects(Object? value) => (value as List? ?? const []).whereType<Json>().toList();
Json object(Object? value) => value is Json ? value : <String, dynamic>{};

class ReportFile {
  const ReportFile(this.id, this.name, this.type, this.bytes);
  final String id;
  final String name;
  final String type;
  final Uint8List bytes;

  Json get metadata => {'contentType': type, 'fileName': name, 'sizeBytes': bytes.length};
}

/// Data lives in the app's private database. Only the hash of the server
/// and account identifies the owner. Photos are loaded one at a time.
class FieldStore {
  FieldStore({this.factory, this.path});

  final DatabaseFactory? factory;
  final String? path;
  Future<Database>? _opening;
  int _generation = 0;

  int get generation => _generation;

  Future<Database> _open() async {
    final wiping = _wiping;
    if (wiping != null) await wiping;
    return _opening ??= _openDatabase();
  }

  Future<Database> _openDatabase() async {
    final factory = this.factory ?? databaseFactory;
    final path = this.path ?? '${await factory.getDatabasesPath()}/field.sqlite';
    return factory.openDatabase(
      path,
      options: OpenDatabaseOptions(
        version: 1,
        onConfigure: (db) => db.execute('PRAGMA foreign_keys = ON'),
        onCreate: (db, _) async {
          await db.execute(
            'CREATE TABLE cache (owner TEXT, workspace TEXT, key TEXT, part INTEGER, data BLOB, PRIMARY KEY (owner, workspace, key, part))',
          );
          await db.execute('''CREATE TABLE reports (
          id TEXT PRIMARY KEY, owner TEXT NOT NULL, workspace TEXT NOT NULL, alert TEXT NOT NULL,
          title TEXT NOT NULL, body TEXT NOT NULL, step TEXT, error TEXT, retry_at INTEGER,
          attempts INTEGER NOT NULL DEFAULT 0, received_at TEXT)''');
          await db.execute('''CREATE TABLE files (
          id TEXT PRIMARY KEY, report TEXT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
          name TEXT NOT NULL, type TEXT NOT NULL, sent INTEGER NOT NULL DEFAULT 0)''');
          await db.execute('CREATE INDEX files_report ON files(report)');
          await db.execute('''CREATE TABLE file_parts (
            file TEXT NOT NULL REFERENCES files(id) ON DELETE CASCADE, part INTEGER NOT NULL,
            data BLOB NOT NULL, PRIMARY KEY (file, part))''');
        },
      ),
    );
  }

  Future<Json?> read(String owner, String workspace, String key) async {
    final rows = await (await _open()).query(
      'cache',
      columns: ['data'],
      where: 'owner = ? AND workspace = ? AND key = ?',
      whereArgs: [owner, workspace, key],
      orderBy: 'part',
    );
    return rows.isEmpty ? null : jsonDecode(utf8.decode(_join(rows))) as Json;
  }

  Future<void> write(
    String owner,
    String workspace,
    Map<String, Json> rows,
    int generation, {
    bool replace = false,
  }) async {
    if (generation != _generation) return;
    await (await _open()).transaction((tx) async {
      if (generation != _generation) return;
      if (replace) {
        await tx.delete('cache', where: 'owner = ? AND workspace = ?', whereArgs: [owner, workspace]);
      }
      final batch = tx.batch();
      for (final row in rows.entries) {
        if (!replace) {
          batch.delete(
            'cache',
            where: 'owner = ? AND workspace = ? AND key = ?',
            whereArgs: [owner, workspace, row.key],
          );
        }
        _insertParts(batch, 'cache', {
          'owner': owner,
          'workspace': workspace,
          'key': row.key,
        }, utf8.encode(jsonEncode(row.value)));
      }
      await batch.commit(noResult: true);
    });
  }

  Future<void> forget(String owner, String workspace, {String? alert}) async {
    await (await _open()).delete(
      'cache',
      where: 'owner = ? AND workspace = ?${alert == null ? '' : ' AND key IN (?, ?, ?)'}',
      whereArgs: [
        owner,
        workspace,
        if (alert != null) ...['alert/$alert', 'history/$alert', 'reports/$alert'],
      ],
    );
  }

  Future<void> enqueue(
    String owner,
    String workspace,
    String alert,
    String title,
    Json body,
    List<ReportFile> files, {
    Json? step,
    required int generation,
  }) async {
    if (generation != _generation) throw StateError('The field data was cleared.');
    await (await _open()).transaction((tx) async {
      if (generation != _generation) throw StateError('The field data was cleared.');
      final id = body['id'] as String;
      await tx.insert('reports', {
        'id': id,
        'owner': owner,
        'workspace': workspace,
        'alert': alert,
        'title': title,
        'body': jsonEncode(body),
        'step': step == null ? null : jsonEncode(step),
      });
      final batch = tx.batch();
      for (final file in files) {
        batch.insert('files', {'id': file.id, 'report': id, 'name': file.name, 'type': file.type});
        _insertParts(batch, 'file_parts', {'file': file.id}, file.bytes);
      }
      await batch.commit(noResult: true);
    });
  }

  Future<List<Json>> reports(String owner, {String? workspace, bool pendingOnly = false, String? alert}) async =>
      (await (await _open()).rawQuery(
        '''SELECT reports.*,
          (SELECT count(*) FROM files WHERE report = reports.id) AS file_count,
          (SELECT count(*) FROM files WHERE report = reports.id AND sent = 1) AS files_sent
          FROM reports WHERE owner = ?
          ${workspace == null ? '' : ' AND workspace = ?'}
          ${alert == null ? '' : ' AND alert = ?'}
          ${pendingOnly ? ' AND received_at IS NULL' : ''} ORDER BY rowid''',
        [owner, ?workspace, ?alert],
      )).map(Json.from).toList();

  Future<List<String>> pendingFiles(String report) async => (await (await _open()).query(
    'files',
    columns: ['id'],
    where: 'report = ? AND sent = 0',
    whereArgs: [report],
    orderBy: 'rowid',
  )).map((row) => row['id']! as String).toList();

  Future<ReportFile?> file(String report, String id) async {
    final db = await _open();
    return db.transaction((tx) async {
      final rows = await tx.query('files', where: 'report = ? AND id = ? AND sent = 0', whereArgs: [report, id]);
      if (rows.isEmpty) return null;
      final row = rows.single;
      final parts = await tx.query(
        'file_parts',
        columns: ['data'],
        where: 'file = ?',
        whereArgs: [id],
        orderBy: 'part',
      );
      return ReportFile(id, row['name']! as String, row['type']! as String, _join(parts));
    });
  }

  Future<void> fileSent(String id, int generation) async {
    if (generation != _generation) return;
    await (await _open()).transaction((tx) async {
      if (generation != _generation) return;
      await tx.update('files', {'sent': 1}, where: 'id = ?', whereArgs: [id]);
      await tx.delete('file_parts', where: 'file = ?', whereArgs: [id]);
    });
  }

  // Android reads rows through a bounded cursor window. Keep each row small.
  void _insertParts(Batch batch, String table, Json keys, Uint8List data) {
    const size = 256 * 1024;
    for (var start = 0; start < data.length; start += size) {
      batch.insert(table, {
        ...keys,
        'part': start ~/ size,
        'data': Uint8List.sublistView(data, start, min(start + size, data.length)),
      });
    }
  }

  Uint8List _join(List<Map<String, Object?>> rows) {
    final builder = BytesBuilder(copy: false);
    for (final row in rows) {
      builder.add(row['data']! as Uint8List);
    }
    return builder.takeBytes();
  }

  Future<void> updateReport(String id, Json changes, int generation) async {
    if (generation != _generation) return;
    await (await _open()).update('reports', changes, where: 'id = ?', whereArgs: [id]);
  }

  Future<void> pruneReceipts(String owner, String workspace, Iterable<String> cachedIds) async {
    final db = await _open();
    final batch = db.batch();
    for (final id in cachedIds) {
      batch.delete(
        'reports',
        where: 'id = ? AND owner = ? AND workspace = ? AND received_at IS NOT NULL',
        whereArgs: [id, owner, workspace],
      );
    }
    await batch.commit(noResult: true);
  }

  /// Invalidates writes already in flight, then deletes the database and its
  /// journals. A later sign-in can open a fresh database.
  Future<void>? _wiping;

  Future<void> wipe() => _wiping ??= _wipe().whenComplete(() => _wiping = null);

  Future<void> _wipe() async {
    _generation++;
    final opened = _opening;
    final factory = this.factory ?? databaseFactory;
    final path = this.path ?? '${await factory.getDatabasesPath()}/field.sqlite';
    try {
      if (opened != null) await (await opened).close();
    } finally {
      _opening = null;
      await factory.deleteDatabase(path);
    }
  }
}

/// The browser build is for online tests. Android and iOS keep the database.
FieldStore? platformFieldStore() => kIsWeb ? null : FieldStore();
