import 'dart:convert';

import 'package:flutter/material.dart';

import '../data/field_store.dart';
import '../data/offline_repository.dart';
import 'widgets.dart';

class FieldSyncStatus extends StatelessWidget {
  const FieldSyncStatus({super.key, required this.repo, this.workspace, this.alert, this.onOpenAlert});
  final OfflineAlertRepository repo;
  final String? workspace;
  final String? alert;
  final OpenOutboxAlert? onOpenAlert;

  @override
  Widget build(BuildContext context) => _ReportStatusBuilder(
    repo: repo,
    workspace: workspace,
    alert: alert,
    builder: (context, snapshot) {
      final unsent = (snapshot.data ?? []).where((item) => item['kind'] != 'draft' && item['received_at'] == null);
      final failed = unsent.where((item) => item['error'] != null && item['retry_at'] == null).toList();
      final waiting = unsent.length - failed.length;
      return Padding(
        padding: alert == null ? const EdgeInsets.fromLTRB(16, 0, 16, 12) : EdgeInsets.zero,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _SyncDetails(repo: repo, showSavedAlerts: repo.lastSynced != null),
            if (waiting > 0 || (alert != null && failed.isNotEmpty)) ...[
              const SizedBox(height: 8),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                children: [
                  if (waiting > 0) _ReportStatus.waiting('$waiting waiting to send'),
                  if (alert != null && failed.isNotEmpty) _ReportStatus.failed('${failed.length} could not send'),
                ],
              ),
            ],
            if (alert == null && failed.isNotEmpty)
              ExpansionTile(
                tilePadding: EdgeInsets.zero,
                expandedCrossAxisAlignment: CrossAxisAlignment.stretch,
                leading: const Icon(Icons.error_outline),
                title: Text('${failed.length} could not send'),
                children: [
                  for (final item in failed)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 8),
                      child: Text("${item['title']}: ${item['error']}"),
                    ),
                ],
              ),
            if (alert == null)
              Padding(
                padding: const EdgeInsets.only(top: 8),
                child: OutlinedButton.icon(
                  onPressed: () => Navigator.of(context).push(
                    MaterialPageRoute<void>(
                      builder: (_) => OutboxScreen(repo: repo, onOpenAlert: onOpenAlert),
                    ),
                  ),
                  icon: const Icon(Icons.outbox_outlined),
                  label: const Text('Report outbox'),
                ),
              ),
          ],
        ),
      );
    },
  );
}

class _SyncDetails extends StatelessWidget {
  const _SyncDetails({required this.repo, this.showSavedAlerts = false});
  final OfflineAlertRepository repo;
  final bool showSavedAlerts;

  @override
  Widget build(BuildContext context) {
    final synced = repo.lastSynced;
    return Semantics(
      liveRegion: true,
      child: Wrap(
        spacing: 8,
        runSpacing: 8,
        children: [
          if (repo.offline)
            StatusBadge(
              icon: Icons.cloud_off_outlined,
              label: showSavedAlerts ? 'Offline · Saved alerts on this phone' : 'Offline',
            ),
          StatusBadge(
            icon: Icons.sync,
            label: repo.syncing
                ? 'Syncing'
                : synced == null
                ? 'Workspace sync pending'
                : 'Last synced ${formatTime(synced)}',
          ),
          if (repo.syncing && synced != null)
            StatusBadge(icon: Icons.schedule, label: 'Last synced ${formatTime(synced)}'),
        ],
      ),
    );
  }
}

class _ReportStatusBuilder extends StatelessWidget {
  const _ReportStatusBuilder({required this.repo, required this.builder, this.workspace, this.alert});
  final OfflineAlertRepository repo;
  final AsyncWidgetBuilder<List<Json>> builder;
  final String? workspace;
  final String? alert;

  @override
  Widget build(BuildContext context) => ValueListenableBuilder(
    valueListenable: repo.changes,
    builder: (context, _, _) => FutureBuilder<List<Json>>(
      future: repo.reportStatus(workspace: workspace, alert: alert),
      builder: builder,
    ),
  );
}

class _ReportStatus extends StatusBadge {
  const _ReportStatus.waiting([String label = 'Waiting to send'])
    : super(icon: Icons.cloud_upload_outlined, label: label);
  const _ReportStatus.failed([String label = 'Could not send']) : super(icon: Icons.error_outline, label: label);
  const _ReportStatus.sent([String label = 'Sent']) : super(icon: Icons.cloud_done_outlined, label: label);
}

