import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../api/mv_api.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

final _tagsProvider = FutureProvider.autoDispose.family<TagsResponse, String>((ref, slug) async {
  final api = ref.watch(apiProvider);
  if (api == null) throw MvApiException('Sesión no configurada', 0);
  return api.threadTags(slug);
});

class NewThreadScreen extends ConsumerStatefulWidget {
  const NewThreadScreen({super.key, required this.slug, required this.name});

  final String slug;
  final String name;

  @override
  ConsumerState<NewThreadScreen> createState() => _NewThreadScreenState();
}

class _NewThreadScreenState extends ConsumerState<NewThreadScreen> {
  final _formKey = GlobalKey<FormState>();
  final _titleController = TextEditingController();
  final _bodyController = TextEditingController();
  String? _selectedTag;
  bool _addToFavorites = false;
  bool _submitting = false;

  @override
  void dispose() {
    _titleController.dispose();
    _bodyController.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!_formKey.currentState!.validate()) return;
    final api = ref.read(apiProvider);
    if (api == null) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Sesión no configurada')),
      );
      return;
    }
    setState(() => _submitting = true);
    try {
      await api.createThread(
        subforum: widget.slug,
        title: _titleController.text.trim(),
        body: _bodyController.text.trim(),
        tag: _selectedTag == null ? null : int.tryParse(_selectedTag!),
        addToFavorites: _addToFavorites,
      );
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Hilo creado')),
      );
      Navigator.pop(context);
    } on MvApiException catch (e) {
      if (!mounted) return;
      setState(() => _submitting = false);
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(e.message)),
      );
    } catch (e) {
      if (!mounted) return;
      setState(() => _submitting = false);
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('$e')),
      );
    }
  }

  @override
  Widget build(BuildContext context) {
    final tagsAsync = ref.watch(_tagsProvider(widget.slug));

    return Scaffold(
      appBar: AppBar(title: const Text('Nuevo hilo')),
      body: tagsAsync.when(
        loading: () => const LoadingView(),
        error: (e, _) => ErrorView(e, onRetry: () => ref.invalidate(_tagsProvider(widget.slug))),
        data: (tags) => _buildForm(context, tags),
      ),
    );
  }

  Widget _buildForm(BuildContext context, TagsResponse tags) {
    return Form(
      key: _formKey,
      child: ListView(
        padding: const EdgeInsets.fromLTRB(14, 16, 14, 24),
        children: [
          Text(
            widget.name,
            style: TextStyle(
              fontSize: 13,
              fontWeight: FontWeight.w600,
              color: context.scheme.primary,
            ),
          ),
          const SizedBox(height: 16),
          TextFormField(
            controller: _titleController,
            enabled: !_submitting,
            textInputAction: TextInputAction.next,
            decoration: const InputDecoration(labelText: 'Título'),
            validator: (v) => (v == null || v.trim().isEmpty) ? 'Introduce un título' : null,
          ),
          const SizedBox(height: 14),
          TextFormField(
            controller: _bodyController,
            enabled: !_submitting,
            minLines: 6,
            maxLines: null,
            keyboardType: TextInputType.multiline,
            decoration: const InputDecoration(
              labelText: 'Mensaje',
              alignLabelWithHint: true,
            ),
            validator: (v) => (v == null || v.trim().isEmpty) ? 'Escribe el mensaje' : null,
          ),
          const SizedBox(height: 14),
          DropdownButtonFormField<String?>(
            initialValue: _selectedTag,
            decoration: const InputDecoration(labelText: 'Etiqueta'),
            items: [
              const DropdownMenuItem<String?>(
                value: null,
                child: Text('Sin etiqueta'),
              ),
              for (final t in tags.tags)
                DropdownMenuItem<String?>(value: t.id, child: Text(t.name)),
            ],
            onChanged: _submitting ? null : (v) => setState(() => _selectedTag = v),
          ),
          const SizedBox(height: 6),
          SwitchListTile(
            value: _addToFavorites,
            onChanged: _submitting ? null : (v) => setState(() => _addToFavorites = v),
            title: const Text('Añadir a favoritos'),
            contentPadding: EdgeInsets.zero,
          ),
          const SizedBox(height: 16),
          FilledButton(
            onPressed: _submitting ? null : _submit,
            child: _submitting
                ? SizedBox(
                    width: 20,
                    height: 20,
                    child: CircularProgressIndicator(strokeWidth: 2, color: context.scheme.onPrimary),
                  )
                : const Text('Crear hilo'),
          ),
        ],
      ),
    );
  }
}
