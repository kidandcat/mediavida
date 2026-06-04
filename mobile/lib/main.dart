import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'router.dart';
import 'theme.dart';

void main() {
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
