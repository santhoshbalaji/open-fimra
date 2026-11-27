import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:geolocator/geolocator.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../auth/pkce.dart';
import '../data/field_store.dart';
import '../device/device_store.dart';
import '../forms/answers.dart';
import '../forms/report_form.dart';
import '../l10n/field_language.dart';
import '../theme/app_theme.dart';
import '../theme/status_icons.dart';
import 'alert_map.dart';
import 'home.dart';
import 'outbox_screen.dart';
import 'step_time_field.dart';
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
    throw LocationProblem.serviceOff;
  }
  var permission = await Geolocator.checkPermission();
  if (permission == LocationPermission.denied) permission = await Geolocator.requestPermission();
  if (permission == LocationPermission.denied || permission == LocationPermission.deniedForever) {
    throw LocationProblem.permissionDenied;
  }
}

enum LocationProblem implements Exception {
  serviceOff,
  permissionDenied;

  String message(BuildContext context) => switch (this) {
    serviceOff => fieldText(context).locationIsSwitchedOffSwitch,
    permissionDenied => fieldText(context).locationAccessIsOffAllow,
  };
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
          _reportsProblem = fieldProblem(context, error.message);
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
    String? provisionalId,
    DateTime? reachedAt,
  }) async {
    if (_formOpen) return;
    _formOpen = true;
    try {
      await _openForm(
        assignment,
        canSend: canSend,
        fromStep: fromStep,
        move: move,
        draft: draft,
        provisionalId: provisionalId,
        reachedAt: reachedAt,
      );
    } on Exception {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(fieldText(context).couldNotOpenTheReport)));
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
    String? provisionalId,
    DateTime? reachedAt,
  }) async {
    final repo = widget.view.services.offline!;
    final generation = repo.store.generation;
    final draftBody = _body(draft);
    if (draft != null) assignment = object(draftBody['assignment']);
    final parent = provisionalId ?? draftBody['provisionalSubmissionId'] as String?;
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
    final title = parent == null ? assignment['title'] as String : fieldText(context).revisedDamageAssessment;
    final result = await Navigator.of(context).push<bool>(
      MaterialPageRoute(
        builder: (_) => ReportForm(
          definition: definition,
          step: step,
          initial: initial,
          reachedAt: reachedAt,
          titleOverride: parent == null ? null : title,
          canSend: canSend,
          draftValues: object(draftBody['values']),
          draftFiles: files,
          draftLanguage: draftBody['language'] as String?,
          saveDraft: (values, files, language) => repo.saveDraft(
            _slug,
            _alert.id,
            title,
            {
              'id': id,
              'form': form,
              'step': step,
              'assignment': assignment,
              'values': values,
              'language': language,
              'provisionalSubmissionId': ?parent,
            },
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
              'provisionalSubmissionId': ?parent,
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
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(fieldText(context).reportSavedToSendLater)));
    }
  }

  Json _body(Json? row) => row == null ? {} : jsonDecode(row['body'] as String) as Json;

  Widget _actions(List<Json> queue) {
    final history = _history;
    final forms = objects(_reportData?['forms']);
    final reached = {
      for (final entry in history?.entries ?? <HistoryEntry>[])
        entry.step: entry.actualAt ?? entry.estimatedAt ?? entry.at,
    };
    var current = _alert.status;
    for (final row in queue) {
      if (row['from_step'] != current) continue;
      if (row['error'] != null && row['retry_at'] == null) break;
      final body = _body(row);
      final step = body['step'] as String;
      if (step == 'response_update') continue;
      current = step;
      final at =
          (body['actualAt'] ??
                  (step == 'team_sent' ? null : body['estimatedAt']) ??
                  body['clientTime'] ??
                  body['at'] ??
                  row['received_at'] ??
                  body['deviceTime'])
              as String?;
      if (at != null) reached[step] = DateTime.parse(at);
    }
    final moves = (current == _alert.status ? history?.moves : history?.offlineMoves[current]) ?? const [];
    final submissions = objects(_reportData?['submissions']);
    Json? savedFor(Json assignment, {required bool draft, String? provisionalId}) => queue.where((row) {
      if ((row['kind'] == 'draft') != draft || row['kind'] == 'step') return false;
      if (row['received_at'] != null &&
          (assignment['form'] == 'response-update' ||
              submissions.any(
                (report) => report['id'] == row['id'] && object(report['approval'])['decision'] == 'returned',
              ))) {
        return false;
      }
      final body = _body(row);
      return body['form'] == assignment['form'] &&
          body['step'] == assignment['step'] &&
          body['provisionalSubmissionId'] == provisionalId;
    }).lastOrNull;

    final reportActions = <Widget>[];
    final otherReports = <Widget>[];
    final assignedSteps = forms.map((form) => form['step']).toSet();
    final needsCurrentReport = forms.any((form) {
      if (form['step'] != current || form['submitted'] == true) return false;
      final saved =
          savedFor(form, draft: false) ??
          submissions
              .where((report) => report['form'] == form['form'])
              .map((report) => savedFor(form, draft: false, provisionalId: report['id'] as String))
              .nonNulls
              .firstOrNull;
      return saved == null || (saved['error'] != null && saved['retry_at'] == null);
    });
    for (final assignment in forms) {
      final step = assignment['step'] as String;
      final title = assignment['title'] as String;
      final saved = savedFor(assignment, draft: false);
      final draft = savedFor(assignment, draft: true);
      final sent = assignment['submitted'] == true || saved?['received_at'] != null;
      final repeatable = assignment['form'] == 'response-update';
      final atStep = reached.containsKey(step) || step == current;
      final move = moves.where((move) => move.step == step).firstOrNull;
      final canSend = atStep || (move != null && !needsCurrentReport);
      final canDraft = canSend || step == 'first_report' || draft != null;
      final provisional = submissions.where(
        (report) => report['form'] == 'damage-assessment' && report['provisionalAssessment'] == null,
      );
      if (assignment['form'] == 'damage-assessment' && atStep && _problem == null) {
        for (final report in provisional) {
          final parent = report['id'] as String;
          final revisionDraft = savedFor(assignment, draft: true, provisionalId: parent);
          final pendingRevision = savedFor(assignment, draft: false, provisionalId: parent);
          final name = fieldText(context).revisedDamageAssessment;
          final area = object(report['values'])['area_ha'];
          if (pendingRevision != null) {
            otherReports.add(ReportDelivery(item: pendingRevision));
            continue;
          }
          reportActions.add(
            OutlinedButton(
              onPressed: () =>
                  _fill(assignment, canSend: true, fromStep: current, draft: revisionDraft, provisionalId: parent),
              child: Text(
                revisionDraft != null
                    ? fieldText(context).resumeDraft(name)
                    : area is num
                    ? fieldText(context).reviseAssessmentArea(formatNumber(context, area))
                    : name,
              ),
            ),
          );
        }
      }
      if ((sent && !repeatable) || saved != null) {
        otherReports.add(
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(workflowName(context, title), style: Theme.of(context).textTheme.titleSmall),
                ReportDelivery(item: saved, sent: sent),
              ],
            ),
          ),
        );
      } else if (canDraft && _problem == null && (assignment['form'] != 'damage-assessment' || provisional.isEmpty)) {
        final name = title == 'First report' ? fieldText(context).firstReportActionName : workflowName(context, title);
        final buttonLabel = draft != null
            ? fieldText(context).resumeDraft(name)
            : canSend
            ? fieldText(context).startReport(name)
            : fieldText(context).startDraft(name);
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
              child: Text(fieldText(context).nextAction, style: text.titleMedium),
            ),
          ...reportActions,
          if (!needsCurrentReport)
            for (final move in nextMoves)
              FilledButton.icon(
                onPressed: () => _take(move, current),
                icon: Icon(move.locationRequired ? Icons.my_location : FimraStatusIcon.step),
                label: Text(_actionLabel(context, move)),
              ),
          if (moves.isEmpty && history != null && reportActions.isEmpty && pending.isEmpty)
            Text(fieldText(context).thereAreNoStepsYou),
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
              label: Text(fieldText(context).reportsWaitingToSend),
            ),
          ),
        if (_reportsProblem != null)
          StatusBadge(icon: Icons.error_outline, label: fieldText(context).couldNotLoadTheForms),
        ...otherReports,
        if (outcomes.isNotEmpty && _problem == null && _reportsProblem == null)
          ExpansionTile(
            tilePadding: EdgeInsets.zero,
            title: Text(fieldText(context).otherOutcomes),
            children: [
              for (final move in outcomes)
                OutlinedButton(onPressed: () => _take(move, current), child: Text(_actionLabel(context, move))),
            ],
          ),
        for (final report in objects(_reportData?['submissions']))
          if (object(report['approval'])['decision'] case final String decision)
            StatusBadge(
              icon: decision == 'approved' ? Icons.check_circle_outline : Icons.assignment_return_outlined,
              label: fieldText(context).reportVersion(
                (report['version']).toString(),
                decision == 'approved' ? fieldText(context).approved : fieldText(context).returned,
              ),
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
            appBar: AppBar(title: Text(fieldText(context).alertMap)),
            body: AlertMapView(view: widget.view, focus: _alert, onOpen: (_) {}),
          ),
        ),
      ),
      icon: const Icon(Icons.map_outlined),
      label: Text(fieldText(context).showOnMap),
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
      if (mounted) setState(() => _problem = fieldProblem(context, error.message));
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
        ? fieldText(context).stepRecorded(workflowName(context, move.label))
        : fieldText(context).stepSavedToSendLater(workflowName(context, move.label));
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
        appBar: AppBar(title: Text(fieldText(context).alertDetails)),
        body: RefreshIndicator(
          onRefresh: _load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              Semantics(
                headingLevel: 1,
                child: Text(
                  fieldText(context).alertAtPlace(_alert.placeName(widget.view.map), _alert.id),
                  style: text.titleLarge,
                ),
              ),
              const SizedBox(height: 8),
              Wrap(
                spacing: 6,
                runSpacing: 6,
                children: [
                  StatusBadge.step(workflowName(context, _alert.stepLabel)),
                  if (_alert.overdue) StatusBadge.overdue(context),
                ],
              ),
              const SizedBox(height: 8),
              Text(fieldText(context).seenAt(formatTime(_alert.observedAt, context))),
              const SizedBox(height: 8),
              if (widget.view.services.offline case final repo?)
                FieldSyncStatus(repo: repo, workspace: _slug, alert: _alert.id)
              else
                Align(
                  alignment: AlignmentDirectional.centerStart,
                  child: StatusBadge(
                    icon: _problem == null ? Icons.cloud_done_outlined : Icons.cloud_off_outlined,
                    label: _problem != null
                        ? fieldText(context).couldNotConnect
                        : history == null
                        ? fieldText(context).connecting
                        : fieldText(context).connected,
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
                  title: fieldText(context).couldNotLoadTheTimeline,
                  body: fieldText(context).checkYourConnectionAndTry,
                  action: FilledButton(onPressed: _load, child: Text(fieldText(context).retry)),
                )
              else if (history == null)
                const Center(child: CircularProgressIndicator())
              else ...[
                Text(fieldText(context).timeline, style: text.titleMedium),
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
      (fieldText(context).forestClass, forestName(context, alert.forest, alert.forestClass)),
      for (final level in map.placeOrder(alert.placeHierarchy))
        if (alert.places[level.slug] case final place?)
          (
            formText(level.labels, Localizations.localeOf(context).toLanguageTag()).isEmpty
                ? level.slug
                : formText(level.labels, Localizations.localeOf(context).toLanguageTag()),
            '${place.name} (${place.code})',
          ),
      (fieldText(context).location, '${alert.latitude.toStringAsFixed(5)}, ${alert.longitude.toStringAsFixed(5)}'),
      if (alert.source case final source?) (fieldText(context).source, sourceLabel(context, source)),
      if (alert.product case final product?) (fieldText(context).product, productLabel(context, product)),
      if (alert.confidence case final confidence?)
        (fieldText(context).confidence, confidenceLabel(confidence, context)),
      if (alert.frpMw case final power?)
        (
          fieldText(context).fireStrength,
          fieldText(context).megawatts(formatNumber(context, power, fractionDigits: 1)),
        ),
      if (alert.alertQueue != AlertQueue.unit)
        (fieldText(context).queue, queueName(context, alert.alertQueue, alert.queue)),
    ];
    final text = Theme.of(context).textTheme;
    return Card(
      child: ExpansionTile(
        title: Text(fieldText(context).details),
        children: [
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (alert.source == 'cdse')
                  Text(fieldText(context).copernicusAttribution(alert.observedAt.toUtc().year), style: text.bodySmall),
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

String _actionLabel(BuildContext context, WorkflowMove move) => switch (move.step) {
  'acknowledged' => fieldText(context).acknowledgeAlert,
  'reached_site' => fieldText(context).markReached,
  'team_sent' => fieldText(context).markTeamSent,
  'controlled' => fieldText(context).markControlled,
  'extinguished' => fieldText(context).markExtinguished,
  _ => workflowName(context, move.label),
};

class _TimelineEntry extends StatelessWidget {
  const _TimelineEntry(this.entry);
  final HistoryEntry entry;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final p = FimraColours.of(context);
    final words = fieldText(context);
    final response = entry.response;
    final nextContact = DateTime.tryParse(response?['nextContact'] as String? ?? '');
    final details = [
      formatTime(entry.actualAt ?? (entry.step == 'team_sent' ? null : entry.estimatedAt) ?? entry.at, context),
      entry.actor ?? fieldText(context).system,
      if (entry.latitude != null) fieldText(context).withLocation,
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
                Text(workflowName(context, entry.label), style: text.titleSmall),
                Text(details, style: text.bodySmall),
                if (entry.note.isNotEmpty) Text(entry.note, style: text.bodyMedium),
                if (entry.reason.isNotEmpty)
                  Text(
                    entry.step == 'reopened' ? words.reopeningReason : words.locationExceptionReason(entry.reason),
                    style: text.bodyMedium,
                  ),
                if (entry.reason.isNotEmpty && entry.step == 'reopened') Text(entry.reason, style: text.bodyMedium),
                if (entry.estimatedAt case final estimate?)
                  Text(
                    '${entry.step == 'team_sent' ? words.estimatedArrival : words.estimatedStepTime} · ${formatTime(estimate, context)}',
                    style: text.bodyMedium,
                  ),
                if (entry.syncedAt case final synced? when entry.actualAt != null || entry.estimatedAt != null)
                  Text(words.lastSynced(formatTime(synced, context)), style: text.bodySmall),
                if (response != null)
                  for (final (key, label) in [
                    ('status', words.fireStatus),
                    ('estimatedExtent', words.estimatedExtent),
                    ('threats', words.responseThreats),
                    ('helpNeeded', words.helpNeeded),
                  ])
                    if (response[key] case final String value when value.isNotEmpty)
                      Text('$label · $value', style: text.bodyMedium),
                if (nextContact != null)
                  Text('${words.nextContact} · ${formatTime(nextContact, context)}', style: text.bodyMedium),
                if (entry.reportPending) StatusBadge.step(fieldText(context).waitingForAReport),
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
  final _status = TextEditingController();
  final _extent = TextEditingController();
  final _threats = TextEditingController();
  final _help = TextEditingController();
  DateTime? _actualAt;
  DateTime? _estimatedAt;
  DateTime? _nextContact;
  bool get _isUpdate => widget.move.step == 'flash_update' || widget.move.step == 'response_update';

  /// One ID per attempt, so pressing Save again after a lost answer is safe.
  final _clientId = randomToken(18);
  DateTime? _clientTime;
  bool _exception = false;
  bool _busy = false;
  String? _problem;

  @override
  void dispose() {
    _note.dispose();
    _reason.dispose();
    _status.dispose();
    _extent.dispose();
    _threats.dispose();
    _help.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    final move = widget.move;
    if ((_exception || move.step == 'reopened') && _reason.text.trim().isEmpty) {
      setState(() => _problem = fieldText(context).giveAReasonForRecording);
      return;
    }
    final words = fieldText(context);
    if (_isUpdate && _status.text.trim().isEmpty) {
      setState(() => _problem = words.fireStatusRequired);
      return;
    }
    if (move.step == 'assigned' && _note.text.trim().isEmpty) {
      setState(() => _problem = words.assigneeRequired);
      return;
    }
    if (move.extended && move.step == 'team_sent' && _estimatedAt == null) {
      setState(() => _problem = words.estimatedArrivalRequired);
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
        clientTime: widget.view.services.offline == null ? null : (_clientTime ??= DateTime.now()),
        actualAt: _actualAt,
        estimatedAt: _estimatedAt,
        response: _isUpdate
            ? {
                'status': _status.text.trim(),
                if (_extent.text.trim().isNotEmpty) 'estimatedExtent': _extent.text.trim(),
                if (_threats.text.trim().isNotEmpty) 'threats': _threats.text.trim(),
                if (_help.text.trim().isNotEmpty) 'helpNeeded': _help.text.trim(),
                if (_nextContact != null) 'nextContact': _nextContact!.toUtc().toIso8601String(),
              }
            : null,
        note: _note.text.trim(),
        reason: _exception || move.step == 'reopened' ? _reason.text.trim() : '',
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
      if (mounted) setState(() => _problem = error.message(context));
    } on ApiException catch (error) {
      if (mounted) setState(() => _problem = fieldProblem(context, error.message));
    } on StateError {
      if (mounted) setState(() => _problem = fieldText(context).couldNotSaveThisStep);
    } on Exception {
      if (mounted) setState(() => _problem = fieldText(context).couldNotSaveThisStep2);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final move = widget.move;
    final words = fieldText(context);
    return Padding(
      padding: EdgeInsets.fromLTRB(16, 0, 16, 16 + MediaQuery.viewInsetsOf(context).bottom),
      child: SafeArea(
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(workflowName(context, move.label), style: text.titleLarge),
              const SizedBox(height: 8),
              Text(
                move.terminal
                    ? fieldText(context).thisEndsTheWorkflowFor
                    : move.locationRequired
                    ? fieldText(context).yourCurrentLocationIsSaved
                    : fieldText(context).thisIsSavedWithYour,
                style: text.bodyMedium,
              ),
              const SizedBox(height: 16),
              TextField(
                controller: _note,
                maxLines: 3,
                minLines: 1,
                decoration: InputDecoration(
                  labelText: move.step == 'assigned' ? words.assignedTeam : words.noteOptional,
                ),
              ),
              if (move.extended) ...[
                StepTimeField(
                  label: words.actualStepTime,
                  value: _actualAt,
                  onChanged: _busy ? null : (value) => setState(() => _actualAt = value),
                ),
                StepTimeField(
                  label: move.step == 'team_sent' ? words.estimatedArrival : words.estimatedStepTime,
                  allowFuture: move.step == 'team_sent',
                  value: _estimatedAt,
                  onChanged: _busy ? null : (value) => setState(() => _estimatedAt = value),
                ),
              ],
              if (_isUpdate) ...[
                for (final (controller, label) in [
                  (_status, words.fireStatus),
                  (_extent, words.estimatedExtent),
                  (_threats, words.responseThreats),
                  (_help, words.helpNeeded),
                ])
                  TextField(
                    controller: controller,
                    enabled: !_busy,
                    maxLength: 300,
                    decoration: InputDecoration(labelText: label),
                  ),
                StepTimeField(
                  label: words.nextContact,
                  allowFuture: true,
                  value: _nextContact,
                  onChanged: _busy ? null : (value) => setState(() => _nextContact = value),
                ),
              ],
              if (move.step == 'reopened')
                TextField(
                  controller: _reason,
                  decoration: InputDecoration(labelText: words.reopeningReason),
                ),
              if (move.locationRequired && move.canOverride) ...[
                const SizedBox(height: 8),
                CheckboxListTile(
                  contentPadding: EdgeInsets.zero,
                  value: _exception,
                  onChanged: _busy ? null : (value) => setState(() => _exception = value ?? false),
                  title: Text(fieldText(context).recordWithoutMyLocation),
                  subtitle: Text(fieldText(context).forSupervisingOfficersAReason),
                ),
                if (_exception)
                  TextField(
                    controller: _reason,
                    decoration: InputDecoration(labelText: fieldText(context).reason),
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
              if (widget.view.services.offline != null) Text(fieldText(context).theServerWillConfirmThis),
              FilledButton(
                onPressed: _busy ? null : _save,
                child: Text(_busy ? fieldText(context).saving : fieldText(context).save),
              ),
              const SizedBox(height: 8),
              TextButton(
                onPressed: _busy ? null : () => Navigator.of(context).pop(false),
                child: Text(fieldText(context).cancel),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
