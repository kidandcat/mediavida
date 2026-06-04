import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

final _userPostsProvider =
    FutureProvider.autoDispose.family<List<ThreadListItem>, String>((ref, username) async {
  final api = ref.watch(apiProvider);
  if (api == null) return const <ThreadListItem>[];
  return api.userPosts(username);
});

final _userMentionsProvider =
    FutureProvider.autoDispose.family<List<Mention>, String>((ref, username) async {
  final api = ref.watch(apiProvider);
  if (api == null) return const <Mention>[];
  return api.mentions(username);
});

class UserScreen extends ConsumerStatefulWidget {
  const UserScreen({super.key, required this.username});

  final String username;

  @override
  ConsumerState<UserScreen> createState() => _UserScreenState();
}

class _UserScreenState extends ConsumerState<UserScreen> {
  @override
  Widget build(BuildContext context) {
    return DefaultTabController(
      length: 2,
      child: Scaffold(
        appBar: AppBar(
          title: Text(widget.username),
          actions: [
            IconButton(
              icon: const Icon(Icons.refresh),
              tooltip: 'Actualizar',
              onPressed: () {
                ref.invalidate(_userPostsProvider(widget.username));
                ref.invalidate(_userMentionsProvider(widget.username));
              },
            ),
          ],
          bottom: const TabBar(
            tabs: [
              Tab(text: 'Hilos'),
              Tab(text: 'Menciones'),
            ],
          ),
        ),
        body: TabBarView(
          children: [
            _PostsTab(username: widget.username),
            _MentionsTab(username: widget.username),
          ],
        ),
      ),
    );
  }
}

class _PostsTab extends ConsumerWidget {
  const _PostsTab({required this.username});

  final String username;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final posts = ref.watch(_userPostsProvider(username));
    void refresh() => ref.invalidate(_userPostsProvider(username));

    return posts.when(
      loading: () => const LoadingView(),
      error: (e, _) => ErrorView(e, onRetry: refresh),
      data: (items) {
        if (items.isEmpty) {
          return const EmptyView('Sin hilos', icon: Icons.forum_outlined);
        }
        return RefreshIndicator(
          onRefresh: () async {
            refresh();
            await ref.read(_userPostsProvider(username).future);
          },
          child: ListView.builder(
            physics: const AlwaysScrollableScrollPhysics(),
            padding: const EdgeInsets.symmetric(vertical: 8),
            itemCount: items.length,
            itemBuilder: (context, i) => _PostTile(item: items[i]),
          ),
        );
      },
    );
  }
}

class _PostTile extends StatelessWidget {
  const _PostTile({required this.item});

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
                    color: context.scheme.onSurface.withValues(alpha: 0.6),
                  ),
                ),
              ),
        trailing: item.unreadCount.isNotEmpty
            ? MvChip(item.unreadCount, color: context.mv.unread)
            : null,
        onTap: () => context.openThread(item.url, title: item.title),
      ),
    );
  }
}

class _MentionsTab extends ConsumerWidget {
  const _MentionsTab({required this.username});

  final String username;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final mentions = ref.watch(_userMentionsProvider(username));
    void refresh() => ref.invalidate(_userMentionsProvider(username));

    return mentions.when(
      loading: () => const LoadingView(),
      error: (e, _) => ErrorView(e, onRetry: refresh),
      data: (items) {
        if (items.isEmpty) {
          return const EmptyView('Sin menciones', icon: Icons.notifications_none);
        }
        return RefreshIndicator(
          onRefresh: () async {
            refresh();
            await ref.read(_userMentionsProvider(username).future);
          },
          child: ListView.builder(
            physics: const AlwaysScrollableScrollPhysics(),
            padding: const EdgeInsets.symmetric(vertical: 8),
            itemCount: items.length,
            itemBuilder: (context, i) => _MentionCard(items[i]),
          ),
        );
      },
    );
  }
}

class _MentionCard extends StatelessWidget {
  const _MentionCard(this.mention);

  final Mention mention;

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 5),
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () => context.openThread(mention.threadUrl, title: mention.threadTitle),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text.rich(
                TextSpan(
                  children: [
                    TextSpan(
                      text: mention.author,
                      style: const TextStyle(fontWeight: FontWeight.w700),
                    ),
                    TextSpan(
                      text: ' en ',
                      style: TextStyle(color: context.mv.textSecondary),
                    ),
                    TextSpan(
                      text: mention.threadTitle,
                      style: TextStyle(
                        color: context.scheme.primary,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                  ],
                ),
                style: const TextStyle(fontSize: 14),
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
              ),
              if (mention.date.isNotEmpty) ...[
                const SizedBox(height: 2),
                Text(
                  mention.date,
                  style: TextStyle(fontSize: 12, color: context.mv.textFaint),
                ),
              ],
              const SizedBox(height: 8),
              Text(
                mention.body,
                maxLines: 3,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(fontSize: 14, height: 1.35, color: context.mv.textSecondary),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
