import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// App connection settings: the backend base URL and the API bearer token.
///
/// Defaults can be baked at build time with
/// `--dart-define=MV_API_URL=... --dart-define=MV_API_TOKEN=...`.
class AppConfig {
  static const _defaultUrl =
      String.fromEnvironment('MV_API_URL', defaultValue: 'https://mediavida-api.fly.dev');
  static const _defaultToken = String.fromEnvironment('MV_API_TOKEN', defaultValue: '');

  static const _kUrl = 'mv_api_url';
  static const _kToken = 'mv_api_token';

  final String baseUrl;
  final String token;

  const AppConfig({required this.baseUrl, required this.token});

  bool get isConfigured => baseUrl.isNotEmpty && token.isNotEmpty;

  static const _secure = FlutterSecureStorage();

  static Future<AppConfig> load() async {
    final prefs = await SharedPreferences.getInstance();
    final url = prefs.getString(_kUrl) ?? _defaultUrl;
    var token = await _secure.read(key: _kToken) ?? '';
    if (token.isEmpty && _defaultToken.isNotEmpty) token = _defaultToken;
    return AppConfig(baseUrl: url, token: token);
  }

  static Future<AppConfig> save({required String baseUrl, required String token}) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(_kUrl, baseUrl);
    await _secure.write(key: _kToken, value: token);
    return AppConfig(baseUrl: baseUrl, token: token);
  }

  static Future<void> clear() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.remove(_kUrl);
    await _secure.delete(key: _kToken);
  }
}
