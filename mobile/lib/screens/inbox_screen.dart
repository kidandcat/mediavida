import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../api/models.dart';
import '../state/providers.dart';
import '../router.dart';
import '../theme.dart';
import '../widgets/common.dart';

final _inboxProvider = FutureProvider.autoDispose<List<InboxItem>>((ref) async {
  final api = ref.watch(apiProvider);
  if (api == null) return <InboxItem>[];
  return api.inbox(page: 1);
});

class InboxScreen extends ConsumerWidget {
  const InboxScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final inbox = ref.watch(_inboxProvider);
    return Scaffold(
      appBar: AppBar(
        title: const Text('Mensajes'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            tooltip: 'Actualizar',
            onPressed: () => ref.invalidate(_inboxProvider),
          ),
        ],
      ),
      body: inbox.when(
        loading: () => const LoadingView(),
        error: (e, _) =>
            ErrorView(e, onRetry: () => ref.invalidate(_inboxProvider)),
        data: (items) {
          if (items.isEmpty) {
            return const EmptyView('Sin mensajes', icon: Icons.mail_outline);
          }
          return RefreshIndicator(
            onRefresh: () async {
              ref.invalidate(_inboxProvider);
              await ref.read(_inboxProvider.future);
            },
            child: ListView.builder(
              padding: const EdgeInsets.symmetric(vertical: 8),
              itemCount: items.length,
              itemBuilder: (context, index) {
                return _InboxTile(item: items[index]);
              },
            ),
          );
        },
      ),
    );
  }
}

class _InboxTile extends StatelessWidget {
  const _InboxTile({required this.item});

  final InboxItem item;

  @override
  Widget build(BuildContext context) {
    return ListTile(
      leading: MvAvatar(url: '', name: item.author),
      title: Text(
        item.author,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(
          fontWeight: item.unread ? FontWeight.bold : FontWeight.w500,
        ),
      ),
      subtitle: item.preview.isEmpty
          ? null
          : Padding(
              padding: const EdgeInsets.only(top: 2),
              child: Text(
                item.preview,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: 12.5,
                  color: context.mv.textSecondary,
                ),
              ),
            ),
      trailing: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        crossAxisAlignment: CrossAxisAlignment.end,
        children: [
          if (item.date.isNotEmpty)
            Text(
              item.date,
              style: TextStyle(
                fontSize: 11.5,
                color: context.mv.textSecondary,
              ),
            ),
          if (item.unread) ...[
            const SizedBox(height: 6),
            Container(
              width: 10,
              height: 10,
              decoration: BoxDecoration(
                color: context.mv.unread,
                shape: BoxShape.circle,
              ),
            ),
          ],
        ],
      ),
      onTap: () => context.openConversation(item.id, title: item.author),
    );
  }
}
