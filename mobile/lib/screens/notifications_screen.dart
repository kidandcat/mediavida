import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

/// Index of the "Avisos" tab in the bottom nav (see HomeScreen._tabs).
const _avisosTabIndex = 2;

class NotificationsScreen extends ConsumerWidget {
  const NotificationsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    // IndexedStack keeps this tab built even when off-screen. Only load the feed
    // while it is actually selected, because fetching marks every notification
    // as seen on Mediavida — we must not do that before the user opens Avisos.
    final active = ref.watch(homeTabProvider) == _avisosTabIndex;

    final Widget body;
    if (!active) {
      body = const SizedBox.shrink();
    } else {
      // Fetching cleared them server-side; refresh the bubble so the badge drops.
      ref.listen(notificationsProvider, (prev, next) {
        if (next is AsyncData) ref.invalidate(bubblesProvider);
      });
      final notifs = ref.watch(notificationsProvider);
      body = notifs.when(
        loading: () => const LoadingView(),
        error: (e, _) => ErrorView(e, onRetry: () => ref.invalidate(notificationsProvider)),
        data: (items) {
          if (items.isEmpty) {
            return const EmptyView('Sin avisos', icon: Icons.notifications_none);
          }
          return RefreshIndicator(
            onRefresh: () async {
              ref.invalidate(notificationsProvider);
              await ref.read(notificationsProvider.future);
            },
            child: ListView.builder(
              physics: const AlwaysScrollableScrollPhysics(),
              padding: const EdgeInsets.symmetric(vertical: 8),
              itemCount: items.length,
              itemBuilder: (context, i) => _NotificationCard(items[i]),
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
            onPressed: () => ref.invalidate(notificationsProvider),
          ),
        ],
      ),
      body: body,
    );
  }
}

class _NotificationCard extends StatelessWidget {
  final MvNotification n;
  const _NotificationCard(this.n);

  @override
  Widget build(BuildContext context) {
    // Split the phrasing into "<author> <action> <target>" so we can bold the
    // author and highlight the linked thread, like the rest of the app.
    final author = n.author;
    var rest = n.text;
    if (author.isNotEmpty && rest.startsWith(author)) {
      rest = rest.substring(author.length);
    }
    final target = n.target;
    String middle = rest;
    if (target.isNotEmpty) {
      final idx = rest.lastIndexOf(target);
      if (idx >= 0) middle = rest.substring(0, idx);
    }

    final unseen = n.unseen;
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 5),
      color: unseen ? context.scheme.primary.withValues(alpha: 0.10) : null,
      shape: unseen
          ? RoundedRectangleBorder(
              borderRadius: BorderRadius.circular(12),
              side: BorderSide(color: context.scheme.primary.withValues(alpha: 0.55)),
            )
          : null,
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: n.url.isEmpty ? null : () => context.openThread(n.url, title: target),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (unseen)
                Padding(
                  padding: const EdgeInsets.only(top: 5, right: 10),
                  child: Container(
                    width: 8,
                    height: 8,
                    decoration: BoxDecoration(
                      color: context.scheme.primary,
                      shape: BoxShape.circle,
                    ),
                  ),
                ),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text.rich(
                      TextSpan(
                        children: [
                          TextSpan(
                            text: author,
                            style: TextStyle(
                              fontWeight: unseen ? FontWeight.w800 : FontWeight.w700,
                            ),
                          ),
                          TextSpan(
                            text: middle,
                            style: TextStyle(color: context.mv.textSecondary),
                          ),
                          if (target.isNotEmpty)
                            TextSpan(
                              text: target,
                              style: TextStyle(
                                color: context.scheme.primary,
                                fontWeight: FontWeight.w600,
                              ),
                            ),
                        ],
                      ),
                      style: const TextStyle(fontSize: 14, height: 1.35),
                      maxLines: 3,
                      overflow: TextOverflow.ellipsis,
                    ),
                    if (n.time.isNotEmpty) ...[
                      const SizedBox(height: 4),
                      Text(
                        n.time,
                        style: TextStyle(fontSize: 12, color: context.mv.textFaint),
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
