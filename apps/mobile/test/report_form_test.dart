import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:fimra_field/src/api/models.dart';
import 'package:fimra_field/src/data/field_store.dart';
import 'package:fimra_field/src/forms/report_form.dart';
import 'package:fimra_field/src/l10n/app_localizations.dart';
import 'package:fimra_field/src/theme/app_theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  testWidgets('the starter flash report queues the workflow note and its photo while offline', (tester) async {
    final definition =
        jsonDecode(File('../../packages/starter-pack/forms/response-flash.json').readAsStringSync()) as Json;
    final photo = ReportFile('00000000-0000-4000-8000-000000000001', 'site.jpg', 'image/jpeg', Uint8List(3 << 20));
    StepRequest? queuedStep;
    List<ReportFile>? queuedFiles;
    tester.view.physicalSize = const Size(900, 2100);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        home: Builder(
          builder: (context) => Scaffold(
            body: TextButton(
              onPressed: () => Navigator.push(
                context,
                MaterialPageRoute<bool>(
                  builder: (_) => ReportForm(
                    definition: definition,
                    step: 'flash_update',
                    alert: const {},
                    move: const WorkflowMove(
                      step: 'flash_update',
                      label: 'Flash update',
                      locationRequired: false,
                      canOverride: false,
                      terminal: false,
                      extended: true,
                    ),
                    draftValues: {
                      'checked_at': '2025-01-01T10:00:00Z',
                      'fire_status': 'No fire seen',
                      'site_location': {
                        'type': 'Point',
                        'coordinates': [86.29, 16.41],
                      },
                      'fire_seen': 'no',
                      'help_needed': 'Another crew to check the ridge',
                      'site_photo': [photo.id],
                    },
                    draftFiles: [photo],
                    save: (answers, files, step) async {
                      queuedStep = step;
                      queuedFiles = files;
                    },
                  ),
                ),
              ),
              child: const Text('Open flash report'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('Open flash report'));
    await tester.pumpAndSettle();
    expect(find.text('Flash update'), findsOneWidget);
    expect(find.text('site.jpg'), findsOneWidget);
    await tester.tap(find.text('Save to send later'));
    await tester.pumpAndSettle();
    expect(queuedStep?.step, 'flash_update');
    expect(queuedStep?.actualAt, DateTime.utc(2025, 1, 1, 10));
    expect(queuedStep?.response, {'status': 'No fire seen', 'helpNeeded': 'Another crew to check the ridge'});
    expect(queuedFiles?.single.bytes.length, 3 << 20);
  });

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
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
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
    await tester.tap(find.text('Save to send later'));
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
    await tester.tap(find.text('English').first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('हिन्दी').last);
    await tester.pumpAndSettle();
    expect(find.text('पहली रिपोर्ट'), findsOneWidget);
    expect(find.text('दल का नाम *'), findsOneWidget);
    await tester.enterText(find.byType(TextFormField), 'Demo Team');
    await tester.tap(find.text('Save to send later'));
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

  testWidgets('Back keeps an incomplete report and its photo, and required fields start visible', (tester) async {
    const id = '00000000-0000-4000-8000-000000000001';
    const definition = {
      'title': {'en': 'First report'},
      'sections': [
        {
          'key': 'summary',
          'title': {'en': 'Summary'},
          'items': [
            {
              'key': 'reached_at',
              'type': 'date-time',
              'label': {'en': 'Team reached the site at'},
              'required': true,
            },
            {
              'key': 'photo',
              'type': 'photo',
              'label': {'en': 'Site photo'},
              'required': true,
            },
          ],
        },
      ],
      'layout': {
        'items': [
          {'item': 'summary', 'collapsed': true},
        ],
      },
    };
    Json values = {};
    List<ReportFile> files = [];
    String? language;
    await tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        theme: fimraTheme(Brightness.light),
        home: Builder(
          builder: (context) => Scaffold(
            body: TextButton(
              onPressed: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => ReportForm(
                    definition: definition,
                    step: 'first_report',
                    alert: const {},
                    draftValues: values,
                    draftFiles: files,
                    draftLanguage: language,
                    saveDraft: (answers, attachments, selectedLanguage) async {
                      values = answers;
                      files = attachments;
                      language = selectedLanguage;
                    },
                    pick: (_) async => ReportFile(id, 'site.jpg', 'image/jpeg', Uint8List.fromList([1, 2, 3])),
                    save: (_, _, _) async => fail('An unfinished draft must not send'),
                  ),
                ),
              ),
              child: const Text('Open report'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('Open report'));
    await tester.pumpAndSettle();
    expect(find.text('Choose Team reached the site at').hitTestable(), findsOneWidget);
    await tester.tap(find.text('Choose photo'));
    await tester.pumpAndSettle();
    expect(values, {
      'photo': [id],
    });
    await tester.pageBack();
    await tester.pumpAndSettle();
    await tester.tap(find.text('Open report'));
    await tester.pumpAndSettle();
    expect(find.text('site.jpg'), findsOneWidget);
    expect(find.text('Save draft'), findsOneWidget);
    expect(find.text('Send when connected'), findsOneWidget);
  });
}
