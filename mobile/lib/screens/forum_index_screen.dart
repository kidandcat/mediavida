import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';
import '../widgets/subforos_title.dart';

/// Home tab: the Mediavida portada (featured-thread feed). Subforums are reached
/// through the dropdown in the app bar, like the website.
class ForumIndexScreen extends ConsumerWidget {
  const ForumIndexScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final portada = ref.watch(portadaProvider);

    return Scaffold(
      appBar: AppBar(
        title: const SubforosTitle(),
        actions: profileBarActions(context),
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
