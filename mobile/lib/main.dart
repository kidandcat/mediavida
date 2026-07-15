import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'core/config.dart';
import 'core/notification_service.dart';
import 'screens/setup_screen.dart';
import 'screens/web_screen.dart';
import 'state/providers.dart';
import 'theme.dart';

Future<void> main() async {
  // Swallow otherwise-fatal errors so a single bad widget, image decode, or
  // async exception logs instead of taking the whole app down. Without these
  // handlers an uncaught exception in a callback or platform message can
  // terminate the process (which read as "the app keeps closing").
  runZonedGuarded(() async {
    WidgetsFlutterBinding.ensureInitialized();

    FlutterError.onError = (details) {
      FlutterError.presentError(details);
      debugPrint('[uncaught:flutter] ${details.exceptionAsString()}');
    };
    // Platform-dispatched (engine/isolate) errors: mark handled so they don't
    // propagate to a hard crash.
    PlatformDispatcher.instance.onError = (error, stack) {
      debugPrint('[uncaught:platform] $error');
      return true;
    };

    // Cap the image cache. The default is generous and, combined with
    // full-resolution forum images, lets memory balloon until the OS kills the
    // app. 80 MB is plenty for a scrolling thread of downsampled images.
    PaintingBinding.instance.imageCache.maximumSizeBytes = 80 << 20; // 80 MB

    // Render immediately; never block the first frame on push/FCM init. A hung
    // or throwing FCM/Play-Services init here used to keep runApp from ever
    // running, leaving the app stuck forever on the splash ("cargando
    // indefinidamente"). Initialize notifications in the background, time-boxed
    // and guarded, so a push/FCM failure can't block startup. Push still
    // re-registers on login via the configProvider listener in MvApp.
    runApp(const ProviderScope(child: MvApp()));

    unawaited(NotificationService()
        .initialize()
        .timeout(const Duration(seconds: 8))
        .catchError((Object e, StackTrace st) => debugPrint('[notif:init] $e')));
  }, (error, stack) {
    debugPrint('[uncaught:zone] $error\n$stack');
  });
}

class MvApp extends ConsumerStatefulWidget {
  const MvApp({super.key});

  @override
  ConsumerState<MvApp> createState() => _MvAppState();
}

class _MvAppState extends ConsumerState<MvApp> {
  @override
  Widget build(BuildContext context) {
    // Subscribe to push while logged in; drop the connection on logout.
    ref.listen(configProvider, (prev, next) {
      final svc = NotificationService();
      if (next != null && next.loggedIn && next.deviceToken.isNotEmpty) {
        svc.connect(next.deviceToken);
      } else {
        svc.disconnect();
      }
    });

    final cfg = ref.watch(configProvider);

    return MaterialApp(
      title: 'Mediavida',
      debugShowCheckedModeBanner: false,
      theme: MvTheme.light(),
      darkTheme: MvTheme.dark(),
      themeMode: ThemeMode.system,
      home: _root(cfg),
    );
  }

  /// Simple auth gate: splash while config loads, then the login screen or the
  /// WebView depending on whether the backend holds an MV session for us.
  Widget _root(AppConfig? cfg) {
    if (cfg == null) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    return cfg.loggedIn ? const WebScreen() : const SetupScreen();
  }
}
