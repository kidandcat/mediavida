import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:url_launcher/url_launcher.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import '../widgets/common.dart';

/// The logged-in user's Mediavida profile: header (cover + avatar + rank +
/// counters + bio) plus account actions (watch pairing, sign out).
class ProfileScreen extends ConsumerWidget {
  const ProfileScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final profile = ref.watch(profileProvider);

    return Scaffold(
      appBar: AppBar(title: const Text('Perfil')),
      body: RefreshIndicator(
        onRefresh: () async {
          ref.invalidate(profileProvider);
          await ref.read(profileProvider.future);
        },
        child: profile.when(
          loading: () => const LoadingView(),
          error: (e, _) => ErrorView(e, onRetry: () => ref.invalidate(profileProvider)),
          data: (p) {
            if (p == null) {
              return const EmptyView('Sin sesión', icon: Icons.person_off_outlined);
            }
            return ListView(
              padding: EdgeInsets.zero,
              children: [
                _Header(p),
                _StatsRow(p),
                if (p.bio.isNotEmpty) _Bio(p.bio),
                const SizedBox(height: 8),
                _actions(context, ref, p),
              ],
            );
          },
        ),
      ),
    );
  }

  Widget _actions(BuildContext context, WidgetRef ref, Profile p) {
    return Column(
      children: [
        ListTile(
          leading: const Icon(Icons.forum_outlined),
          title: const Text('Mis hilos y menciones'),
          trailing: const Icon(Icons.chevron_right),
          onTap: () => context.openUser(p.username),
        ),
        ListTile(
          leading: const Icon(Icons.open_in_browser),
          title: const Text('Ver perfil en el navegador'),
          trailing: const Icon(Icons.chevron_right),
          onTap: () => launchUrl(
            Uri.parse('https://www.mediavida.com/id/${p.username}'),
            mode: LaunchMode.externalApplication,
          ),
        ),
        const Divider(height: 1),
        ListTile(
          leading: const Icon(Icons.watch),
          title: const Text('Relojes'),
          subtitle: const Text('Emparejar un smartwatch'),
          trailing: const Icon(Icons.chevron_right),
          onTap: () => context.openWatches(),
        ),
        ListTile(
          leading: Icon(Icons.logout, color: context.scheme.error),
          title: Text('Cerrar sesión', style: TextStyle(color: context.scheme.error)),
          onTap: () => _confirmSignOut(context, ref),
        ),
        const SizedBox(height: 24),
      ],
    );
  }

  Future<void> _confirmSignOut(BuildContext context, WidgetRef ref) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: const Text('Cerrar sesión'),
        content: const Text('¿Seguro que quieres cerrar sesión?'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(c, false), child: const Text('Cancelar')),
          TextButton(
            onPressed: () => Navigator.pop(c, true),
            child: Text('Cerrar sesión', style: TextStyle(color: context.scheme.error)),
          ),
        ],
      ),
    );
    if (ok == true) {
      // signOut flips loggedIn=false; the router redirects the whole stack to /setup.
      await ref.read(configProvider.notifier).signOut();
    }
  }
}

class _Header extends StatelessWidget {
  final Profile p;
  const _Header(this.p);

