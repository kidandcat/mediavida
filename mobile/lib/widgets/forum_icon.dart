import 'package:cached_network_image/cached_network_image.dart';
import 'package:flutter/material.dart';

/// Renders a subforum icon from Mediavida's CSS sprite (foro-v13.png), the same
/// icons the website uses. The icon class is "fid-N".
class ForumIcon extends StatelessWidget {
  final String icon;
  final double size;
  const ForumIcon(this.icon, {super.key, this.size = 22});

  static const _spriteUrl = 'https://www.mediavida.com/style/455/img/icon/foro-v13.png';
  static const double _cell = 24; // sprite cell size in px

  // fid number -> x offset (px) into the sprite, extracted from dark_v7.css.
  static const Map<int, int> _offsets = {
    1: 725, 3: 975, 4: 75, 6: 325, 7: 200, 8: 950, 9: 275, 14: 800, 23: 825,
    26: 1000, 32: 575, 38: 150, 45: 425, 82: 525, 83: 225, 90: 1100, 96: 450,
    99: 1025, 102: 50, 106: 1050, 109: 500, 110: 25, 112: 925, 114: 375,
    116: 775, 126: 350, 127: 675, 128: 625, 132: 300, 133: 475, 135: 900,
    136: 1075, 137: 875, 143: 750, 144: 175, 148: 650, 150: 550, 152: 125,
    153: 850, 162: 700, 164: 100, 165: 600, 168: 400, 169: 250,
  };

  @override
  Widget build(BuildContext context) {
    final n = int.tryParse(icon.replaceAll(RegExp(r'[^0-9]'), ''));
    final off = n == null ? null : _offsets[n];
    if (off == null) {
      return Icon(Icons.forum_outlined, size: size, color: Theme.of(context).colorScheme.primary);
    }
    final scale = size / _cell;
    return SizedBox(
      width: size,
      height: size,
      child: ClipRect(
        child: OverflowBox(
          alignment: Alignment.topLeft,
          minWidth: 0,
          minHeight: 0,
          maxWidth: double.infinity,
          maxHeight: double.infinity,
          child: Transform.translate(
            offset: Offset(-off.toDouble() * scale, 0),
            child: Transform.scale(
              scale: scale,
              alignment: Alignment.topLeft,
              child: Image(
                image: CachedNetworkImageProvider(_spriteUrl),
                fit: BoxFit.none,
                alignment: Alignment.topLeft,
                filterQuality: FilterQuality.medium,
                errorBuilder: (c, _, _) => SizedBox(width: size, height: size),
              ),
            ),
          ),
        ),
      ),
    );
  }
}
