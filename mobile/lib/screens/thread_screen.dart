import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../api/mv_api.dart';
import '../state/providers.dart';
import '../router.dart';
import '../theme.dart';
import '../widgets/common.dart';

/// Thread detail screen: read a thread, navigate its pages, like posts and reply.
class ThreadScreen extends ConsumerStatefulWidget {
  final String url;
  final String title;
  final int initialPage;
  const ThreadScreen({super.key, required this.url, this.title = '', this.initialPage = 0});

  @override
  ConsumerState<ThreadScreen> createState() => _ThreadScreenState();
}

class _ThreadScreenState extends ConsumerState<ThreadScreen> {
  ThreadPage? _page;
  Object? _error;
  bool _loading = true;
  bool _sending = false;
  int _replyToNum = 0;

  // Optimistic like state keyed by post number (Post itself is immutable).
  final Map<int, bool> _likeOverride = {};

  final _composer = TextEditingController();
  final _composerFocus = FocusNode();

  bool _isLiked(Post post) => _likeOverride[post.num] ?? post.liked;

  @override
  void initState() {
    super.initState();
    _load(widget.initialPage);
  }

  @override
  void dispose() {
    _composer.dispose();
    _composerFocus.dispose();
    super.dispose();
  }

  Future<void> _load(int page) async {
    final api = ref.read(apiProvider);
    if (api == null) {
      setState(() {
        _loading = false;
        _error = 'No hay sesión configurada.';
      });
      return;
    }
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final result = await api.thread(widget.url, page: page);
      if (!mounted) return;
      setState(() {
        _page = result;
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e is MvApiException ? e.message : e;
        _loading = false;
      });
    }
  }

  void _snack(String message) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(message)));
  }

  Future<void> _toggleLike(Post post) async {
    final api = ref.read(apiProvider);
    if (api == null) return;
    final wasLiked = _isLiked(post);
    setState(() => _likeOverride[post.num] = !wasLiked);
    try {
      await api.like(post.num);
    } catch (e) {
      if (mounted) setState(() => _likeOverride[post.num] = wasLiked);
      _snack(e is MvApiException ? e.message : '$e');
    }
  }

  void _quote(Post post) {
    setState(() => _replyToNum = post.num);
    _composerFocus.requestFocus();
  }

  Future<void> _submitReply() async {
    final text = _composer.text.trim();
    if (text.isEmpty || _sending) return;
    final api = ref.read(apiProvider);
    if (api == null) {
      _snack('No hay sesión configurada.');
      return;
    }
    setState(() => _sending = true);
    try {
      await api.reply(text, replyToNum: _replyToNum);
      _composer.clear();
      _composerFocus.unfocus();
      setState(() => _replyToNum = 0);
      _snack('Publicado');
      final target = _page?.totalPages ?? 0;
      await _load(target > 0 ? target : widget.initialPage);
    } catch (e) {
      _snack(e is MvApiException ? e.message : '$e');
    } finally {
      if (mounted) setState(() => _sending = false);
    }
  }

  Future<void> _showJumpDialog() async {
    final page = _page;
    if (page == null || page.totalPages <= 1) return;
    final controller = TextEditingController(text: '${page.currentPage}');
    final target = await showDialog<int>(
      context: context,
      builder: (ctx) => AlertDialog(
        backgroundColor: context.scheme.surface,
        title: const Text('Ir a página'),
        content: TextField(
          controller: controller,
          autofocus: true,
          keyboardType: TextInputType.number,
          decoration: InputDecoration(hintText: '1 - ${page.totalPages}'),
          onSubmitted: (v) => Navigator.pop(ctx, int.tryParse(v.trim())),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancelar'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, int.tryParse(controller.text.trim())),
            child: const Text('Ir'),
          ),
        ],
      ),
    );
    if (target != null) {
      final clamped = target.clamp(1, page.totalPages);
      if (clamped != page.currentPage) _load(clamped);
    }
  }

  @override
  Widget build(BuildContext context) {
    final page = _page;
    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              widget.title.isNotEmpty ? widget.title : 'Hilo',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(fontSize: 16),
            ),
            if (page != null)
              GestureDetector(
                onTap: page.totalPages > 1 ? _showJumpDialog : null,
                child: Text(
                  'pag ${page.currentPage}/${page.totalPages}',
                  style: TextStyle(fontSize: 12, color: context.mv.textSecondary),
                ),
              ),
          ],
        ),
      ),
      body: _buildBody(),
      bottomNavigationBar: page == null ? null : _buildBottomBar(page),
    );
  }

  Widget _buildBody() {
    if (_loading) return const LoadingView();
    final page = _page;
    if (_error != null || page == null) {
      return ErrorView(_error ?? 'Error', onRetry: () => _load(widget.initialPage));
    }
    if (page.messages.isEmpty) {
      return const EmptyView('No hay mensajes en esta página.', icon: Icons.forum_outlined);
    }
    return RefreshIndicator(
      onRefresh: () => _load(page.currentPage),
      child: ListView.builder(
        padding: const EdgeInsets.only(top: 6, bottom: 16),
        itemCount: page.messages.length,
        itemBuilder: (c, i) => _PostCard(
          post: page.messages[i],
          liked: _isLiked(page.messages[i]),
          onAuthorTap: () => context.openUser(page.messages[i].author),
          onLike: () => _toggleLike(page.messages[i]),
          onQuote: () => _quote(page.messages[i]),
        ),
      ),
    );
  }

  Widget _buildBottomBar(ThreadPage page) {
    final canPrev = page.currentPage > 1;
    final canNext = page.currentPage < page.totalPages;
    return SafeArea(
      top: false,
      child: Container(
        decoration: BoxDecoration(
          color: context.scheme.surface,
          border: const Border(top: BorderSide(color: Color(0xFF243036))),
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (page.totalPages > 1)
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 2),
                child: Row(
                  mainAxisAlignment: MainAxisAlignment.spaceBetween,
                  children: [
                    IconButton(
                      tooltip: 'Primera',
                      icon: const Icon(Icons.first_page),
                      onPressed: canPrev ? () => _load(1) : null,
                    ),
                    IconButton(
                      tooltip: 'Anterior',
                      icon: const Icon(Icons.chevron_left),
                      onPressed: canPrev ? () => _load(page.currentPage - 1) : null,
                    ),
                    TextButton(
                      onPressed: _showJumpDialog,
                      child: Text('${page.currentPage} / ${page.totalPages}'),
                    ),
                    IconButton(
                      tooltip: 'Siguiente',
                      icon: const Icon(Icons.chevron_right),
                      onPressed: canNext ? () => _load(page.currentPage + 1) : null,
                    ),
                    IconButton(
                      tooltip: 'Última',
                      icon: const Icon(Icons.last_page),
                      onPressed: canNext ? () => _load(page.totalPages) : null,
                    ),
                  ],
                ),
              ),
            _buildComposer(),
          ],
        ),
      ),
    );
  }

  Widget _buildComposer() {
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 4, 12, 8),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (_replyToNum > 0)
            Padding(
              padding: const EdgeInsets.only(bottom: 6),
              child: Row(
                children: [
                  Icon(Icons.format_quote, size: 16, color: context.scheme.primary),
                  const SizedBox(width: 6),
                  Expanded(
                    child: Text(
                      'Respondiendo al #$_replyToNum',
                      style: TextStyle(fontSize: 12, color: context.mv.textSecondary),
                    ),
                  ),
                  InkWell(
                    onTap: () => setState(() => _replyToNum = 0),
                    child: Padding(
                      padding: const EdgeInsets.all(2),
                      child: Icon(Icons.close, size: 16, color: context.mv.textFaint),
                    ),
                  ),
                ],
              ),
            ),
          Row(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Expanded(
                child: TextField(
                  controller: _composer,
                  focusNode: _composerFocus,
                  minLines: 1,
                  maxLines: 5,
                  textInputAction: TextInputAction.newline,
                  decoration: const InputDecoration(hintText: 'Escribe una respuesta...'),
                ),
              ),
              const SizedBox(width: 8),
              _sending
                  ? const Padding(
                      padding: EdgeInsets.all(10),
                      child: SizedBox(
                        width: 20,
                        height: 20,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      ),
                    )
                  : IconButton.filled(
                      icon: const Icon(Icons.send),
                      onPressed: _submitReply,
                    ),
            ],
          ),
        ],
      ),
    );
  }
}

