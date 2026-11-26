import 'dart:async';
import 'dart:convert';
import 'dart:ui' as ui;

import 'package:file_selector/file_selector.dart';
import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';
import 'package:intl/intl.dart';

import '../api/models.dart';
import '../data/field_store.dart';
import '../device/device_store.dart';
import '../l10n/app_localizations.dart';
import '../l10n/field_language.dart';
import '../ui/alert_detail.dart';
import '../ui/widgets.dart' show formatNumber;
import 'answers.dart';
import 'geometry_field.dart';
import 'report_media_cache.dart';

typedef SaveReport = Future<void> Function(Json values, List<ReportFile> files, StepRequest? step);
typedef SaveDraft = Future<void> Function(Json values, List<ReportFile> files, String? language);
typedef PickReportFile = Future<ReportFile?> Function(String source);

class ReportForm extends StatefulWidget {
  const ReportForm({
    super.key,
    required this.definition,
    required this.step,
    required this.alert,
    required this.save,
    this.initial = const {},
    this.draftValues = const {},
    this.draftFiles = const [],
    this.draftLanguage,
    this.saveDraft,
    this.canSend = true,
    this.reachedAt,
    this.attachments = const {},
    this.move,
    this.reference,
    this.pick,
    this.locate = deviceLocation,
  });

  final Json definition;
  final String step;
  final Json alert;
  final Json initial;
  final Json draftValues;
  final List<ReportFile> draftFiles;
  final String? draftLanguage;
  final SaveDraft? saveDraft;
  final bool canSend;
  final DateTime? reachedAt;
  final Json attachments;
  final WorkflowMove? move;
  final Json? reference;
  final SaveReport save;
  final PickReportFile? pick;
  final Locate locate;

  @override
  State<ReportForm> createState() => _ReportFormState();
}

class _ReportFormState extends State<ReportForm> {
  late final FormTree _tree = FormTree(widget.definition);
  late final Json _values = {
    ..._earlier(widget.initial, _tree.members),
    if (widget.reachedAt case final time?)
      if (_tree.byKey['reached_at']?.kind == 'date-time') 'reached_at': time.toUtc().toIso8601String(),
    ...widget.draftValues,
  };
  late final _files = {for (final file in widget.draftFiles) file.id: file};
  late String? _language = widget.draftLanguage;
  Future<void>? _draftWrite;
  bool _draftDirty = false;
  String? _error;
  bool _busy = false;
  bool _tried = false;

  Json _earlier(Json raw, List<FormNode> members) => {
    for (final node in members)
      if (!node.computed && node.stage < (_tree.steps[widget.step] ?? -1) && raw.containsKey(node.key))
        node.key: node.kind == 'repeat'
            ? objects(raw[node.key]).map((entry) => _earlier(entry, node.members)).toList()
            : raw[node.key],
  };

  Json _metadata() => {...widget.attachments, for (final file in _files.values) file.id: file.metadata};

  AnswerSession _session([Json? values]) =>
      AnswerSession(_tree, values ?? _values, step: widget.step, alert: widget.alert, attachments: _metadata());

  String get _lang => _language ?? Localizations.localeOf(context).toLanguageTag();
  String _text(Object? value) => formText(value, _lang);

  Json _scope(EntryPath path) {
    var scope = _values;
    for (final (key, i) in path) {
      scope = (scope[key] as List)[i] as Json;
    }
    return scope;
  }

  void _edit(Json row, String key, Object? value) {
    if (value == null) {
      row.remove(key);
    } else {
      row[key] = value;
    }
  }

  void _change(Json row, String key, Object? value) {
    setState(() {
      _edit(row, key, value);
      final session = _session();
      _discardUnavailableChoices(session, _tree.members, _values, []);
      final referenced = <String>{};
      void visit(Object? value) {
        if (value is Map) {
          value.values.forEach(visit);
        } else if (value is List) {
          value.forEach(visit);
        } else if (value is String && _files.containsKey(value)) {
          referenced.add(value);
        }
      }

      visit(_values);
      _files.removeWhere((id, _) => !referenced.contains(id));
    });
    _draftDirty = true;
    unawaited(_persistDraft());
  }

