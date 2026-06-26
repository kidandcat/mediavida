import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../core/watch_pairing_server.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

/// "Relojes" — pair an Amazfit watchface with the user's session.
///
/// Pairing runs through [WatchPairingHub] (app-scoped), NOT this screen, so the
/// loopback server stays up while the user switches to the Zepp app to open the
/// watch mini-app — the exact moment the watch performs its handshake. The screen
/// just drives the window and reflects state.
class WatchesScreen extends ConsumerStatefulWidget {
  const WatchesScreen({super.key});

  @override
  ConsumerState<WatchesScreen> createState() => _WatchesScreenState();
}

class _WatchesScreenState extends ConsumerState<WatchesScreen> {
  Timer? _ticker;

  bool _loading = true;
  Object? _loadError;
  List<PairedWatch> _watches = const [];

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _init());
  }

  @override
  void dispose() {
    _ticker?.cancel();
    // NOTE: deliberately does NOT stop the pairing server — the hub keeps it
    // alive for its window so the watch can still reach it after we leave here.
    super.dispose();
  }

  Future<void> _init() async {
    // Always (re)open a pairing window the moment this screen appears — even
    // when a watch is already paired. This binds the loopback pairing server
    // and puts it in the "ready" state, so a watch whose token lapsed (or whose
    // refresh chain died) can silently re-pair on its next cycle / a tap on its
    // logo, with no manual un-pair/re-pair dance. The ambient server may already
    // be up from the home screen; openWindow is idempotent.
    _startPairing();
    await _loadWatches(initial: true);
    // Tick once a second: refresh the countdown and poll the backend so a
    // completed handshake shows up.
    _ticker = Timer.periodic(const Duration(seconds: 1), (t) {
      if (!mounted) return;
      setState(() {}); // refresh countdown
      if (t.tick % 3 == 0 &&
          WatchPairingHub.instance.state.value.phase != PairingPhase.idle) {
        _loadWatches();
      }
    });
  }

  void _startPairing() {
    final api = ref.read(apiProvider);
    if (api == null) return;
    WatchPairingHub.instance.openWindow(api);
    setState(() {});
  }

  Future<void> _loadWatches({bool initial = false}) async {
    final api = ref.read(apiProvider);
    if (api == null) return;
    try {
      final watches = await api.watchTokens();
      if (!mounted) return;
      setState(() {
        _watches = watches;
        _loading = false;
        _loadError = null;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        if (initial) _loadError = e;
      });
    }
  }

  Future<void> _revoke(PairedWatch watch) async {
    final api = ref.read(apiProvider);
    if (api == null) return;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Desemparejar reloj'),
        content: Text(
          '¿Quitar el acceso de "${watch.label.isEmpty ? 'este reloj' : watch.label}"? '
          'El reloj dejará de recibir datos.',
        ),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(ctx, false), child: const Text('Cancelar')),
          FilledButton(
              onPressed: () => Navigator.pop(ctx, true), child: const Text('Desemparejar')),
        ],
      ),
    );
    if (confirmed != true) return;
    try {
      await api.revokeWatch(watch.token);
      await _loadWatches();
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Relojes'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            tooltip: 'Actualizar',
            onPressed: () => _loadWatches(),
          ),
        ],
      ),
      body: _loading
          ? const LoadingView()
          : _loadError != null
              ? ErrorView(_loadError!, onRetry: () {
                  setState(() => _loading = true);
                  _loadWatches(initial: true);
                })
              : ListView(
                  padding: const EdgeInsets.symmetric(vertical: 8),
                  children: [
                    _status(context),
                    _instructions(context),
                    const SizedBox(height: 8),
                    _watchesHeader(context),
                    if (_watches.isEmpty)
                      const Padding(
                        padding: EdgeInsets.symmetric(vertical: 24),
                        child: EmptyView('Sin relojes emparejados', icon: Icons.watch_outlined),
                      )
                    else
                      ..._watches.map((w) => _WatchTile(watch: w, onRevoke: () => _revoke(w))),
                  ],
                ),
    );
  }

  Widget _status(BuildContext context) {
    final hub = WatchPairingHub.instance.state.value;
    final paired = _watches.isNotEmpty || hub.phase == PairingPhase.paired;

    IconData icon;
    Color color;
    String text;
    bool spinner = false;
    late Widget action;

    if (hub.phase == PairingPhase.error) {
      icon = Icons.error_outline;
      color = const Color(0xFFE03D3D);
      text = hub.message ?? 'Error de emparejamiento';
      action = TextButton(onPressed: _startPairing, child: const Text('Reintentar'));
    } else if (hub.phase == PairingPhase.waiting) {
      final secs = hub.until == null
          ? 0
          : hub.until!.difference(DateTime.now()).inSeconds.clamp(0, 599);
      icon = Icons.watch;
      color = context.scheme.primary;
      text = 'Empareja ahora: en el reloj, abre la mini-app de Mediavida '
          '(ventana ${secs}s)';
      spinner = true;
      action = TextButton(
          onPressed: () {
            WatchPairingHub.instance.closeWindow();
            setState(() {});
          },
          child: const Text('Detener'));
    } else if (paired) {
      icon = Icons.check_circle;
      color = context.scheme.primary;
      text = _watches.length <= 1
          ? 'Reloj emparejado ✓'
          : '${_watches.length} relojes emparejados ✓';
      action = TextButton(onPressed: _startPairing, child: const Text('Emparejar otro'));
    } else {
      icon = Icons.watch_outlined;
      color = context.mv.textSecondary;
      text = 'Pulsa Emparejar para vincular un reloj';
      action = FilledButton(onPressed: _startPairing, child: const Text('Emparejar'));
    }

    return Card(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 12, 8, 12),
        child: Row(
          children: [
            if (spinner)
              const SizedBox(
                  width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2))
            else
              Icon(icon, size: 22, color: color),
            const SizedBox(width: 12),
            Expanded(child: Text(text, style: TextStyle(fontSize: 13.5, color: color))),
            action,
          ],
        ),
      ),
    );
  }

  Widget _instructions(BuildContext context) {
    Widget step(int n, String text) => Padding(
          padding: const EdgeInsets.symmetric(vertical: 4),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              CircleAvatar(
                radius: 11,
                backgroundColor: context.scheme.primary,
                child: Text('$n',
                    style: TextStyle(
                        fontSize: 12,
                        fontWeight: FontWeight.w800,
                        color: context.scheme.onPrimary)),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Text(text, style: const TextStyle(fontSize: 14, height: 1.35)),
              ),
            ],
          ),
        );

    return Card(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 14, 14, 14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('Cómo emparejar',
                style: TextStyle(
                    fontSize: 15,
                    fontWeight: FontWeight.w800,
                    color: context.scheme.primary)),
            const SizedBox(height: 10),
            step(1, 'Instala el watchface y su mini-app en tu Amazfit (app Zepp).'),
            step(2, 'Pulsa "Emparejar" arriba para abrir la ventana.'),
            step(3,
                'Sin cerrar Mediavida, ve a la app Zepp y abre la mini-app de Mediavida en el reloj. Se emparejará durante la ventana.'),
          ],
        ),
      ),
    );
  }

  Widget _watchesHeader(BuildContext context) => Padding(
        padding: const EdgeInsets.fromLTRB(20, 8, 20, 4),
        child: Text(
          'RELOJES EMPAREJADOS',
          style: TextStyle(
            fontSize: 11.5,
            fontWeight: FontWeight.w800,
            letterSpacing: 0.5,
            color: context.scheme.primary,
          ),
        ),
      );
}

class _WatchTile extends StatelessWidget {
  const _WatchTile({required this.watch, required this.onRevoke});

  final PairedWatch watch;
  final VoidCallback onRevoke;

  @override
  Widget build(BuildContext context) {
    final rel = relativeTime(watch.createdAt);
    final subtitle = rel.isEmpty ? null : 'Emparejado $rel';
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      child: ListTile(
        leading: Icon(Icons.watch, color: context.scheme.primary),
        title: Text(
          watch.label.isEmpty ? 'Reloj' : watch.label,
          style: const TextStyle(fontWeight: FontWeight.bold),
        ),
        subtitle: subtitle == null
            ? null
            : Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(subtitle,
                    style: TextStyle(fontSize: 12, color: context.mv.textSecondary)),
              ),
        trailing: IconButton(
          icon: const Icon(Icons.delete_outline),
          tooltip: 'Desemparejar',
          color: context.mv.textSecondary,
          onPressed: onRevoke,
        ),
      ),
    );
  }
}
