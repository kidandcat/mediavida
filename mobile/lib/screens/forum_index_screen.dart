import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

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

  void _showSubforums(BuildContext context, WidgetRef ref) {
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      backgroundColor: context.scheme.surface,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(16)),
      ),
      builder: (ctx) => DraggableScrollableSheet(
        expand: false,
        initialChildSize: 0.7,
        maxChildSize: 0.92,
        builder: (c, scrollCtrl) => Consumer(
          builder: (context, ref, _) {
            final categories = ref.watch(forumsProvider);
            return categories.when(
            loading: () => const LoadingView(),
            error: (e, _) => ErrorView(e, onRetry: () => ref.invalidate(forumsProvider)),
            data: (data) => ListView(
              controller: scrollCtrl,
              padding: const EdgeInsets.only(bottom: 16),
              children: [
                const Padding(
                  padding: EdgeInsets.fromLTRB(18, 14, 18, 6),
                  child: Text('Subforos',
                      style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold)),
                ),
                for (final category in data) ...[
                  Padding(
                    padding: const EdgeInsets.fromLTRB(18, 12, 18, 4),
                    child: Text(
                      category.name.toUpperCase(),
                      style: TextStyle(
                        color: context.scheme.primary,
                        fontSize: 12,
                        fontWeight: FontWeight.w700,
                        letterSpacing: 0.6,
                      ),
                    ),
                  ),
                  for (final forum in category.forums)
                    ListTile(
                      dense: true,
                      title: Text(forum.name, style: const TextStyle(fontWeight: FontWeight.w600)),
                      subtitle: forum.description.trim().isEmpty
                          ? null
                          : Text(forum.description,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: TextStyle(color: context.mv.textFaint, fontSize: 12)),
                      onTap: () {
                        Navigator.pop(ctx);
                        context.openForum(forum.slug, name: forum.name);
                      },
                    ),
                ],
              ],
            ),
            );
          },
        ),
      ),
    );
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
