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
  /// When true, open positioned at the newest post (used when entering from
  /// Favorites). Otherwise the thread opens at the top of the loaded page.
  final bool jumpToLatest;
  const ThreadScreen({
    super.key,
    required this.url,
    this.title = '',
    this.initialPage = 0,
    this.jumpToLatest = false,
  });

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

  bool _isLiked(Post post) => _likeOverride[post.num] ?? post.liked;

  /// Resolve a referenced post (#NNNN) for inline expansion. Uses the locally
  /// loaded post when it's on the current page (rendered HTML), otherwise
  /// fetches its text from the backend.
  Future<RefPost?> _fetchRef(int num) async {
    for (final m in _page?.messages ?? const <Post>[]) {
      if (m.num == num) {
        return RefPost(
          author: m.author,
          bodyHtml: m.bodyHtml.trim().isNotEmpty ? m.bodyHtml : null,
          bodyText: m.bodyHtml.trim().isEmpty ? m.body : null,
        );
      }
    }
    final api = ref.read(apiProvider);
    if (api == null) return null;
    try {
      final text = await api.quotedPost(num);
      return RefPost(author: '', bodyText: text);
    } catch (_) {
      return null;
    }
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _loadUser();
    // Load the most recent page; only jump to the newest post when requested
    // (e.g. entering from Favorites).
    _load(widget.initialPage, scrollToBottom: widget.jumpToLatest);
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
            post: post,
            liked: _isLiked(post),
            mine: _isMine(post),
            onAuthorTap: () => context.openUser(post.author),
            onLike: () => _toggleLike(post),
            onQuote: () => _quote(post),
            onEdit: () => _startEdit(post),
            fetchRef: _fetchRef,
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

/// A referenced post resolved for inline expansion.
class RefPost {
  final String author;
  final String? bodyHtml;
  final String? bodyText;
  RefPost({required this.author, this.bodyHtml, this.bodyText});
}

class _PostCard extends StatefulWidget {
  final Post post;
  final bool liked;
  final bool mine;
  final VoidCallback onAuthorTap;
  final VoidCallback onLike;
  final VoidCallback onQuote;
  final VoidCallback onEdit;
  final Future<RefPost?> Function(int postNum) fetchRef;
  const _PostCard({
    required this.post,
    required this.liked,
    required this.mine,
    required this.onAuthorTap,
    required this.onLike,
    required this.onQuote,
    required this.onEdit,
    required this.fetchRef,
  });

  @override
  State<_PostCard> createState() => _PostCardState();
}

class _PostCardState extends State<_PostCard> {
  // Inline-expanded references: num -> content (null while loading).
  final Map<int, RefPost?> _refs = {};
  final Set<int> _loading = {};

  Future<void> _toggleRef(int num) async {
    if (_refs.containsKey(num)) {
      setState(() => _refs.remove(num)); // collapse
      return;
    }
    setState(() {
      _refs[num] = null;
      _loading.add(num);
    });
    final rp = await widget.fetchRef(num);
    if (!mounted) return;
    setState(() {
      _loading.remove(num);
      _refs[num] = rp ?? RefPost(author: '', bodyText: 'No se pudo cargar el mensaje #$num');
    });
  }

  @override
  Widget build(BuildContext context) {
    final post = widget.post;
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
                        onTap: widget.onAuthorTap,
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
                ? PostHtml(post.bodyHtml, onPostRef: _toggleRef)
                : Text(post.body, style: const TextStyle(fontSize: 15, height: 1.45)),
            // Inline-expanded references appear right below the body, like the web.
            for (final num in _refs.keys)
              _RefBox(
                num: num,
                content: _refs[num],
                loading: _loading.contains(num),
                onClose: () => _toggleRef(num),
                onRefTap: _toggleRef,
              ),
            const SizedBox(height: 6),
            Row(
              children: [
                TextButton.icon(
                  onPressed: widget.onLike,
                  icon: Icon(
                    widget.liked ? Icons.thumb_up : Icons.thumb_up_outlined,
                    size: 18,
                    color: widget.liked ? context.scheme.primary : context.mv.textSecondary,
                  ),
                  label: Text(
                    'Me gusta',
                    style: TextStyle(
                      fontSize: 13,
                      color: widget.liked ? context.scheme.primary : context.mv.textSecondary,
                    ),
                  ),
                ),
                const Spacer(),
                if (widget.mine)
                  TextButton.icon(
                    onPressed: widget.onEdit,
                    icon: Icon(Icons.edit_outlined, size: 18, color: context.mv.textSecondary),
                    label: Text(
                      'Editar',
                      style: TextStyle(fontSize: 13, color: context.mv.textSecondary),
                    ),
                  ),
                TextButton.icon(
                  onPressed: widget.onQuote,
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

/// Inline-expanded referenced post, shown within a message (like the web).
class _RefBox extends StatelessWidget {
  final int num;
  final RefPost? content;
  final bool loading;
  final VoidCallback onClose;
  final void Function(int) onRefTap;
  const _RefBox({
    required this.num,
    required this.content,
    required this.loading,
    required this.onClose,
    required this.onRefTap,
  });

  @override
  Widget build(BuildContext context) {
    return Container(
      margin: const EdgeInsets.only(top: 8),
      padding: const EdgeInsets.fromLTRB(10, 6, 6, 8),
      decoration: BoxDecoration(
        color: context.mv.surfaceHigh.withValues(alpha: 0.5),
        border: Border(left: BorderSide(color: context.scheme.primary, width: 3)),
        borderRadius: const BorderRadius.only(
          topRight: Radius.circular(8),
          bottomRight: Radius.circular(8),
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(
                content != null && content!.author.isNotEmpty ? '#$num · ${content!.author}' : '#$num',
                style: TextStyle(
                  fontSize: 12.5,
                  fontWeight: FontWeight.w700,
                  color: context.scheme.primary,
                ),
              ),
              const Spacer(),
              InkWell(
                onTap: onClose,
                child: Icon(Icons.close, size: 16, color: context.mv.textFaint),
              ),
            ],
          ),
          const SizedBox(height: 4),
          if (loading)
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 6),
              child: SizedBox(
                  height: 16, width: 16, child: CircularProgressIndicator(strokeWidth: 2)),
            )
          else if (content?.bodyHtml != null && content!.bodyHtml!.trim().isNotEmpty)
            PostHtml(content!.bodyHtml!, onPostRef: onRefTap)
          else
            Text(content?.bodyText ?? '', style: const TextStyle(fontSize: 14, height: 1.4)),
        ],
      ),
    );
  }
}
