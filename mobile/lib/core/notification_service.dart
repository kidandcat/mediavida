import 'dart:async';
import 'dart:convert';

import 'package:crypto/crypto.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:http/http.dart' as http;

/// Receives push notifications from the mediavida ntfy server: keeps an SSE
/// connection to the device's secret topic and shows a local notification for
/// each message. Mirrors the backend's topic derivation (sha256 of the device
/// token) so no extra registration is needed.
class NotificationService {
  static final NotificationService _instance = NotificationService._internal();
  factory NotificationService() => _instance;
  NotificationService._internal();

  /// ntfy server base URL (hardcoded; the official mediavida push server).
  static const ntfyUrl = String.fromEnvironment(
    'MV_NTFY_URL',
    defaultValue: 'https://mediavida-ntfy.fly.dev',
  );

  final FlutterLocalNotificationsPlugin _notifications =
      FlutterLocalNotificationsPlugin();

  StreamSubscription<String>? _sseSubscription;
  http.Client? _httpClient;
  bool _isConnected = false;
  bool _initialized = false;
  String? _topic;

  /// The ntfy topic for a device token — must match the server's ntfyTopic().
  static String topicFor(String deviceToken) {
    final digest = sha256.convert(utf8.encode(deviceToken)).toString();
    return 'mv_${digest.substring(0, 32)}';
  }

  Future<void> initialize() async {
    if (_initialized) return;
    _initialized = true;

    const androidSettings = AndroidInitializationSettings('@mipmap/ic_launcher');
    const iosSettings = DarwinInitializationSettings(
      requestAlertPermission: true,
      requestBadgePermission: true,
      requestSoundPermission: true,
    );
    await _notifications.initialize(
      const InitializationSettings(android: androidSettings, iOS: iosSettings),
    );

    await _notifications
        .resolvePlatformSpecificImplementation<IOSFlutterLocalNotificationsPlugin>()
        ?.requestPermissions(alert: true, badge: true, sound: true);
    await _notifications
        .resolvePlatformSpecificImplementation<AndroidFlutterLocalNotificationsPlugin>()
        ?.requestNotificationsPermission();
  }

  /// Connect (or reconnect) to the topic for the given device token.
  Future<void> connect(String deviceToken) async {
    if (deviceToken.isEmpty) return;
    final topic = topicFor(deviceToken);
    if (_isConnected && topic == _topic) return;
    _topic = topic;
    await _connect();
  }

  Future<void> _connect() async {
    if (_topic == null) return;
    _httpClient?.close();
    _httpClient = http.Client();
    try {
      final uri = Uri.parse('$ntfyUrl/$_topic/sse');
      final req = http.Request('GET', uri)..headers['Accept'] = 'text/event-stream';
      final resp = await _httpClient!.send(req);
      if (resp.statusCode == 200) {
        _isConnected = true;
        debugPrint('[ntfy] connected: $_topic');
        _sseSubscription = resp.stream
            .transform(utf8.decoder)
            .transform(const LineSplitter())
            .listen(
          _handleLine,
          onError: (_) => _onDisconnected(),
          onDone: _onDisconnected,
          cancelOnError: false,
        );
      } else {
        debugPrint('[ntfy] connect failed: HTTP ${resp.statusCode}');
        _onDisconnected();
      }
    } catch (e) {
      debugPrint('[ntfy] connect error: $e');
      _onDisconnected();
    }
  }

  void _onDisconnected() {
    _isConnected = false;
    // Reconnect with a short backoff while we still have a topic.
    Future.delayed(const Duration(seconds: 5), () {
      if (!_isConnected && _topic != null) _connect();
    });
  }

  String _data = '';

  void _handleLine(String line) {
    if (line.startsWith('data: ')) {
      _data = line.substring(6);
    } else if (line.isEmpty && _data.isNotEmpty) {
      _process(_data);
      _data = '';
    }
  }

  void _process(String data) {
    try {
      final json = jsonDecode(data) as Map<String, dynamic>;
      if (json['event'] != 'message') return; // skip open/keepalive events
      final title = json['title'] as String? ?? 'Mediavida';
      final message = json['message'] as String? ?? '';
      _show(title, message);
    } catch (e) {
      debugPrint('[ntfy] bad message: $e');
    }
  }

  Future<void> _show(String title, String body) async {
    const android = AndroidNotificationDetails(
      'mv_notifications',
      'Avisos de Mediavida',
      channelDescription: 'Avisos, menciones y mensajes privados',
      importance: Importance.high,
      priority: Priority.high,
    );
    const ios = DarwinNotificationDetails();
    await _notifications.show(
      DateTime.now().millisecondsSinceEpoch ~/ 1000,
      title,
      body,
      const NotificationDetails(android: android, iOS: ios),
    );
  }

  void disconnect() {
    _topic = null;
    _sseSubscription?.cancel();
    _sseSubscription = null;
    _httpClient?.close();
    _httpClient = null;
    _isConnected = false;
    debugPrint('[ntfy] disconnected');
  }
}
