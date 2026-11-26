import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:geolocator/geolocator.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../auth/pkce.dart';
import '../data/field_store.dart';
import '../device/device_store.dart';
import '../forms/report_form.dart';
import '../theme/app_theme.dart';
import 'alert_map.dart';
import 'home.dart';
import 'outbox_screen.dart';
import 'widgets.dart';

/// A point from the phone's location service.
typedef Locate = Future<({double latitude, double longitude})> Function();

/// Asks for permission and reads the current position.
Future<({double latitude, double longitude})> deviceLocation() async {
  await ensureLocationPermission();
  final position = await Geolocator.getCurrentPosition(
    locationSettings: const LocationSettings(accuracy: LocationAccuracy.high, timeLimit: Duration(seconds: 30)),
  );
  return (latitude: position.latitude, longitude: position.longitude);
}

Future<void> ensureLocationPermission() async {
  if (!await Geolocator.isLocationServiceEnabled()) {
    throw const LocationProblem('Location is switched off. Switch it on and try again.');
  }
  var permission = await Geolocator.checkPermission();
  if (permission == LocationPermission.denied) permission = await Geolocator.requestPermission();
  if (permission == LocationPermission.denied || permission == LocationPermission.deniedForever) {
    throw const LocationProblem('The app needs your location for this step. Allow location access and try again.');
  }
}

class LocationProblem implements Exception {
  const LocationProblem(this.message);
  final String message;
}

/// Details of one alert, its timeline and the steps the user may take.
class AlertDetailScreen extends StatefulWidget {
  const AlertDetailScreen({super.key, required this.view, required this.alert, this.locate = deviceLocation});
  final WorkspaceView view;
  final Alert alert;
  final Locate locate;

  @override
  State<AlertDetailScreen> createState() => _AlertDetailScreenState();
}

class _AlertDetailScreenState extends State<AlertDetailScreen> {
  late Alert _alert = widget.alert;
  AlertHistory? _history;
  String? _problem;
  bool _changed = false;
  Json? _reportData;
  String? _reportsProblem;
  DateTime? _lastSynced;

  String get _slug => widget.view.workspace.slug;

  @override
  void initState() {
    super.initState();
    final repo = widget.view.services.offline;
    _lastSynced = repo?.lastSynced;
    repo?.changes.addListener(_syncChanged);
    _load();
  }

  @override
  void dispose() {
    widget.view.services.offline?.changes.removeListener(_syncChanged);
    super.dispose();
  }

  void _syncChanged() {
    final stamp = widget.view.services.offline?.lastSynced;
    if (mounted && stamp != null && stamp != _lastSynced) {
      _lastSynced = stamp;
      unawaited(_load());
    }
  }

  Future<void> _loadReports() async {
    final repo = widget.view.services.offline;
    if (repo == null) return;
    try {
      final data = await repo.reportsForAlert(_slug, _alert.id);
      if (mounted) {
        setState(() {
          _reportData = data;
          _reportsProblem = null;
        });
      }
    } on DeviceActionException {
      return;
    } on ApiException catch (error) {
      if (mounted) {
        setState(() {
          _reportData = null;
          _reportsProblem = error.message;
        });
      }
    }
  }

  Future<void> _fill(Json assignment, WorkflowMove? move) async {
    final repo = widget.view.services.offline!;
    final generation = repo.store.generation;
    final definition = object(assignment['definition']);
    final step = assignment['step'] as String;
    final form = assignment['form'] as String;
    final stages = objects(definition['stages']);
    final stage = stages.indexWhere((row) => row['workflowStep'] == step);
    final earlierSteps = stages.take(stage < 0 ? 0 : stage).map((row) => row['workflowStep']).toList();
    final reports = objects(_reportData?['submissions']);
    final initial = <String, dynamic>{};
    for (final earlier in earlierSteps) {
      final report = reports
          .where(
            (row) => row['form'] == form && row['step'] == earlier && object(row['approval'])['decision'] != 'returned',
          )
          .lastOrNull;
      initial.addAll(object(report?['values']));
    }
    final reference = await repo.savedReference(_slug);
    if (!mounted || repo.store.generation != generation) return;
    final raised = _alert.raisedAt;
    final title = assignment['title'] as String;
    final result = await Navigator.of(context).push<bool>(
      MaterialPageRoute(
        builder: (_) => ReportForm(
          definition: definition,
          step: step,
          initial: initial,
          attachments: object(_reportData?['attachments']),
          reference: reference,
          move: move,
          locate: widget.locate,
          alert: {
            'alert-id': _alert.id,
            'observed-at': _alert.observedAt.toUtc().toIso8601String(),
            if (raised != null) 'raised-at': raised.toUtc().toIso8601String(),
            'location': {
              'type': 'Point',
              'coordinates': [_alert.longitude, _alert.latitude],
            },
            if (_alert.unitPath != null) 'unit': _alert.placeName(widget.view.map),
            'forest-class': _alert.forestClass,
            'source': _alert.source,
            'confidence': _alert.confidence,
            'fire-power': _alert.frpMw,
          },
          save: (values, files, queuedStep) => repo.queueReport(
            _slug,
            _alert.id,
            title,
            {
              'id': newDeviceId(),
              'form': form,
              'version': assignment['version'],
              'step': step,
              'deviceTime': DateTime.now().toUtc().toIso8601String(),
              'location': ?queuedStep?.toJson()['location'],
              'values': values,
            },
            files,
            step: queuedStep,
            generation: generation,
          ),
        ),
      ),
    );
    if (result == true && mounted) {
      _changed = true;
      ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Report saved to the outbox.')));
    }
  }

