import 'dart:async';
import 'dart:math';

import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_messaging/firebase_messaging.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:http/http.dart' as http;

import '../firebase_options.dart';
import 'config.dart';

/// Background isolate handler. Messages we send carry a `notification` block, so
/// the OS shows them automatically when the app is backgrounded/closed; this
/// handler just needs to exist for firebase_messaging to wire up the isolate.
@pragma('vm:entry-point')
Future<void> _firebaseBackgroundHandler(RemoteMessage message) async {}

/// Push notifications via FCM. Registers the device's FCM token with the backend
/// (tied to the device bearer token) and renders foreground messages as local
/// notifications. Background/closed delivery is handled by the OS via APNs/FCM.
class NotificationService {
  static final NotificationService _instance = NotificationService._internal();
  factory NotificationService() => _instance;
  NotificationService._internal();

  final FlutterLocalNotificationsPlugin _local = FlutterLocalNotificationsPlugin();
  static final _rand = Random();

  bool _initialized = false;
  String? _deviceToken; // backend bearer, used to register the FCM token
  String? _fcmToken;
  StreamSubscription<String>? _tokenRefreshSub;

  Future<void> initialize() async {
    if (_initialized) return;
    _initialized = true;

    await Firebase.initializeApp(options: DefaultFirebaseOptions.currentPlatform);
    FirebaseMessaging.onBackgroundMessage(_firebaseBackgroundHandler);

    const androidInit = AndroidInitializationSettings('@mipmap/ic_launcher');
    const iosInit = DarwinInitializationSettings();
    await _local.initialize(const InitializationSettings(android: androidInit, iOS: iosInit));

    final messaging = FirebaseMessaging.instance;
    await messaging.requestPermission();
    // iOS: also surface notifications while the app is in the foreground.
    await messaging.setForegroundNotificationPresentationOptions(
      alert: true,
      badge: true,
      sound: true,
    );

    // Foreground messages aren't shown by the OS on Android — render them.
    FirebaseMessaging.onMessage.listen((msg) {
      final n = msg.notification;
      if (n != null) _showLocal(n.title ?? 'Mediavida', n.body ?? '');
    });

    // Tapping a push (backgrounded → resumed) or launching from one (terminated)
    // should clear the tray so the same notification doesn't linger — most
    // visibly on iOS, where delivered notifications stay in Notification Center.
    final initial = await FirebaseMessaging.instance.getInitialMessage();
    if (initial != null) await clearDelivered();
    FirebaseMessaging.onMessageOpenedApp.listen((_) => clearDelivered());
  }

  /// Clear all delivered notifications from the tray / Notification Center and
  /// reset the iOS app badge. On iOS [cancelAll] removes already-delivered
  /// notifications; on Android it cancels them too.
  Future<void> clearDelivered() async {
    await _local.cancelAll();
  }

  /// Register this device for push under the given backend bearer token.
  Future<void> connect(String deviceToken) async {
    if (deviceToken.isEmpty) return;
    await initialize();
    _deviceToken = deviceToken;
    try {
      _fcmToken = await FirebaseMessaging.instance.getToken();
      if (_fcmToken != null) await _register(_fcmToken!);
    } catch (e) {
      debugPrint('[fcm] getToken failed: $e');
    }
    // Re-register when FCM rotates the token.
    _tokenRefreshSub ??= FirebaseMessaging.instance.onTokenRefresh.listen((t) {
      _fcmToken = t;
      _register(t);
    });
  }

  /// Stop receiving push for this device (called on logout).
  Future<void> disconnect() async {
    final token = _fcmToken;
    final device = _deviceToken;
    _deviceToken = null;
    if (token != null && device != null) {
      try {
        await http.delete(
          Uri.parse('${AppConfig.apiBaseUrl}/push/register'),
          headers: {'Authorization': 'Bearer $device', 'Content-Type': 'application/json'},
          body: '{"token":"$token"}',
        );
      } catch (_) {}
    }
  }

  /// Register the FCM token with the backend, retrying transient failures.
  ///
  /// A single fire-and-forget POST used to silently drop push registration if it
  /// hit a 429/503 (e.g. during a backend deploy). Retry a few times with a
  /// short bounded backoff + jitter so a transient blip recovers on its own.
  Future<void> _register(String fcmToken) async {
    final device = _deviceToken;
    if (device == null) return;
    const maxTries = 3;
    for (var attempt = 0; attempt < maxTries; attempt++) {
      // Bail if the device logged out (or token changed) between retries.
      if (_deviceToken != device) return;
      try {
        final r = await http.post(
          Uri.parse('${AppConfig.apiBaseUrl}/push/register'),
          headers: {'Authorization': 'Bearer $device', 'Content-Type': 'application/json'},
          body: '{"token":"$fcmToken"}',
        );
        debugPrint('[fcm] register: HTTP ${r.statusCode} (try ${attempt + 1})');
        if (r.statusCode >= 200 && r.statusCode < 300) return;
        // Non-2xx (incl. 429/503): fall through to backoff unless out of tries.
      } catch (e) {
        debugPrint('[fcm] register failed (try ${attempt + 1}): $e');
      }
      if (attempt < maxTries - 1) {
        final ms = 500 * (1 << attempt);
        final jitter = (ms * 0.2 * (_rand.nextDouble() * 2 - 1)).round();
        await Future<void>.delayed(Duration(milliseconds: ms + jitter));
      }
    }
  }

  Future<void> _showLocal(String title, String body) async {
    const android = AndroidNotificationDetails(
      'mv_notifications',
      'Avisos de Mediavida',
      channelDescription: 'Avisos, menciones y mensajes privados',
      importance: Importance.high,
      priority: Priority.high,
    );
    const ios = DarwinNotificationDetails();
    await _local.show(
      DateTime.now().millisecondsSinceEpoch ~/ 1000,
      title,
      body,
      const NotificationDetails(android: android, iOS: ios),
    );
  }
}
