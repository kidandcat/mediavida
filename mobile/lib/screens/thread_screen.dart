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

class _ThreadScreenState extends ConsumerState<ThreadScreen> with WidgetsBindingObserver {
  ThreadPage? _page;
  Object? _error;
  bool _loading = true;
  bool _sending = false;
  int _replyToNum = 0;

  // Optimistic like state keyed by post number (Post itself is immutable).
  final Map<int, bool> _likeOverride = {};

  final _composer = TextEditingController();
  final _composerFocus = FocusNode();
  final _scroll = ScrollController();

  bool _isLiked(Post post) => _likeOverride[post.num] ?? post.liked;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // Open on the most recent page, positioned at the newest post (bottom).
    _load(widget.initialPage, scrollToBottom: true);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _composer.dispose();
    _composerFocus.dispose();
    _scroll.dispose();
    super.dispose();
  }

  // When the keyboard opens (composer focused), keep the newest post in view.
  @override
  void didChangeMetrics() {
    if (_composerFocus.hasFocus) _jumpToBottom();
  }

  void _jumpToBottom() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_scroll.hasClients) {
        _scroll.jumpTo(_scroll.position.maxScrollExtent);
      }
    });
  }

  Future<void> _load(int page, {bool scrollToBottom = false}) async {
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
      if (scrollToBottom) _jumpToBottom();
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
      await _load(target > 0 ? target : widget.initialPage, scrollToBottom: true);
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
      // Composer lives in the body (not bottomNavigationBar) so it rises above
      // the keyboard when typing a reply.
      body: Column(
        children: [
          Expanded(child: _buildBody()),
          if (page != null) _buildBottomBar(page),
        ],
      ),
    );
  }

  Widget _pagerBtn(IconData icon, String tooltip, VoidCallback? onPressed) {
    return IconButton(
      tooltip: tooltip,
      icon: Icon(icon),
      iconSize: 22,
      visualDensity: VisualDensity.compact,
      padding: EdgeInsets.zero,
      constraints: const BoxConstraints(minWidth: 40, minHeight: 34),
      onPressed: onPressed,
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
        controller: _scroll,
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
              SizedBox(
                height: 34,
                child: Row(
                  mainAxisAlignment: MainAxisAlignment.spaceBetween,
                  children: [
                    _pagerBtn(Icons.first_page, 'Primera', canPrev ? () => _load(1) : null),
                    _pagerBtn(Icons.chevron_left, 'Anterior',
                        canPrev ? () => _load(page.currentPage - 1) : null),
                    TextButton(
                      onPressed: _showJumpDialog,
                      style: TextButton.styleFrom(
                        padding: const EdgeInsets.symmetric(horizontal: 8),
                        minimumSize: const Size(0, 34),
                        tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                      ),
                      child: Text('${page.currentPage} / ${page.totalPages}',
                          style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w600)),
                    ),
                    _pagerBtn(Icons.chevron_right, 'Siguiente',
                        canNext ? () => _load(page.currentPage + 1) : null),
                    _pagerBtn(Icons.last_page, 'Última',
                        canNext ? () => _load(page.totalPages) : null),
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
