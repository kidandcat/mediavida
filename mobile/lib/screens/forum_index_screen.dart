import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';
import '../widgets/forum_icon.dart';

/// Home tab: the Mediavida portada (featured-thread feed). Subforums are reached
/// through a dropdown, like the website.
class ForumIndexScreen extends ConsumerWidget {
  const ForumIndexScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final portada = ref.watch(portadaProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('Portada'),
        actions: [
          TextButton.icon(
            onPressed: () => _showSubforums(context, ref),
            icon: const Icon(Icons.menu, size: 20),
            label: const Text('Subforos'),
            style: TextButton.styleFrom(foregroundColor: context.scheme.primary),
          ),
          IconButton(
            icon: const Icon(Icons.watch),
            tooltip: 'Relojes',
            onPressed: () => context.openWatches(),
          ),
          IconButton(
            icon: const Icon(Icons.logout),
            tooltip: 'Cerrar sesión',
            onPressed: () => ref.read(configProvider.notifier).signOut(),
          ),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: () async => ref.invalidate(portadaProvider),
        child: portada.when(
          loading: () => const LoadingView(),
          error: (e, _) => ErrorView(e, onRetry: () => ref.invalidate(portadaProvider)),
          data: (items) {
            if (items.isEmpty) {
              return const EmptyView('Portada vacía', icon: Icons.article_outlined);
            }
            return ListView.builder(
              physics: const AlwaysScrollableScrollPhysics(),
              padding: const EdgeInsets.symmetric(vertical: 8),
              itemCount: items.length,
              itemBuilder: (context, i) => _PortadaCard(items[i]),
            );
          },
        ),
      ),
    );
  }

  Future<void> _showSubforums(BuildContext context, WidgetRef ref) async {
    // Load the forum index, then drop a menu down from the app bar (like the web).
    List<ForumCategory> cats;
    try {
      cats = await ref.read(forumsProvider.future);
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
      }
      return;
    }
    if (!context.mounted) return;

    final items = <PopupMenuEntry<ForumInfo>>[];
    for (final cat in cats) {
      items.add(PopupMenuItem<ForumInfo>(
        enabled: false,
        height: 30,
        child: Text(
          cat.name.toUpperCase(),
          style: TextStyle(
            color: context.scheme.primary,
            fontSize: 11.5,
            fontWeight: FontWeight.w800,
            letterSpacing: 0.5,
          ),
        ),
      ));
      for (final forum in cat.forums) {
        items.add(PopupMenuItem<ForumInfo>(
          value: forum,
          height: 42,
          child: Row(
            children: [
              ForumIcon(forum.icon, size: 22),
              const SizedBox(width: 12),
              Expanded(
                child: Text(forum.name,
                    style: const TextStyle(fontWeight: FontWeight.w600),
                    overflow: TextOverflow.ellipsis),
              ),
            ],
          ),
        ));
      }
    }

    final media = MediaQuery.of(context);
    final top = media.padding.top + kToolbarHeight;
    final selected = await showMenu<ForumInfo>(
      context: context,
      color: context.scheme.surface,
      position: RelativeRect.fromLTRB(media.size.width, top, 8, 0),
      constraints: BoxConstraints(maxHeight: media.size.height * 0.7, minWidth: 240),
      items: items,
    );
    if (selected != null && context.mounted) {
      context.openForum(selected.slug, name: selected.name);
    }
  }
}

class _PortadaCard extends StatelessWidget {
  final PortadaItem item;
  const _PortadaCard(this.item);

  @override
  Widget build(BuildContext context) {
    return Card(
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: () => context.openThread(item.url, title: item.title),
        child: Padding(
          padding: const EdgeInsets.all(10),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (item.image.isNotEmpty)
                ClipRRect(
                  borderRadius: BorderRadius.circular(8),
                  child: CachedNetworkImage(
                    imageUrl: item.image,
                    width: 96,
                    height: 72,
                    fit: BoxFit.cover,
                    placeholder: (c, _) => Container(width: 96, height: 72, color: context.mv.surfaceHigh),
                    errorWidget: (c, _, _) => Container(width: 96, height: 72, color: context.mv.surfaceHigh),
                  ),
                ),
              if (item.image.isNotEmpty) const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        if (item.forum.isNotEmpty)
                          Expanded(
                            child: Text(
                              item.forum.toUpperCase(),
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: TextStyle(
                                color: context.scheme.primary,
                                fontSize: 10.5,
                                fontWeight: FontWeight.w700,
                                letterSpacing: 0.4,
                              ),
                            ),
                          ),
                        if (item.replies.isNotEmpty)
                          Row(
                            children: [
                              Icon(Icons.mode_comment_outlined, size: 12, color: context.mv.textFaint),
                              const SizedBox(width: 3),
                              Text(item.replies,
                                  style: TextStyle(fontSize: 11.5, color: context.mv.textFaint)),
                            ],
                          ),
                      ],
                    ),
                    const SizedBox(height: 3),
                    Text(
                      item.title,
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(fontWeight: FontWeight.w700, fontSize: 14.5, height: 1.2),
                    ),
                    if (item.intro.isNotEmpty) ...[
                      const SizedBox(height: 3),
                      Text(
                        item.intro,
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(fontSize: 12.5, height: 1.3, color: context.mv.textSecondary),
                      ),
                    ],
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
