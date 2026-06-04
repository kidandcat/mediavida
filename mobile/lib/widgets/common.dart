import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:flutter_widget_from_html_core/flutter_widget_from_html_core.dart';
import 'package:url_launcher/url_launcher.dart';

import '../theme.dart';

/// Renders a post's rich HTML body (`body_html`) with tappable links/images.
class PostHtml extends StatelessWidget {
  final String html;
  const PostHtml(this.html, {super.key});

  @override
  Widget build(BuildContext context) {
    if (html.trim().isEmpty) return const SizedBox.shrink();
    final accentHex = _hex(context.scheme.primary);
    final quoteHex = _hex(context.mv.textSecondary);
    final codeBg = _hex(context.mv.surfaceHigh);
    return HtmlWidget(
      html,
      textStyle: TextStyle(fontSize: 15, height: 1.45, color: context.scheme.onSurface),
      onTapUrl: (url) async {
        final uri = Uri.tryParse(url);
        if (uri == null) return false;
        if (await canLaunchUrl(uri)) {
          await launchUrl(uri, mode: LaunchMode.externalApplication);
          return true;
        }
        return false;
      },
      customStylesBuilder: (e) {
        if (e.localName == 'blockquote') {
          return {
            'border-left': '3px solid $accentHex',
            'padding-left': '10px',
            'margin': '6px 0',
            'color': quoteHex,
          };
        }
        if (e.localName == 'pre' || e.localName == 'code') {
          return {'background': codeBg, 'border-radius': '6px'};
        }
        return null;
      },
    );
  }
}

/// CSS hex string (#rrggbb) for a [Color].
String _hex(Color c) =>
    '#${(c.toARGB32() & 0xFFFFFF).toRadixString(16).padLeft(6, '0')}';

/// Circular user avatar with a fallback initial.
class MvAvatar extends StatelessWidget {
  final String url;
  final String name;
  final double size;
  const MvAvatar({super.key, required this.url, required this.name, this.size = 38});

  @override
  Widget build(BuildContext context) {
    final initial = name.isNotEmpty ? name[0].toUpperCase() : '?';
    final fallback = CircleAvatar(
      radius: size / 2,
      backgroundColor: context.mv.surfaceHigh,
      child: Text(initial, style: TextStyle(fontSize: size * 0.4, color: context.scheme.primary)),
    );
    if (url.isEmpty) return fallback;
    return ClipOval(
      child: CachedNetworkImage(
        imageUrl: url,
        width: size,
        height: size,
        fit: BoxFit.cover,
        placeholder: (c, _) => fallback,
        errorWidget: (c, _, _) => fallback,
      ),
    );
  }
}

/// "hace 3h", "ayer", etc. from a unix timestamp (seconds). Empty if 0.
String relativeTime(int unixSeconds) {
  if (unixSeconds <= 0) return '';
  final then = DateTime.fromMillisecondsSinceEpoch(unixSeconds * 1000);
  final diff = DateTime.now().difference(then);
  if (diff.inMinutes < 1) return 'ahora';
  if (diff.inMinutes < 60) return 'hace ${diff.inMinutes} min';
  if (diff.inHours < 24) return 'hace ${diff.inHours} h';
  if (diff.inDays < 7) return 'hace ${diff.inDays} d';
  if (diff.inDays < 30) return 'hace ${(diff.inDays / 7).floor()} sem';
  if (diff.inDays < 365) return 'hace ${(diff.inDays / 30).floor()} mes';
  return 'hace ${(diff.inDays / 365).floor()} a';
}

/// A small pill chip (tags, unread counts).
class MvChip extends StatelessWidget {
  final String label;
  final Color? color;
  final Color? fg;
  const MvChip(this.label, {super.key, this.color, this.fg});

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        color: color ?? context.mv.surfaceHigh,
        borderRadius: BorderRadius.circular(8),
      ),
      child: Text(
        label,
        style: TextStyle(
          fontSize: 11,
          fontWeight: FontWeight.w700,
          color: fg ?? context.mv.textSecondary,
        ),
      ),
    );
  }
}

/// Centered loading spinner.
class LoadingView extends StatelessWidget {
  const LoadingView({super.key});
  @override
  Widget build(BuildContext context) =>
      const Center(child: Padding(padding: EdgeInsets.all(32), child: CircularProgressIndicator()));
}

/// Error state with a retry button.
class ErrorView extends StatelessWidget {
  final Object error;
  final VoidCallback? onRetry;
  const ErrorView(this.error, {super.key, this.onRetry});

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(28),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.cloud_off, size: 44, color: context.mv.textFaint),
            const SizedBox(height: 12),
            Text('$error', textAlign: TextAlign.center, style: TextStyle(color: context.mv.textSecondary)),
            if (onRetry != null) ...[
              const SizedBox(height: 16),
              FilledButton.tonal(onPressed: onRetry, child: const Text('Reintentar')),
            ],
          ],
        ),
      ),
    );
  }
}

/// Empty state.
class EmptyView extends StatelessWidget {
  final String message;
  final IconData icon;
  const EmptyView(this.message, {super.key, this.icon = Icons.inbox_outlined});
  @override
  Widget build(BuildContext context) => Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 44, color: context.mv.textFaint),
            const SizedBox(height: 12),
            Text(message, style: TextStyle(color: context.mv.textSecondary)),
          ],
        ),
      );
}
