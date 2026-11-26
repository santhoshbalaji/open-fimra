import 'dart:async';
import 'dart:convert';

import 'package:crypto/crypto.dart';
import 'package:flutter/foundation.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import 'alert_repository.dart';
import 'field_store.dart';
import 'outbox.dart';

/// Reads fresh state when connected. The saved workspace and outbox let
/// staff work offline.
class OfflineAlertRepository extends OnlineAlertRepository {
  OfflineAlertRepository(super.api, this.store, this.account) : outbox = ReportOutbox(api, store);

  final FieldStore store;
  final String? Function() account;
  final ReportOutbox outbox;
  final changes = ValueNotifier<int>(0);
  String? _workspace;
  bool _disposed = false;
  DateTime? lastSynced;
  String? syncProblem;
  bool offline = false;
  bool syncing = false;
  Timer? _timer;

  String? get _owner {
    final subject = account();
    return subject == null ? null : sha256.convert(utf8.encode(jsonEncode([api.baseUrl, subject]))).toString();
  }

  void dispose() {
    _disposed = true;
    _timer?.cancel();
    changes.dispose();
  }

  void _notify() {
    if (!_disposed) changes.value++;
  }

  void activate(String slug) {
    if (_workspace == slug && _timer != null) return;
    _workspace = slug;
    _timer ??= Timer.periodic(const Duration(seconds: 20), (_) => unawaited(sync()));
    lastSynced = null;
    syncProblem = null;
    unawaited(sync(refresh: true));
  }

  Future<Json> _read(String slug, String key, Future<Json> Function() onlineCall, {String? alert}) async {
    final owner = _owner;
    final generation = store.generation;
    if (offline && owner != null) {
      final saved = await store.read(owner, slug, key);
      if (saved != null && _owner == owner && generation == store.generation) return saved;
    }
    try {
      final result = await onlineCall();
      if (owner != null && _owner == owner && !_disposed) {
        await store.write(owner, slug, {key: result}, generation);
      }
      return result;
    } on ApiException catch (error) {
      if (owner == null || _owner != owner || generation != store.generation) rethrow;
      if (error.status == 403 || error.status == 404) await store.forget(owner, slug, alert: alert);
      if (!error.offline) rethrow;
      final saved = await store.read(owner, slug, key);
      if (saved == null || generation != store.generation) rethrow;
      if (!offline) {
        offline = true;
        _notify();
      }
      return saved;
    }
  }

  @override
  Future<Profile> profile() async => Profile.fromJson(await _read('', 'profile', api.profileData));

  @override
  Future<WorkspaceMap> workspace(String slug) async =>
      WorkspaceMap.fromJson(await _read(slug, 'map', () => api.workspaceData(slug)));

  @override
  Future<Alert> alert(String slug, String id) async =>
      Alert.fromJson(await _read(slug, 'alert/$id', () => api.alertData(slug, id), alert: id));

  @override
  Future<AlertHistory> history(String slug, String id) async =>
      AlertHistory.fromJson(await _read(slug, 'history/$id', () => api.historyData(slug, id), alert: id));

  @override
  Future<AlertPage> alerts(String slug, {AlertQueue? queue, String? cursor}) async {
    Future<AlertPage?> cached() async {
      final owner = _owner;
      if (owner == null) return null;
      final saved = await store.read(owner, slug, 'alerts');
      if (saved == null || _owner != owner) return null;
      final page = AlertPage.fromJson(saved);
      return AlertPage(
        items: [if (cursor == null) ...page.items.where((alert) => queue == null || alert.queue == queue.slug)],
        counts: page.counts,
        canReview: page.canReview,
        nextCursor: null,
      );
    }

    if (offline) {
      final saved = await cached();
      if (saved != null) return saved;
    }
    try {
      return await super.alerts(slug, queue: queue, cursor: cursor);
    } on ApiException catch (error) {
      if (!error.offline) rethrow;
      final saved = await cached();
      if (saved == null) rethrow;
      offline = true;
      _notify();
      return saved;
    }
  }

