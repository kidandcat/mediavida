import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:scrollable_positioned_list/scrollable_positioned_list.dart';

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
  /// When > 0, after loading scroll to (and highlight) this post number — used
  /// to land on the first unread post of a favorited thread.
  final int scrollToPost;
  const ThreadScreen({
    super.key,
    required this.url,
    this.title = '',
    this.initialPage = 0,
    this.jumpToLatest = false,
    this.scrollToPost = 0,
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
  final ItemScrollController _itemScroll = ItemScrollController();
  String? _me;

  int? _highlightNum; // briefly-highlighted post (the landed-on first unread)
  int _pendingScrollPost = 0;

  bool _isLiked(Post post) => _likeOverride[post.num] ?? post.liked;

  Future<void> _scrollToPost(int num) async {
    final msgs = _page?.messages ?? const <Post>[];
    final idx = msgs.indexWhere((m) => m.num == num);
    if (idx < 0) return;
    if (!_itemScroll.isAttached) await WidgetsBinding.instance.endOfFrame;
    if (!mounted || !_itemScroll.isAttached) return;
    await _itemScroll.scrollTo(
      index: idx,
      alignment: 0.06,
      duration: const Duration(milliseconds: 350),
      curve: Curves.easeOut,
    );
    if (!mounted) return;
    setState(() => _highlightNum = num);
    Future.delayed(const Duration(milliseconds: 1800), () {
      if (mounted) setState(() => _highlightNum = null);
    });
  }

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
      final q = await api.quotedPost(num, url: widget.url);
      return RefPost(
        author: q.author,
        bodyHtml: q.bodyHtml.trim().isNotEmpty ? q.bodyHtml : null,
      );
    } catch (_) {
      return null;
    }
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _loadUser();
    _pendingScrollPost = widget.scrollToPost;
    // Load the requested page; jump to newest only when asked and not landing on
    // a specific unread post.
    _load(widget.initialPage, scrollToBottom: widget.jumpToLatest && widget.scrollToPost == 0);
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
    super.dispose();
  }

  // When the keyboard opens (composer focused), keep the newest post in view.
  @override
  void didChangeMetrics() {
    if (_composerFocus.hasFocus) _jumpToBottom();
  }

  void _jumpToBottom() {
    final n = _page?.messages.length ?? 0;
    if (n > 0 && _itemScroll.isAttached) {
      _itemScroll.jumpTo(index: n - 1);
    }
  }

  /// Scroll to the newest post (last item). Reliable for off-screen items.
  Future<void> _goBottom({bool animate = false}) async {
    final n = _page?.messages.length ?? 0;
    if (n == 0) return;
    if (!_itemScroll.isAttached) await WidgetsBinding.instance.endOfFrame;
    if (!mounted || !_itemScroll.isAttached) return;
    if (animate) {
      await _itemScroll.scrollTo(
        index: n - 1,
        duration: const Duration(milliseconds: 300),
        curve: Curves.easeOut,
      );
    } else {
      _itemScroll.jumpTo(index: n - 1);
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
      if (_pendingScrollPost > 0) {
        final target = _pendingScrollPost;
        _pendingScrollPost = 0;
        _scrollToPost(target);
      } else if (scrollToBottom) {
        _goBottom();
      }
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
      await api.like(post.num, url: widget.url);
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
      source = await api.postSource(post.num, url: widget.url);
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
      await api.editPost(post.num, text, url: widget.url);
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
      await api.reply(text, replyToNum: _replyToNum, url: widget.url);
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
      child: ScrollablePositionedList.builder(
        itemScrollController: _itemScroll,
        physics: const AlwaysScrollableScrollPhysics(),
        padding: const EdgeInsets.only(top: 6, bottom: 16),
        itemCount: page.messages.length,
        itemBuilder: (c, i) {
          final post = page.messages[i];
          return _PostCard(
            // Key by post num so per-card expansion state (refs / replies) stays
            // bound to its post and doesn't bleed across posts when the list
            // rebuilds (e.g. on page change).
            key: ValueKey(post.num),
            post: post,
            liked: _isLiked(post),
            mine: _isMine(post),
            highlighted: _highlightNum == post.num,
            onAuthorTap: () => context.openUser(post.author),
            onLike: () => _toggleLike(post),
            onQuote: () => _quote(post),
            onEdit: () => _startEdit(post),
            fetchRef: _fetchRef,
            fetchReplies: (postNum) async =>
                await ref.read(apiProvider)?.postQuoted(postNum, url: widget.url) ??
                const <QuotedReply>[],
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
  final bool highlighted;
  final VoidCallback onAuthorTap;
  final VoidCallback onLike;
  final VoidCallback onQuote;
  final VoidCallback onEdit;
  final Future<RefPost?> Function(int postNum) fetchRef;
  final Future<List<QuotedReply>> Function(int postNum) fetchReplies;
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
    required this.fetchRef,
    required this.fetchReplies,
  });

  @override
  State<_PostCard> createState() => _PostCardState();
}

class _PostCardState extends State<_PostCard> {
  // Inline-expanded references: num -> content (null while loading).
  final Map<int, RefPost?> _refs = {};
  final Set<int> _loading = {};

  // Inline "N respuestas" expansion (forward-quote replies to this post).
  List<QuotedReply>? _replies;
  bool _loadingReplies = false;
  bool _repliesOpen = false;

  Future<void> _toggleReplies() async {
    if (_repliesOpen) {
      setState(() => _repliesOpen = false); // collapse, keep cached replies
      return;
    }
    if (_replies != null) {
      setState(() => _repliesOpen = true); // re-open without refetching
      return;
    }
    setState(() {
      _repliesOpen = true;
      _loadingReplies = true;
    });
    try {
      final replies = await widget.fetchReplies(widget.post.num);
      if (!mounted) return;
      setState(() {
        _loadingReplies = false;
        _replies = replies;
      });
    } catch (_) {
      // Don't leave the spinner stuck on; collapse so the user can retry.
      if (!mounted) return;
      setState(() {
        _loadingReplies = false;
        _repliesOpen = false;
      });
    }
  }

  /// Renders the post body, inserting each expanded reference (#NNNN) right
  /// below its own `#ID` link — like the web — instead of all at the bottom.
  /// It splits the HTML immediately after the ref's `<a>…</a>` so the quote
  /// lands between the id and the rest of the message. With nothing expanded it
  /// renders the whole body as a single widget (no splitting, no behavior change).
  List<Widget> _buildBodyWithRefs(String bodyHtml) {
    if (_refs.isEmpty) {
      return [PostHtml(bodyHtml, onPostRef: _toggleRef)];
    }
    // Drop a unique marker right after each expanded reference's <a>…</a>.
    var html = bodyHtml;
    for (final num in _refs.keys) {
      final re = RegExp('(<a\\b[^>]*#$num"[^>]*>.*?</a>)', dotAll: true);
      html = html.replaceFirstMapped(re, (m) => '${m.group(1)}@@mvref:$num@@');
    }
    final widgets = <Widget>[];
    final emitted = <int>{};
    final marker = RegExp(r'@@mvref:(\d+)@@');
    var last = 0;
    for (final m in marker.allMatches(html)) {
      final chunk = html.substring(last, m.start);
      if (chunk.trim().isNotEmpty) widgets.add(PostHtml(chunk, onPostRef: _toggleRef));
      final num = int.parse(m.group(1)!);
      widgets.add(_refBox(num));
      emitted.add(num);
      last = m.end;
    }
    final tail = html.substring(last);
    if (tail.trim().isNotEmpty) widgets.add(PostHtml(tail, onPostRef: _toggleRef));
    // Refs whose <a> wasn't found in the body fall back to the bottom.
    for (final num in _refs.keys) {
      if (!emitted.contains(num)) widgets.add(_refBox(num));
    }
    return widgets;
  }

  Widget _refBox(int num) => _RefBox(
        num: num,
        content: _refs[num],
        loading: _loading.contains(num),
        onClose: () => _toggleRef(num),
        onRefTap: _toggleRef,
      );

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
      color: widget.highlighted ? context.scheme.primary.withValues(alpha: 0.16) : null,
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
            if (post.bodyHtml.trim().isNotEmpty)
              ..._buildBodyWithRefs(post.bodyHtml)
            else
              Text(post.body, style: const TextStyle(fontSize: 15, height: 1.45)),
            // "N respuestas": forward-quote replies expanded inline, like the web.
            if (post.replies > 0) ...[
              const SizedBox(height: 6),
              Align(
                alignment: Alignment.centerLeft,
                child: TextButton.icon(
                  onPressed: _loadingReplies ? null : _toggleReplies,
                  style: TextButton.styleFrom(
                    padding: const EdgeInsets.symmetric(horizontal: 8),
                    minimumSize: const Size(0, 32),
                    tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                    foregroundColor: context.scheme.primary,
                  ),
                  icon: Icon(
                    _repliesOpen ? Icons.expand_less : Icons.expand_more,
                    size: 18,
                  ),
                  label: Text(
                    '${post.replies} ${post.replies == 1 ? "respuesta" : "respuestas"}',
                    style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w600),
                  ),
                ),
              ),
              if (_repliesOpen)
                if (_loadingReplies)
                  const Padding(
                    padding: EdgeInsets.symmetric(vertical: 8),
                    child: SizedBox(
                        height: 16, width: 16, child: CircularProgressIndicator(strokeWidth: 2)),
                  )
                else
                  for (final r in _replies ?? const <QuotedReply>[])
                    _ReplyBox(reply: r, onRefTap: _toggleRef),
            ],
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

/// A forward-quote reply ("N respuestas") expanded inline below a post, styled
/// like [_RefBox]. Renders the reply's HTML so nested spoilers/links work.
class _ReplyBox extends StatelessWidget {
  final QuotedReply reply;
  final void Function(int) onRefTap;
  const _ReplyBox({required this.reply, required this.onRefTap});

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
          Text(
            reply.author.isNotEmpty ? '#${reply.num} · ${reply.author}' : '#${reply.num}',
            style: TextStyle(
              fontSize: 12.5,
              fontWeight: FontWeight.w700,
              color: context.scheme.primary,
            ),
          ),
          const SizedBox(height: 4),
          if (reply.bodyHtml.trim().isNotEmpty)
            PostHtml(reply.bodyHtml, onPostRef: onRefTap)
          else
            const Text('(sin contenido)', style: TextStyle(fontSize: 14, height: 1.4)),
        ],
      ),
    );
  }
}