  Widget _reports(AlertHistory? history) {
    final repo = widget.view.services.offline!;
    return ValueListenableBuilder(
      valueListenable: repo.changes,
      builder: (context, _, _) => FutureBuilder<List<Json>>(
        future: repo.reportStatus(workspace: _slug, alert: _alert.id),
        builder: (context, snapshot) {
          final forms = objects(_reportData?['forms']);
          final queue = (snapshot.data ?? []);
          final submissions = objects(_reportData?['submissions']);
          final problem = _reportsProblem;
          if (forms.isEmpty && queue.isEmpty && submissions.isEmpty && problem == null) return const SizedBox.shrink();
          return Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const SizedBox(height: 16),
              Text('Reports', style: Theme.of(context).textTheme.titleMedium),
              if (problem != null) StatusBadge(icon: Icons.error_outline, label: problem),
              for (final assignment in forms) ...[
                Builder(
                  builder: (context) {
                    final step = assignment['step'] as String;
                    final entry = history?.entries.where((entry) => entry.step == step).lastOrNull;
                    final move = history?.moves.where((move) => move.step == step).firstOrNull;
                    final saved = queue.where((row) {
                      final body = jsonDecode(row['body'] as String) as Json;
                      return body['form'] == assignment['form'] && body['step'] == step;
                    }).lastOrNull;
                    final sent = assignment['submitted'] == true || saved?['received_at'] != null;
                    final label = entry?.label ?? move?.label ?? step.replaceAll('_', ' ');
                    return Padding(
                      padding: const EdgeInsets.only(top: 12),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text('${assignment['title']}', style: Theme.of(context).textTheme.titleSmall),
                          Text(label),
                          ReportDelivery(item: saved, sent: sent),
                          if (!sent && saved != null)
                            OutlinedButton.icon(
                              label: const Text('Report outbox'),
                              icon: const Icon(Icons.outbox_outlined),
                              onPressed: () => Navigator.of(
                                context,
                              ).push(MaterialPageRoute<void>(builder: (_) => OutboxScreen(repo: repo))),
                            )
                          else if (!sent && snapshot.hasData && (entry != null || move != null))
                            OutlinedButton(
                              onPressed: () => _fill(assignment, entry != null ? null : move),
                              child: const Text('Fill in'),
                            ),
                        ],
                      ),
                    );
                  },
                ),
              ],
              for (final report in submissions)
                if (object(report['approval'])['decision'] case final String decision)
                  StatusBadge(
                    icon: decision == 'approved' ? Icons.check_circle_outline : Icons.assignment_return_outlined,
                    label: 'Report version ${report['version']} · ${decision == 'approved' ? 'Approved' : 'Returned'}',
                  ),
            ],
          );
        },
      ),
    );
  }

  Future<void> _load() async {
    final repo = widget.view.services.repository;
    unawaited(_loadReports());
    try {
      final results = await Future.wait([repo.alert(_slug, _alert.id), repo.history(_slug, _alert.id)]);
      if (!mounted) return;
      setState(() {
        _alert = results[0] as Alert;
        _history = results[1] as AlertHistory;
        _problem = null;
      });
    } on ApiException catch (error) {
      if (mounted) setState(() => _problem = error.message);
    }
  }

  Future<void> _take(WorkflowMove move) async {
    final saved = await showModalBottomSheet<bool>(
      context: context,
      isScrollControlled: true,
      useSafeArea: true,
      showDragHandle: true,
      builder: (_) => StepSheet(view: widget.view, alert: _alert, move: move, locate: widget.locate),
    );
    if (saved != true || !mounted) return;
    _changed = true;
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('${move.label} recorded.')));
    await _load();
  }

  @override
  Widget build(BuildContext context) {
    final history = _history;
    final text = Theme.of(context).textTheme;
    return PopScope<Object?>(
      canPop: false,
      onPopInvokedWithResult: (didPop, _) {
        if (!didPop) Navigator.of(context).pop(_changed);
      },
      child: Scaffold(
        appBar: AppBar(title: const Text('Alert details')),
        body: RefreshIndicator(
          onRefresh: _load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              Semantics(headingLevel: 1, child: Text(_alert.placeName(widget.view.map), style: text.titleLarge)),
              const SizedBox(height: 8),
              Wrap(
                spacing: 6,
                runSpacing: 6,
                children: [
                  StatusBadge.step(_alert.status, _alert.stepLabel),
                  if (_alert.overdue) StatusBadge.overdue(),
                ],
              ),
              const SizedBox(height: 16),
              if (widget.view.services.offline case final repo?)
                FieldSyncStatus(repo: repo, workspace: _slug, alert: _alert.id),
              _Facts(alert: _alert, map: widget.view.map),
              if (widget.view.services.offline != null) ...[
                Align(
                  alignment: Alignment.centerLeft,
                  child: OutlinedButton.icon(
                    onPressed: () => Navigator.of(context).push(
                      MaterialPageRoute<void>(
                        builder: (_) => Scaffold(
                          appBar: AppBar(title: const Text('Alert map')),
                          body: AlertMapView(view: widget.view, focus: _alert, onOpen: (_) {}),
                        ),
                      ),
                    ),
                    icon: const Icon(Icons.map_outlined),
                    label: const Text('View alert map'),
                  ),
                ),
                _reports(history),
              ],
              const SizedBox(height: 24),
              if (_problem != null)
                MessageView(
                  icon: Icons.cloud_off_outlined,
                  title: 'Could not load the timeline',
                  body: _problem,
                  action: FilledButton(onPressed: _load, child: const Text('Try again')),
                )
              else if (history == null)
                const Center(child: CircularProgressIndicator())
              else ...[
                _Moves(history: history, onTake: _take),
                const SizedBox(height: 24),
                Text('Timeline', style: text.titleMedium),
                const SizedBox(height: 8),
                for (final entry in history.entries.reversed) _TimelineEntry(entry),
              ],
            ],
          ),
        ),
      ),
    );
  }
}

