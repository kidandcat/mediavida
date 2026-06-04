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

  Future<void> signOut() async {
    try {
      await ref.read(apiProvider)?.logout();
    } catch (_) {}
    await AppConfig.setLoggedIn(false);
    state = state?.copyWith(loggedIn: false) ?? await AppConfig.load();
  }
}

final configProvider =
    NotifierProvider<ConfigNotifier, AppConfig?>(ConfigNotifier.new);

/// The API client for the current device token. Null until config is loaded.
final apiProvider = Provider<MvApi?>((ref) {
  final cfg = ref.watch(configProvider);
  if (cfg == null || cfg.deviceToken.isEmpty) return null;
  return MvApi(baseUrl: cfg.baseUrl, token: cfg.deviceToken);
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
