import 'dart:typed_data';

import 'package:fimra_field/src/data/field_store.dart';
import 'package:fimra_field/src/forms/report_form.dart';
import 'package:fimra_field/src/theme/app_theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  testWidgets('a translated repeat and a conditional photo are checked and saved', (tester) async {
    const definition = {
      'title': {'en': 'First report', 'hi': 'पहली रिपोर्ट'},
      'languages': ['en', 'hi'],
      'sections': [
        {
          'key': 'site',
          'title': {'en': 'Site'},
          'items': [
            {
              'key': 'status',
              'type': 'single-choice',
              'label': {'en': 'Status'},
              'required': true,
              'choices': [
                {
                  'key': 'open',
                  'label': {'en': 'Open'},
                },
              ],
            },
            {
              'key': 'photo',
              'type': 'photo',
              'label': {'en': 'Site photo'},
              'required': true,
              'showWhen': {'op': 'equals', 'field': 'status', 'value': 'open'},
            },
            {
              'key': 'teams',
              'type': 'repeat',
              'title': {'en': 'Teams'},
              'itemLabel': {'en': 'team'},
              'minRepeats': 1,
              'items': [
                {
                  'key': 'name',
                  'type': 'text',
                  'label': {'en': 'Team name', 'hi': 'दल का नाम'},
                  'required': true,
                },
              ],
            },
          ],
        },
      ],
    };
    const id = '00000000-0000-4000-8000-000000000001';
    Json? saved;
    List<ReportFile>? files;
    tester.view.physicalSize = const Size(820, 1400);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(
      MaterialApp(
        theme: fimraTheme(Brightness.light),
        home: Builder(
          builder: (context) => Scaffold(
            body: TextButton(
              onPressed: () => Navigator.push(
                context,
                MaterialPageRoute<bool>(
                  builder: (_) => ReportForm(
                    definition: definition,
                    step: 'first_report',
                    alert: const {},
                    pick: (source) async {
                      expect(source, 'gallery');
                      return ReportFile(id, 'site.jpg', 'image/jpeg', Uint8List.fromList([1, 2, 3]));
                    },
                    save: (answers, attachments, step) async {
                      saved = answers;
                      files = attachments;
                    },
                  ),
                ),
              ),
              child: const Text('Fill report'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('Fill report'));
    await tester.pumpAndSettle();
    expect(find.text('Choose photo'), findsNothing);
    await tester.tap(find.text('Save to outbox'));
    await tester.pumpAndSettle();
    expect(find.textContaining('“Status” is required'), findsOneWidget);
    await tester.tap(find.byType(DropdownButtonFormField<String>).last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Open').last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Choose photo'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Add team'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('en').first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('hi').last);
    await tester.pumpAndSettle();
    expect(find.text('पहली रिपोर्ट'), findsOneWidget);
    expect(find.text('दल का नाम *'), findsOneWidget);
    await tester.enterText(find.byType(TextFormField), 'Demo Team');
    await tester.tap(find.text('Save to outbox'));
    await tester.pumpAndSettle();
    expect(saved, {
      'status': 'open',
      'photo': [id],
      'teams': [
        {'name': 'Demo Team'},
      ],
    });
    expect(files?.single.id, id);
  });
}
