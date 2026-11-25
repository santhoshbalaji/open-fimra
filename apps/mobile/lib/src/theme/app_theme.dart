import 'package:flutter/material.dart';

import 'tokens.g.dart';

export 'tokens.g.dart';

/// Gives widgets the current theme's token palette.
class FimraColours extends ThemeExtension<FimraColours> {
  const FimraColours(this.palette);
  final FimraPalette palette;

  static FimraPalette of(BuildContext context) => Theme.of(context).extension<FimraColours>()!.palette;

  @override
  FimraColours copyWith({FimraPalette? palette}) => FimraColours(palette ?? this.palette);

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
ThemeData fimraTheme(Brightness brightness) {
  final p = brightness == Brightness.light ? lightPalette : darkPalette;
  final scheme = ColorScheme(
    brightness: brightness,
    primary: p.brand,
    onPrimary: p.brandForeground,
    primaryContainer: p.brandSoft,
    onPrimaryContainer: p.text,
    secondary: p.brand,
    onSecondary: p.brandForeground,
    secondaryContainer: p.brandSoft,
    onSecondaryContainer: p.text,
    error: p.danger,
    onError: brightness == Brightness.light ? p.card : p.background,
    surface: p.card,
    onSurface: p.text,
    onSurfaceVariant: p.textSecondary,
    surfaceContainerLowest: p.background,
    surfaceContainerLow: p.background,
    surfaceContainer: p.card,
    surfaceContainerHigh: p.card,
    surfaceContainerHighest: p.brandSoft,
    outline: p.border,
    outlineVariant: p.border,
    scrim: p.scrim,
    shadow: const Color(0xFF000000),
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
  final control = RoundedRectangleBorder(borderRadius: BorderRadius.circular(FimraSize.radiusControl));
  return ThemeData(
    useMaterial3: true,
    brightness: brightness,
    colorScheme: scheme,
    fontFamily: 'Inter',
    textTheme: text,
    scaffoldBackgroundColor: p.background,
    materialTapTargetSize: MaterialTapTargetSize.padded,
    visualDensity: VisualDensity.standard,
    extensions: [FimraColours(p)],
    appBarTheme: AppBarTheme(
      backgroundColor: p.card,
      foregroundColor: p.text,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 1,
      titleTextStyle: text.titleMedium,
      shape: Border(bottom: BorderSide(color: p.border)),
    ),
    cardTheme: CardThemeData(
      color: p.card,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(FimraSize.radiusCard),
        side: BorderSide(color: p.border),
      ),
    ),
    dividerTheme: DividerThemeData(color: p.border, space: 1, thickness: 1),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(minimumSize: minimum, shape: control, textStyle: text.labelLarge),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        minimumSize: minimum,
        shape: control,
        side: BorderSide(color: p.border),
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
      fillColor: p.card,
      constraints: const BoxConstraints(minHeight: FimraSize.touchTarget),
      border: OutlineInputBorder(borderRadius: BorderRadius.circular(FimraSize.radiusControl)),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(FimraSize.radiusControl),
        borderSide: BorderSide(color: p.border),
      ),
    ),
    navigationBarTheme: NavigationBarThemeData(
      backgroundColor: p.card,
      indicatorColor: p.brandSoft,
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
      contentTextStyle: text.bodyMedium?.copyWith(color: p.background),
    ),
    chipTheme: ChipThemeData(
      backgroundColor: p.card,
      side: BorderSide(color: p.border),
      labelStyle: text.labelMedium,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(FimraSize.radiusPill)),
    ),
  );
}