class _PostCard extends StatelessWidget {
  final Post post;
  final bool liked;
  final VoidCallback onAuthorTap;
  final VoidCallback onLike;
  final VoidCallback onQuote;
  const _PostCard({
    required this.post,
    required this.liked,
    required this.onAuthorTap,
    required this.onLike,
    required this.onQuote,
  });

  @override
  Widget build(BuildContext context) {
    final rel = relativeTime(post.time);
    final when = rel.isNotEmpty ? rel : post.date;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                MvAvatar(url: post.avatar, name: post.author),
                const SizedBox(width: 10),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      InkWell(
                        onTap: onAuthorTap,
                        child: Text(
                          post.author,
                          style: const TextStyle(fontWeight: FontWeight.w700, fontSize: 14),
                        ),
                      ),
                      if (when.isNotEmpty)
                        Text(
                          when,
                          style: TextStyle(fontSize: 11.5, color: context.mv.textFaint),
                        ),
                    ],
                  ),
                ),
                MvChip('#${post.num}', color: context.mv.surfaceHigh),
              ],
            ),
            const SizedBox(height: 10),
            post.bodyHtml.trim().isNotEmpty
                ? PostHtml(post.bodyHtml)
                : Text(post.body, style: const TextStyle(fontSize: 15, height: 1.45)),
            const SizedBox(height: 6),
            Row(
              children: [
                TextButton.icon(
                  onPressed: onLike,
                  icon: Icon(
                    liked ? Icons.thumb_up : Icons.thumb_up_outlined,
                    size: 18,
                    color: liked ? context.scheme.primary : context.mv.textSecondary,
                  ),
                  label: Text(
                    'Me gusta',
                    style: TextStyle(
                      fontSize: 13,
                      color: liked ? context.scheme.primary : context.mv.textSecondary,
                    ),
                  ),
                ),
                const Spacer(),
                TextButton.icon(
                  onPressed: onQuote,
                  icon: Icon(Icons.reply, size: 18, color: context.mv.textSecondary),
                  label: Text(
                    'Responder',
                    style: TextStyle(fontSize: 13, color: context.mv.textSecondary),
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
