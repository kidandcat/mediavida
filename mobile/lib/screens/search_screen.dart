import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../api/models.dart';
import '../api/mv_api.dart';
import '../state/providers.dart';
import '../router.dart';
import '../theme.dart';
import '../widgets/common.dart';

class SearchScreen extends ConsumerStatefulWidget {
  const SearchScreen({super.key});

  @override
  ConsumerState<SearchScreen> createState() => _SearchScreenState();
}

class _SearchScreenState extends ConsumerState<SearchScreen> {
  final _controller = TextEditingController();

  String _query = '';
  bool _searched = false;
  bool _loading = false;
  Object? _error;
  List<SearchResult> _results = const [];

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  Future<void> _runSearch() async {
    final query = _controller.text.trim();
    if (query.isEmpty) return;
    FocusScope.of(context).unfocus();

    final api = ref.read(apiProvider);
    if (api == null) {
      setState(() {
        _query = query;
        _searched = true;
        _loading = false;
        _error = 'No hay sesion configurada.';
        _results = const [];
      });
      return;
    }

    setState(() {
      _query = query;
      _searched = true;
      _loading = true;
      _error = null;
    });

    try {
      final results = await api.search(query);
      if (!mounted) return;
      setState(() {
        _loading = false;
        _results = results;
      });
    } on MvApiException catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _error = e.message;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _error = e;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: TextField(
          controller: _controller,
          autofocus: false,
          textInputAction: TextInputAction.search,
          onSubmitted: (_) => _runSearch(),
          decoration: InputDecoration(
            hintText: 'Buscar en Mediavida',
            prefixIcon: const Icon(Icons.search),
            suffixIcon: _controller.text.isEmpty
                ? null
                : IconButton(
                    icon: const Icon(Icons.clear),
                    tooltip: 'Limpiar',
                    onPressed: () {
                      setState(() {
                        _controller.clear();
                        _query = '';
                        _searched = false;
                        _error = null;
                        _results = const [];
                      });
                    },
                  ),
          ),
          onChanged: (_) => setState(() {}),
        ),
      ),
      body: _buildBody(),
    );
  }

  Widget _buildBody() {
    if (!_searched) {
      return const EmptyView('Busca en Mediavida', icon: Icons.search);
    }
    if (_loading) {
      return const LoadingView();
    }
    if (_error != null) {
      return ErrorView(_error!, onRetry: _runSearch);
    }
    if (_results.isEmpty) {
      return const EmptyView('Sin resultados', icon: Icons.search_off);
    }
    return ListView.builder(
      padding: const EdgeInsets.symmetric(vertical: 8),
      itemCount: _results.length,
      itemBuilder: (context, index) => _ResultTile(result: _results[index]),
    );
  }
}

class _ResultTile extends StatelessWidget {
  const _ResultTile({required this.result});

  final SearchResult result;

  @override
  Widget build(BuildContext context) {
    final subtitle = [
      if (result.forum.isNotEmpty) result.forum,
      if (result.author.isNotEmpty) result.author,
      if (result.date.isNotEmpty) result.date,
    ].join('  ·  ');

    return Card(
      child: ListTile(
        title: Text(
          result.title,
          style: const TextStyle(fontWeight: FontWeight.bold),
        ),
        subtitle: subtitle.isEmpty
            ? null
            : Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  subtitle,
                  style: TextStyle(
                    fontSize: 12,
                    color: context.mv.textSecondary,
                  ),
                ),
              ),
        onTap: () => context.openThread(result.url, title: result.title),
      ),
    );
  }
}
