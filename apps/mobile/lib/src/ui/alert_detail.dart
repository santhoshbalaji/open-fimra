import 'package:flutter/material.dart';
import 'package:geolocator/geolocator.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../auth/pkce.dart';
import '../theme/app_theme.dart';
import 'home.dart';
import 'widgets.dart';

/// A point from the phone's location service.
typedef Locate = Future<({double latitude, double longitude})> Function();

/// Asks for permission and reads the current position.
Future<({double latitude, double longitude})> deviceLocation() async {
  if (!await Geolocator.isLocationServiceEnabled()) {
    throw const LocationProblem('Location is switched off. Switch it on and try again.');
  }
  var permission = await Geolocator.checkPermission();
  if (permission == LocationPermission.denied) permission = await Geolocator.requestPermission();
  if (permission == LocationPermission.denied || permission == LocationPermission.deniedForever) {
    throw const LocationProblem('The app needs your location for this step. Allow location access and try again.');
  }
  final position = await Geolocator.getCurrentPosition(
    locationSettings: const LocationSettings(accuracy: LocationAccuracy.high, timeLimit: Duration(seconds: 30)),
  );
  return (latitude: position.latitude, longitude: position.longitude);
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

  String get _slug => widget.view.workspace.slug;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final repo = widget.view.services.repository;
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
        appBar: AppBar(title: Text(_alert.placeName(widget.view.map))),
        body: RefreshIndicator(
          onRefresh: _load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              Wrap(
                spacing: 6,
                runSpacing: 6,
                children: [
                  StatusBadge.step(_alert.status, _alert.stepLabel),
                  if (_alert.overdue) StatusBadge.overdue(),
                ],
              ),
              const SizedBox(height: 16),
              _Facts(alert: _alert, map: widget.view.map),
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
    final p = FimraColours.of(context);
    return Card(
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
        child: Column(
          children: [
            for (final (label, value) in rows)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 6),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    SizedBox(
                      width: 110,
                      child: Text(label, style: text.bodyMedium?.copyWith(color: p.textSecondary)),
                    ),
                    Expanded(child: Text(value, style: text.bodyMedium)),
                  ],
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
                if (entry.reportPending) Text('Report pending', style: text.bodySmall),
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
    );
  }
}