  @override
  Future<Alert?> alertForDetection(String slug, String detectionId) async {
    if (!offline) {
      try {
        return await super.alertForDetection(slug, detectionId);
      } on ApiException catch (error) {
        if (!error.offline) rethrow;
      }
    }
    return (await savedAlerts(slug)).where((alert) => alert.detectionId == detectionId).firstOrNull;
  }

  Future<List<Alert>> savedAlerts(String slug) async {
    final owner = _owner;
    if (owner == null) return [];
    final saved = await store.read(owner, slug, 'alerts');
    return saved == null ? [] : AlertPage.fromJson(saved).items;
  }

  Future<Json?> savedReference(String slug) async {
    final owner = _owner;
    return owner == null ? null : store.read(owner, slug, 'reference');
  }

  Future<Json> reportsForAlert(String slug, String id) =>
      _read(slug, 'reports/$id', () => _reportBundle(slug, id, {}), alert: id);

  Future<Json> _reportBundle(String slug, String id, Map<String, Future<Json>> versions) async {
    final data = await api.assignedForms(slug, id);
    final forms = objects(data['forms']);
    final results = await Future.wait([
      api.submissions(slug, id),
      Future.wait(
        forms.map((row) async {
          final form = row['form'] as String;
          final version = row['version'] as int;
          final definition = await versions.putIfAbsent('$form/$version', () => api.formVersion(slug, form, version));
          return {...row, 'definition': definition['definition']};
        }),
      ),
    ]);
    return {'forms': results[1], 'submissions': results[0], 'attachments': data['attachments']};
  }

  Future<List<Json>> reportStatus({String? workspace, String? alert}) async {
    final owner = _owner;
    return owner == null ? [] : store.reports(owner, workspace: workspace, alert: alert);
  }

  String get _signedInOwner => _owner ?? (throw const ApiException(401, 'Sign in again before saving.'));

  Future<void> queueStep(String slug, String alert, WorkflowMove move, String fromStep, StepRequest request) async {
    await store.enqueueStep(
      _signedInOwner,
      slug,
      alert,
      move.label,
      request.toJson(),
      fromStep: fromStep,
      generation: store.generation,
    );
    _notify();
    _sendSoon();
  }

  Future<void> removeRefusedStep(String id) async {
    await store.removeRefusedStep(_signedInOwner, id, store.generation);
    _notify();
  }

  Future<void> saveDraft(
    String slug,
    String alert,
    String title,
    Json body,
    List<ReportFile> files,
    int generation,
  ) async {
    await store.saveDraft(_signedInOwner, slug, alert, title, body, files, generation: generation);
    _notify();
  }

  Future<void> queueReport(
    String slug,
    String alert,
    String title,
    Json body,
    List<ReportFile> files, {
    StepRequest? step,
    String? fromStep,
    required int generation,
  }) async {
    await store.enqueue(
      _signedInOwner,
      slug,
      alert,
      title,
      body,
      files,
      step: step?.toJson(),
      fromStep: fromStep,
      generation: generation,
    );
    _notify();
    _sendSoon();
  }

  void _sendSoon() {
    // A sync that already read the outbox may have missed the new item.
    unawaited(
      Future<void>(() async {
        await _currentSync;
        if (!offline) await sync();
      }),
    );
  }

  Future<void> sync({bool retry = false, bool refresh = false}) async {
    final owner = _owner;
    final slug = _workspace;
    if (owner == null || slug == null || _disposed) return;
    if (syncing) {
      if (!retry && !refresh) return;
      await _currentSync;
      if (_disposed || owner != _owner) return;
      return sync(retry: retry, refresh: refresh);
    }
    syncing = true;
    _notify();
    final generation = store.generation;
    bool current() => !_disposed && owner == _owner && generation == store.generation;
    final task = _sync(owner, slug, retry, refresh, current);
    _currentSync = task;
    try {
      await task;
    } finally {
      syncing = false;
      _notify();
    }
  }

