import 'dart:async';
import 'dart:math';

import 'package:dio/dio.dart';

import '../core/config.dart';

/// Retries transient backend failures (429 rate-limit, 503/connection/timeout)
/// with exponential backoff + jitter, honoring `Retry-After` when present.
///
/// `validateStatus` is `s < 500`, so a 429 reaches us via [onResponse] (Dio
/// treats it as a successful response) while a 503 / connection / timeout
/// reaches us via [onError]. Both paths re-dispatch the original request.
class _RetryInterceptor extends Interceptor {
  _RetryInterceptor(this._dio);

  final Dio _dio;
  static const _maxRetries = 3;
  static const _baseDelay = Duration(milliseconds: 500);
  static final _rand = Random();

  static const _attemptKey = '_retryAttempt';

  int _attempt(RequestOptions o) => (o.extra[_attemptKey] as int?) ?? 0;

  /// Exponential backoff (0.5s, 1s, 2s…) with ±20% jitter.
  Duration _backoff(int attempt) {
    final base = _baseDelay.inMilliseconds * (1 << attempt);
    final jitter = base * (0.2 * (_rand.nextDouble() * 2 - 1));
    return Duration(milliseconds: (base + jitter).round());
  }

  Duration? _retryAfter(Headers headers) {
    final v = headers.value('retry-after');
    if (v == null) return null;
    final secs = int.tryParse(v.trim());
    if (secs != null) return Duration(seconds: secs);
    // HTTP-date form: fall back to a small fixed wait.
    return const Duration(seconds: 2);
  }

  bool _retriable(int? status) => status == 429 || status == 503;

  bool _retriableError(DioException e) {
    if (_retriable(e.response?.statusCode)) return true;
    switch (e.type) {
      case DioExceptionType.connectionTimeout:
      case DioExceptionType.sendTimeout:
      case DioExceptionType.receiveTimeout:
      case DioExceptionType.connectionError:
        return true;
      default:
        return false;
    }
  }

  Future<Response<dynamic>> _redispatch(RequestOptions options, int attempt,
      {Duration? retryAfter}) async {
    await Future<void>.delayed(retryAfter ?? _backoff(attempt));
    final next = options..extra[_attemptKey] = attempt + 1;
    return _dio.fetch(next);
  }

  @override
  void onResponse(Response response, ResponseInterceptorHandler handler) async {
    final attempt = _attempt(response.requestOptions);
    if (response.statusCode == 429 && attempt < _maxRetries) {
      try {
        final retried = await _redispatch(response.requestOptions, attempt,
            retryAfter: _retryAfter(response.headers));
        return handler.resolve(retried);
      } catch (e) {
        if (e is DioException) return handler.reject(e);
        rethrow;
      }
    }
    handler.next(response);
  }

  @override
  void onError(DioException err, ErrorInterceptorHandler handler) async {
    final attempt = _attempt(err.requestOptions);
    if (attempt < _maxRetries && _retriableError(err)) {
      try {
        final retried = await _redispatch(err.requestOptions, attempt,
            retryAfter: _retryAfter(err.response?.headers ?? Headers()));
        return handler.resolve(retried);
      } catch (e) {
        if (e is DioException) return handler.reject(e);
        rethrow;
      }
    }
    handler.next(err);
  }
}

enum AppLoginStatus { authenticated, guardRequired, failed }

class AppLoginResult {
  final AppLoginStatus status;
  final String message;
  final String? username;
  AppLoginResult(this.status, {this.message = '', this.username});
}

/// An MV cookie exported by the backend, for injecting into the WebView so the
/// embedded browser shares the backend's logged-in session (single sign-on).
class MvCookie {
  final String name;
  final String value;
  final String domain;
  final String path;
  final bool secure;
  MvCookie({
    required this.name,
    required this.value,
    required this.domain,
    required this.path,
    required this.secure,
  });

  factory MvCookie.fromJson(Map<String, dynamic> j) => MvCookie(
        name: j['name']?.toString() ?? '',
        value: j['value']?.toString() ?? '',
        domain: j['domain']?.toString() ?? '.mediavida.com',
        path: j['path']?.toString() ?? '/',
        secure: j['secure'] == true,
      );
}

/// Thrown when the API rejects the request or the session is unavailable.
class MvApiException implements Exception {
  final int? statusCode;
  final String message;
  MvApiException(this.message, [this.statusCode]);
  @override
  String toString() => 'MvApiException($statusCode): $message';
}

