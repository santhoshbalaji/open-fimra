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
import '../theme/status_icons.dart';
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
    throw const LocationProblem('Location access is off. Allow location access and try again.');
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
  bool _formOpen = false;

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

  Future<void> _fill(
    Json assignment, {
    required bool canSend,
    required String fromStep,
    WorkflowMove? move,
    Json? draft,
    DateTime? reachedAt,
  }) async {
    if (_formOpen) return;
    _formOpen = true;
    try {
      await _openForm(assignment, canSend: canSend, fromStep: fromStep, move: move, draft: draft, reachedAt: reachedAt);
    } on Exception {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Could not open the report.')));
      }
    } finally {
      _formOpen = false;
    }
  }

  Future<void> _openForm(
    Json assignment, {
    required bool canSend,
    required String fromStep,
    WorkflowMove? move,
    Json? draft,
    DateTime? reachedAt,
  }) async {
    final repo = widget.view.services.offline!;
    final generation = repo.store.generation;
    final draftBody = _body(draft);
    if (draft != null) assignment = object(draftBody['assignment']);
    final definition = object(assignment['definition']);
    final id = draft?['id'] as String? ?? newDeviceId();
    final step = assignment['step'] as String;
    final form = assignment['form'] as String;
    final stages = objects(definition['stages']);
    final stage = stages.indexWhere((row) => row['workflowStep'] == step);
    final earlierSteps = stages.take(stage < 0 ? 0 : stage).map((row) => row['workflowStep']).toList();
    final reports = objects(_reportData?['submissions']);
    final queued = earlierSteps.isEmpty ? <Json>[] : await repo.reportStatus(workspace: _slug, alert: _alert.id);
    final attachments = {...object(_reportData?['attachments'])};
    final initial = <String, dynamic>{};
    for (final earlier in earlierSteps) {
      final report = reports
          .where(
            (row) => row['form'] == form && row['step'] == earlier && object(row['approval'])['decision'] != 'returned',
          )
          .lastOrNull;
      if (report != null) {
        initial.addAll(object(report['values']));
        continue;
      }
      final pending = queued.where((row) {
        if (row['kind'] != 'report' || row['received_at'] != null) return false;
        final body = _body(row);
        return body['form'] == form && body['step'] == earlier;
      }).lastOrNull;
      if (pending != null) {
        initial.addAll(object(_body(pending)['values']));
        attachments.addAll(await repo.store.reportFileMetadata(pending['id'] as String));
      }
    }
    final (reference, files) = await (
      repo.savedReference(_slug),
      draft == null ? Future.value(<ReportFile>[]) : repo.store.draftFiles(id),
    ).wait;
    if (!mounted || repo.store.generation != generation) return;
    final raised = _alert.raisedAt;
    final title = assignment['title'] as String;
    final result = await Navigator.of(context).push<bool>(
      MaterialPageRoute(
        builder: (_) => ReportForm(
          definition: definition,
          step: step,
          initial: initial,
          reachedAt: reachedAt,
          canSend: canSend,
          draftValues: object(draftBody['values']),
          draftFiles: files,
          draftLanguage: draftBody['language'] as String?,
          saveDraft: (values, files, language) => repo.saveDraft(
            _slug,
            _alert.id,
            title,
            {'id': id, 'form': form, 'step': step, 'assignment': assignment, 'values': values, 'language': language},
            files,
            generation,
          ),
          attachments: attachments,
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
              'id': id,
              'form': form,
              'version': assignment['version'],
              'step': step,
              'deviceTime': DateTime.now().toUtc().toIso8601String(),
              'location': ?queuedStep?.toJson()['location'],
              'values': values,
            },
            files,
            step: queuedStep,
            fromStep: queuedStep == null ? null : fromStep,
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

  Json _body(Json? row) => row == null ? {} : jsonDecode(row['body'] as String) as Json;

  Widget _actions(List<Json> queue) {
    final history = _history;
    final forms = objects(_reportData?['forms']);
    final reached = {for (final entry in history?.entries ?? <HistoryEntry>[]) entry.step: entry.at};
    var current = _alert.status;
    for (final row in queue) {
      if (row['from_step'] != current) continue;
      if (row['error'] != null && row['retry_at'] == null) break;
      final body = _body(row);
      final step = body['step'] as String;
      current = step;
      final at = body['clientTime'] as String? ?? (row['received_at'] as String?) ?? body['deviceTime'] as String?;
      if (at != null) reached[step] = DateTime.parse(at);
    }
    final moves = (current == _alert.status ? history?.moves : history?.offlineMoves[current]) ?? const [];
    Json? savedFor(Json assignment, {required bool draft}) => queue.where((row) {
      if ((row['kind'] == 'draft') != draft || row['kind'] == 'step') return false;
      final body = _body(row);
      return body['form'] == assignment['form'] && body['step'] == assignment['step'];
    }).lastOrNull;

    final reportActions = <Widget>[];
    final otherReports = <Widget>[];
    final assignedSteps = forms.map((form) => form['step']).toSet();
    final needsCurrentReport = forms.any((form) {
      if (form['step'] != current || form['submitted'] == true) return false;
      final saved = savedFor(form, draft: false);
      return saved == null || (saved['error'] != null && saved['retry_at'] == null);
    });
    for (final assignment in forms) {
      final step = assignment['step'] as String;
      final title = assignment['title'] as String;
      final saved = savedFor(assignment, draft: false);
      final draft = savedFor(assignment, draft: true);
      final sent = assignment['submitted'] == true || saved?['received_at'] != null;
      final atStep = reached.containsKey(step) || step == current;
      final move = moves.where((move) => move.step == step).firstOrNull;
      final canSend = atStep || (move != null && !needsCurrentReport);
      final canDraft = canSend || step == 'first_report' || draft != null;
      if (sent || saved != null) {
        otherReports.add(
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(title, style: Theme.of(context).textTheme.titleSmall),
                ReportDelivery(item: saved, sent: sent),
              ],
            ),
          ),
        );
      } else if (canDraft && _problem == null) {
        final name = title == 'First report' ? 'first report' : title;
        final buttonLabel = draft != null
            ? 'Resume $name draft'
            : canSend
            ? 'File $name'
            : 'Start $name draft';
        void open() => _fill(
          assignment,
          canSend: canSend,
          fromStep: current,
          move: atStep ? null : move,
          draft: draft,
          reachedAt: reached['reached_site'],
        );
        if (canSend) {
          reportActions.add(
            FilledButton.icon(onPressed: open, icon: const Icon(Icons.edit_note), label: Text(buttonLabel)),
          );
        } else {
          otherReports.add(OutlinedButton(onPressed: open, child: Text(buttonLabel)));
        }
      }
    }

    final pending = queue.where((row) => row['kind'] == 'step' && row['received_at'] == null);
    final nextMoves = moves.where((move) => !move.terminal && !assignedSteps.contains(move.step));
    final outcomes = moves.where((move) => move.terminal && !assignedSteps.contains(move.step));
    final text = Theme.of(context).textTheme;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (_problem == null && _reportsProblem == null) ...[
          if (reportActions.isNotEmpty || (!needsCurrentReport && nextMoves.isNotEmpty))
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Text('Next action', style: text.titleMedium),
            ),
          ...reportActions,
          if (!needsCurrentReport)
            for (final move in nextMoves)
              FilledButton.icon(
                onPressed: () => _take(move, current),
                icon: Icon(move.locationRequired ? Icons.my_location : FimraStatusIcon.step),
                label: Text(_actionLabel(move)),
              ),
          if (moves.isEmpty && history != null && reportActions.isEmpty && pending.isEmpty)
            const Text('There are no steps you can take on this alert.'),
        ],
        const SizedBox(height: 8),
        _mapButton(),
        if (pending.isNotEmpty) ...[
          const SizedBox(height: 8),
          for (final row in pending)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('${row['title']}', style: text.titleSmall),
                  ReportDelivery(item: row),
                ],
              ),
            ),
        ],
        if (queue.any((row) => row['kind'] != 'draft' && row['received_at'] == null))
          Align(
            alignment: AlignmentDirectional.centerStart,
            child: OutlinedButton.icon(
              onPressed: () => Navigator.of(
                context,
              ).push(MaterialPageRoute<void>(builder: (_) => OutboxScreen(repo: widget.view.services.offline!))),
              icon: const Icon(Icons.outbox_outlined),
              label: const Text('Report outbox'),
            ),
          ),
        if (_reportsProblem != null) const StatusBadge(icon: Icons.error_outline, label: 'Could not load the forms'),
        ...otherReports,
        if (outcomes.isNotEmpty && _problem == null && _reportsProblem == null)
          ExpansionTile(
            tilePadding: EdgeInsets.zero,
            title: const Text('Other outcomes'),
            children: [
              for (final move in outcomes)
                OutlinedButton(onPressed: () => _take(move, current), child: Text(_actionLabel(move))),
            ],
          ),
        for (final report in objects(_reportData?['submissions']))
          if (object(report['approval'])['decision'] case final String decision)
            StatusBadge(
              icon: decision == 'approved' ? Icons.check_circle_outline : Icons.assignment_return_outlined,
              label: 'Report version ${report['version']} · ${decision == 'approved' ? 'Approved' : 'Returned'}',
            ),
      ],
    );
  }

  Widget _mapButton() => Align(
    alignment: AlignmentDirectional.centerStart,
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
      label: const Text('Show on map'),
    ),
  );

  Widget _actionPanel() {
    final repo = widget.view.services.offline;
    if (repo == null) return _actions(const []);
    return ValueListenableBuilder(
      valueListenable: repo.changes,
      builder: (context, _, _) => FutureBuilder<List<Json>>(
        future: repo.reportStatus(workspace: _slug, alert: _alert.id),
        builder: (context, snapshot) => snapshot.hasData ? _actions(snapshot.data!) : _mapButton(),
      ),
    );
  }

  Future<void> _load() async {
    final repo = widget.view.services.repository;
    try {
      final results = await Future.wait([repo.alert(_slug, _alert.id), repo.history(_slug, _alert.id), _loadReports()]);
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

  Future<void> _take(WorkflowMove move, String fromStep) async {
    final saved = await showModalBottomSheet<bool>(
      context: context,
      isScrollControlled: true,
      useSafeArea: true,
      showDragHandle: true,
      builder: (_) =>
          StepSheet(view: widget.view, alert: _alert, move: move, fromStep: fromStep, locate: widget.locate),
    );
    if (saved != true || !mounted) return;
    _changed = true;
    final message = widget.view.services.offline == null
        ? '${move.label} recorded.'
        : '${move.label} saved to the outbox.';
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(message)));
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
              Semantics(
                headingLevel: 1,
                child: Text('${_alert.placeName(widget.view.map)} · Alert #${_alert.id}', style: text.titleLarge),
              ),
              const SizedBox(height: 8),
              Wrap(
                spacing: 6,
                runSpacing: 6,
                children: [StatusBadge.step(_alert.stepLabel), if (_alert.overdue) StatusBadge.overdue()],
              ),
              const SizedBox(height: 8),
              Text('Seen ${formatTime(_alert.observedAt)}'),
              const SizedBox(height: 8),
              if (widget.view.services.offline case final repo?)
                FieldSyncStatus(repo: repo, workspace: _slug, alert: _alert.id)
              else
                Align(
                  alignment: AlignmentDirectional.centerStart,
                  child: StatusBadge(
                    icon: _problem == null ? Icons.cloud_done_outlined : Icons.cloud_off_outlined,
                    label: _problem != null
                        ? 'Could not connect'
                        : history == null
                        ? 'Connecting'
                        : 'Connected',
                  ),
                ),
              const SizedBox(height: 8),
              _actionPanel(),
              const SizedBox(height: 8),
              _Facts(alert: _alert, map: widget.view.map),
              const SizedBox(height: 24),
              if (_problem != null)
                MessageView(
                  icon: Icons.cloud_off_outlined,
                  title: 'Could not load the timeline',
                  body: 'Check your connection and try again.',
                  action: FilledButton(onPressed: _load, child: const Text('Retry')),
                )
              else if (history == null)
                const Center(child: CircularProgressIndicator())
              else ...[
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
      ('Forest class', alert.forest?.label ?? alert.forestClass),
      for (final level in map.placeOrder(alert.placeHierarchy))
        if (alert.places[level.slug] case final place?) (level.label, '${place.name} (${place.code})'),
      ('Location', '${alert.latitude.toStringAsFixed(5)}, ${alert.longitude.toStringAsFixed(5)}'),
      if (alert.source case final source?) ('Source', sourceLabel(source)),
      if (alert.product case final product?) ('Product', productLabel(product)),
      if (alert.confidence case final confidence?) ('Confidence', confidenceLabel(confidence)),
      if (alert.frpMw case final power?) ('Fire radiative power', '${power.toStringAsFixed(1)} megawatts'),
      if (alert.alertQueue != AlertQueue.unit) ('Queue', alert.alertQueue?.label ?? alert.queue),
    ];
    final text = Theme.of(context).textTheme;
    return Card(
      child: ExpansionTile(
        title: const Text('Details'),
        children: [
          Padding(
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
        ],
      ),
    );
  }
}

