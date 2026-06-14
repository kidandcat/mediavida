import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:url_launcher/url_launcher.dart';

import 'screens/conversation_screen.dart';
import 'screens/forum_threads_screen.dart';
import 'screens/home_screen.dart';
import 'screens/new_thread_screen.dart';
import 'screens/profile_screen.dart';
import 'screens/search_screen.dart';
import 'screens/setup_screen.dart';
import 'screens/thread_screen.dart';
import 'screens/user_screen.dart';
import 'screens/watches_screen.dart';
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
      GoRoute(path: '/watches', builder: (c, s) => const WatchesScreen()),
      GoRoute(path: '/profile', builder: (c, s) => const ProfileScreen()),
      GoRoute(path: '/search', builder: (c, s) => const SearchScreen()),
    ],
  );
}

/// App-bar actions shared across the home tabs: search (magnifier) on the left,
/// profile on the right.
List<Widget> profileBarActions(BuildContext context) => [
      IconButton(
        icon: const Icon(Icons.search),
        tooltip: 'Buscar',
        onPressed: () => context.openSearch(),
      ),
      IconButton(
        icon: const Icon(Icons.account_circle_outlined),
        tooltip: 'Perfil',
        onPressed: () => context.openProfile(),
      ),
    ];

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
  void openWatches() => push('/watches');
  void openProfile() => push('/profile');
  void openSearch() => push('/search');

  /// Opens a thread from a Mediavida URL, handling the two shapes MV uses:
  ///  - legacy/notification form: `.../foro/tema.php?tid=X&pagina=Y#POSTNUM`
  ///  - pretty form:              `.../foro/<sub>/<slug>-<tid>/<PAGE>#<POSTNUM>`
  /// In both, the page and the target post are parsed so the thread scrolls to
  /// it (like Favorites). Non-thread links (groups, profiles) open externally.
  Future<void> openThreadFromUrl(String url, {String title = ''}) {
    final uri = Uri.tryParse(url);
    if (uri == null) return openThread(url, title: title);
    // Post number from the fragment (#NNNN), 0 if absent/non-numeric.
    final postNum = int.tryParse(uri.fragment) ?? 0;

    // Notification links use tema.php?tid=X&pagina=Y. Keep the query (the
    // backend paginates it); just drop the fragment.
    if (uri.queryParameters['tid'] != null) {
      final page = int.tryParse(uri.queryParameters['pagina'] ?? '') ?? 0;
      final base = uri.removeFragment().toString();
      return openThread(base, title: title, page: page, scrollToPost: postNum);
    }

    // Pretty thread URL: .../slug-tid[/PAGE].
    if (uri.path.contains('/foro/')) {
      final segments = List<String>.from(uri.pathSegments);
      var page = 0;
      if (segments.isNotEmpty && int.tryParse(segments.last) != null) {
        page = int.parse(segments.removeLast());
      }
      final base = Uri(scheme: uri.scheme, host: uri.host, pathSegments: segments).toString();
      return openThread(base, title: title, page: page, scrollToPost: postNum);
    }

    // Not a thread (e.g. /g/<group>, /id/<user>): open in the browser.
    return launchUrl(uri, mode: LaunchMode.externalApplication).then((_) {});
  }
}