/// Thin client for the Mediavida backend. The app is now a WebView over
/// mediavida.com; this client only handles the pieces that must stay native:
/// login (so the backend holds the MV session that drives push), session status,
/// logout, and exporting the MV cookies for WebView single sign-on.
class MvApi {
  MvApi({required String baseUrl, required String token, this.onUnauthorized})
      : _baseUrl = baseUrl.replaceAll(RegExp(r'/+$'), ''),
        _token = token,
        _dio = Dio(BaseOptions(
          baseUrl: baseUrl.replaceAll(RegExp(r'/+$'), ''),
          headers: {'Authorization': 'Bearer $token'},
          connectTimeout: const Duration(seconds: 20),
          receiveTimeout: const Duration(seconds: 40),
          // We handle non-2xx ourselves to surface API error messages.
          validateStatus: (s) => s != null && s < 500,
        )) {
    // A 401 from any endpoint means the backend lost the Mediavida session and
    // could not renew it: tell the app to drop to the login screen.
    _dio.interceptors.add(InterceptorsWrapper(
      onResponse: (response, handler) {
        if (response.statusCode == 401) onUnauthorized?.call();
        handler.next(response);
      },
    ));
    // Retry transient 429/503 (and connection/timeout) errors. Added after the
    // 401 handler so a 401 still drops to login on the first response.
    _dio.interceptors.add(_RetryInterceptor(_dio));
  }

  /// Invoked when the backend reports the session is gone (HTTP 401), so the
  /// app can sign out and prompt for login again. Never called for login calls.
  final void Function()? onUnauthorized;

  final String _baseUrl;
  final String _token;
  final Dio _dio;

  String get baseUrl => _baseUrl;

  /// Per-device login with Mediavida credentials. The backend ties the MV
  /// session to this device's [_token]. If MV requires guard verification the
  /// result is [AppLoginStatus.guardRequired]; call again passing [totp].
  Future<AppLoginResult> appLogin(String user, String pass, {String? totp}) async {
    final r = await _dio.post(
      '/auth/app-login',
      data: {
        'token': _token,
        'user': user,
        'pass': pass,
        if (totp != null && totp.isNotEmpty) 'totp': totp,
      },
      options: Options(headers: {'X-App-Key': AppConfig.appKey}),
    );
    final d = r.data is Map ? r.data as Map : const {};
    if (r.statusCode != 200) {
      return AppLoginResult(AppLoginStatus.failed,
          message: d['error']?.toString() ?? 'Error de conexión');
    }
    switch (d['status']) {
      case 'authenticated':
        return AppLoginResult(AppLoginStatus.authenticated, username: d['username']?.toString());
      case 'guard_required':
        return AppLoginResult(AppLoginStatus.guardRequired, message: d['message']?.toString() ?? '');
      default:
        return AppLoginResult(AppLoginStatus.failed, message: d['error']?.toString() ?? 'Login fallido');
    }
  }

  Future<void> logout() async {
    try {
      await _dio.post('/auth/logout');
    } catch (_) {}
  }

  /// Whether the backend still holds an authenticated MV session.
  ///
  /// Only a 200 response is conclusive. A non-2xx (e.g. a transient 503 while the
  /// session store is briefly unavailable, surviving the retry interceptor)
  /// THROWS rather than returning false, so the caller treats it as "unknown" and
  /// keeps the session instead of forcing a spurious logout.
  Future<bool> isAuthenticated() async {
    final r = await _dio.get('/auth/status');
    if (r.statusCode != 200) {
      throw MvApiException('status check failed', r.statusCode);
    }
    final d = r.data is Map ? r.data as Map : <String, dynamic>{};
    return d['status'] == 'authenticated';
  }

  /// The MV session cookies for the current account, to inject into the WebView
  /// so the embedded browser is logged in as the same user the backend polls.
  Future<List<MvCookie>> mvCookies() async {
    final r = await _dio.get('/auth/mv-cookies');
    if (r.statusCode != 200) {
      throw MvApiException('cookie fetch failed', r.statusCode);
    }
    final d = r.data is Map ? r.data as Map : <String, dynamic>{};
    return ((d['cookies'] as List?) ?? [])
        .map((e) => MvCookie.fromJson(e as Map<String, dynamic>))
        .toList();
  }
}