class _Facts extends StatelessWidget {
  const _Facts({required this.alert, required this.map});
  final Alert alert;
  final WorkspaceMap map;

  @override
  Widget build(BuildContext context) {
    final rows = <(String, String)>[
      ('Seen', formatTime(alert.observedAt)),
      ('Forest', alert.forest?.label ?? alert.forestClass),
      for (final level in map.placeOrder(alert.placeHierarchy))
        if (alert.places[level.slug] case final place?) (level.label, '${place.name} (${place.code})'),
      ('Location', '${alert.latitude.toStringAsFixed(5)}, ${alert.longitude.toStringAsFixed(5)}'),
      if (alert.source != null) ('Source', [alert.source, alert.product].nonNulls.join(' · ')),
      if (alert.confidence != null) ('Confidence', alert.confidence!),
      if (alert.frpMw != null) ('Fire power', '${alert.frpMw!.toStringAsFixed(1)} MW'),
      if (alert.alertQueue != AlertQueue.unit) ('Queue', alert.alertQueue?.label ?? alert.queue),
    ];
    final text = Theme.of(context).textTheme;
    return Card(
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            for (final (label, value) in rows)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 6),
                child: MergeSemantics(
                  child: LayoutBuilder(
                    builder: (context, constraints) {
                      final heading = Text(label, style: text.titleSmall);
                      final detail = Text(value, style: text.bodyMedium);
                      if (constraints.maxWidth < 280 || MediaQuery.textScalerOf(context).scale(16) > 20) {
                        return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [heading, detail]);
                      }
                      return Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          SizedBox(width: 110, child: heading),
                          Expanded(child: detail),
                        ],
                      );
                    },
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }
}

class _Moves extends StatelessWidget {
  const _Moves({required this.history, required this.onTake});
  final AlertHistory history;
  final void Function(WorkflowMove move) onTake;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final steps = history.moves.where((m) => !m.terminal).toList();
    final outcomes = history.moves.where((m) => m.terminal).toList();
    if (history.moves.isEmpty) {
      return Text('There are no steps you can take on this alert.', style: text.bodyMedium);
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('Next step', style: text.titleMedium),
        const SizedBox(height: 8),
        for (final move in steps)
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: FilledButton.icon(
              onPressed: () => onTake(move),
              icon: Icon(move.locationRequired ? Icons.my_location : stepIcon(move.step)),
              label: Text(move.label),
            ),
          ),
        if (outcomes.isNotEmpty) ...[
          const SizedBox(height: 8),
          Text('Other outcomes', style: text.titleSmall),
          const SizedBox(height: 8),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              for (final move in outcomes)
                OutlinedButton.icon(
                  onPressed: () => onTake(move),
                  icon: Icon(stepIcon(move.step)),
                  label: Text(move.label),
                ),
            ],
          ),
        ],
      ],
    );
  }
}