/// The same report state and failure reason on the alert and in the outbox.
class ReportDelivery extends StatelessWidget {
  const ReportDelivery({super.key, this.item, this.sent = false});
  final Json? item;
  final bool sent;

  @override
  Widget build(BuildContext context) {
    final item = this.item;
    if (item?['kind'] == 'draft') {
      return const StatusBadge(icon: Icons.edit_note, label: 'Draft saved on this phone');
    }
    final received = item?['received_at'] as String?;
    final error = item?['error'] as String?;
    if (received != null) return _ReportStatus.sent('Sent ${formatTime(DateTime.parse(received))}');
    if (sent) return const _ReportStatus.sent();
    if (item == null) return const SizedBox.shrink();
    final step = item['kind'] == 'step' ? jsonDecode(item['body'] as String) as Json : null;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        const StatusBadge(icon: Icons.phone_android, label: 'Saved on this phone'),
        const SizedBox(height: 6),
        if (error == null || item['retry_at'] != null) const _ReportStatus.waiting() else const _ReportStatus.failed(),
        if (error != null) Text(error),
        if ((item['file_count'] as int? ?? 0) > 0) Text("${item['files_sent']} of ${item['file_count']} files sent"),
        if (step?['note'] case final String note) Text(note),
        if (step?['reason'] case final String reason) Text('Location exception · $reason'),
        if (item['retry_at'] != null) const Text('Will retry automatically'),
      ],
    );
  }
}

typedef OpenOutboxAlert = Future<void> Function(String workspace, String alert);

class OutboxScreen extends StatelessWidget {
  const OutboxScreen({super.key, required this.repo, this.onOpenAlert});
  final OfflineAlertRepository repo;
  final OpenOutboxAlert? onOpenAlert;

  Future<void> _remove(BuildContext context, Json item) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Remove this step from the outbox?'),
        content: const Text('This deletes the saved note and location from this phone. You can record the step again.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('Cancel')),
          FilledButton(onPressed: () => Navigator.pop(context, true), child: const Text('Remove')),
        ],
      ),
    );
    if (confirmed == true) await repo.removeRefusedStep(item['id'] as String);
  }

  @override
  Widget build(BuildContext context) => Scaffold(
    appBar: AppBar(title: const Text('Report outbox')),
    body: SafeArea(
      child: _ReportStatusBuilder(
        repo: repo,
        builder: (context, snapshot) {
          final reports = snapshot.data ?? [];
          return ListView(
            padding: const EdgeInsets.all(16),
            children: [
              _SyncDetails(repo: repo),
              if (repo.syncProblem case final problem?) StatusBadge(icon: Icons.error_outline, label: problem),
              const SizedBox(height: 8),
              Align(
                alignment: AlignmentDirectional.centerStart,
                child: FilledButton.icon(
                  onPressed: repo.syncing ? null : () => repo.sync(retry: true, refresh: true),
                  icon: const Icon(Icons.sync),
                  label: Text(repo.syncing ? 'Syncing' : 'Sync now'),
                ),
              ),
              const SizedBox(height: 12),
              if (snapshot.hasError)
                const StatusBadge(icon: Icons.error_outline, label: 'Could not read saved work.')
              else if (!snapshot.hasData)
                const Center(child: CircularProgressIndicator(semanticsLabel: 'Reading saved work'))
              else if (reports.isEmpty)
                const Text('No reports, drafts or steps in the outbox.'),
              for (final item in reports)
                Padding(
                  padding: const EdgeInsets.only(bottom: 8),
                  child: Card(
                    child: Padding(
                      padding: const EdgeInsets.all(12),
                      child: Semantics(
                        container: true,
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Semantics(
                              headingLevel: 2,
                              child: Text('${item['title']}', style: Theme.of(context).textTheme.titleMedium),
                            ),
                            const SizedBox(height: 8),
                            ReportDelivery(item: item),
                            if (item['kind'] == 'draft' && onOpenAlert != null)
                              OutlinedButton(
                                onPressed: () => onOpenAlert!(item['workspace'] as String, item['alert'] as String),
                                child: const Text('Open alert'),
                              ),
                            if (item['kind'] == 'step' &&
                                item['received_at'] == null &&
                                item['error'] != null &&
                                item['retry_at'] == null)
                              TextButton(onPressed: () => _remove(context, item), child: const Text('Remove step')),
                          ],
                        ),
                      ),
                    ),
                  ),
                ),
            ],
          );
        },
      ),
    ),
  );
}
