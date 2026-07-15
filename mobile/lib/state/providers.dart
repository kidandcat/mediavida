import 'package:flutter_riverpod/flutter_riverpod.dart';

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

  /// Called when an API request returns 401: the backend lost the Mediavida
  /// session and couldn't renew it. Drop to the login screen. Ignored while not
  /// logged in so it never interferes with the login flow itself.
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
