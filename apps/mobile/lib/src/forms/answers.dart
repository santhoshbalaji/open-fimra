// The answer rules follow packages/form-schema/ts/answers.ts and the core API.
// The Dart, TypeScript and Go tests read the same examples/answers cases.
import 'dart:convert';
import 'dart:math' as math;

import '../data/field_store.dart';

typedef EntryPath = List<(String, int)>;

String formText(Object? value, String language) {
  final text = object(value);
  return (text[language] ?? text[language.split('-').first] ?? text['en'] ?? '') as String;
}

class FormNode {
  FormNode(this.data, this.kind, this.stage, this.parent, this.repeat);
  final Json data;
  final String kind;
  final int stage;
  final FormNode? parent;
  final FormNode? repeat;
  final children = <FormNode>[];
  final members = <FormNode>[];
  String get key => data['key'] as String;
  bool get field => !['section', 'group', 'repeat'].contains(kind);
  bool get computed => kind == 'calculated' || kind == 'alert-value';
}

class FormTree {
  FormTree(this.definition) {
    final stages = objects(definition['stages']);
    for (final (index, row) in stages.indexed) {
      steps[row['workflowStep'] as String] = index;
    }
    final stageKeys = stages.map((row) => row['key']).toList();
    for (final row in objects(definition['lists'])) {
      lists[row['key'] as String] = objects(row['choices']);
    }
    for (final section in objects(definition['sections'])) {
      sections.add(_walk(section, 'section', stageKeys.indexOf(section['stage']), null, null));
    }
  }

  final Json definition;
  final byKey = <String, FormNode>{};
  final sections = <FormNode>[];
  final members = <FormNode>[];
  final steps = <String, int>{};
  final lists = <String, List<Json>>{};

  FormNode _walk(Json data, String kind, int stage, FormNode? parent, FormNode? repeat) {
    final node = FormNode(data, kind, stage, parent, repeat);
    byKey[node.key] = node;
    if (node.field || kind == 'repeat') (repeat?.members ?? members).add(node);
    for (final item in objects(data['items'])) {
      node.children.add(_walk(item, item['type'] as String, stage, node, kind == 'repeat' ? node : repeat));
    }
    return node;
  }

  List<Json> choices(FormNode node) => switch (node.data['list']) {
    final String list => lists[list] ?? [],
    _ => objects(node.data['choices']),
  };
}

class _Scope {
  _Scope(this.repeat, this.parent, this.label);
  final FormNode? repeat;
  final _Scope? parent;
  final String label;
  final clean = <String, Object>{};
  final entries = <String, List<_Scope>>{};
  final memo = <String, Object?>{};
  final busy = <String>{};

  _Scope? containing(FormNode node) {
    for (_Scope? scope = this; scope != null; scope = scope.parent) {
      if (scope.repeat == node.repeat) return scope;
    }
    return null;
  }
}

final _date = RegExp(r'^\d{4}-\d{2}-\d{2}$');
final _time = RegExp(r'^([01]\d|2[0-3]):[0-5]\d(:[0-5]\d)?$');
final _dateTime = RegExp(
  r'^(\d{4}-\d{2}-\d{2})T((?:[01]\d|2[0-3]):[0-5]\d)(?::[0-5]\d(?:\.\d{1,9})?)?(Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$',
);
final _uuid = RegExp(r'^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');

DateTime? _day(Object? raw) {
  if (raw is! String || !_date.hasMatch(raw)) return null;
  final date = DateTime.tryParse('${raw}T00:00:00Z');
  return date?.toIso8601String().startsWith(raw) == true ? date : null;
}

DateTime? _instant(Object? raw) {
  if (raw is! String) return null;
  final match = _dateTime.firstMatch(raw);
  if (match == null || _day(match[1]) == null) return null;
  return DateTime.tryParse(raw);
}

int? _seconds(Object? raw) {
  if (raw is! String || !_time.hasMatch(raw)) return null;
  final parts = raw.split(':').map(int.parse).toList();
  return parts[0] * 3600 + parts[1] * 60 + (parts.length == 3 ? parts[2] : 0);
}

String _number(num value) => value == value.roundToDouble() ? value.toStringAsFixed(0) : '$value';