  Future<void>? _currentSync;

  Future<void> _sync(String owner, String slug, bool retry, bool refresh, bool Function() current) async {
    try {
      final sent = await outbox.send(owner, retry: retry, active: current);
      if (!current()) return;
      final stamp = await store.read(owner, slug, 'sync');
      final previous = DateTime.tryParse(stamp?['at'] as String? ?? '');
      if (current() && slug == _workspace) lastSynced = previous;
      final stale = previous == null || DateTime.now().difference(previous) >= const Duration(minutes: 5);
      if (refresh || offline || stale || sent.contains(slug)) {
        await _refresh(owner, slug, current);
      } else {
        // Detect signal loss and grant changes even with an empty outbox.
        await api.workspaceData(slug);
      }
      for (final other in sent.where((workspace) => workspace != slug)) {
        if (current()) await _refresh(owner, other, current);
      }
    } on DeviceActionException {
      return;
    } on ApiException catch (error) {
      if (!current() || slug != _workspace) return;
      if (error.status == 403) {
        await store.forget(owner, slug);
        lastSynced = null;
      }
      offline = error.offline;
      syncProblem = error.offline ? 'No connection. Saved work stays on this phone.' : error.message;
    } on Exception {
      if (current() && slug == _workspace) syncProblem = 'Could not save updates on this phone.';
    }
  }

  Future<void> _refresh(String owner, String slug, bool Function() current) async {
    final generation = store.generation;
    try {
      if (!current()) return;
      final results = await Future.wait([api.workspaceData(slug), api.offlineReference(slug)]);
      final rows = <String, Json>{'map': results[0], 'reference': results[1]};
      final items = <Json>[];
      Json? first;
      String? cursor;
      do {
        if (!current()) return;
        final page = await api.alertsData(slug, hours: OnlineAlertRepository.windowHours, cursor: cursor, limit: 100);
        first ??= page;
        items.addAll(objects(page['items']));
        cursor = page['nextCursor'] as String?;
      } while (cursor != null && cursor.isNotEmpty);
      // Keep alerts with unsent work available even after they age out of the list.
      final work = await store.reports(owner, workspace: slug);
      final ids = {
        ...items.map((item) => '${item['id']}'),
        ...work.where((item) => item['received_at'] == null).map((item) => item['alert'] as String),
      }.toList();
      final versions = <String, Future<Json>>{};
      final available = <String>{};
      final receipts = <String>[];
      for (var start = 0; start < ids.length; start += 4) {
        if (!current()) return;
        await Future.wait(
          ids.skip(start).take(4).map((id) async {
            try {
              final detail = await Future.wait([
                api.alertData(slug, id),
                api.historyData(slug, id),
                _reportBundle(slug, id, versions),
              ]);
              rows['alert/$id'] = detail[0];
              rows['history/$id'] = detail[1];
              rows['reports/$id'] = detail[2];
              available.add(id);
              receipts.addAll(objects(detail[2]['submissions']).map((report) => report['id'] as String));
            } on ApiException catch (error) {
              // An alert can leave the caller's queue during a refresh.
              if (error.status != 404) rethrow;
            }
          }),
        );
      }
      if (!current()) return;
      final now = DateTime.now().toUtc();
      rows['alerts'] = {
        ...first,
        'items': items.where((item) => available.contains('${item['id']}')).toList(),
        'nextCursor': null,
      };
      rows['sync'] = {'at': now.toIso8601String()};
      await store.write(owner, slug, rows, generation, replace: true);
      if (!current()) return;
      await store.pruneReceipts(owner, slug, receipts);
      if (slug == _workspace && current()) {
        lastSynced = now;
        offline = false;
        syncProblem = null;
      }
    } on ApiException catch (error) {
      if (current() && error.status == 403) await store.forget(owner, slug);
      rethrow;
    }
  }
}