  Future<void> _persistDraft() {
    final save = widget.saveDraft;
    if (save == null) return Future.value();
    final prior = _draftWrite;
    final values = jsonDecode(jsonEncode(_values)) as Json;
    final files = _files.values.toList();
    final language = _lang;
    final write = _writeDraft(prior, save, values, files, language);
    _draftWrite = write;
    return write;
  }

  Future<void> _writeDraft(
    Future<void>? prior,
    SaveDraft save,
    Json values,
    List<ReportFile> files,
    String language,
  ) async {
    await prior;
    try {
      await save(values, files, language);
      if (mounted && !_busy) setState(() => _error = null);
    } on Exception {
      _draftDirty = true;
      if (mounted) setState(() => _error = fieldText(context).couldNotSaveYourDraft);
    } on StateError {
      _draftDirty = true;
      if (mounted) setState(() => _error = fieldText(context).couldNotSaveYourDraft2);
    }
  }

  Future<void> _leave({bool save = false}) async {
    if (_busy) return;
    setState(() => _busy = true);
    final shouldSave = save || _draftDirty;
    _draftDirty = false;
    if (shouldSave) await _persistDraft();
    if (!mounted) return;
    setState(() => _busy = false);
    if (!_draftDirty) Navigator.of(context).pop();
  }

  void _discardUnavailableChoices(AnswerSession session, List<FormNode> members, Json values, EntryPath path) {
    for (final node in members) {
      if (node.kind == 'repeat') {
        for (final (i, entry) in objects(values[node.key]).indexed) {
          _discardUnavailableChoices(session, node.members, entry, [...path, (node.key, i)]);
        }
      } else if (!session.readOnly(node) && node.data['parent'] != null && values.containsKey(node.key)) {
        final available = session.choices(node, path).map((choice) => choice['key']).toSet();
        final value = values[node.key];
        if (value is List) {
          values[node.key] = value.where(available.contains).toList();
        } else if (!available.contains(value)) {
          values.remove(node.key);
        }
      }
    }
  }

  Json _visibleValues(AnswerSession session, List<FormNode> members, Json values, EntryPath path) => {
    for (final node in members)
      if (!node.computed && values.containsKey(node.key) && session.visible(node, path))
        node.key: node.kind == 'repeat'
            ? [
                for (final (i, row) in objects(values[node.key]).indexed)
                  _visibleValues(session, node.members, row, [...path, (node.key, i)]),
              ]
            : values[node.key],
  };

  AnswerSession _validate(AnswerSession display) => _session(_visibleValues(display, _tree.members, _values, []));

