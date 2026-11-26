import 'dart:convert';
import 'dart:io';

import 'package:fimra_field/src/data/field_store.dart';
import 'package:fimra_field/src/forms/answers.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  final examples = Directory('../../packages/form-schema/examples');
  const alert = {
    'alert-id': '42',
    'observed-at': '2026-03-15T06:00:00Z',
    'raised-at': '2026-03-15T06:20:00Z',
    'location': {
      'type': 'Point',
      'coordinates': [86.3, 16.4],
    },
    'unit': 'Beat 05',
    'forest-class': 'inside',
    'source': 'demo',
    'confidence': 'nominal',
    'fire-power': 12.5,
  };
  const attachments = {
    '00000000-0000-4000-8000-000000000001': {'contentType': 'image/jpeg', 'fileName': 'site.jpg', 'sizeBytes': 1000},
    '00000000-0000-4000-8000-000000000002': {'contentType': 'image/png', 'fileName': 'signature.png', 'sizeBytes': 500},
    '00000000-0000-4000-8000-000000000003': {
      'contentType': 'application/pdf',
      'fileName': 'map.pdf',
      'sizeBytes': 3 << 20,
    },
  };
  final files = Directory('${examples.path}/answers').listSync().whereType<File>().toList();
  test('shared form answer cases are present', () => expect(files.length, greaterThanOrEqualTo(3)));
  for (final file in files) {
    final fixture = jsonDecode(file.readAsStringSync()) as Json;
    final form = fixture['form'] as String;
    final tree = FormTree(jsonDecode(File('${examples.path}/$form.json').readAsStringSync()) as Json);
    for (final item in objects(fixture['cases'])) {
      test('$form / ${item['name']}', () {
        final session = AnswerSession(
          tree,
          item['values'],
          step: item['step'] as String?,
          now: DateTime.parse('2026-03-16T12:00:00Z'),
          alert: alert,
          attachments: attachments,
          measure: (shape) => shape['type'] == 'Polygon' ? 1.23456 : 0.5,
        );
        expect(session.problems, item['expected']);
        for (final answer in object(item['answers']).entries) {
          expect(session.answers?[answer.key], answer.value, reason: answer.key);
        }
        for (final absent in item['absent'] as List? ?? []) {
          expect(session.answers, isNot(contains(absent)));
        }
      });
    }
  }
}