  @override
  Widget build(BuildContext context) {
    return Stack(
      clipBehavior: Clip.none,
      children: [
        // Cover banner with a fade so the avatar/name stay legible.
        SizedBox(
          height: 132,
          width: double.infinity,
          child: p.cover.isEmpty
              ? Container(color: context.mv.surfaceHigh)
              : CachedNetworkImage(
                  imageUrl: p.cover,
                  fit: BoxFit.cover,
                  placeholder: (c, _) => Container(color: context.mv.surfaceHigh),
                  errorWidget: (c, _, _) => Container(color: context.mv.surfaceHigh),
                ),
        ),
        Positioned.fill(
          child: DecoratedBox(
            decoration: BoxDecoration(
              gradient: LinearGradient(
                begin: Alignment.topCenter,
                end: Alignment.bottomCenter,
                colors: [Colors.transparent, context.scheme.surface.withValues(alpha: 0.85)],
              ),
            ),
          ),
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 76, 16, 12),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              _Avatar(p),
              const SizedBox(width: 14),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Row(
                      children: [
                        Flexible(
                          child: Text(
                            p.username,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: const TextStyle(fontSize: 20, fontWeight: FontWeight.w800),
                          ),
                        ),
                        if (p.online) ...[
                          const SizedBox(width: 8),
                          Container(
                            width: 9,
                            height: 9,
                            decoration: const BoxDecoration(
                              color: Color(0xFF4CAF50),
                              shape: BoxShape.circle,
                            ),
                          ),
                        ],
                      ],
                    ),
                    if (p.rank.isNotEmpty)
                      Text(
                        p.rank,
                        style: TextStyle(
                          fontSize: 13,
                          fontWeight: FontWeight.w600,
                          color: context.scheme.primary,
                        ),
                      ),
                    if (p.registered.isNotEmpty)
                      Padding(
                        padding: const EdgeInsets.only(top: 2),
                        child: Text(
                          'Desde ${p.registered}',
                          style: TextStyle(fontSize: 12, color: context.mv.textFaint),
                        ),
                      ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ],
    );
  }
}

class _Avatar extends StatelessWidget {
  final Profile p;
  const _Avatar(this.p);

  @override
  Widget build(BuildContext context) {
    const size = 84.0;
    return Container(
      width: size,
      height: size,
      decoration: BoxDecoration(
        shape: BoxShape.circle,
        border: Border.all(color: context.scheme.surface, width: 3),
        color: context.mv.surfaceHigh,
      ),
      clipBehavior: Clip.antiAlias,
      child: p.avatar.isEmpty
          ? Center(
              child: Text(
                p.username.isNotEmpty ? p.username[0].toUpperCase() : '?',
                style: const TextStyle(fontSize: 34, fontWeight: FontWeight.w700),
              ),
            )
          : CachedNetworkImage(
              imageUrl: p.avatar,
              fit: BoxFit.cover,
              placeholder: (c, _) => const SizedBox(),
              errorWidget: (c, _, _) => Center(
                child: Text(
                  p.username.isNotEmpty ? p.username[0].toUpperCase() : '?',
                  style: const TextStyle(fontSize: 34, fontWeight: FontWeight.w700),
                ),
              ),
            ),
    );
  }
}

class _StatsRow extends StatelessWidget {
  final Profile p;
  const _StatsRow(this.p);

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 4, 16, 12),
      child: Row(
        children: [
          _stat(context, _fmt(p.posts), 'posts'),
          _stat(context, _fmt(p.threads), 'temas'),
          _stat(context, _fmt(p.visits), 'visitas'),
        ],
      ),
    );
  }

  Widget _stat(BuildContext context, String value, String label) {
    return Expanded(
      child: Column(
        children: [
          Text(value, style: const TextStyle(fontSize: 18, fontWeight: FontWeight.w800)),
          const SizedBox(height: 2),
          Text(label, style: TextStyle(fontSize: 12, color: context.mv.textSecondary)),
        ],
      ),
    );
  }
}

class _Bio extends StatelessWidget {
  final String bio;
  const _Bio(this.bio);

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Text(
          bio,
          style: TextStyle(fontSize: 14, height: 1.4, color: context.mv.textSecondary),
        ),
      ),
    );
  }
}

/// Groups thousands with a dot (Spanish style): 4946 -> "4.946".
String _fmt(int n) {
  final s = n.toString();
  final buf = StringBuffer();
  for (var i = 0; i < s.length; i++) {
    if (i > 0 && (s.length - i) % 3 == 0) buf.write('.');
    buf.write(s[i]);
  }
  return buf.toString();
}
