import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

/// Forum index tab: forums grouped by category.
class ForumIndexScreen extends ConsumerWidget {
  const ForumIndexScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final categories = ref.watch(forumsProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('Mediavida'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            tooltip: 'Actualizar',
            onPressed: () => ref.invalidate(forumsProvider),
          ),
          IconButton(
            icon: const Icon(Icons.logout),
            tooltip: 'Cerrar sesión',
            onPressed: () => ref.read(configProvider.notifier).signOut(),
          ),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: () async => ref.invalidate(forumsProvider),
        child: categories.when(
          loading: () => const LoadingView(),
          error: (e, _) => ErrorView(e, onRetry: () => ref.invalidate(forumsProvider)),
          data: (data) {
            if (data.isEmpty) {
              return const EmptyView('No hay foros disponibles', icon: Icons.forum_outlined);
            }
            return ListView.builder(
              physics: const AlwaysScrollableScrollPhysics(),
              padding: const EdgeInsets.only(top: 8, bottom: 16),
              itemCount: data.length,
              itemBuilder: (context, i) {
                final category = data[i];
                return Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    Padding(
                      padding: const EdgeInsets.fromLTRB(18, 16, 18, 6),
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
                      Card(
                        child: ListTile(
                          leading: const Icon(Icons.chevron_right),
                          title: Text(
                            forum.name,
                            style: const TextStyle(fontWeight: FontWeight.bold),
                          ),
                          subtitle: forum.description.trim().isEmpty
                              ? null
                              : Text(
                                  forum.description,
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                  style: const TextStyle(color: Color(0xFF9FB0B8)),
                                ),
                          onTap: () => context.openForum(forum.slug, name: forum.name),
                        ),
                      ),
                  ],
                );
              },
            );
          },
        ),
      ),
    );
  }
}
