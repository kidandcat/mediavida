import 'package:flutter/material.dart';

/// Mediavida brand colors, extracted from the live site stylesheets
/// (dark_v7.css and light.css). Exposed as a ThemeExtension so widgets can
/// pick the right value for the active light/dark theme.
@immutable
class MvColors extends ThemeExtension<MvColors> {
  final Color accent; // brand orange (links)
  final Color accentHover; // pressed/darker orange
  final Color surfaceHigh; // chips, inputs, header
  final Color border;
  final Color textSecondary;
  final Color textFaint;
  final Color unread; // "new posts" highlight

  const MvColors({
    required this.accent,
    required this.accentHover,
    required this.surfaceHigh,
    required this.border,
    required this.textSecondary,
    required this.textFaint,
    required this.unread,
  });

  @override
  MvColors copyWith({
    Color? accent,
    Color? accentHover,
    Color? surfaceHigh,
    Color? border,
    Color? textSecondary,
    Color? textFaint,
    Color? unread,
  }) =>
      MvColors(
        accent: accent ?? this.accent,
        accentHover: accentHover ?? this.accentHover,
        surfaceHigh: surfaceHigh ?? this.surfaceHigh,
        border: border ?? this.border,
        textSecondary: textSecondary ?? this.textSecondary,
        textFaint: textFaint ?? this.textFaint,
        unread: unread ?? this.unread,
      );

  @override
  MvColors lerp(ThemeExtension<MvColors>? other, double t) {
    if (other is! MvColors) return this;
    return MvColors(
      accent: Color.lerp(accent, other.accent, t)!,
      accentHover: Color.lerp(accentHover, other.accentHover, t)!,
      surfaceHigh: Color.lerp(surfaceHigh, other.surfaceHigh, t)!,
      border: Color.lerp(border, other.border, t)!,
      textSecondary: Color.lerp(textSecondary, other.textSecondary, t)!,
      textFaint: Color.lerp(textFaint, other.textFaint, t)!,
      unread: Color.lerp(unread, other.unread, t)!,
    );
  }

  static const dark = MvColors(
    accent: Color(0xFFFC8F22),
    accentHover: Color(0xFFDC5A0B),
    surfaceHigh: Color(0xFF343B41),
    border: Color(0xFF3A434A),
    textSecondary: Color(0xFF8F989E),
    textFaint: Color(0xFF5F696F),
    unread: Color(0xFFFC8F22),
  );

  static const light = MvColors(
    accent: Color(0xFFED540C),
    accentHover: Color(0xFFBB5B07),
    surfaceHigh: Color(0xFFEFEAE6),
    border: Color(0xFFE3E1DE),
    textSecondary: Color(0xFF545250),
    textFaint: Color(0xFF8E8A85),
    unread: Color(0xFFE6720E),
  );
}

/// Mediavida theme. Static color getters resolve against [BuildContext] via the
/// [MvContext] extension; the legacy const fields below are dark-theme fallbacks.
class MvTheme {
  // --- Dark palette (tuned to user-specified scale) ---
  static const _darkBg = Color(0xFF1C1F22); // darkest of all
  static const _darkSurface = Color(0xFF2A3237); // next level (cards, appbar, nav)
  static const _darkText = Color(0xFFECEDEF);

  // --- Light palette (mediavida light.css) ---
  static const _lightBg = Color(0xFFFFFCFA);
  static const _lightSurface = Color(0xFFFFFFFF);
  static const _lightText = Color(0xFF262422);

  // Legacy const fallbacks (dark) kept for any non-context usage.
  static const accent = Color(0xFFFC8F22);
  static const bg = _darkBg;
  static const surface = _darkSurface;
  static const surfaceHigh = Color(0xFF343B41);
  static const unread = Color(0xFFFC8F22);

  static ThemeData dark() => _build(
        brightness: Brightness.dark,
        scaffold: _darkBg,
        surface: _darkSurface,
        text: _darkText,
        ext: MvColors.dark,
      );

  static ThemeData light() => _build(
        brightness: Brightness.light,
        scaffold: _lightBg,
        surface: _lightSurface,
        text: _lightText,
        ext: MvColors.light,
      );

  static ThemeData _build({
    required Brightness brightness,
    required Color scaffold,
    required Color surface,
    required Color text,
    required MvColors ext,
  }) {
    final scheme = ColorScheme.fromSeed(
      seedColor: ext.accent,
      brightness: brightness,
    ).copyWith(
      primary: ext.accent,
      onPrimary: brightness == Brightness.dark ? const Color(0xFF1C1F22) : Colors.white,
      secondary: ext.accent,
      surface: surface,
      onSurface: text,
      onSurfaceVariant: ext.textSecondary,
      outline: ext.border,
    );

    final dividerColor = ext.border;

    return ThemeData(
      useMaterial3: true,
      brightness: brightness,
      colorScheme: scheme,
      scaffoldBackgroundColor: scaffold,
      appBarTheme: AppBarTheme(
        backgroundColor: surface,
        foregroundColor: text,
        elevation: 0,
        centerTitle: false,
        scrolledUnderElevation: 2,
      ),
      cardTheme: CardThemeData(
        color: surface,
        elevation: 0,
        margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 5),
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(14)),
      ),
      listTileTheme: ListTileThemeData(iconColor: ext.accent),
      dividerTheme: DividerThemeData(color: dividerColor, thickness: 1, space: 1),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: ext.surfaceHigh,
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(12),
          borderSide: BorderSide.none,
        ),
        contentPadding: const EdgeInsets.symmetric(horizontal: 14, vertical: 14),
      ),
      navigationBarTheme: NavigationBarThemeData(
        backgroundColor: surface,
        indicatorColor: ext.accent.withValues(alpha: 0.18),
        labelTextStyle: WidgetStateProperty.all(
          const TextStyle(fontSize: 11, fontWeight: FontWeight.w600),
        ),
      ),
      snackBarTheme: SnackBarThemeData(
        behavior: SnackBarBehavior.floating,
        backgroundColor: ext.surfaceHigh,
        contentTextStyle: TextStyle(color: text),
      ),
      floatingActionButtonTheme: FloatingActionButtonThemeData(
        backgroundColor: ext.accent,
        foregroundColor: brightness == Brightness.dark ? const Color(0xFF1C1F22) : Colors.white,
      ),
      extensions: [ext],
    );
  }
}

/// Convenient theme accessors for widgets.
extension MvContext on BuildContext {
  ColorScheme get scheme => Theme.of(this).colorScheme;
  MvColors get mv => Theme.of(this).extension<MvColors>() ?? MvColors.dark;
}
