import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../api/models.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

final _conversationProvider =
    FutureProvider.autoDispose.family<Conversation, String>((ref, id) async {
  final api = ref.watch(apiProvider);
  if (api == null) return const Conversation(title: '', messages: []);
  return api.conversation(id);
});

class ConversationScreen extends ConsumerWidget {
  final String id;
  final String title;

  const ConversationScreen({super.key, required this.id, this.title = ''});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final conversation = ref.watch(_conversationProvider(id));
    return Scaffold(
      appBar: AppBar(
        title: Text(title.isEmpty ? 'Conversación' : title),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            tooltip: 'Actualizar',
            onPressed: () => ref.invalidate(_conversationProvider(id)),
          ),
        ],
      ),
      body: conversation.when(
        loading: () => const LoadingView(),
        error: (e, _) => ErrorView(
          e,
          onRetry: () => ref.invalidate(_conversationProvider(id)),
        ),
        data: (conv) {
          final messages = conv.messages;
          if (messages.isEmpty) {
            return const EmptyView(
              'Conversación vacía',
              icon: Icons.forum_outlined,
            );
          }
          return RefreshIndicator(
            onRefresh: () async {
              ref.invalidate(_conversationProvider(id));
              await ref.read(_conversationProvider(id).future);
            },
            child: ListView.builder(
              padding: const EdgeInsets.symmetric(vertical: 8),
              itemCount: messages.length,
              itemBuilder: (context, index) =>
                  _MessageCard(message: messages[index]),
            ),
          );
        },
      ),
    );
  }
}

class _MessageCard extends StatelessWidget {
  const _MessageCard({required this.message});

  final PrivateMessage message;

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    message.author,
                    style: const TextStyle(
                      fontWeight: FontWeight.bold,
                      fontSize: 13,
                    ),
                  ),
                ),
                if (message.date.isNotEmpty)
                  Text(
                    message.date,
                    style: TextStyle(
                      fontSize: 12,
                      color: context.mv.textSecondary,
                    ),
                  ),
              ],
            ),
            const SizedBox(height: 6),
            Text(
              message.body,
              style: const TextStyle(fontSize: 15, height: 1.45),
            ),
          ],
        ),
      ),
    );
  }
}