  Future<void> _save() async {
    final checked = _validate(_session());
    setState(() {
      _tried = true;
      _error = null;
    });
    final answers = checked.answers;
    if (answers == null) {
      if (_scroll.hasClients) {
        unawaited(_scroll.animateTo(0, duration: const Duration(milliseconds: 220), curve: Curves.easeOut));
      }
      return;
    }
    setState(() => _busy = true);
    try {
      await _draftWrite;
      if (!mounted) return;
      final move = widget.move;
      StepRequest? step;
      if (move != null) {
        final fix = move.locationRequired ? await widget.locate() : null;
        if (!mounted) return;
        step = StepRequest(
          step: move.step,
          clientId: newDeviceId(),
          clientTime: DateTime.now(),
          latitude: fix?.latitude,
          longitude: fix?.longitude,
        );
      }
      await widget.save(answers, [for (final id in checked.attachmentIds) ?_files[id]], step);
      if (mounted) Navigator.of(context).pop(true);
    } on LocationProblem catch (error) {
      if (mounted) setState(() => _error = error.message(context));
    } on Exception {
      if (mounted) setState(() => _error = fieldText(context).couldNotSaveThisReport);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  final _scroll = ScrollController();

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final session = _session();
    final problems = _tried ? _validate(session).problems : const <String>[];
    final languages =
        (widget.definition['languages'] as List?)?.cast<String>() ??
        ({'en', ..._translations(widget.definition)}.toList()..sort());
    final layout = object(widget.definition['layout']);
    final settings = {for (final item in objects(layout['items'])) item['item'] as String: item};
    final pages = objects(layout['pages']);
    final description = _text(widget.definition['description']);
    return PopScope<Object?>(
      canPop: widget.saveDraft == null,
      onPopInvokedWithResult: (didPop, _) {
        if (!didPop) unawaited(_leave());
      },
      child: Scaffold(
        appBar: AppBar(title: Text(fieldText(context).fieldReport)),
        body: SafeArea(
          child: ListView(
            controller: _scroll,
            padding: const EdgeInsets.all(16),
            children: [
              Semantics(
                headingLevel: 1,
                child: Text(_text(widget.definition['title']), style: Theme.of(context).textTheme.titleLarge),
              ),
              if (description.isNotEmpty) Text(description),
              const SizedBox(height: 12),
              if (languages.length > 1)
                DropdownButtonFormField<String>(
                  key: ValueKey(_lang),
                  initialValue: languages.contains(_lang)
                      ? _lang
                      : languages.contains(_lang.split('-').first)
                      ? _lang.split('-').first
                      : languages.first,
                  decoration: InputDecoration(labelText: fieldText(context).formLanguage),
                  items: [
                    for (final lang in languages) DropdownMenuItem(value: lang, child: Text(fieldLanguageName(lang))),
                  ],
                  onChanged: _busy
                      ? null
                      : (value) {
                          setState(() => _language = value);
                          _draftDirty = true;
                          unawaited(_persistDraft());
                        },
                ),
              if (problems.isNotEmpty)
                Semantics(
                  liveRegion: true,
                  child: Card(
                    child: Padding(
                      padding: const EdgeInsets.all(16),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(fieldText(context).checkTheseAnswers, style: Theme.of(context).textTheme.titleMedium),
                          for (final problem in problems) Text(_explain(problem)),
                        ],
                      ),
                    ),
                  ),
                ),
              if (pages.isEmpty)
                for (final section in _tree.sections) _node(session, section, [], settings)
              else
                for (final page in pages)
                  if ((page['sections'] as List).map((key) => _tree.byKey[key]!).any(session.visible)) ...[
                    if (_text(page['title']).isNotEmpty)
                      Text(_text(page['title']), style: Theme.of(context).textTheme.titleLarge),
                    for (final key in page['sections'] as List) _node(session, _tree.byKey[key]!, [], settings),
                  ],
              if (widget.move case final move?)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 12),
                  child: Text(fieldText(context).serverChecksStep(workflowName(context, move.label))),
                ),
              if (!widget.canSend) Text(fieldText(context).saveADraftNowFinish),
              if (_error != null)
                Padding(
                  padding: const EdgeInsets.only(bottom: 12),
                  child: Semantics(
                    liveRegion: true,
                    child: Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
                  ),
                ),
              if (widget.saveDraft != null)
                OutlinedButton(
                  onPressed: _busy ? null : () => _leave(save: true),
                  child: Text(fieldText(context).saveDraft),
                ),
              if (widget.canSend)
                FilledButton(
                  onPressed: _busy ? null : _save,
                  child: Text(
                    _busy
                        ? fieldText(context).savingReport
                        : widget.saveDraft == null
                        ? fieldText(context).saveToSendLater
                        : fieldText(context).sendWhenConnected,
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }

  Iterable<String> _translations(Object? node) sync* {
    if (node is List) {
      for (final item in node) {
        yield* _translations(item);
      }
    } else if (node is Json) {
      for (final entry in node.entries) {
        if (['label', 'title', 'hint', 'description', 'itemLabel', 'patternHint'].contains(entry.key)) {
          yield* object(entry.value).keys;
        } else {
          yield* _translations(entry.value);
        }
      }
    }
  }

  String _explain(String problem) {
    final explained = problem.replaceAllMapped(RegExp(r'"([a-z][a-z0-9_]*)"'), (match) {
      final item = _tree.byKey[match[1]];
      final label = _text(item?.data['label'] ?? item?.data['title']);
      return label.isEmpty ? match[0]! : '“$label”';
    });
    if (Localizations.localeOf(context).languageCode == 'en') return explained;
    final match = RegExp(r'^(.*?)(?:Field|Repeating group) (“[^”]+”|[^ ]+) (.+)$').firstMatch(explained);
    if (match == null) return fieldProblem(context, explained);
    final text = fieldText(context);
    final field = match[2]!;
    final clause = match[3]!;
    final limit = RegExp(r'^(needs at least|can have at most) ([0-9.]+)(?: (.+))?\.$').firstMatch(clause);
    final result = switch (clause) {
      'is required.' => text.answerRequired(field),
      'cannot be in the future.' => text.answerFuture(field),
      _ when clause.startsWith('does not match the expected pattern') => text.answerFormat(field),
      _ when limit != null =>
        limit[1] == 'needs at least'
            ? text.answerMin(field, formatNumber(context, num.parse(limit[2]!)))
            : text.answerMax(field, formatNumber(context, num.parse(limit[2]!))),
      _ => text.answerInvalid(field),
    };
    return '${match[1]}$result';
  }

  bool _hasRequired(AnswerSession session, FormNode node, EntryPath path) {
    if (!session.visible(node, path) || session.readOnly(node)) return false;
    if (session.required(node, path)) return true;
    if (node.kind == 'repeat') {
      if ((node.data['minRepeats'] as int? ?? 0) > 0) return true;
      return objects(_scope(path)[node.key]).indexed.any(
        (entry) => node.children.any((child) => _hasRequired(session, child, [...path, (node.key, entry.$1)])),
      );
    }
    return node.children.any((child) => _hasRequired(session, child, path));
  }

  Widget _node(AnswerSession session, FormNode node, EntryPath path, Map<String, Json> layout) {
    if (!session.visible(node, path)) return const SizedBox.shrink();
    final data = node.data;
    final label = _text(data[node.field ? 'label' : 'title']);
    final hint = _text(data[node.field ? 'hint' : 'description']);
    final readonly = session.readOnly(node);
    final row = _scope(path);
    final value = row[node.key];
    final setting = layout[node.key];
    void set(Object? next) => _change(row, node.key, next);
    if (!node.field) {
      final entries = objects(value);
      final max = data['maxRepeats'] as int?;
      final entryLabel = _text(data['itemLabel']);
      final children = [
        if (hint.isNotEmpty) Text(hint),
        if (node.kind == 'repeat') ...[
          for (final (index, entry) in entries.indexed)
            Card(
              key: ObjectKey(entry),
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    ListTile(
                      contentPadding: EdgeInsets.zero,
                      title: Text('${entryLabel.isEmpty ? label : entryLabel} ${index + 1}'),
                      trailing: readonly
                          ? null
                          : IconButton(
                              tooltip: fieldText(context).removeEntry((index + 1).toString()),
                              icon: const Icon(Icons.remove_circle_outline),
                              onPressed: _busy ? null : () => set([...entries]..removeAt(index)),
                            ),
                    ),
                    for (final item in node.children) _node(session, item, [...path, (node.key, index)], layout),
                  ],
                ),
              ),
            ),
          if (!readonly)
            OutlinedButton(
              onPressed: _busy || (max != null && entries.length >= max)
                  ? null
                  : () => set([...entries, <String, dynamic>{}]),
              child: Text(fieldText(context).addEntry((entryLabel.isEmpty ? 'entry' : entryLabel).toString())),
            ),
        ] else
          for (final child in node.children) _node(session, child, path, layout),
      ];
      return Padding(
        key: ValueKey(node.key),
        padding: const EdgeInsets.symmetric(vertical: 10),
        child: setting?['collapsed'] == true && !_hasRequired(session, node, path)
            ? ExpansionTile(title: Text(label), children: children)
            : Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Text(label, style: Theme.of(context).textTheme.titleMedium),
                  ...children,
                ],
              ),
      );
    }

