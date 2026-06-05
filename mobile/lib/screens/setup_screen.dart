import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/mv_api.dart';
import '../state/providers.dart';
import '../theme.dart';

/// Mediavida login: username + password, with a second step for guard
/// verification (email PIN / authenticator code) when MV requires it.
class SetupScreen extends ConsumerStatefulWidget {
  const SetupScreen({super.key});

  @override
  ConsumerState<SetupScreen> createState() => _SetupScreenState();
}

class _SetupScreenState extends ConsumerState<SetupScreen> {
  final _user = TextEditingController();
  final _pass = TextEditingController();
  final _code = TextEditingController();
  bool _busy = false;
  bool _guard = false;
  String? _error;

  @override
  void dispose() {
    _user.dispose();
    _pass.dispose();
    _code.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    final user = _user.text.trim();
    final pass = _pass.text;
    if (user.isEmpty || pass.isEmpty) {
      setState(() => _error = 'Introduce usuario y contraseña');
      return;
    }
    if (_guard && _code.text.trim().isEmpty) {
      setState(() => _error = 'Introduce el código de verificación');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final api = ref.read(apiProvider);
      if (api == null) {
        setState(() => _error = 'Configuración no lista, reintenta');
        return;
      }
      final res = await api.appLogin(user, pass, totp: _guard ? _code.text.trim() : null);
      switch (res.status) {
        case AppLoginStatus.authenticated:
          await ref.read(configProvider.notifier).markLoggedIn();
          // Router redirect handles navigation to '/'.
          break;
        case AppLoginStatus.guardRequired:
          setState(() {
            _guard = true;
            _error = null;
          });
          break;
        case AppLoginStatus.failed:
          setState(() => _error = res.message.isEmpty ? 'Login fallido' : res.message);
          break;
      }
    } catch (e) {
      setState(() => _error = 'No se pudo conectar: $e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 420),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Icon(Icons.forum_rounded, size: 56, color: context.scheme.primary),
                  const SizedBox(height: 16),
                  const Text('Mediavida',
                      textAlign: TextAlign.center,
                      style: TextStyle(fontSize: 26, fontWeight: FontWeight.bold)),
                  const SizedBox(height: 6),
                  Text(_guard ? 'Verificación en dos pasos' : 'Inicia sesión',
                      textAlign: TextAlign.center,
                      style: TextStyle(color: context.mv.textSecondary)),
                  const SizedBox(height: 28),
                  if (!_guard) ...[
                    TextField(
                      controller: _user,
                      autocorrect: false,
                      enableSuggestions: false,
                      textInputAction: TextInputAction.next,
                      decoration: const InputDecoration(
                        labelText: 'Usuario',
                        prefixIcon: Icon(Icons.person_outline),
                      ),
                    ),
                    const SizedBox(height: 14),
                    TextField(
                      controller: _pass,
                      obscureText: true,
                      autocorrect: false,
                      enableSuggestions: false,
                      onSubmitted: (_) => _submit(),
                      decoration: const InputDecoration(
                        labelText: 'Contraseña',
                        prefixIcon: Icon(Icons.lock_outline),
                      ),
                    ),
                  ] else ...[
                    Text(
                      'Introduce el código (PIN del email o tu app de autenticación)',
                      style: TextStyle(color: context.mv.textSecondary, fontSize: 13),
                    ),
                    const SizedBox(height: 12),
                    TextField(
                      controller: _code,
                      autofocus: true,
                      keyboardType: TextInputType.number,
                      onSubmitted: (_) => _submit(),
                      decoration: const InputDecoration(
                        labelText: 'Código',
                        prefixIcon: Icon(Icons.verified_user_outlined),
                      ),
                    ),
                  ],
                  if (_error != null) ...[
                    const SizedBox(height: 14),
                    Text(_error!, style: const TextStyle(color: Color(0xFFE57373))),
                  ],
                  const SizedBox(height: 24),
                  FilledButton(
                    onPressed: _busy ? null : _submit,
                    style: FilledButton.styleFrom(
                      padding: const EdgeInsets.symmetric(vertical: 16),
                      backgroundColor: context.scheme.primary,
                      foregroundColor: context.scheme.onPrimary,
                    ),
                    child: _busy
                        ? const SizedBox(
                            height: 20, width: 20, child: CircularProgressIndicator(strokeWidth: 2))
                        : Text(_guard ? 'Verificar' : 'Entrar',
                            style: const TextStyle(fontWeight: FontWeight.bold)),
                  ),
                  if (_guard)
                    TextButton(
                      onPressed: _busy
                          ? null
                          : () => setState(() {
                                _guard = false;
                                _code.clear();
                                _error = null;
                              }),
                      child: const Text('Volver'),
                    ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
