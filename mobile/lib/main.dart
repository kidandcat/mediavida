import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'core/notification_service.dart';
import 'router.dart';
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

    await NotificationService().initialize();
    runApp(const ProviderScope(child: MvApp()));
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
  late final _router = buildRouter(ref);

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

    return MaterialApp.router(
      title: 'Mediavida',
      debugShowCheckedModeBanner: false,
      theme: MvTheme.light(),
      darkTheme: MvTheme.dark(),
      themeMode: ThemeMode.system,
      routerConfig: _router,
    );
  }
}
