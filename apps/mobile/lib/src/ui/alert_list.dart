import 'dart:async';

import 'package:flutter/material.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../theme/app_theme.dart';
import 'home.dart';
import 'outbox_screen.dart';
import 'widgets.dart';

/// The alerts the user may see, newest first.
class AlertListView extends StatefulWidget {
  const AlertListView({super.key, required this.view, required this.onOpen, this.clock});
  final WorkspaceView view;
  final void Function(Alert alert) onOpen;
  final DateTime Function()? clock;

  @override
  State<AlertListView> createState() => AlertListViewState();
}

class AlertListViewState extends State<AlertListView> {
  final List<Alert> _items = [];
  Map<String, int> _counts = const {};
  bool _canReview = false;
  String? _cursor;
  String? _problem;
  bool _loading = true;
  bool _loadingMore = false;

  /// Null shows every queue the user may see.
  AlertQueue? _queue;

  @override
  void initState() {
    super.initState();
    reload(refresh: false);
  }

  /// Reads the first page again, for example after a step was recorded.
  Future<void> reload({bool refresh = true}) async {
    if (refresh) unawaited(widget.view.services.offline?.sync(refresh: true));
    setState(() {
      _loading = _items.isEmpty;
      _problem = null;
    });
    try {
      final page = await widget.view.services.repository.alerts(widget.view.workspace.slug, queue: _queue);
      if (!mounted) return;
      setState(() {
        _items
          ..clear()
          ..addAll(page.items);
        _counts = page.counts;
        _canReview = page.canReview;
        _cursor = page.nextCursor;
        _loading = false;
      });
    } on ApiException catch (error) {
      if (!mounted) return;
      setState(() {
        _problem = error.message;
        _loading = false;
      });
    }
  }

  Future<void> _more() async {
    final cursor = _cursor;
    if (cursor == null || _loadingMore) return;
    setState(() => _loadingMore = true);
    try {
      final page = await widget.view.services.repository.alerts(
        widget.view.workspace.slug,
        queue: _queue,
        cursor: cursor,
      );
      if (!mounted) return;
      setState(() {
        _items.addAll(page.items);
        _cursor = page.nextCursor;
      });
    } on ApiException catch (error) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(error.message)));
    } finally {
      if (mounted) setState(() => _loadingMore = false);
    }
  }

  void _filter(AlertQueue? queue) {
    if (queue == _queue) return;
    setState(() {
      _queue = queue;
      _items.clear();
    });
    reload();
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) return const Center(child: CircularProgressIndicator());
    final problem = _problem;
    final total = _counts.values.fold(0, (a, b) => a + b);
    return RefreshIndicator(
      onRefresh: reload,
      child: CustomScrollView(
        physics: const AlwaysScrollableScrollPhysics(),
        slivers: [
          SliverToBoxAdapter(child: _header(context, total)),
          if (widget.view.services.offline case final repo?) SliverToBoxAdapter(child: FieldSyncStatus(repo: repo)),
          if (problem != null)
            SliverFillRemaining(
              hasScrollBody: false,
              child: MessageView(
                icon: Icons.cloud_off_outlined,
                title: 'Could not load alerts',
                body: problem,
                action: FilledButton(onPressed: reload, child: const Text('Try again')),
              ),
            )
          else if (_items.isEmpty)
            const SliverFillRemaining(
              hasScrollBody: false,
              child: MessageView(
                icon: Icons.check_circle_outline,
                title: 'No alerts in the past 7 days',
                body: 'New fire alerts for your area will appear here. Pull down to check again.',
              ),
            )
          else
            SliverPadding(
              padding: const EdgeInsets.fromLTRB(12, 0, 12, 12),
              sliver: SliverList.separated(
                itemCount: _items.length,
                separatorBuilder: (_, _) => const SizedBox(height: 8),
                itemBuilder: (context, index) => AlertTile(
                  alert: _items[index],
                  map: widget.view.map,
                  now: (widget.clock ?? DateTime.now)(),
                  onTap: () => widget.onOpen(_items[index]),
                ),
              ),
            ),
          if (_cursor != null && problem == null)
            SliverToBoxAdapter(
              child: Padding(
                padding: const EdgeInsets.fromLTRB(12, 0, 12, 24),
                child: OutlinedButton(
                  onPressed: _loadingMore ? null : _more,
                  child: Text(_loadingMore ? 'Loading' : 'Show more alerts'),
                ),
              ),
            ),
        ],
      ),
    );
  }

  Widget _header(BuildContext context, int total) {
    final text = Theme.of(context).textTheme;
    final grants = widget.view.grants;
    final area = grants.map((g) => g.unit.name).toSet().join(', ');
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 16, 16, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Alerts', style: text.titleLarge),
          const SizedBox(height: 4),
          Text(
            '$total ${total == 1 ? 'alert' : 'alerts'} in the past 7 days · $area',
            style: text.bodyMedium?.copyWith(color: FimraColours.of(context).textSecondary),
          ),
          if (_canReview) ...[
            const SizedBox(height: 12),
            SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: Row(
                children: [
                  _chip(null, 'All', total),
                  for (final queue in AlertQueue.values) _chip(queue, queue.label, _counts[queue.slug] ?? 0),
                ],
              ),
            ),
          ],
        ],
      ),
    );
  }

  Widget _chip(AlertQueue? queue, String label, int count) => Padding(
    padding: const EdgeInsets.only(right: 8),
    child: ChoiceChip(
      label: Text('$label ($count)'),
      selected: _queue == queue,
      onSelected: (_) => _filter(queue),
      materialTapTargetSize: MaterialTapTargetSize.padded,
    ),
  );
}

class AlertTile extends StatelessWidget {
  const AlertTile({super.key, required this.alert, required this.map, required this.now, required this.onTap});
  final Alert alert;
  final WorkspaceMap map;
  final DateTime now;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final p = FimraColours.of(context);
    final forest = alert.forest;
    return Card(
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Padding(
                padding: const EdgeInsets.only(top: 2),
                child: DetectionSymbol(forestClass: forest, age: fireAgeIndex(alert.observedAt, now), size: 20),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(alert.placeName(map), style: text.titleMedium),
                    const SizedBox(height: 2),
                    Text(
                      '${forest?.label ?? alert.forestClass} · seen ${formatAge(alert.observedAt, now)}',
                      style: text.bodyMedium?.copyWith(color: p.textSecondary),
                    ),
                    const SizedBox(height: 8),
                    Wrap(
                      spacing: 6,
                      runSpacing: 6,
                      children: [
                        StatusBadge.step(alert.status, alert.stepLabel),
                        if (alert.overdue) StatusBadge.overdue(),
                        if (alert.alertQueue != AlertQueue.unit)
                          StatusBadge(icon: Icons.rule, label: alert.alertQueue?.label ?? alert.queue),
                      ],
                    ),
                  ],
                ),
              ),
              Icon(Icons.chevron_right, color: p.textSecondary),
            ],
          ),
        ),
      ),
    );
  }
}
