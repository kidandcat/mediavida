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

class _HomeScreenState extends ConsumerState<HomeScreen> {
  int _index = 0;

  static const _tabs = [
    ForumIndexScreen(),
    FavoritesScreen(),
    NotificationsScreen(),
    InboxScreen(),
    SearchScreen(),
  ];

  @override
  Widget build(BuildContext context) {
    // Prefer the live SSE stream; fall back to the polled value.
    final live = ref.watch(bubblesStreamProvider).value;
    final polled = ref.watch(bubblesProvider).value;
    final b = live ?? polled ?? const Bubbles();

    return Scaffold(
      body: IndexedStack(index: _index, children: _tabs),
      bottomNavigationBar: NavigationBar(
        selectedIndex: _index,
        onDestinationSelected: (i) => setState(() => _index = i),
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