    final title = session.required(node, path) ? '$label *' : label;
    final control = setting?['widget'];
    final unit = data['unit'] as String?;
    final shown = session.value(node, path);
    final offered = session.choices(node, path);
    final widget = readonly
        ? ListTile(
            contentPadding: EdgeInsets.zero,
            title: Text(title),
            subtitle: Text(_display(node, shown, session.tree.choices(node))),
          )
        : switch (node.kind) {
            'text' || 'number' => _input(node, title, hint, unit, value, control, set),
            'single-choice' || 'multiple-choice' => _choice(node, title, hint, value, offered, control, set),
            'date' || 'time' || 'date-time' => Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(title),
                if (hint.isNotEmpty) Text(hint),
                Wrap(
                  spacing: 8,
                  children: [
                    OutlinedButton(
                      onPressed: _busy ? null : () => _chooseTime(node.kind, value, set),
                      child: Text(value == null ? fieldText(context).chooseAnswer(label) : _display(node, value, [])),
                    ),
                    if (value != null)
                      TextButton(onPressed: _busy ? null : () => set(null), child: Text(fieldText(context).clear)),
                  ],
                ),
              ],
            ),
            'point' || 'track' || 'polygon' => GeometryField(
              label: title,
              hint: hint,
              kind: node.kind,
              value: value as Json?,
              anchor: object(this.widget.alert['location']),
              reference: this.widget.reference,
              maxAccuracy: data['maxAccuracyMetres'] as num?,
              changed: _busy ? null : set,
            ),
            'photo' || 'signature' || 'file' => _attachment(node, title, hint, value, set),
            _ => const SizedBox.shrink(),
          };

    return Padding(key: ValueKey(node.key), padding: const EdgeInsets.symmetric(vertical: 8), child: widget);
  }

  Widget _input(
    FormNode node,
    String title,
    String hint,
    String? unit,
    Object? value,
    Object? control,
    ValueChanged<Object?> set,
  ) {
    final number = node.kind == 'number';
    final input = TextFormField(
      key: ValueKey(node.key),
      initialValue: value is num ? _display(node, value, [], showUnit: false) : value?.toString(),
      enabled: !_busy,
      decoration: InputDecoration(suffixText: unit),
      maxLines: control == 'paragraph' ? 3 : 1,
      keyboardType: number ? const TextInputType.numberWithOptions(decimal: true, signed: true) : TextInputType.text,
      onChanged: (text) => set(
        text.isEmpty
            ? null
            : number
            ? num.tryParse(text) ?? text
            : text,
      ),
    );
    if (control != 'stepper') return _FieldLabel(title: title, hint: hint, child: input);
    final places = node.data['decimals'] as int? ?? 0;
    final increment = num.parse('1e-$places');
    final current = value is num ? value : 0;
    void adjust(num change) => set(num.parse((current + change).toStringAsFixed(places)));
    return Row(
      children: [
        IconButton(
          tooltip: fieldText(context).decreaseValue(title),
          onPressed: _busy ? null : () => adjust(-increment),
          icon: const Icon(Icons.remove),
        ),
        Expanded(
          child: ListTile(
            title: Text(title),
            subtitle: Text(value == null ? fieldText(context).noAnswer : _display(node, value, [])),
          ),
        ),
        IconButton(
          tooltip: fieldText(context).increaseValue(title),
          onPressed: _busy ? null : () => adjust(increment),
          icon: const Icon(Icons.add),
        ),
      ],
    );
  }

  Widget _choice(
    FormNode node,
    String title,
    String hint,
    Object? value,
    List<Json> choices,
    Object? control,
    ValueChanged<Object?> set,
  ) {
    final multiple = node.kind == 'multiple-choice';
    final selected = multiple ? (value as List? ?? const []).toSet() : {value};
    if (!multiple && (control == null || control == 'dropdown')) {
      return _FieldLabel(
        title: title,
        hint: hint,
        child: DropdownButtonFormField<String>(
          key: ValueKey((value, choices.map((choice) => choice['key']).join('|'))),
          initialValue: choices.any((choice) => choice['key'] == value) ? value as String? : null,
          isExpanded: true,
          itemHeight: null,
          items: [
            DropdownMenuItem(value: '', child: Text(fieldText(context).noAnswer)),
            for (final choice in choices)
              DropdownMenuItem(value: choice['key'] as String, child: Text(_text(choice['label']))),
          ],
          onChanged: _busy || choices.isEmpty ? null : (value) => set(value?.isEmpty == true ? null : value),
        ),
      );
    }
    void toggle(String key, bool checked) => set(
      multiple
          ? (checked ? {...selected, key} : {...selected}
                  ..remove(key))
                .toList()
          : checked
          ? key
          : null,
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(title),
        if (hint.isNotEmpty) Text(hint),
        if (control == 'buttons')
          Wrap(
            spacing: 8,
            children: [
              for (final choice in choices)
                FilterChip(
                  materialTapTargetSize: MaterialTapTargetSize.padded,
                  selected: selected.contains(choice['key']),
                  label: Text(_text(choice['label'])),
                  onSelected: _busy ? null : (checked) => toggle(choice['key'] as String, checked),
                ),
            ],
          )
        else if (multiple)
          for (final choice in choices)
            CheckboxListTile(
              contentPadding: EdgeInsets.zero,
              title: Text(_text(choice['label'])),
              value: selected.contains(choice['key']),
              onChanged: _busy ? null : (checked) => toggle(choice['key'] as String, checked == true),
            )
        else
          RadioGroup<String>(
            groupValue: value as String?,
            onChanged: _busy ? (_) {} : set,
            child: Column(
              children: [
                for (final choice in choices)
                  RadioListTile<String>(
                    enabled: !_busy,
                    toggleable: true,
                    contentPadding: EdgeInsets.zero,
                    value: choice['key'] as String,
                    title: Text(_text(choice['label'])),
                  ),
              ],
            ),
          ),
      ],
    );
  }

  Future<void> _chooseTime(String kind, Object? value, ValueChanged<Object?> set) async {
    final now = DateTime.now();
    final previous = value is String ? DateTime.tryParse(value)?.toLocal() : null;
    var day = previous ?? now;
    if (kind != 'time') {
      final picked = await showDatePicker(
        context: context,
        initialDate: day,
        firstDate: DateTime(1900),
        lastDate: DateTime(2200),
      );
      if (picked == null || !mounted) return;
      day = picked;
    }
    if (kind == 'date') {
      set(DateFormat('yyyy-MM-dd').format(day));
      return;
    }
    final parts = kind == 'time' && value is String ? value.split(':') : null;
    final initialTime = parts == null
        ? TimeOfDay.fromDateTime(previous ?? now)
        : TimeOfDay(hour: int.parse(parts[0]), minute: int.parse(parts[1]));
    final time = await showTimePicker(context: context, initialTime: initialTime);
    if (!mounted || time == null) return;
    final result = DateTime(day.year, day.month, day.day, time.hour, time.minute);
    set(kind == 'time' ? DateFormat('HH:mm').format(result) : result.toUtc().toIso8601String());
  }

  String _display(FormNode node, Object? value, List<Json> choices, {bool showUnit = true}) {
    String choice(Object? key) =>
        _text(choices.where((row) => row['key'] == key).firstOrNull?['label'] ?? {'en': '$key'});
    if (value == null) return fieldText(context).noAnswer;
    if (node.kind == 'single-choice') return choice(value);
    if (node.kind == 'multiple-choice') return (value as List).map(choice).join(', ');
    if (node.kind == 'photo' || node.kind == 'file' || node.kind == 'signature') {
      final ids = value is List ? value : [value];
      return ids.map((id) => object(_metadata()[id])['fileName'] ?? fieldText(context).attachedFile).join(', ');
    }
    if (value is Map && value['type'] == 'Point') {
      final coords = value['coordinates'] as List;
      return '${coords[1]}, ${coords[0]}';
    }
    if (value is Map) return node.kind == 'track' ? fieldText(context).trackSaved : fieldText(context).areaSaved;
    if (node.kind == 'date-time' || ['observed-at', 'raised-at'].contains(node.data['source'])) {
      final time = DateTime.tryParse('$value')?.toLocal();
      if (time != null) return DateFormat.yMMMd(Localizations.localeOf(context).toLanguageTag()).add_jm().format(time);
    }
    final unit = showUnit ? node.data['unit'] : null;
    final text = value is double && value == value.roundToDouble() ? value.toStringAsFixed(0) : '$value';
    return unit == null ? text : '$text $unit';
  }

  Widget _attachment(FormNode node, String title, String hint, Object? value, ValueChanged<Object?> set) {
    final signature = node.kind == 'signature';
    final ids = signature ? [if (value != null) value as String] : (value as List? ?? []).cast<String>();
    final max = node.data['maxCount'] as int? ?? 20;
    Future<void> add(String source) async {
      setState(() {
        _busy = true;
        _error = null;
      });
      try {
        final file = source == 'signature'
            ? await showDialog<ReportFile>(context: context, builder: (_) => const _SignatureDialog())
            : await (widget.pick?.call(source) ?? pickReportFile(source, fieldText(context)));
        if (file == null || !mounted) return;
        _files[file.id] = file;
        set(signature ? file.id : [...ids, file.id]);
      } on FormatException catch (error) {
        if (mounted) _showFileError(error.message);
      } on Exception {
        if (mounted) _showFileError(fieldText(context).couldNotOpenTheFile);
      } finally {
        if (mounted) setState(() => _busy = false);
      }
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(title),
        if (hint.isNotEmpty) Text(hint),
        for (final id in ids)
          ListTile(
            contentPadding: EdgeInsets.zero,
            leading: switch (_files[id]) {
              final file? when file.type.startsWith('image/') => Image.memory(
                file.bytes,
                width: 44,
                height: 44,
                fit: BoxFit.cover,
                cacheWidth: 100,
                errorBuilder: (_, _, _) => const Icon(Icons.attachment),
              ),
              _ => const Icon(Icons.attachment),
            },
            title: Text('${object(_metadata()[id])['fileName'] ?? 'Attached file'}'),
            trailing: IconButton(
              tooltip: fieldText(context).removeAttachment,
              icon: const Icon(Icons.close),
              onPressed: _busy ? null : () => set(signature ? null : ([...ids]..remove(id))),
            ),
          ),
        if (signature || ids.length < max)
          Wrap(
            spacing: 8,
            children: [
              if (node.kind == 'photo') ...[
                OutlinedButton.icon(
                  onPressed: _busy ? null : () => add('camera'),
                  icon: const Icon(Icons.photo_camera_outlined),
                  label: Text(fieldText(context).takePhoto),
                ),
                OutlinedButton.icon(
                  onPressed: _busy ? null : () => add('gallery'),
                  icon: const Icon(Icons.photo_library_outlined),
                  label: Text(fieldText(context).choosePhoto),
                ),
              ] else
                OutlinedButton.icon(
                  onPressed: _busy ? null : () => add(signature ? 'signature' : 'file'),
                  icon: Icon(signature ? Icons.draw_outlined : Icons.attach_file),
                  label: Text(signature ? fieldText(context).drawSignature : fieldText(context).chooseFile),
                ),
            ],
          ),
      ],
    );
  }

  void _showFileError(String message) {
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(message)));
  }
}

