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
  String? _me;

  // Per-post keys (to scroll to a referenced #NNNN) and the briefly-highlighted post.
  final Map<int, GlobalKey> _postKeys = {};
  int? _highlightNum;

  bool _isLiked(Post post) => _likeOverride[post.num] ?? post.liked;

  Future<void> _openPostRef(int num) async {
    final key = _postKeys[num];
    if (key?.currentContext != null) {
      await Scrollable.ensureVisible(key!.currentContext!,
          duration: const Duration(milliseconds: 350), alignment: 0.15);
      if (!mounted) return;
      setState(() => _highlightNum = num);
      Future.delayed(const Duration(milliseconds: 1600), () {
        if (mounted) setState(() => _highlightNum = null);
      });
      return;
    }
    // Referenced post is on another page: fetch its text and show it.
    final api = ref.read(apiProvider);
    if (api == null) return;
    String text;
    try {
      text = await api.quotedPost(num);
    } catch (e) {
      _snack(e is MvApiException ? e.message : '$e');
      return;
    }
    if (!mounted || text.trim().isEmpty) return;
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      backgroundColor: context.scheme.surface,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(16)),
      ),
      builder: (ctx) => DraggableScrollableSheet(
        expand: false,
        initialChildSize: 0.5,
        maxChildSize: 0.85,
        builder: (c, scrollCtrl) => ListView(
          controller: scrollCtrl,
          padding: const EdgeInsets.all(16),
          children: [
            Text('Mensaje #$num',
                style: const TextStyle(fontWeight: FontWeight.w700, fontSize: 15)),
            const SizedBox(height: 10),
            SelectableText(text, style: const TextStyle(fontSize: 15, height: 1.4)),
          ],
        ),
      ),
    );
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _loadUser();
    // Open on the most recent page, positioned at the newest post (bottom).
    _load(widget.initialPage, scrollToBottom: true);
  }

  Future<void> _loadUser() async {
    final api = ref.read(apiProvider);
    if (api == null) return;
    try {
      final u = await api.currentUser();
      if (mounted) setState(() => _me = u);
    } catch (_) {}
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

  /// Scrolls to the true bottom. With ListView.builder the max scroll extent is
  /// only an estimate until the last items are laid out (and grows as images
  /// load), so we re-settle to the latest extent over several frames.
  Future<void> _goBottom({bool animate = false}) async {
    if (!_scroll.hasClients) {
      await WidgetsBinding.instance.endOfFrame;
      if (!mounted || !_scroll.hasClients) return;
    }
    if (animate) {
      await _scroll.animateTo(
        _scroll.position.maxScrollExtent,
        duration: const Duration(milliseconds: 280),
        curve: Curves.easeOut,
      );
    }
    for (final ms in const [0, 60, 180, 400]) {
      await Future<void>.delayed(Duration(milliseconds: ms));
      if (!mounted || !_scroll.hasClients) return;
      _scroll.jumpTo(_scroll.position.maxScrollExtent);
    }
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
      if (scrollToBottom) _goBottom();
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

  bool _isMine(Post post) =>
      _me != null && post.author.toLowerCase() == _me!.toLowerCase();

  Future<void> _startEdit(Post post) async {
    final api = ref.read(apiProvider);
    if (api == null) return;
    // Ensure the backend's last-read thread is this page, then fetch the source.
    String source;
    try {
      source = await api.postSource(post.num);
    } catch (e) {
      _snack(e is MvApiException ? e.message : '$e');
      return;
    }
    if (!mounted) return;
    final controller = TextEditingController(text: source);
    final saved = await showModalBottomSheet<bool>(
      context: context,
      isScrollControlled: true,
      backgroundColor: context.scheme.surface,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(16)),
      ),
      builder: (ctx) => Padding(
        padding: EdgeInsets.only(
          left: 14,
          right: 14,
          top: 14,
          bottom: MediaQuery.of(ctx).viewInsets.bottom + 14,
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text('Editar mensaje #${post.num}',
                style: const TextStyle(fontWeight: FontWeight.w700, fontSize: 15)),
            const SizedBox(height: 12),
            TextField(
              controller: controller,
              autofocus: true,
              minLines: 4,
              maxLines: 12,
              decoration: const InputDecoration(hintText: 'Contenido del mensaje'),
            ),
            const SizedBox(height: 12),
            Row(
              mainAxisAlignment: MainAxisAlignment.end,
              children: [
                TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Cancelar')),
                const SizedBox(width: 8),
                FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Guardar')),
              ],
            ),
          ],
        ),
      ),
    );
    if (saved != true) return;
    final text = controller.text.trim();
    if (text.isEmpty) return;
    try {
      await api.editPost(post.num, text);
      _snack('Mensaje editado');
      await _load(_page?.currentPage ?? widget.initialPage);
    } catch (e) {
      _snack(e is MvApiException ? e.message : '$e');
    }
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
        itemBuilder: (c, i) {
          final post = page.messages[i];
          return _PostCard(
            key: _postKeys.putIfAbsent(post.num, () => GlobalKey()),
            post: post,
            liked: _isLiked(post),
            mine: _isMine(post),
            highlighted: _highlightNum == post.num,
            onAuthorTap: () => context.openUser(post.author),
            onLike: () => _toggleLike(post),
            onQuote: () => _quote(post),
            onEdit: () => _startEdit(post),
            onPostRef: _openPostRef,
          );
        },
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
                    _pagerBtn(Icons.vertical_align_bottom, 'Ir al final de la página',
                        () => _goBottom(animate: true)),
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
  final bool mine;
  final bool highlighted;
  final VoidCallback onAuthorTap;
  final VoidCallback onLike;
  final VoidCallback onQuote;
  final VoidCallback onEdit;
  final void Function(int postNum) onPostRef;
  const _PostCard({
    super.key,
    required this.post,
    required this.liked,
    required this.mine,
    required this.highlighted,
    required this.onAuthorTap,
    required this.onLike,
    required this.onQuote,
    required this.onEdit,
    required this.onPostRef,
  });

  @override
  Widget build(BuildContext context) {
    final rel = relativeTime(post.time);
    final when = rel.isNotEmpty ? rel : post.date;
    return Card(
      color: highlighted ? context.scheme.primary.withValues(alpha: 0.16) : null,
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
                ? PostHtml(post.bodyHtml, onPostRef: onPostRef)
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
                if (mine)
                  TextButton.icon(
                    onPressed: onEdit,
                    icon: Icon(Icons.edit_outlined, size: 18, color: context.mv.textSecondary),
                    label: Text(
                      'Editar',
                      style: TextStyle(fontSize: 13, color: context.mv.textSecondary),
                    ),
                  ),
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