String _actionLabel(WorkflowMove move) => switch (move.step) {
  'acknowledged' => 'Acknowledge',
  'reached_site' => 'Mark reached',
  'team_sent' => 'Mark team sent',
  'controlled' => 'Mark controlled',
  'extinguished' => 'Mark extinguished',
  _ => move.label,
};

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
          Icon(FimraStatusIcon.step, size: 20, color: p.textSecondary),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(entry.label, style: text.titleSmall),
                Text(details, style: text.bodySmall),
                if (entry.note.isNotEmpty) Text(entry.note, style: text.bodyMedium),
                if (entry.reason.isNotEmpty) Text('Location exception: ${entry.reason}', style: text.bodyMedium),
                if (entry.reportPending) StatusBadge.step('Report pending'),
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
  const StepSheet({
    super.key,
    required this.view,
    required this.alert,
    required this.move,
    required this.fromStep,
    required this.locate,
  });
  final WorkspaceView view;
  final Alert alert;
  final WorkflowMove move;
  final String fromStep;
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
      if (!mounted) return;
      final request = StepRequest(
        step: move.step,
        clientId: _clientId,
        clientTime: widget.view.services.offline == null ? null : DateTime.now(),
        note: _note.text.trim(),
        reason: _exception ? _reason.text.trim() : '',
        latitude: point?.latitude,
        longitude: point?.longitude,
      );
      final slug = widget.view.workspace.slug;
      final repo = widget.view.services.offline;
      if (repo == null) {
        await widget.view.services.repository.recordStep(slug, widget.alert.id, request);
      } else {
        await repo.queueStep(slug, widget.alert.id, move, widget.fromStep, request);
      }
      if (mounted) Navigator.of(context).pop(true);
    } on LocationProblem catch (error) {
      if (mounted) setState(() => _problem = error.message);
    } on ApiException catch (error) {
      if (mounted) setState(() => _problem = error.message);
    } on StateError {
      if (mounted) setState(() => _problem = 'Could not save this step on the phone.');
    } on Exception {
      if (mounted) setState(() => _problem = 'Could not save this step. Check your location and try again.');
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
              if (widget.view.services.offline != null)
                const Text('The server will confirm this step when it receives it.'),
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