/// Facts and attachment metadata are from the cached alert and local outbox.
/// The server validates again and uses its own clock and geographic measures.
class AnswerSession {
  AnswerSession(
    this.tree,
    Object? values, {
    String? step,
    this.alert = const {},
    this.attachments = const {},
    DateTime? now,
    this.measure = sphereMeasure,
  }) : _stage = tree.steps[step] ?? -1,
       _now = now ?? DateTime.now() {
    if (tree.steps.isNotEmpty && !tree.steps.containsKey(step)) {
      problems.add('The form has no stage at the step ${step ?? '(none)'}.');
      return;
    }
    if (values is! Json) {
      problems.add('values must be a JSON object of answers by field key.');
      return;
    }
    final root = _root = _readScope(null, null, '', values);
    if (problems.isNotEmpty) return;
    final output = _output(root, tree.members);
    if (problems.isEmpty) answers = output;
  }

  final FormTree tree;
  final Json attachments;
  final Json alert;
  final int _stage;
  final DateTime _now;
  final num? Function(Json) measure;
  final problems = <String>[];
  final attachmentIds = <String>{};
  Json? answers;
  _Scope? _root;

  _Scope? _at(EntryPath path) {
    var scope = _root;
    for (final (key, index) in path) {
      final entries = scope?.entries[key];
      if (entries == null || index >= entries.length) return null;
      scope = entries[index];
    }
    return scope;
  }

  bool visible(FormNode node, [EntryPath path = const []]) => _visible(node, _at(path)?.containing(node));

  bool readOnly(FormNode node) => node.computed || node.stage < _stage;

  bool required(FormNode node, [EntryPath path = const []]) {
    final scope = _at(path)?.containing(node);
    return scope != null && !node.computed && _required(node, scope);
  }

  Object? value(FormNode node, [EntryPath path = const []]) => _value(node.key, _at(path));

  List<Json> choices(FormNode node, [EntryPath path = const []]) {
    final choices = tree.choices(node);
    final parent = node.data['parent'] as String?;
    if (parent == null) return choices;
    final answer = _value(parent, _at(path));
    return answer == null ? [] : choices.where((choice) => choice['parent'] == answer).toList();
  }

  void _add(_Scope scope, String message) =>
      problems.add(scope.label.isEmpty ? message : 'In ${scope.label}: $message');

  _Scope _readScope(FormNode? repeat, _Scope? parent, String label, Json values) {
    final scope = _Scope(repeat, parent, label);
    final members = repeat?.members ?? tree.members;
    for (final key in values.keys.toList()..sort()) {
      final node = tree.byKey[key];
      final Object? value = values[key];
      if (node == null || (!node.field && node.kind != 'repeat')) {
        _add(scope, 'values has "$key", which is not a field of this form.');
      } else if (!members.contains(node)) {
        final group = node.repeat;
        _add(
          scope,
          group == null
              ? 'Field "$key" is not inside a repeating group.'
              : 'Field "$key" belongs inside the repeating group "${group.key}".',
        );
      } else if (node.computed || value == null) {
        continue;
      } else if (node.kind == 'repeat') {
        if (value is! List) {
          _add(scope, 'Repeating group "$key" must be a list of entries.');
          continue;
        }
        final entries = scope.entries[key] = [];
        for (final (i, entry) in value.indexed) {
          if (entry is! Json) {
            _add(scope, 'Entry ${i + 1} of "$key" must be an object of answers.');
            continue;
          }
          final where = 'entry ${i + 1} of "$key"';
          entries.add(_readScope(node, scope, label.isEmpty ? where : '$label, $where', entry));
        }
      } else if (_checkValue(scope, node, value) case final Object clean) {
        scope.clean[key] = clean;
      }
    }
    return scope;
  }

