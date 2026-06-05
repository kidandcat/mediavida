import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import 'screens/conversation_screen.dart';
import 'screens/forum_threads_screen.dart';
import 'screens/home_screen.dart';
import 'screens/new_thread_screen.dart';
import 'screens/setup_screen.dart';
import 'screens/thread_screen.dart';
import 'screens/user_screen.dart';
import 'state/providers.dart';

/// Router. Redirects to /setup until the app has a base URL + token.
GoRouter buildRouter(WidgetRef ref) {
  return GoRouter(
    initialLocation: '/',
    refreshListenable: _ConfigListenable(ref),
    redirect: (context, state) {
      final cfg = ref.read(configProvider);
      if (cfg == null) return null; // still loading
      final atLogin = state.matchedLocation == '/setup';
      if (!cfg.loggedIn && !atLogin) return '/setup';
      if (cfg.loggedIn && atLogin) return '/';
      return null;
    },
    routes: [
      GoRoute(path: '/setup', builder: (c, s) => const SetupScreen()),
      GoRoute(path: '/', builder: (c, s) => const HomeScreen()),
      GoRoute(
        path: '/forum/:slug',
        builder: (c, s) => ForumThreadsScreen(
          slug: s.pathParameters['slug']!,
          name: (s.extra as Map?)?['name'] as String? ?? s.pathParameters['slug']!,
        ),
      ),
      GoRoute(
        path: '/forum/:slug/new',
        builder: (c, s) => NewThreadScreen(
          slug: s.pathParameters['slug']!,
          name: (s.extra as Map?)?['name'] as String? ?? s.pathParameters['slug']!,
        ),
      ),
      GoRoute(
        path: '/thread',
        builder: (c, s) {
          final extra = (s.extra as Map?) ?? {};
          return ThreadScreen(
            url: extra['url'] as String? ?? s.uri.queryParameters['url'] ?? '',
            title: extra['title'] as String? ?? '',
            initialPage: extra['page'] as int? ?? 0,
            jumpToLatest: extra['jumpToLatest'] as bool? ?? false,
            scrollToPost: extra['scrollToPost'] as int? ?? 0,
          );
        },
      ),
      GoRoute(
        path: '/conversation/:id',
        builder: (c, s) => ConversationScreen(
          id: s.pathParameters['id']!,
          title: (s.extra as Map?)?['title'] as String? ?? '',
        ),
      ),
      GoRoute(
        path: '/user/:username',
        builder: (c, s) => UserScreen(username: s.pathParameters['username']!),
      ),
    ],
  );
}

/// Bridges the Riverpod config provider to go_router's refresh mechanism.
class _ConfigListenable extends ChangeNotifier {
  _ConfigListenable(WidgetRef ref) {
    ref.listen(configProvider, (_, _) => notifyListeners());
  }
}

/// Navigation helpers used across screens.
extension MvNav on BuildContext {
  void openForum(String slug, {String? name}) =>
      push('/forum/$slug', extra: {'name': name ?? slug});
  void openForumNewThread(String slug, {String? name}) =>
      push('/forum/$slug/new', extra: {'name': name ?? slug});
  Future<void> openThread(String url,
          {String title = '', int page = 0, bool jumpToLatest = false, int scrollToPost = 0}) =>
      push('/thread', extra: {
        'url': url,
        'title': title,
        'page': page,
        'jumpToLatest': jumpToLatest,
        'scrollToPost': scrollToPost,
      });
  void openConversation(String id, {String title = ''}) =>
      push('/conversation/$id', extra: {'title': title});
  void openUser(String username) => push('/user/$username');
}
