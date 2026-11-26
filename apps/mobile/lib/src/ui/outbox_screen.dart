import 'package:flutter/material.dart';

import '../data/field_store.dart';
import '../data/offline_repository.dart';
import 'widgets.dart';

class FieldSyncStatus extends StatelessWidget {
  const FieldSyncStatus({super.key, required this.repo, this.workspace, this.alert});
  final OfflineAlertRepository repo;
  final String? workspace;
  final String? alert;

  @override
  Widget build(BuildContext context) => _ReportStatusBuilder(
    repo: repo,
    workspace: workspace,
    alert: alert,
    builder: (context, snapshot) {
      final unsent = (snapshot.data ?? []).where((item) => item['received_at'] == null);
      final waiting = unsent.where((item) => item['error'] == null).length;
      final failed = unsent.where((item) => item['error'] != null).toList();
      return Padding(
        padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
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
                  onPressed: () =>
                      Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => OutboxScreen(repo: repo))),
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
    final received = item?['received_at'] as String?;
    final error = item?['error'] as String?;
    if (received != null) return _ReportStatus.sent('Sent ${formatTime(DateTime.parse(received))}');
    if (sent) return const _ReportStatus.sent();
    if (item == null) return const SizedBox.shrink();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        const StatusBadge(icon: Icons.phone_android, label: 'Saved on this phone'),
        const SizedBox(height: 6),
        if (error == null) const _ReportStatus.waiting() else ...[const _ReportStatus.failed(), Text(error)],
        if ((item['file_count'] as int? ?? 0) > 0) Text("${item['files_sent']} of ${item['file_count']} files sent"),
        if (item['retry_at'] != null) const Text('Will retry automatically'),
      ],
    );
  }
}

class OutboxScreen extends StatelessWidget {
  const OutboxScreen({super.key, required this.repo});
  final OfflineAlertRepository repo;

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
                const StatusBadge(icon: Icons.error_outline, label: 'Could not read saved reports.')
              else if (!snapshot.hasData)
                const Center(child: CircularProgressIndicator(semanticsLabel: 'Reading saved reports'))
              else if (reports.isEmpty)
                const Text('No reports in the outbox.'),
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
