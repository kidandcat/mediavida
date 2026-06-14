import 'dart:async';
import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../core/notification_service.dart';
import '../core/watch_pairing_server.dart';
import '../state/providers.dart';
import 'favorites_screen.dart';
import 'forum_index_screen.dart';
import 'inbox_screen.dart';
import 'notifications_screen.dart';

/// Bottom-nav shell. Tabs are kept alive via IndexedStack.
class HomeScreen extends ConsumerStatefulWidget {
  const HomeScreen({super.key});

  @override
  ConsumerState<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends ConsumerState<HomeScreen> with WidgetsBindingObserver {
  int _index = 0;

  // Self-scheduling bubbles poll with exponential backoff + jitter. On
  // consecutive errors the interval grows (25→50→100s, capped) so a struggling
  // backend isn't hammered; ±20% jitter de-syncs clients. The backoff resets on
  // any successful fetch and on app resume — otherwise badges would go stale,
  // stuck polling at the cap.
  Timer? _pollTimer;
  int _pollFailures = 0;
  static const _pollBase = Duration(seconds: 25);
  static const _pollMax = Duration(minutes: 5);
  static final _rand = Random();

  static const _tabs = [
    ForumIndexScreen(),
    FavoritesScreen(),
    NotificationsScreen(),
    InboxScreen(),
  ];

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // Drop to login if the backend session is gone (redeploy / expiry).
    WidgetsBinding.instance.addPostFrameCallback((_) {
      ref.read(configProvider.notifier).verifySession();
      _ensureWatchPairing();
    });
    // Poll the notification counters so badges clear after reading.
    _scheduleNextPoll();
  }

  /// Keeps the ambient watch-pairing loopback server bound while the app is
  /// open, so a watch whose token got rejected can silently re-pair the moment
  /// the user opens the app (no manual un-pair/re-pair needed).
  void _ensureWatchPairing() {
    final api = ref.read(apiProvider);
    if (api != null) WatchPairingHub.instance.ensureRunning(api);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _pollTimer?.cancel();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) {
      // Resume from background: reset backoff and poll immediately so badges
      // don't stay stuck at the cap interval after a flaky period.
      _pollFailures = 0;
      _pollNow();
      ref.read(configProvider.notifier).verifySession();
      _ensureWatchPairing();
      // Returning to the foreground (e.g. after tapping a push) clears the tray.
      NotificationService().clearDelivered();
    }
  }

  /// Current poll interval: base doubled per consecutive failure, capped, with
  /// ±20% random jitter.
  Duration _pollInterval() {
    final exp = _pollBase.inMilliseconds * (1 << _pollFailures.clamp(0, 10));
    final capped = exp.clamp(0, _pollMax.inMilliseconds);
    final jitter = capped * (0.2 * (_rand.nextDouble() * 2 - 1));
    return Duration(milliseconds: (capped + jitter).round());
  }

  void _scheduleNextPoll() {
    _pollTimer?.cancel();
    if (!mounted) return;
    _pollTimer = Timer(_pollInterval(), _pollNow);
  }

  /// Refresh the bubbles, then reschedule with backoff based on the outcome.
  Future<void> _pollNow() async {
    if (!mounted) return;
    ref.invalidate(bubblesProvider);
    try {
      await ref.read(bubblesProvider.future);
      _pollFailures = 0; // success → back to base interval
    } catch (_) {
      _pollFailures++; // grow the interval (capped)
    }
    _scheduleNextPoll();
  }

  /// Triggered by SSE pushes, tab switches and nav taps: refresh the counters
  /// without disturbing the poll schedule. The dedup in MvApi.bubbles()
  /// collapses this with a concurrent poll so it doesn't double the GET.
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
          ref.read(homeTabProvider.notifier).select(i);
          _refreshBubbles();
        },
        destinations: [
          const NavigationDestination(
              icon: Icon(Icons.forum_outlined), selectedIcon: Icon(Icons.forum), label: 'Foros'),
          _dest(Icons.star_border, Icons.star, 'Favoritos', b.favorites),
          _dest(Icons.notifications_none, Icons.notifications, 'Avisos', b.notifications),
          _dest(Icons.mail_outline, Icons.mail, 'MPs', b.messages),
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
