import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../api/models.dart';
import '../state/providers.dart';
import '../router.dart';
import '../theme.dart';
import '../widgets/common.dart';

final _favoritesProvider =
    FutureProvider.autoDispose<List<ThreadListItem>>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return <ThreadListItem>[];
  return api.favorites();
});

class FavoritesScreen extends ConsumerWidget {
  const FavoritesScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final favorites = ref.watch(_favoritesProvider);
    return Scaffold(
      appBar: AppBar(
        title: const Text('Favoritos'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            tooltip: 'Actualizar',
            onPressed: () => ref.invalidate(_favoritesProvider),
          ),
        ],
      ),
      body: favorites.when(
        loading: () => const LoadingView(),
        error: (e, _) =>
            ErrorView(e, onRetry: () => ref.invalidate(_favoritesProvider)),
        data: (items) {
          if (items.isEmpty) {
            return const EmptyView('Sin favoritos', icon: Icons.star_border);
          }
          return RefreshIndicator(
            onRefresh: () async {
              ref.invalidate(_favoritesProvider);
              await ref.read(_favoritesProvider.future);
            },
            child: ListView.builder(
              padding: const EdgeInsets.symmetric(vertical: 8),
              itemCount: items.length,
              itemBuilder: (context, index) {
                final item = items[index];
                return _FavoriteTile(item: item);
              },
            ),
          );
        },
      ),
    );
  }
}

class _FavoriteTile extends StatelessWidget {
  const _FavoriteTile({required this.item});

  final ThreadListItem item;

  @override
  Widget build(BuildContext context) {
    final subtitle = [
      item.forum,
      if (item.replies.isNotEmpty) '${item.replies} resp.',
      if (item.lastActivity.isNotEmpty) item.lastActivity,
    ].where((s) => s.isNotEmpty).join('  ·  ');

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      child: ListTile(
        title: Text(
          item.title,
          style: const TextStyle(fontWeight: FontWeight.bold),
        ),
        subtitle: subtitle.isEmpty
            ? null
            : Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  subtitle,
                  style: TextStyle(
                    fontSize: 12,
                    color: context.mv.textSecondary,
                  ),
                ),
              ),
        trailing: item.unreadCount.isNotEmpty
            ? MvChip(item.unreadCount, color: context.mv.unread, fg: const Color(0xFF1C1F22))
            : null,
        onTap: () {
          // With unread posts, jump to the first unread (oldest pending), like
          // the web; otherwise open at the latest post.
          if (item.unreadPostNum > 0) {
            context.openThread(item.url,
                title: item.title,
                page: item.unreadPage > 0 ? item.unreadPage : 0,
                scrollToPost: item.unreadPostNum);
          } else {
            context.openThread(item.url, title: item.title, jumpToLatest: true);
          }
        },
      ),
    );
  }
}
