import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';
import 'package:flutter_widget_from_html_core/flutter_widget_from_html_core.dart';
import 'package:url_launcher/url_launcher.dart';

import '../theme.dart';

/// Max decoded width (in pixels) for images embedded in post HTML. Forum posts
/// routinely carry full-resolution photos/screenshots (4000px+); decoding those
/// at native size into ARGB bitmaps (width×height×4 bytes) is the dominant OOM
/// source — a handful per thread page blows the Android per-app heap and the OS
/// kills the app. Phone columns never need more than ~1080px, so downsample at
/// decode time. Aspect ratio is preserved (height left null) and smaller images
/// aren't upscaled.
const int _kMaxImageDecodeWidth = 1080;

/// [WidgetFactory] that downsamples network images at decode time. The stock
/// factory returns a bare `NetworkImage(url)` (full resolution), which is what
/// made image-heavy threads OOM. Wrapping it in [ResizeImage] caps the decoded
/// bitmap regardless of the source dimensions.
class _DownsamplingWidgetFactory extends WidgetFactory {
  @override
  ImageProvider? imageProviderFromNetwork(String url) {
    if (url.isEmpty) return null;
    return ResizeImage(
      NetworkImage(url),
      width: _kMaxImageDecodeWidth,
      allowUpscaling: false,
    );
  }
}

/// Renders a post's rich HTML body (`body_html`) with tappable links/images.
class PostHtml extends StatelessWidget {
  final String html;
  /// Called when the user taps a #NNNN post reference inside the body.
  final void Function(int postNum)? onPostRef;
  const PostHtml(this.html, {super.key, this.onPostRef});

  @override
  Widget build(BuildContext context) {
    if (html.trim().isEmpty) return const SizedBox.shrink();
    final accentHex = _hex(context.scheme.primary);
    final quoteHex = _hex(context.mv.textSecondary);
    final codeBg = _hex(context.mv.surfaceHigh);
    return HtmlWidget(
      html,
      // Downsample embedded images at decode time so a thread full of
      // full-resolution photos can't exhaust the heap and get the app killed.
      factoryBuilder: () => _DownsamplingWidgetFactory(),
      textStyle: TextStyle(fontSize: 15, height: 1.45, color: context.scheme.onSurface),
      // Mediavida spoiler/NSFW tags: render a collapsible box instead of the
      // dead `<a href="#">` + hidden content div.
      customWidgetBuilder: (element) {
        if (!element.classes.contains('spoiler-wrap')) return null;
        final label = element.querySelector('a.spoiler')?.text.trim();
        // The actual content lives in the sibling `div.spoiler` (display:none).
        final content = element.children
            .where((c) => c.localName == 'div' && c.classes.contains('spoiler'))
            .map((c) => c.innerHtml)
            .join();
        return _Spoiler(
          label: (label == null || label.isEmpty) ? 'Spoiler' : label,
          innerHtml: content,
          onPostRef: onPostRef,
        );
      },
      onTapUrl: (url) async {
        // Intercept references to other posts (#NNNN) and open them in-app.
        final ref = RegExp(r'#(\d+)$').firstMatch(url);
        if (ref != null && onPostRef != null) {
          onPostRef!(int.parse(ref.group(1)!));
          return true;
        }
        final uri = Uri.tryParse(url);
        if (uri == null) return false;
        if (await canLaunchUrl(uri)) {
          await launchUrl(uri, mode: LaunchMode.externalApplication);
          return true;
        }
        return false;
      },
      customStylesBuilder: (e) {
        // Links and #NNNN post references are both <a>: drop the underline but
        // keep the accent color the theme already applies.
        if (e.localName == 'a') {
          return {'text-decoration': 'none'};
        }
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

/// Collapsible spoiler/NSFW box, like the web's `spoiler-wrap`. Tap the header
/// to reveal the inner HTML (rendered with [PostHtml] so nested refs/links work).
class _Spoiler extends StatefulWidget {
  final String label;
  final String innerHtml;
  final void Function(int postNum)? onPostRef;
  const _Spoiler({required this.label, required this.innerHtml, this.onPostRef});

  @override
  State<_Spoiler> createState() => _SpoilerState();
}

class _SpoilerState extends State<_Spoiler> {
  bool _open = false;

  @override
  Widget build(BuildContext context) {
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 4),
      decoration: BoxDecoration(
        color: context.mv.surfaceHigh,
        borderRadius: BorderRadius.circular(8),
        border: Border.all(color: context.scheme.primary.withValues(alpha: 0.4)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          InkWell(
            borderRadius: BorderRadius.circular(8),
            onTap: () => setState(() => _open = !_open),
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(
                    _open ? Icons.visibility_off : Icons.visibility,
                    size: 16,
                    color: context.scheme.primary,
                  ),
                  const SizedBox(width: 6),
                  Text(
                    widget.label,
                    style: TextStyle(
                      fontSize: 13,
                      fontWeight: FontWeight.w700,
                      color: context.scheme.primary,
                    ),
                  ),
                ],
              ),
            ),
          ),
          if (_open)
            Padding(
              padding: const EdgeInsets.fromLTRB(10, 0, 10, 10),
              child: PostHtml(widget.innerHtml, onPostRef: widget.onPostRef),
            ),
        ],
      ),
    );
  }
}

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
