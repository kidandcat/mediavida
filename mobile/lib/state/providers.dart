import 'dart:async';
import 'dart:math';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../api/mv_api.dart';
import '../core/config.dart';
import '../core/watch_pairing_server.dart';

/// Index of the currently selected bottom-nav tab. Lets a tab kept alive by
/// IndexedStack know whether it is actually on screen (e.g. to avoid loading
/// notifications—which marks them seen—before the user opens that tab).
class HomeTabNotifier extends Notifier<int> {
  @override
  int build() => 0;
  void select(int i) => state = i;
}

final homeTabProvider = NotifierProvider<HomeTabNotifier, int>(HomeTabNotifier.new);

/// Notifications feed for the "Avisos" tab. autoDispose so leaving and
/// re-entering the tab refetches (and re-marks as seen) fresh data.
final notificationsProvider =
    FutureProvider.autoDispose<List<MvNotification>>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return const <MvNotification>[];
  return api.notifications();
});

/// Holds the current app config (base URL + device token + login state).
/// Null until loaded.
class ConfigNotifier extends Notifier<AppConfig?> {
  @override
  AppConfig? build() {
    _load();
    return null;
  }

  Future<void> _load() async {
    state = await AppConfig.load();
  }

  Future<void> markLoggedIn() async {
    await AppConfig.setLoggedIn(true);
    state = state?.copyWith(loggedIn: true) ?? await AppConfig.load();
  }

  /// If we think we're logged in but the backend has no active session
  /// (e.g. it was redeployed or the MV session expired), drop to the login
  /// screen instead of showing errors everywhere.
  Future<void> verifySession() async {
    final cfg = state;
    if (cfg == null || !cfg.loggedIn) return;
    final api = ref.read(apiProvider);
    if (api == null) return;
    try {
      if (!await api.isAuthenticated()) {
        await AppConfig.setLoggedIn(false);
        state = cfg.copyWith(loggedIn: false);
      }
    } catch (_) {}
  }

  Future<void> signOut() async {
    // Explicit sign-out: tear down the ambient watch-pairing server too (a
    // logged-out app must not mint watch tokens).
    await WatchPairingHub.instance.stop();
    try {
      await ref.read(apiProvider)?.logout();
    } catch (_) {}
    await AppConfig.setLoggedIn(false);
    state = state?.copyWith(loggedIn: false) ?? await AppConfig.load();
  }

  /// Called when any API request returns 401: the backend lost the Mediavida
  /// session and couldn't renew it. Drop to the login screen. We don't hit the
  /// backend logout endpoint (the session is already gone) and ignore it while
  /// not logged in, so it never interferes with the login flow itself.
  void handleUnauthorized() {
    final cfg = state;
    if (cfg == null || !cfg.loggedIn) return;
    AppConfig.setLoggedIn(false);
    state = cfg.copyWith(loggedIn: false);
  }
}

final configProvider =
    NotifierProvider<ConfigNotifier, AppConfig?>(ConfigNotifier.new);

/// The API client for the current device token. Null until config is loaded.
final apiProvider = Provider<MvApi?>((ref) {
  final cfg = ref.watch(configProvider);
  if (cfg == null || cfg.deviceToken.isEmpty) return null;
  return MvApi(
    baseUrl: cfg.baseUrl,
    token: cfg.deviceToken,
    onUnauthorized: () => ref.read(configProvider.notifier).handleUnauthorized(),
  );
});

/// The homepage portada feed (featured threads).
final portadaProvider = FutureProvider<List<PortadaItem>>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return [];
  return api.portada();
});

/// The forum index (categories + subforums).
final forumsProvider = FutureProvider<List<ForumCategory>>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return [];
  return api.forums();
});

/// The logged-in user's Mediavida profile (avatar, rank, counters, ...).
/// autoDispose so opening the profile screen refetches fresh stats.
final profileProvider = FutureProvider.autoDispose<Profile?>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return null;
  return api.profile();
});

/// Current notification counters, refreshed on demand and via [bubblesStreamProvider].
final bubblesProvider = FutureProvider<Bubbles>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return const Bubbles();
  return api.bubbles();
});

/// Live notification stream (SSE) with auto-reconnect.
///
/// `events()` completes when the connection drops or its idle watchdog fires;
/// this loop reconnects with exponential backoff + jitter (capped) so an outage
/// doesn't turn into a tight reconnect storm. The backoff resets once a
/// connection has stayed up long enough to be considered healthy. A 401 stops
/// reconnecting (the app drops to login via onUnauthorized inside events()).
final bubblesStreamProvider = StreamProvider<Bubbles>((ref) async* {
  final api = ref.watch(apiProvider);
  if (api == null) return;

  const baseDelay = Duration(seconds: 2);
  const maxDelay = Duration(minutes: 5);
  const healthyAfter = Duration(seconds: 30);
  final rand = Random();

  var disposed = false;
  ref.onDispose(() => disposed = true);

  var attempt = 0;
  while (!disposed) {
    final start = DateTime.now();
    try {
      yield* api.events();
      // Stream ended without error (idle watchdog or server closed it).
    } on MvApiException catch (e) {
      // 401 already triggered onUnauthorized; stop trying to reconnect.
      if (e.statusCode == 401) return;
    } catch (_) {
      // Network error: fall through to the backoff below.
    }
    if (disposed) return;

    // Reset backoff if the connection was healthy (stayed up long enough).
    if (DateTime.now().difference(start) > healthyAfter) {
      attempt = 0;
    } else {
      attempt++;
    }

    final expMs = baseDelay.inMilliseconds * (1 << (attempt - 1).clamp(0, 10));
    final cappedMs = expMs.clamp(0, maxDelay.inMilliseconds);
    final jitter = cappedMs * (0.2 * (rand.nextDouble() * 2 - 1));
    final delay = Duration(milliseconds: (cappedMs + jitter).round());
    await Future<void>.delayed(delay);
  }
});

/// One page of a subforum's threads. Family keyed by "slug:page".
final forumThreadsProvider =
    FutureProvider.family<ForumThreadList, ({String slug, int page})>((ref, arg) async {
  final api = ref.watch(apiProvider);
  if (api == null) {
    return ForumThreadList(subforum: arg.slug, currentPage: arg.page, totalPages: 0, threads: []);
  }
  return api.forumThreads(arg.slug, page: arg.page);
});
