import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../api/mv_api.dart';
import '../state/providers.dart';
import '../router.dart';
import '../theme.dart';
import '../widgets/common.dart';

class ForumThreadsScreen extends ConsumerStatefulWidget {
  final String slug;
  final String name;
  const ForumThreadsScreen({super.key, required this.slug, required this.name});

  @override
  ConsumerState<ForumThreadsScreen> createState() => _ForumThreadsScreenState();
}

class _ForumThreadsScreenState extends ConsumerState<ForumThreadsScreen> {
  final _scrollController = ScrollController();
  final List<ForumThread> _threads = [];

  int _currentPage = 0;
  int _totalPages = 1;
  bool _loading = false;
  bool _initialLoaded = false;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _scrollController.addListener(_onScroll);
    WidgetsBinding.instance.addPostFrameCallback((_) => _loadPage(1, reset: true));
  }

  @override
  void dispose() {
    _scrollController.removeListener(_onScroll);
    _scrollController.dispose();
    super.dispose();
  }

  void _onScroll() {
    if (!_scrollController.hasClients) return;
    final position = _scrollController.position;
    if (position.pixels >= position.maxScrollExtent - 400) {
      _loadMore();
    }
  }

  void _loadMore() {
    if (_loading || _currentPage >= _totalPages) return;
    _loadPage(_currentPage + 1);
  }

  Future<void> _loadPage(int page, {bool reset = false}) async {
    final api = ref.read(apiProvider);
    if (api == null) {
      setState(() {
        _error = 'No hay sesión configurada.';
        _initialLoaded = true;
      });
      return;
    }
    if (_loading) return;
    setState(() {
      _loading = true;
      if (reset) _error = null;
    });
    try {
      final list = await api.forumThreads(widget.slug, page: page);
      if (!mounted) return;
      setState(() {
        if (reset) _threads.clear();
        _threads.addAll(list.threads);
        _currentPage = list.currentPage;
        _totalPages = list.totalPages;
        _loading = false;
        _initialLoaded = true;
        _error = null;
      });
    } on MvApiException catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _initialLoaded = true;
        if (reset || _threads.isEmpty) _error = e.message;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _initialLoaded = true;
        if (reset || _threads.isEmpty) _error = e;
      });
    }
  }

  Future<void> _refresh() => _loadPage(1, reset: true);

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: Text(widget.name)),
      floatingActionButton: FloatingActionButton(
        onPressed: () => context.openForumNewThread(widget.slug, name: widget.name),
        child: const Icon(Icons.edit),
      ),
      body: _buildBody(),
    );
  }

  Widget _buildBody() {
    if (!_initialLoaded && _loading) return const LoadingView();
    if (_error != null && _threads.isEmpty) {
      return ErrorView(_error!, onRetry: () => _loadPage(1, reset: true));
    }
    if (_threads.isEmpty) {
      return RefreshIndicator(
        onRefresh: _refresh,
        child: ListView(
          children: const [
            SizedBox(height: 120),
            EmptyView('No hay hilos en este subforo', icon: Icons.forum_outlined),
          ],
        ),
      );
    }

    final showBottomSpinner = _loading && _threads.isNotEmpty;
    return RefreshIndicator(
      onRefresh: _refresh,
      child: ListView.separated(
        controller: _scrollController,
        padding: const EdgeInsets.symmetric(vertical: 6),
        itemCount: _threads.length + (showBottomSpinner ? 1 : 0),
        separatorBuilder: (_, __) => const Divider(),
        itemBuilder: (context, index) {
          if (index >= _threads.length) {
            return const Padding(
              padding: EdgeInsets.symmetric(vertical: 18),
              child: Center(
                child: SizedBox(
                  width: 22,
                  height: 22,
                  child: CircularProgressIndicator(strokeWidth: 2.5),
                ),
              ),
            );
          }
          return _ThreadRow(thread: _threads[index]);
        },
      ),
    );
  }
}

class _ThreadRow extends StatelessWidget {
  final ForumThread thread;
  const _ThreadRow({required this.thread});

  @override
  Widget build(BuildContext context) {
    final when = relativeTime(thread.lastTime);
    final timeLabel = when.isNotEmpty ? when : thread.lastActivity;

    return ListTile(
      contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 6),
      onTap: () => context.openThread(thread.url, title: thread.title),
      title: Row(
        children: [
          if (thread.pinned) ...[
            Icon(Icons.push_pin, size: 15, color: context.scheme.primary),
            const SizedBox(width: 5),
          ],
          if (thread.closed) ...[
            Icon(Icons.lock_outline, size: 15, color: context.mv.textFaint),
            const SizedBox(width: 5),
          ],
          Expanded(
            child: Text(
              thread.title,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(fontWeight: FontWeight.w700, fontSize: 15),
            ),
          ),
        ],
      ),
      subtitle: Padding(
        padding: const EdgeInsets.only(top: 6),
        child: Wrap(
          spacing: 8,
          runSpacing: 4,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Text(
              thread.author,
              style: TextStyle(color: context.scheme.primary, fontSize: 12.5, fontWeight: FontWeight.w600),
            ),
            if (thread.tag.isNotEmpty) MvChip(thread.tag),
            Text(
              '${thread.replies} resp',
              style: TextStyle(color: context.mv.textSecondary, fontSize: 12.5),
            ),
            Text(
              '${thread.views} vistas',
              style: TextStyle(color: context.mv.textFaint, fontSize: 12.5),
            ),
          ],
        ),
      ),
      trailing: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        crossAxisAlignment: CrossAxisAlignment.end,
        children: [
          if (timeLabel.isNotEmpty)
            Text(timeLabel, style: TextStyle(color: context.mv.textFaint, fontSize: 11.5)),
          if (thread.unreadCount.isNotEmpty) ...[
            const SizedBox(height: 6),
            MvChip(thread.unreadCount, color: context.mv.unread, fg: context.scheme.onSurface),
          ],
        ],
      ),
    );
  }
}