  Object? _checkValue(_Scope scope, FormNode node, Object value) {
    final who = 'Field "${node.key}"';
    final data = node.data;
    Object? invalid(String message) {
      _add(scope, '$who $message');
      return null;
    }

    void checkCount(int count, String unit, num? min, num? max) {
      if (min != null && count < min) _add(scope, '$who needs at least ${_number(min)} $unit.');
      if (max != null && count > max) _add(scope, '$who can have at most ${_number(max)} $unit.');
    }

    switch (node.kind) {
      case 'text':
        if (value is! String) return invalid('must be text.');
        if (value.isEmpty) return null;
        checkCount(value.runes.length, 'characters', data['minLength'] as num?, data['maxLength'] as num?);
        final pattern = data['pattern'] as String?;
        if (pattern != null) {
          final match = RegExp('^(?:$pattern)\$', unicode: true).firstMatch(value);
          if (match?.end != value.length) {
            final hint = formText(data['patternHint'], 'en').replaceFirst(RegExp(r'\.$'), '');
            _add(scope, '$who does not match the expected pattern${hint.isEmpty ? '' : ': $hint'}.');
          }
        }
      case 'number':
        if (value is! num || !value.isFinite) return invalid('must be a number.');
        final lo = data['min'] as num?;
        final hi = data['max'] as num?;
        if (lo != null && value < lo) _add(scope, '$who must be at least ${_number(lo)}.');
        if (hi != null && value > hi) _add(scope, '$who must be at most ${_number(hi)}.');
        if (data['decimals'] case final int places) {
          final scaled = value * math.pow(10, places);
          if (scaled.isFinite && (scaled - scaled.roundToDouble()).abs() > 1e-9 * math.max(1, scaled.abs())) {
            _add(scope, places == 0 ? '$who must be a whole number.' : '$who can have at most $places decimal places.');
          }
        }
      case 'date':
        final day = _day(value);
        if (day == null) return invalid('must be a date such as 2026-03-15.');
        if (data['notInFuture'] == true && day.isAfter(_now.add(const Duration(hours: 14)))) {
          _add(scope, '$who cannot be in the future.');
        }
      case 'time':
        if (_seconds(value) == null) return invalid('must be a time such as 14:30.');
      case 'date-time':
        final time = _instant(value);
        if (time == null) {
          return invalid(
            'must be a date and time with its offset from universal time, such as 2026-03-15T14:30:00+05:30.',
          );
        }
        if (data['notInFuture'] == true && time.isAfter(_now.add(const Duration(minutes: 5)))) {
          _add(scope, '$who cannot be in the future.');
        }
      case 'single-choice':
        if (_choice(node, value) == null) return invalid('has no choice ${jsonEncode(value)}.');
      case 'multiple-choice':
        if (value is! List) return invalid('must be a list of choices.');
        final seen = <Object>{};
        for (final choice in value) {
          if (_choice(node, choice) == null) return invalid('has no choice ${jsonEncode(choice)}.');
          if (!seen.add(choice as String)) return invalid('lists "$choice" more than once.');
        }
        if (seen.isEmpty) return null;
        checkCount(seen.length, 'choices', data['minSelected'] as num?, data['maxSelected'] as num?);
      case 'point':
      case 'track':
      case 'polygon':
        final issue = _checkGeometry(node.kind, value);
        if (issue != null) return invalid(issue);
      case 'photo':
      case 'file':
        if (value is! List) return invalid('must be a list of attachment IDs.');
        final seen = <String>{};
        for (final raw in value) {
          if (!_attachment(scope, node, raw)) return null;
          final id = raw as String;
          if (!seen.add(id)) return invalid('lists the attachment "$id" more than once.');
        }
        if (seen.isEmpty) return null;
        checkCount(seen.length, 'files', data['minCount'] as num?, (data['maxCount'] as num?) ?? 20);
      case 'signature':
        if (!_attachment(scope, node, value)) return null;
    }
    return value;
  }

  Json? _choice(FormNode node, Object? key) => tree.choices(node).where((choice) => choice['key'] == key).firstOrNull;

  bool _attachment(_Scope scope, FormNode node, Object? id) {
    final who = 'Field "${node.key}"';
    String? issue;
    if (id is! String || !_uuid.hasMatch(id)) {
      issue = 'must give attachment IDs, as returned by the upload.';
    } else if (!attachments.containsKey(id)) {
      issue = 'uses the attachment "$id", which was not uploaded for this alert.';
    } else if (attachments[id] case final Json meta) {
      final name = meta['fileName'] as String;
      final type = meta['contentType'];
      final accept = node.data['accept'] as List?;
      final max = node.data['maxSizeMegabytes'] as num?;
      if (node.kind != 'file' && !['image/jpeg', 'image/png', 'image/webp'].contains(type)) {
        issue = 'needs an image, but the attachment "$id" is $type.';
      } else if (accept != null && !accept.any((ending) => name.toLowerCase().endsWith('$ending'.toLowerCase()))) {
        issue = 'does not accept the file "$name".';
      } else if (max != null && (meta['sizeBytes'] as num) > max * 1024 * 1024) {
        issue = 'accepts files up to ${_number(max)} MB, but "$name" is larger.';
      }
    }
    if (issue != null) _add(scope, '$who $issue');
    return issue == null;
  }

