import 'package:flutter/material.dart';

import 'tokens.g.dart';

export 'tokens.g.dart';

/// Gives widgets the current theme's token palette.
class FimraColours extends ThemeExtension<FimraColours> {
  const FimraColours(this.palette, {this.density = FimraDensity.comfortable});
  final FimraPalette palette;
  final FimraDensity density;

  static FimraPalette of(BuildContext context) => Theme.of(context).extension<FimraColours>()!.palette;

  @override
  FimraColours copyWith({FimraPalette? palette, FimraDensity? density}) =>
      FimraColours(palette ?? this.palette, density: density ?? this.density);

  @override
  FimraColours lerp(FimraColours? other, double t) => t < 0.5 || other == null ? this : other;
}

TextStyle _style(FimraTypeStep step, Color colour) => TextStyle(
  fontFamily: 'Inter',
  fontSize: step.size,
  height: step.lineHeight / step.size,
  fontWeight: FontWeight.values.firstWhere((w) => w.value >= step.weight, orElse: () => FontWeight.w700),
  // Inter is a variable font: set the weight axis so the exact token weight shows.
  fontVariations: [FontVariation.weight(step.weight)],
  letterSpacing: step.trackingEm * step.size,
  color: colour,
);

/// Builds the light or dark theme from the shared design tokens.
ThemeData fimraTheme(Brightness brightness, {FimraDensity density = FimraDensity.comfortable}) {
  final p = brightness == Brightness.light ? lightPalette : darkPalette;
  final scheme = ColorScheme(
    brightness: brightness,
    primary: p.action,
    onPrimary: p.actionText,
    primaryContainer: p.selected,
    onPrimaryContainer: p.text,
    secondary: p.action,
    onSecondary: p.actionText,
    secondaryContainer: p.selected,
    onSecondaryContainer: p.text,
    error: p.danger,
    onError: brightness == Brightness.light ? p.surface : p.canvas,
    surface: p.surface,
    onSurface: p.text,
    onSurfaceVariant: p.textSecondary,
    surfaceContainerLowest: p.canvas,
    surfaceContainerLow: p.canvas,
    surfaceContainer: p.surface,
    surfaceContainerHigh: p.surfaceRaised,
    surfaceContainerHighest: p.selected,
    outline: p.controlBoundary,
    outlineVariant: p.divider,
    scrim: p.scrim,
    shadow: p.shadow,
  );
  final text = TextTheme(
    headlineSmall: _style(FimraType.display, p.text),
    titleLarge: _style(FimraType.title, p.text),
    titleMedium: _style(FimraType.subhead, p.text),
    titleSmall: _style(FimraType.subhead, p.text).copyWith(fontSize: FimraType.body.size),
    bodyLarge: _style(
      FimraType.subhead,
      p.text,
    ).copyWith(fontWeight: FontWeight.w400, fontVariations: const [FontVariation('wght', 400)]),
    bodyMedium: _style(FimraType.body, p.text),
    bodySmall: _style(
      FimraType.label,
      p.textSecondary,
    ).copyWith(fontWeight: FontWeight.w400, fontVariations: const [FontVariation('wght', 400)]),
    labelLarge: _style(FimraType.label, p.text).copyWith(fontSize: FimraType.body.size),
    labelMedium: _style(FimraType.label, p.text),
    labelSmall: _style(FimraType.label, p.textSecondary),
  );
  const minimum = Size(FimraSize.touchTarget, FimraSize.touchTarget);
  final controlRadius = BorderRadius.circular(FimraSize.radiusControl);
  final panelRadius = BorderRadius.circular(FimraSize.radiusPanel);
  final control = RoundedRectangleBorder(borderRadius: controlRadius);
  return ThemeData(
    useMaterial3: true,
    brightness: brightness,
    colorScheme: scheme,
    fontFamily: 'Inter',
    textTheme: text,
    scaffoldBackgroundColor: p.canvas,
    materialTapTargetSize: MaterialTapTargetSize.padded,
    visualDensity: VisualDensity.standard,
    extensions: [FimraColours(p, density: density)],
    focusColor: p.focus,
    appBarTheme: AppBarTheme(
      backgroundColor: p.surface,
      foregroundColor: p.text,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 1,
      titleTextStyle: text.titleMedium,
      shape: Border(bottom: BorderSide(color: p.divider)),
    ),
    cardTheme: CardThemeData(
      color: p.surface,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(FimraSize.radiusCard),
        side: BorderSide(color: p.divider),
      ),
    ),
    dividerTheme: DividerThemeData(color: p.divider, space: 1, thickness: 1),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(minimumSize: minimum, shape: control, textStyle: text.labelLarge),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        minimumSize: minimum,
        shape: control,
        side: BorderSide(color: p.controlBoundary),
        foregroundColor: p.text,
        textStyle: text.labelLarge,
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(minimumSize: minimum, shape: control, textStyle: text.labelLarge),
    ),
    iconButtonTheme: IconButtonThemeData(style: IconButton.styleFrom(minimumSize: minimum)),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: p.surface,
      constraints: const BoxConstraints(minHeight: FimraSize.touchTarget),
      border: OutlineInputBorder(borderRadius: controlRadius),
      enabledBorder: OutlineInputBorder(
        borderRadius: controlRadius,
        borderSide: BorderSide(color: p.controlBoundary),
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: controlRadius,
        borderSide: BorderSide(color: p.focus, width: FimraSize.focusWidth),
      ),
      focusedErrorBorder: OutlineInputBorder(
        borderRadius: controlRadius,
        borderSide: BorderSide(color: p.focus, width: FimraSize.focusWidth),
      ),
    ),
    dialogTheme: DialogThemeData(
      backgroundColor: p.surfaceRaised,
      elevation: FimraElevation.dialog,
      shape: RoundedRectangleBorder(borderRadius: panelRadius),
    ),
    popupMenuTheme: PopupMenuThemeData(color: p.surfaceRaised, elevation: FimraElevation.menu, shape: control),
    navigationBarTheme: NavigationBarThemeData(
      backgroundColor: p.surface,
      indicatorColor: p.selected,
      surfaceTintColor: Colors.transparent,
      labelTextStyle: WidgetStatePropertyAll(text.labelMedium),
      height: 64,
    ),
    listTileTheme: ListTileThemeData(
      minVerticalPadding: 8,
      minTileHeight: FimraSize.touchTarget,
      iconColor: p.textSecondary,
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: p.text,
      contentTextStyle: text.bodyMedium?.copyWith(color: p.canvas),
    ),
    chipTheme: ChipThemeData(
      backgroundColor: p.surface,
      side: BorderSide(color: p.controlBoundary),
      labelStyle: text.labelMedium,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(FimraSize.radiusPill)),
    ),
  );
}
