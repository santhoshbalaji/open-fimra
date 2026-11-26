import 'dart:convert';
import 'dart:math';

import 'package:crypto/crypto.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import 'field_store.dart';

class ReportOutbox {
  ReportOutbox(this.api, this.store, {DateTime Function()? clock}) : clock = clock ?? DateTime.now;

  final FimraApi api;
  final FieldStore store;
  final DateTime Function() clock;
  Future<Set<String>>? _sending;

  Future<Set<String>> send(String owner, {bool retry = false, bool Function()? active}) =>
      _sending ??= _send(owner, retry, active ?? () => true).whenComplete(() => _sending = null);

  Future<Set<String>> _send(String owner, bool retry, bool Function() active) async {
    final generation = store.generation;
    final changed = <String>{};
    final blocked = <(String, String)>{};
    bool current() => active() && generation == store.generation;
    for (final item in await store.reports(owner, pendingOnly: true)) {
      if (!current()) break;
      final id = item['id'] as String;
      final slug = item['workspace'] as String;
      final alert = item['alert'] as String;
      final scope = (slug, alert);
      if (blocked.contains(scope)) continue;
      final due = item['retry_at'] as int?;
      if (!retry && item['error'] != null && (due == null || due > clock().millisecondsSinceEpoch)) {
        blocked.add(scope);
        continue;
      }
      final body = jsonDecode(item['body'] as String) as Json;
      String? filename;
      try {
        if (item['kind'] == 'step') {
          final receipt = await api.recordStep(slug, alert, StepRequest.fromJson(body));
          if (!current()) return changed;
          await store.updateReport(id, {
            'received_at': receipt.at.toUtc().toIso8601String(),
            'error': null,
            'retry_at': null,
            'body': jsonEncode({'step': body['step'], 'clientTime': body['clientTime']}),
          }, generation);
          changed.add(slug);
          continue;
        }
        for (final fileId in await store.pendingFiles(id)) {
          if (!current()) return changed;
          final file = await store.file(id, fileId);
          if (file == null) continue;
          filename = file.name;
          final receipt = await api.attachmentReceipt(slug, alert, file.id);
          if (!current()) return changed;
          if (receipt == null) {
            await api.uploadAttachment(slug, alert, file.id, file.name, file.type, file.bytes);
          } else if (receipt['sha256'] != sha256.convert(file.bytes).toString() ||
              file.metadata.entries.any((entry) => receipt[entry.key] != entry.value)) {
            throw const ApiException(409, 'A different file has this ID. The saved file was not replaced.');
          }
          if (!current()) return changed;
          await store.fileSent(file.id, generation);
          filename = null;
        }
        if (!current()) return changed;
        final step = item['step'] as String?;
        if (step != null) {
          final request = jsonDecode(step) as Json;
          final history = await api.history(slug, alert);
          if (!current()) return changed;
          if (!history.entries.any((entry) => entry.step == request['step'])) {
            await api.recordStep(slug, alert, StepRequest.fromJson(request));
          }
        }
        if (!current()) return changed;
        final receipt = await api.submitReport(slug, alert, body);
        if (!current()) return changed;
        await store.updateReport(id, {
          'received_at': receipt['receivedAt'] ?? clock().toUtc().toIso8601String(),
          'error': null,
          'retry_at': null,
          // The form and its step are enough to label the local receipt.
          'body': jsonEncode({'form': body['form'], 'step': body['step']}),
          'step': null,
        }, generation);
        changed.add(slug);
      } on DeviceActionException {
        return changed;
      } on ApiException catch (error) {
        if (!current()) return changed;
        final attempts = (item['attempts'] as int) + 1;
        final transient = error.offline || error.status >= 500 || [408, 425, 429].contains(error.status);
        await store.updateReport(id, {
          'attempts': attempts,
          'error': [
            if (filename != null) 'File: $filename',
            error.offline ? 'Waiting for a connection.' : error.message,
            ...error.problems,
          ].join('\n'),
          'retry_at': transient
              ? clock().add(Duration(seconds: 15 * (1 << min(attempts - 1, 5)))).millisecondsSinceEpoch
              : null,
        }, generation);
        blocked.add(scope);
        if (error.offline || error.status == 401) break;
        // Refresh confirmed state when a request was refused.
        if (!transient) changed.add(slug);
      }
    }
    return changed;
  }
}
