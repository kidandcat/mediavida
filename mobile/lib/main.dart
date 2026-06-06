import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'core/notification_service.dart';
import 'router.dart';
import 'state/providers.dart';
import 'theme.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await NotificationService().initialize();
  runApp(const ProviderScope(child: MvApp()));
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
