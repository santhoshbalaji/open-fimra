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
    final palette = (tokens['primitives'] as Map)['palette'] as Map;
    expect(generator.colour('#1f5a70'), 'Color(0xFF1F5A70)');
    expect(generator.colour('rgb(0 0 0 / 35%)'), 'Color(0x59000000)');
    expect(hexOf(lightPalette.brand), (palette['light'] as Map)['petrol']);
    expect(hexOf(darkPalette.brand), (palette['dark'] as Map)['petrol']);
    expect(lightPalette.fireAge.map(hexOf), ((tokens['map'] as Map)['age'] as Map)['light']);
    expect(FimraSize.touchTarget, ((tokens['density'] as Map)['touchTargetMinPx'] as Map)['mobile']);
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

  for (final brightness in Brightness.values) {
    test('${brightness.name}: the Material theme uses strong boundaries and focus', () {
      final theme = fimraTheme(brightness, density: FimraDensity.compact);
      final palette = theme.extension<FimraColours>()!.palette;
      final input = theme.inputDecorationTheme;
      final button = theme.outlinedButtonTheme.style!;
      final border = button.side!.resolve({})!;
      final focus = input.focusedBorder!.borderSide;
      expect(theme.extension<FimraColours>()!.density, FimraDensity.compact);
      expect(button.minimumSize?.resolve({}), const Size(48, 48));
      expect(theme.dividerTheme.color, palette.divider);
      expect(input.enabledBorder!.borderSide.color, palette.controlBoundary);
      expect(border.color, palette.controlBoundary);
      expect(focus.color, palette.focus);
      expect(focus.width, 3);
      for (final surface in [input.fillColor!, theme.scaffoldBackgroundColor, theme.dialogTheme.backgroundColor!]) {
        expect(contrast(border.color, surface), greaterThanOrEqualTo(3));
        expect(contrast(focus.color, surface), greaterThanOrEqualTo(3));
        expect(contrast(palette.successText, surface), greaterThanOrEqualTo(4.5));
      }
    });
  }

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

double contrast(Color foreground, Color background) {
  final values = [foreground.computeLuminance(), background.computeLuminance()]..sort();
  return (values.last + 0.05) / (values.first + 0.05);
}
