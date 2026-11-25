import 'dart:convert';
import 'dart:io';

import 'package:fimra_field/src/theme/app_theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import '../tool/generate_tokens.dart' as generator;

/// The repository root, from apps/mobile where the tests run.
const root = '../..';

void main() {
  final tokens = jsonDecode(File('$root/docs/design-system/tokens.json').readAsStringSync()) as Map<String, dynamic>;

  test('the generated theme matches the design tokens', () {
    expect(
      File('lib/src/theme/tokens.g.dart').readAsStringSync(),
      generator.formatted(generator.generate(tokens)),
      reason: 'Run make mobile-tokens',
    );
  });

  test('colours come from the tokens', () {
    final colours = tokens['color'] as Map<String, dynamic>;
    expect(generator.colour('#1f5a70'), 'Color(0xFF1F5A70)');
    expect(generator.colour('rgb(0 0 0 / 35%)'), 'Color(0x59000000)');
    expect(hexOf(lightPalette.brand), (colours['light'] as Map)['brand']);
    expect(hexOf(darkPalette.brand), (colours['dark'] as Map)['brand']);
    expect(lightPalette.fireAge.map(hexOf), (colours['fireAge'] as Map)['light']);
    expect(FimraSize.touchTarget, (tokens['touchTargetMinPx'] as Map)['mobile']);
  });

  test('both themes use Inter and 48 px controls', () {
    for (final brightness in Brightness.values) {
      final theme = fimraTheme(brightness);
      expect(theme.textTheme.bodyMedium?.fontFamily, 'Inter');
      expect(theme.filledButtonTheme.style?.minimumSize?.resolve({}), const Size(48, 48));
      expect(theme.outlinedButtonTheme.style?.minimumSize?.resolve({}), const Size(48, 48));
      expect(theme.textButtonTheme.style?.minimumSize?.resolve({}), const Size(48, 48));
      expect(theme.colorScheme.primary, brightness == Brightness.light ? lightPalette.brand : darkPalette.brand);
      expect(
        theme.scaffoldBackgroundColor,
        brightness == Brightness.light ? lightPalette.background : darkPalette.background,
      );
    }
  });

  test('the logo files are copies of the project logo', () {
    for (final name in ['mark-light.svg', 'mark-dark.svg', 'lockup-light.svg', 'lockup-dark.svg']) {
      expect(
        File('assets/logo/$name').readAsBytesSync(),
        File('$root/docs/design-system/logo/$name').readAsBytesSync(),
        reason: name,
      );
    }
  });

  test('the Android launcher icon uses the logo mark', () {
    final mark = File('$root/docs/design-system/logo/app-icon-maskable.svg').readAsStringSync();
    final foreground = File('android/app/src/main/res/drawable/ic_launcher_foreground.xml').readAsStringSync();
    for (final path in RegExp(r' d="([^"]+)"').allMatches(mark).map((m) => m.group(1)!)) {
      expect(foreground, contains('android:pathData="$path"'));
    }
  });
}

String hexOf(Color colour) => '#${(colour.toARGB32() & 0xFFFFFF).toRadixString(16).padLeft(6, '0')}';