  bool _visible(FormNode node, _Scope? holder) {
    if (holder == null || node.stage > _stage) return false;
    final parent = node.parent;
    if (parent != null && !_visible(parent, holder.containing(parent))) return false;
    final condition = node.data['showWhen'] as Json?;
    return condition == null || _condition(condition, holder);
  }

  Object? _value(String key, _Scope? scope) {
    final node = tree.byKey[key];
    if (node == null) return null;
    final holder = scope?.containing(node);
    if (holder == null) return null;
    if (holder.memo.containsKey(key)) return holder.memo[key];
    if (!holder.busy.add(key)) return null;
    Object? value;
    if (_visible(node, holder)) {
      value = switch (node.kind) {
        'calculated' => _calculated(node, holder),
        'alert-value' => alert[node.data['source']],
        _ => holder.clean[key],
      };
    }
    holder.busy.remove(key);
    return holder.memo[key] = value;
  }

  bool _required(FormNode node, _Scope scope) {
    final condition = node.data['requireWhen'] as Json?;
    return node.data['required'] == true || (condition != null && _condition(condition, scope));
  }

  bool _condition(Json condition, _Scope scope) {
    final op = condition['op'];
    switch (op) {
      case 'all':
        return objects(condition['conditions']).every((rule) => _condition(rule, scope));
      case 'any':
        return objects(condition['conditions']).any((rule) => _condition(rule, scope));
      case 'not':
        return !_condition(object(condition['condition']), scope);
    }
    final key = condition['field'] as String;
    final answer = _value(key, scope);
    if (op == 'answered') return answer != null;
    if (answer == null) return false;
    final want = condition['value'];
    if (op == 'includes') return answer is List && answer.contains(want);
    final node = tree.byKey[key]!;
    final kind = node.kind == 'alert-value'
        ? const {'observed-at': 'date-time', 'raised-at': 'date-time', 'fire-power': 'number'}[node.data['source']]
        : node.kind;
    final order = switch (kind) {
      'number' || 'calculated' when answer is num && want is num => answer.compareTo(want),
      'date' => (answer as String).compareTo(want as String),
      'time' => _seconds(answer)?.compareTo(_seconds(want)!),
      'date-time' => _instant(answer)?.compareTo(_instant(want)!),
      _ => null,
    };
    return switch (op) {
      'equals' => order == null ? answer == want : order == 0,
      'not-equals' => order == null ? answer != want : order != 0,
      'more-than' => order != null && order > 0,
      'at-least' => order != null && order >= 0,
      'less-than' => order != null && order < 0,
      'at-most' => order != null && order <= 0,
      _ => false,
    };
  }

  num? _calculated(FormNode node, _Scope scope) {
    final result = _calculate(object(node.data['calculate']), scope);
    if (result == null || !result.isFinite) return null;
    final places = node.data['decimals'] as int?;
    if (places == null) return result;
    final scale = math.pow(10, places);
    return (result * scale).roundToDouble() / scale;
  }

  num? _calculate(Json expression, _Scope scope) {
    final op = expression['op'];
    Object? field(String name) => _value(expression[name] as String, scope);
    switch (op) {
      case 'number':
        return expression['value'] as num;
      case 'field':
        return field('field') as num?;
      case 'add':
      case 'multiply':
      case 'subtract':
      case 'divide':
        final args = <num>[];
        for (final arg in objects(expression['args'])) {
          final value = _calculate(arg, scope);
          if (value == null) return null;
          args.add(value);
        }
        return switch (op) {
          'add' => args.reduce((a, b) => a + b),
          'multiply' => args.reduce((a, b) => a * b),
          'subtract' => args[0] - args[1],
          _ => args[1] == 0 ? null : args[0] / args[1],
        };
      case 'count':
      case 'sum':
        final group = tree.byKey[expression['group']]!;
        final holder = scope.containing(group);
        if (!_visible(group, holder)) return null;
        final entries = holder!.entries[group.key] ?? [];
        if (op == 'count') return entries.length;
        return entries.fold<num>(
          0,
          (total, entry) => total + ((_value(expression['field'] as String, entry) as num?) ?? 0),
        );
      case 'area':
      case 'length':
        final geometry = field('field');
        return geometry is Json ? measure(geometry) : null;
      case 'hours-between':
        final start = _instant(field('start'));
        final end = _instant(field('end'));
        return start == null || end == null ? null : end.difference(start).inMicroseconds / 3600000000;
    }
    return null;
  }

