import 'dart:math';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// App connection + auth state.
///
/// - [baseUrl]: the backend base URL (default baked via --dart-define=MV_API_URL).
/// - [deviceToken]: a per-install random bearer token; the backend ties the
///   Mediavida session to it on login. Generated once, kept in secure storage.
/// - [loggedIn]: whether the device has an authenticated Mediavida session.
///
/// [appKey] is a build-time anti-abuse secret (not a user credential), baked
/// with --dart-define=MV_APP_KEY=... and sent on login as X-App-Key.
class AppConfig {
  // Shared anti-abuse key for the official backend proxy. Baked into the app
  // (an app key always lives on the client), overridable at build time.
  static const appKey = String.fromEnvironment('MV_APP_KEY',
      defaultValue: 'mvapp_8f149532c666c107ebdae87d6fba5fe9fce182d45f81e5e5');
  static const _defaultUrl =
      String.fromEnvironment('MV_API_URL', defaultValue: 'https://mediavida.jairo.cloud');

  /// The hardcoded backend base URL (for non-config consumers like push).
  static String get apiBaseUrl => _defaultUrl;

  static const _kDeviceToken = 'mv_device_token';
  static const _kLoggedIn = 'mv_logged_in';

  final String baseUrl;
  final String deviceToken;
  final bool loggedIn;

  const AppConfig({required this.baseUrl, required this.deviceToken, required this.loggedIn});

  AppConfig copyWith({String? baseUrl, bool? loggedIn}) => AppConfig(
        baseUrl: baseUrl ?? this.baseUrl,
        deviceToken: deviceToken,
        loggedIn: loggedIn ?? this.loggedIn,
      );

  static const _secure = FlutterSecureStorage();

  static Future<AppConfig> load() async {
    final prefs = await SharedPreferences.getInstance();
    // Backend URL is hardcoded (no runtime server selector); ignore any value
    // a previous build may have persisted, which could point at a dead host.
    var token = await _secure.read(key: _kDeviceToken) ?? '';
    if (token.isEmpty) {
      token = _genToken();
      await _secure.write(key: _kDeviceToken, value: token);
    }
    final loggedIn = prefs.getBool(_kLoggedIn) ?? false;
    return AppConfig(baseUrl: _defaultUrl, deviceToken: token, loggedIn: loggedIn);
  }

  static Future<void> setLoggedIn(bool v) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setBool(_kLoggedIn, v);
  }

  static String _genToken() {
    final r = Random.secure();
    final bytes = List<int>.generate(24, (_) => r.nextInt(256));
    return 'mvapp_${bytes.map((b) => b.toRadixString(16).padLeft(2, '0')).join()}';
  }
}
