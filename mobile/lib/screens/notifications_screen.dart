import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

final _currentUserProvider = FutureProvider.autoDispose<String?>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return null;
  return api.currentUser();
});

final _mentionsProvider = FutureProvider.autoDispose<List<Mention>>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return const <Mention>[];
  final username = await ref.watch(_currentUserProvider.future);
  if (username == null || username.isEmpty) return const <Mention>[];
  return api.mentions(username);
});

class NotificationsScreen extends ConsumerWidget {
  const NotificationsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final user = ref.watch(_currentUserProvider);
    final mentions = ref.watch(_mentionsProvider);

    void refresh() {
      ref.invalidate(_mentionsProvider);
      ref.invalidate(bubblesProvider);
    }

    final username = user.value;
    final Widget body;
    if (!user.isLoading && (username == null || username.isEmpty)) {
      body = const EmptyView('No has iniciado sesión', icon: Icons.notifications_none);
    } else {
      body = mentions.when(
        loading: () => const LoadingView(),
        error: (e, _) => ErrorView(e, onRetry: refresh),
        data: (items) {
          if (items.isEmpty) {
            return const EmptyView('Sin menciones', icon: Icons.notifications_none);
          }
          return RefreshIndicator(
            onRefresh: () async => refresh(),
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

    return Scaffold(
      appBar: AppBar(
        title: const Text('Avisos'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            tooltip: 'Actualizar',
            onPressed: refresh,
          ),
        ],
      ),
      body: body,
    );
  }
}

class _MentionCard extends StatelessWidget {
  final Mention mention;
  const _MentionCard(this.mention);

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
