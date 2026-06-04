import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../state/providers.dart';
import 'favorites_screen.dart';
import 'forum_index_screen.dart';
import 'inbox_screen.dart';
import 'notifications_screen.dart';
import 'search_screen.dart';

/// Bottom-nav shell. Tabs are kept alive via IndexedStack.
class HomeScreen extends ConsumerStatefulWidget {
  const HomeScreen({super.key});

  @override
  ConsumerState<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends ConsumerState<HomeScreen> with WidgetsBindingObserver {
  int _index = 0;
  Timer? _timer;

  static const _tabs = [
    ForumIndexScreen(),
    FavoritesScreen(),
    NotificationsScreen(),
    InboxScreen(),
    SearchScreen(),
  ];

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // Poll the notification counters so badges clear after reading.
    _timer = Timer.periodic(const Duration(seconds: 25), (_) => _refreshBubbles());
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _timer?.cancel();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) _refreshBubbles();
  }

  void _refreshBubbles() {
    if (mounted) ref.invalidate(bubblesProvider);
  }

  @override
  Widget build(BuildContext context) {
    // A live SSE push just triggers a fresh poll (avoids a stale stream value
    // overriding the counters after they've been cleared server-side).
    ref.listen(bubblesStreamProvider, (prev, next) {
      if (next.hasValue) _refreshBubbles();
    });
    final b = ref.watch(bubblesProvider).value ?? const Bubbles();

    return Scaffold(
      body: IndexedStack(index: _index, children: _tabs),
      bottomNavigationBar: NavigationBar(
        selectedIndex: _index,
        onDestinationSelected: (i) {
          setState(() => _index = i);
          _refreshBubbles();
        },
        destinations: [
          const NavigationDestination(
              icon: Icon(Icons.forum_outlined), selectedIcon: Icon(Icons.forum), label: 'Foros'),
          _dest(Icons.star_border, Icons.star, 'Favoritos', b.favorites),
          _dest(Icons.notifications_none, Icons.notifications, 'Avisos', b.notifications),
          _dest(Icons.mail_outline, Icons.mail, 'MPs', b.messages),
          const NavigationDestination(
              icon: Icon(Icons.search), selectedIcon: Icon(Icons.search), label: 'Buscar'),
        ],
      ),
    );
  }

  NavigationDestination _dest(IconData icon, IconData selected, String label, int count) {
    Widget wrap(IconData i) => count > 0
        ? Badge(label: Text('$count'), child: Icon(i))
        : Icon(i);
    return NavigationDestination(icon: wrap(icon), selectedIcon: wrap(selected), label: label);
  }
}