const _fileTypes = {
  'jpg': 'image/jpeg',
  'jpeg': 'image/jpeg',
  'png': 'image/png',
  'webp': 'image/webp',
  'pdf': 'application/pdf',
  'zip': 'application/zip',
  'txt': 'text/plain',
  'csv': 'text/csv',
};

Future<ReportFile?> pickReportFile(String source, AppLocalizations messages) async {
  final file = source == 'file'
      ? await openFile(
          acceptedTypeGroups: [XTypeGroup(label: messages.reportFiles, extensions: _fileTypes.keys.toList())],
        )
      : await ImagePicker().pickImage(
          source: source == 'camera' ? ImageSource.camera : ImageSource.gallery,
          maxWidth: 2560,
          maxHeight: 2560,
        );
  if (file == null) return null;
  try {
    final name = file.name.replaceAll(RegExp(r'[\x00-\x1f\x7f"]'), '_').trim();
    final type = _fileTypes[name.split('.').last.toLowerCase()];
    if (type == null || name.runes.length > 200) {
      throw FormatException(messages.chooseAJPEGPNGWebP);
    }
    if (await file.length() > (25 << 20)) throw FormatException(messages.chooseAFileSmallerThan);
    return ReportFile(newDeviceId(), name, type, await file.readAsBytes());
  } finally {
    if (source != 'file') await removePickedCopy(file.path);
  }
}

