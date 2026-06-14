import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/models.dart';
import '../router.dart';
import '../state/providers.dart';
import '../theme.dart';
import 'forum_icon.dart';

/// App-bar title shared across the home tabs: a "Subforos" dropdown that opens
/// the forum index menu. Replaces the per-screen title (Portada/Favoritos/...),
/// which the bottom navigation bar already makes obvious.
class SubforosTitle extends ConsumerWidget {
  const SubforosTitle({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return Align(
      alignment: Alignment.centerLeft,
      child: TextButton.icon(
        onPressed: () => _showSubforums(context, ref),
        icon: const Icon(Icons.menu, size: 20),
        label: const Text('Subforos'),
        style: TextButton.styleFrom(foregroundColor: context.scheme.primary),
      ),
    );
  }

  Future<void> _showSubforums(BuildContext context, WidgetRef ref) async {
    // Load the forum index, then drop a full-width panel down from the app bar
    // (like the web): two columns of subforums, grouped by category.
    List<ForumCategory> cats;
    try {
      cats = await ref.read(forumsProvider.future);
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
      }
      return;
    }
    if (!context.mounted) return;

    final media = MediaQuery.of(context);
    final topOffset = media.padding.top + kToolbarHeight;

    await showGeneralDialog<void>(
      context: context,
      barrierDismissible: true,
      barrierLabel: 'Subforos',
      barrierColor: Colors.black54,
      transitionDuration: const Duration(milliseconds: 150),
      pageBuilder: (dialogCtx, _, _) {
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            SizedBox(height: topOffset),
            // Cap the panel at 3/5 of the screen height; it scrolls inside, and
            // the remaining area below stays as a tappable barrier to dismiss.
            ConstrainedBox(
              constraints: BoxConstraints(maxHeight: media.size.height * 3 / 5),
              child: Material(
                color: context.scheme.surface,
                elevation: 8,
                child: SingleChildScrollView(
                  padding: const EdgeInsets.symmetric(vertical: 6),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      for (var c = 0; c < cats.length; c++) ...[
                        if (c > 0) const Divider(height: 1, thickness: 1),
                        _section(context, dialogCtx, cats[c]),
                      ],
                    ],
                  ),
                ),
              ),
            ),
          ],
        );
      },
      transitionBuilder: (ctx, anim, _, child) => FadeTransition(
        opacity: CurvedAnimation(parent: anim, curve: Curves.easeOut),
        child: child,
      ),
    );
  }

  /// One category rendered as rows of two subforum cells (full-width columns).
  Widget _section(BuildContext context, BuildContext dialogCtx, ForumCategory cat) {
    final forums = cat.forums;
    final rows = <Widget>[];
    for (var i = 0; i < forums.length; i += 2) {
      rows.add(Row(
        children: [
          Expanded(child: _cell(context, dialogCtx, forums[i])),
          Expanded(
            child: i + 1 < forums.length
                ? _cell(context, dialogCtx, forums[i + 1])
                : const SizedBox(),
          ),
        ],
      ));
    }
    return Column(mainAxisSize: MainAxisSize.min, children: rows);
  }

  Widget _cell(BuildContext context, BuildContext dialogCtx, ForumInfo forum) {
    return InkWell(
      onTap: () {
        Navigator.of(dialogCtx).pop();
        context.openForum(forum.slug, name: forum.name);
      },
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 11),
        child: Row(
          children: [
            ForumIcon(forum.icon, size: 24),
            const SizedBox(width: 12),
            Expanded(
              child: Text(
                forum.name,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w600),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