  Json _output(_Scope scope, List<FormNode> members) {
    final output = <String, dynamic>{};
    for (final node in members) {
      if (!_visible(node, scope)) continue;
      final key = node.key;
      if (node.kind == 'repeat') {
        final entries = scope.entries[key] ?? [];
        final min = node.data['minRepeats'] as int?;
        final max = node.data['maxRepeats'] as int?;
        if (min != null && entries.length < min) _add(scope, 'Repeating group "$key" needs at least $min entries.');
        if (max != null && entries.length > max) _add(scope, 'Repeating group "$key" can have at most $max entries.');
        if (entries.isNotEmpty) output[key] = entries.map((entry) => _output(entry, node.members)).toList();
        continue;
      }
      final answer = _value(key, scope);
      if (answer == null) {
        if (!node.computed && _required(node, scope)) _add(scope, 'Field "$key" is required.');
        continue;
      }
      if (node.data['parent'] case final String parent) {
        final choices = answer is List ? answer : [answer];
        final parentAnswer = _value(parent, scope);
        for (final choice in choices) {
          if (parentAnswer == null || _choice(node, choice)?['parent'] != parentAnswer) {
            _add(scope, 'Field "$key": the choice "$choice" does not go with the answer to "$parent".');
            break;
          }
        }
      }
      if (node.kind == 'photo' || node.kind == 'file') attachmentIds.addAll((answer as List).cast<String>());
      if (node.kind == 'signature') attachmentIds.add(answer as String);
      output[key] = answer;
    }
    return output;
  }
}

String? _checkGeometry(String kind, Object value) {
  final type = const {'point': 'Point', 'track': 'LineString', 'polygon': 'Polygon'}[kind];
  final problem = 'must be a GeoJSON $type in longitude and latitude.';
  if (value is! Json || value['type'] != type || value.length != 2) return problem;
  bool position(Object? point) =>
      point is List &&
      (point.length == 2 || point.length == 3) &&
      point.every((part) => part is num && part.isFinite) &&
      (point[0] as num).abs() <= 180 &&
      (point[1] as num).abs() <= 90;
  bool line(Object? shape, int min, bool closed) {
    if (shape is! List || shape.length < min || !shape.every(position)) return false;
    final first = shape.first as List;
    final last = shape.last as List;
    return !closed || (first[0] == last[0] && first[1] == last[1]);
  }

  final coordinates = value['coordinates'];
  return switch (kind) {
    'point' => position(coordinates) ? null : problem,
    'track' => line(coordinates, 2, false) ? null : problem,
    _ =>
      coordinates is List && coordinates.isNotEmpty && coordinates.every((ring) => line(ring, 4, true))
          ? null
          : 'must be a GeoJSON Polygon with closed rings of at least four positions.',
  };
}

num? sphereMeasure(Json geometry) {
  const radius = 6371008.8;
  double radians(num angle) => angle * math.pi / 180;
  final coordinates = geometry['coordinates'] as List;
  if (geometry['type'] == 'LineString') {
    var length = 0.0;
    for (var i = 1; i < coordinates.length; i++) {
      final a = (coordinates[i - 1] as List).cast<num>();
      final b = (coordinates[i] as List).cast<num>();
      final side =
          math.pow(math.sin(radians(b[1] - a[1]) / 2), 2) +
          math.cos(radians(a[1])) * math.cos(radians(b[1])) * math.pow(math.sin(radians(b[0] - a[0]) / 2), 2);
      length += 2 * radius * math.asin(math.min(1, math.sqrt(side)));
    }
    return length / 1000;
  }
  if (geometry['type'] != 'Polygon') return null;
  var area = 0.0;
  for (final (index, ring) in coordinates.indexed) {
    final points = ring as List;
    var sum = 0.0;
    for (var i = 1; i < points.length; i++) {
      final a = (points[i - 1] as List).cast<num>();
      final b = (points[i] as List).cast<num>();
      sum += radians(b[0] - a[0]) * (2 + math.sin(radians(a[1])) + math.sin(radians(b[1])));
    }
    area += (index == 0 ? 1 : -1) * sum.abs() * radius * radius / 2;
  }
  return area / 10000;
}
