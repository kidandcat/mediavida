import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../api/mv_api.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

/// Chat-style private-message conversation: oldest at top, newest at the bottom,
/// with a composer to send replies.
class ConversationScreen extends ConsumerStatefulWidget {
  final String id;
  final String title;
  const ConversationScreen({super.key, required this.id, this.title = ''});

  @override
  ConsumerState<ConversationScreen> createState() => _ConversationScreenState();
}

class _ConversationScreenState extends ConsumerState<ConversationScreen> with WidgetsBindingObserver {
  final _scroll = ScrollController();
  final _composer = TextEditingController();
  final _composerFocus = FocusNode();

  Conversation? _conv;
  Object? _error;
  bool _loading = true;
  bool _sending = false;
  String? _me;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _loadUser();
    _load(scrollToBottom: true);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _scroll.dispose();
    _composer.dispose();
    _composerFocus.dispose();
    super.dispose();
  }

  // Keep the latest message visible when the keyboard opens.
  @override
  void didChangeMetrics() {
    if (_composerFocus.hasFocus) _jumpToBottom();
  }

  Future<void> _loadUser() async {
    final api = ref.read(apiProvider);
    if (api == null) return;
    try {
      final u = await api.currentUser();
      if (mounted) setState(() => _me = u);
    } catch (_) {}
  }

  Future<void> _load({bool scrollToBottom = false}) async {
    final api = ref.read(apiProvider);
    if (api == null) {
      setState(() {
        _loading = false;
        _error = 'Sin conexión';
      });
      return;
    }
    try {
      final c = await api.conversation(widget.id);
      if (!mounted) return;
      setState(() {
        _conv = c;
        _loading = false;
        _error = null;
      });
      if (scrollToBottom) _jumpToBottom();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e is MvApiException ? e.message : e;
        _loading = false;
      });
    }
  }

  void _jumpToBottom() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_scroll.hasClients) {
        _scroll.jumpTo(_scroll.position.maxScrollExtent);
      }
    });
  }

  Future<void> _send() async {
    final text = _composer.text.trim();
    if (text.isEmpty || _sending) return;
    final api = ref.read(apiProvider);
    if (api == null) return;
    setState(() => _sending = true);
    try {
      await api.sendMessage(widget.id, text);
      _composer.clear();
      await _load(scrollToBottom: true);
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context)
          ..hideCurrentSnackBar()
          ..showSnackBar(SnackBar(content: Text(e is MvApiException ? e.message : '$e')));
      }
    } finally {
      if (mounted) setState(() => _sending = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final title = widget.title.isNotEmpty
        ? widget.title
        : (_conv?.title.isNotEmpty == true ? _conv!.title : 'Conversación');
    return Scaffold(
      appBar: AppBar(
        title: Text(title, maxLines: 1, overflow: TextOverflow.ellipsis),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: () => _load(scrollToBottom: true),
          ),
        ],
      ),
      body: Column(
        children: [
          Expanded(child: _buildList()),
          _buildComposer(),
        ],
      ),
    );
  }

  Widget _buildList() {
    if (_loading) return const LoadingView();
    final conv = _conv;
    if (_error != null || conv == null) {
      return ErrorView(_error ?? 'Error', onRetry: () => _load(scrollToBottom: true));
    }
    if (conv.messages.isEmpty) {
      return const EmptyView('Conversación vacía', icon: Icons.mail_outline);
    }
    return RefreshIndicator(
      onRefresh: () => _load(),
      child: ListView.builder(
        controller: _scroll,
        padding: const EdgeInsets.fromLTRB(10, 12, 10, 12),
        itemCount: conv.messages.length,
        itemBuilder: (c, i) {
          final m = conv.messages[i];
          final mine = _me != null && m.author.toLowerCase() == _me!.toLowerCase();
          return _Bubble(message: m, mine: mine);
        },
      ),
    );
  }

  Widget _buildComposer() {
    return SafeArea(
      top: false,
      child: Container(
        padding: const EdgeInsets.fromLTRB(10, 6, 6, 6),
        decoration: BoxDecoration(
          color: context.scheme.surface,
          border: Border(top: BorderSide(color: context.mv.border)),
        ),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.end,
          children: [
            Expanded(
              child: TextField(
                controller: _composer,
                focusNode: _composerFocus,
                minLines: 1,
                maxLines: 5,
                textCapitalization: TextCapitalization.sentences,
                decoration: const InputDecoration(
                  hintText: 'Escribe un mensaje…',
                  contentPadding: EdgeInsets.symmetric(horizontal: 14, vertical: 10),
                ),
              ),
            ),
            const SizedBox(width: 6),
            _sending
                ? const Padding(
                    padding: EdgeInsets.all(10),
                    child: SizedBox(width: 22, height: 22, child: CircularProgressIndicator(strokeWidth: 2)),
                  )
                : IconButton.filled(
                    onPressed: _send,
                    icon: const Icon(Icons.send),
                    style: IconButton.styleFrom(
                      backgroundColor: context.scheme.primary,
                      foregroundColor: context.scheme.onPrimary,
                    ),
                  ),
          ],
        ),
      ),
    );
  }
}

class _Bubble extends StatelessWidget {
  final PrivateMessage message;
  final bool mine;
  const _Bubble({required this.message, required this.mine});

  @override
  Widget build(BuildContext context) {
    // Two slate tones, one per participant; in a 1:1 chat they alternate as
    // the conversation goes back and forth.
    final bubbleColor = mine ? const Color(0xFF39464C) : const Color(0xFF323E43);
    final textColor = context.scheme.onSurface;
    final metaColor = context.mv.textFaint;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        mainAxisAlignment: mine ? MainAxisAlignment.end : MainAxisAlignment.start,
        crossAxisAlignment: CrossAxisAlignment.end,
        children: [
          if (!mine) ...[
            MvAvatar(url: '', name: message.author, size: 30),
            const SizedBox(width: 8),
          ],
          Flexible(
            child: Container(
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              constraints: BoxConstraints(maxWidth: MediaQuery.of(context).size.width * 0.74),
              decoration: BoxDecoration(
                color: bubbleColor,
                borderRadius: BorderRadius.only(
                  topLeft: const Radius.circular(14),
                  topRight: const Radius.circular(14),
                  bottomLeft: Radius.circular(mine ? 14 : 4),
                  bottomRight: Radius.circular(mine ? 4 : 14),
                ),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  if (!mine)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 2),
                      child: Text(
                        message.author,
                        style: TextStyle(fontSize: 12, fontWeight: FontWeight.w700, color: textColor),
                      ),
                    ),
                  Text(message.body, style: TextStyle(fontSize: 15, height: 1.35, color: textColor)),
                  if (message.date.isNotEmpty)
                    Padding(
                      padding: const EdgeInsets.only(top: 3),
                      child: Text(message.date, style: TextStyle(fontSize: 10.5, color: metaColor)),
                    ),
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }
}
