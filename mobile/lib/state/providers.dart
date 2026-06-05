import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../api/mv_api.dart';
import '../core/config.dart';

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

  Future<void> setBaseUrl(String url) async {
    await AppConfig.setBaseUrl(url);
    state = state?.copyWith(baseUrl: url) ?? await AppConfig.load();
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

/// Current notification counters, refreshed on demand and via [bubblesStreamProvider].
final bubblesProvider = FutureProvider<Bubbles>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return const Bubbles();
  return api.bubbles();
});

/// Live notification stream (SSE). Falls back gracefully when disconnected.
final bubblesStreamProvider = StreamProvider<Bubbles>((ref) async* {
  final api = ref.watch(apiProvider);
  if (api == null) return;
  yield* api.events();
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