class _TimelineEntry extends StatelessWidget {
  const _TimelineEntry(this.entry);
  final HistoryEntry entry;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final p = FimraColours.of(context);
    final details = [
      formatTime(entry.at),
      entry.actor ?? 'System',
      if (entry.latitude != null) 'with location',
    ].join(' · ');
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(stepIcon(entry.step), size: 20, color: p.textSecondary),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(entry.label, style: text.titleSmall),
                Text(details, style: text.bodySmall),
                if (entry.note.isNotEmpty) Text(entry.note, style: text.bodyMedium),
                if (entry.reason.isNotEmpty) Text('Location exception: ${entry.reason}', style: text.bodyMedium),
                if (entry.reportPending) const StatusBadge(icon: Icons.schedule, label: 'Report pending'),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// Confirms one step, with an optional note, and records it.
class StepSheet extends StatefulWidget {
  const StepSheet({super.key, required this.view, required this.alert, required this.move, required this.locate});
  final WorkspaceView view;
  final Alert alert;
  final WorkflowMove move;
  final Locate locate;

  @override
  State<StepSheet> createState() => _StepSheetState();
}

class _StepSheetState extends State<StepSheet> {
  final _note = TextEditingController();
  final _reason = TextEditingController();

  /// One ID per attempt, so pressing Save again after a lost answer is safe.
  final _clientId = randomToken(18);
  bool _exception = false;
  bool _busy = false;
  String? _problem;

  @override
  void dispose() {
    _note.dispose();
    _reason.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    final move = widget.move;
    if (_exception && _reason.text.trim().isEmpty) {
      setState(() => _problem = 'Give a reason for recording this step without your location.');
      return;
    }
    setState(() {
      _busy = true;
      _problem = null;
    });
    try {
      ({double latitude, double longitude})? point;
      if (move.locationRequired && !_exception) point = await widget.locate();
      await widget.view.services.repository.recordStep(
        widget.view.workspace.slug,
        widget.alert.id,
        StepRequest(
          step: move.step,
          clientId: _clientId,
          note: _note.text.trim(),
          reason: _exception ? _reason.text.trim() : '',
          latitude: point?.latitude,
          longitude: point?.longitude,
        ),
      );
      if (mounted) Navigator.of(context).pop(true);
    } on LocationProblem catch (error) {
      setState(() => _problem = error.message);
    } on ApiException catch (error) {
      setState(() => _problem = error.message);
    } on Exception {
      setState(() => _problem = 'Could not get your location. Try again outside or near a window.');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final move = widget.move;
    return Padding(
      padding: EdgeInsets.fromLTRB(16, 0, 16, 16 + MediaQuery.viewInsetsOf(context).bottom),
      child: SafeArea(
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(move.label, style: text.titleLarge),
              const SizedBox(height: 8),
              Text(
                move.terminal
                    ? 'This ends the workflow for this alert.'
                    : move.locationRequired
                    ? 'Your current location is saved with this step. You need to be near the fire.'
                    : 'This is saved with your name and the time.',
                style: text.bodyMedium,
              ),
              const SizedBox(height: 16),
              TextField(
                controller: _note,
                maxLines: 3,
                minLines: 1,
                decoration: const InputDecoration(labelText: 'Note (optional)'),
              ),
              if (move.locationRequired && move.canOverride) ...[
                const SizedBox(height: 8),
                CheckboxListTile(
                  contentPadding: EdgeInsets.zero,
                  value: _exception,
                  onChanged: _busy ? null : (value) => setState(() => _exception = value ?? false),
                  title: const Text('Record without my location'),
                  subtitle: const Text('For supervising officers. A reason is required.'),
                ),
                if (_exception)
                  TextField(
                    controller: _reason,
                    decoration: const InputDecoration(labelText: 'Reason'),
                  ),
              ],
              if (_problem != null) ...[
                const SizedBox(height: 12),
                Row(
                  children: [
                    Icon(Icons.error_outline, color: FimraColours.of(context).danger),
                    const SizedBox(width: 8),
                    Expanded(child: Text(_problem!, style: text.bodyMedium)),
                  ],
                ),
              ],
              const SizedBox(height: 16),
              FilledButton(onPressed: _busy ? null : _save, child: Text(_busy ? 'Saving' : 'Save')),
              const SizedBox(height: 8),
              TextButton(onPressed: _busy ? null : () => Navigator.of(context).pop(false), child: const Text('Cancel')),
            ],
          ),
        ),
      ),
    );
  }
}