/// Form authors supply labels and hints, which can need several lines at large text sizes.
class _FieldLabel extends StatelessWidget {
  const _FieldLabel({required this.title, required this.hint, required this.child});
  final String title;
  final String hint;
  final Widget child;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      ExcludeSemantics(child: Text(title)),
      if (hint.isNotEmpty) Text(hint),
      const SizedBox(height: 4),
      MergeSemantics(
        child: Semantics(label: title, child: child),
      ),
    ],
  );
}

class _SignatureDialog extends StatefulWidget {
  const _SignatureDialog();
  @override
  State<_SignatureDialog> createState() => _SignatureDialogState();
}

class _SignatureDialogState extends State<_SignatureDialog> {
  List<Offset?> _ink = [];
  Size _size = Size.zero;
  bool _saving = false;

  Future<void> _use() async {
    setState(() => _saving = true);
    final recorder = ui.PictureRecorder();
    final canvas = Canvas(recorder);
    canvas.drawColor(Colors.white, BlendMode.src); // fixed-colour: paper
    _SignaturePainter(_ink).paint(canvas, _size);
    final picture = recorder.endRecording();
    final image = await picture.toImage(_size.width.ceil(), _size.height.ceil());
    picture.dispose();
    final data = await image.toByteData(format: ui.ImageByteFormat.png);
    image.dispose();
    if (mounted && data != null) {
      Navigator.pop(context, ReportFile(newDeviceId(), 'signature.png', 'image/png', data.buffer.asUint8List()));
    }
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text(fieldText(context).drawYourSignature),
    content: SizedBox(
      width: 360,
      height: 180,
      child: LayoutBuilder(
        builder: (context, constraints) {
          _size = constraints.biggest;
          return ColoredBox(
            color: Colors.white, // fixed-colour: signature paper
            child: GestureDetector(
              behavior: HitTestBehavior.opaque,
              onPanStart: _saving ? null : (event) => setState(() => _ink = [..._ink, event.localPosition]),
              onPanUpdate: _saving ? null : (event) => setState(() => _ink = [..._ink, event.localPosition]),
              onPanEnd: _saving ? null : (_) => setState(() => _ink = [..._ink, null]),
              child: CustomPaint(painter: _SignaturePainter(_ink), size: _size),
            ),
          );
        },
      ),
    ),
    actions: [
      TextButton(onPressed: _saving ? null : () => Navigator.pop(context), child: Text(fieldText(context).cancel)),
      TextButton(onPressed: _saving ? null : () => setState(() => _ink = []), child: Text(fieldText(context).clear)),
      FilledButton(
        onPressed: _saving || _ink.whereType<Offset>().length < 2 ? null : _use,
        child: Text(fieldText(context).useSignature),
      ),
    ],
  );
}

class _SignaturePainter extends CustomPainter {
  const _SignaturePainter(this.points);
  final List<Offset?> points;

  @override
  void paint(Canvas canvas, Size size) {
    canvas.clipRect(Offset.zero & size);
    const ink = Colors.black; // fixed-colour: signature ink
    final pen = Paint()
      ..color = ink
      ..strokeWidth = 2.5
      ..strokeCap = StrokeCap.round;
    for (var index = 1; index < points.length; index++) {
      final start = points[index - 1];
      final end = points[index];
      if (start != null && end != null) canvas.drawLine(start, end, pen);
    }
  }

  @override
  bool shouldRepaint(_SignaturePainter oldDelegate) => !identical(points, oldDelegate.points);
}
