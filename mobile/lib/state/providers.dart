import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../api/mv_api.dart';
import '../core/config.dart';

/// Holds the current app config (base URL + token). Null until loaded.
class ConfigNotifier extends Notifier<AppConfig?> {
  @override
  AppConfig? build() {
    _load();
    return null;
  }

  Future<void> _load() async {
    state = await AppConfig.load();
  }

  Future<void> setConfig({required String baseUrl, required String token}) async {
    state = await AppConfig.save(baseUrl: baseUrl, token: token);
  }

  Future<void> signOut() async {
    await AppConfig.clear();
    state = const AppConfig(baseUrl: '', token: '');
  }
}

final configProvider =
    NotifierProvider<ConfigNotifier, AppConfig?>(ConfigNotifier.new);

/// The API client, rebuilt whenever the config changes. Null until configured.
final apiProvider = Provider<MvApi?>((ref) {
  final cfg = ref.watch(configProvider);
  if (cfg == null || !cfg.isConfigured) return null;
  return MvApi(baseUrl: cfg.baseUrl, token: cfg.token);
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
