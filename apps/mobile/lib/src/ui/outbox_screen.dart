import 'package:flutter/material.dart';

import '../data/field_store.dart';
import '../data/offline_repository.dart';
import 'widgets.dart';

class FieldSyncStatus extends StatelessWidget {
  const FieldSyncStatus({super.key, required this.repo});
  final OfflineAlertRepository repo;

  @override
  Widget build(BuildContext context) => ValueListenableBuilder(
    valueListenable: repo.changes,
    builder: (context, _, _) => FutureBuilder<List<Json>>(
      future: repo.reportStatus(),
      builder: (context, snapshot) {
        final pending = (snapshot.data ?? []).where((item) => item['received_at'] == null).toList();
        final failed = pending.where((item) => item['error'] != null).length;
        final synced = repo.lastSynced;
        final status = [
          if (repo.offline) 'Offline',
          if (repo.syncing) 'Syncing',
          if (synced != null) 'Last synced ${formatTime(synced)}' else 'Workspace sync pending',
          if (pending.isNotEmpty) '${pending.length} pending',
          if (failed > 0) '$failed could not send',
        ].join(' · ');
        return ListTile(
          leading: Icon(repo.offline ? Icons.cloud_off_outlined : Icons.sync),
          title: Text(status),
          onTap: () => Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => OutboxScreen(repo: repo))),
          trailing: const Icon(Icons.chevron_right),
        );
      },
    ),
  );
}

class OutboxScreen extends StatelessWidget {
  const OutboxScreen({super.key, required this.repo});
  final OfflineAlertRepository repo;

  @override
  Widget build(BuildContext context) => Scaffold(
    appBar: AppBar(title: const Text('Report outbox')),
    body: SafeArea(
      child: ValueListenableBuilder(
        valueListenable: repo.changes,
        builder: (context, _, _) => FutureBuilder<List<Json>>(
          future: repo.reportStatus(),
          builder: (context, snapshot) {
            final reports = snapshot.data ?? [];
            final stamp = repo.lastSynced;
            return ListView(
              padding: const EdgeInsets.all(16),
              children: [
                if (repo.offline) const Text('Offline. Reports are saved on this phone.'),
                Text(stamp == null ? 'Workspace sync pending' : 'Last synced ${formatTime(stamp)}'),
                if (repo.syncProblem case final problem?) Text(problem),
                const SizedBox(height: 8),
                Align(
                  alignment: Alignment.centerLeft,
                  child: OutlinedButton.icon(
                    onPressed: repo.syncing ? null : () => repo.sync(retry: true, refresh: true),
                    icon: const Icon(Icons.sync),
                    label: Text(repo.syncing ? 'Syncing' : 'Sync now'),
                  ),
                ),
                if (snapshot.hasError)
                  const Text('Could not read saved reports.')
                else if (!snapshot.hasData)
                  const Center(child: CircularProgressIndicator())
                else if (reports.isEmpty)
                  const Text('No reports waiting to send.'),
                for (final item in reports)
                  Card(
                    child: ListTile(
                      leading: Icon(
                        item['received_at'] != null
                            ? Icons.cloud_done_outlined
                            : item['error'] != null
                            ? Icons.error_outline
                            : Icons.cloud_upload_outlined,
                      ),
                      title: Text('${item['title']}'),
                      subtitle: Text(
                        [
                          if (item['received_at'] case final String received)
                            'Sent ${formatTime(DateTime.parse(received))}'
                          else if (item['error'] case final String error)
                            'Could not send\n$error'
                          else
                            'Waiting to send',
                          if (item['received_at'] == null && (item['file_count'] as int? ?? 0) > 0)
                            "${item['files_sent']} of ${item['file_count']} files sent",
                          if (item['received_at'] == null && item['retry_at'] != null) 'Will retry automatically',
                        ].join('\n'),
                      ),
                    ),
                  ),
              ],
            );
          },
        ),
      ),
    ),
  );
}
