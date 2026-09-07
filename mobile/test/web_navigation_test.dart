import 'package:flutter_test/flutter_test.dart';
import 'package:mvmobile/screens/web_navigation.dart';

void main() {
  group('isMediavidaHost', () {
    test('matches apex and subdomains only', () {
      expect(isMediavidaHost('mediavida.com'), isTrue);
      expect(isMediavidaHost('www.mediavida.com'), isTrue);
      expect(isMediavidaHost('s.mediavida.com'), isTrue);
      expect(isMediavidaHost('MEDIAVIDA.COM'), isTrue);
      expect(isMediavidaHost('evilmediavida.com'), isFalse);
      expect(isMediavidaHost('mediavida.com.evil.test'), isFalse);
      expect(isMediavidaHost('youtube.com'), isFalse);
    });
  });

  group('classifyNavigation', () {
    test('keeps forum pages in the WebView', () {
      expect(
        classifyNavigation(
          Uri.parse('https://www.mediavida.com/foro/foo'),
          isMainFrame: true,
        ),
        WebNavAction.stay,
      );
      expect(
        classifyNavigation(
          Uri.parse('http://mediavida.com/id/user'),
          isMainFrame: true,
        ),
        WebNavAction.stay,
      );
    });

    test('signs out on /login/salir', () {
      expect(
        classifyNavigation(
          Uri.parse('https://www.mediavida.com/login/salir'),
          isMainFrame: true,
        ),
        WebNavAction.signOut,
      );
    });

    test('opens main-frame third-party http(s) in the system browser', () {
      expect(
        classifyNavigation(
          Uri.parse('https://www.youtube.com/watch?v=dQw4w9WgXcQ'),
          isMainFrame: true,
        ),
        WebNavAction.openExternal,
      );
      expect(
        classifyNavigation(
          Uri.parse('https://github.com/kidandcat/mediavida'),
          isMainFrame: true,
        ),
        WebNavAction.openExternal,
      );
    });

    test('does not steal third-party iframes into the browser', () {
      expect(
        classifyNavigation(
          Uri.parse('https://www.youtube.com/embed/dQw4w9WgXcQ'),
          isMainFrame: false,
        ),
        WebNavAction.stay,
      );
    });

    test('keeps in-page schemes inside the WebView', () {
      expect(
        classifyNavigation(Uri.parse('javascript:void(0)'), isMainFrame: true),
        WebNavAction.stay,
      );
      expect(
        classifyNavigation(Uri.parse('about:blank'), isMainFrame: true),
        WebNavAction.stay,
      );
      expect(
        classifyNavigation(
          Uri.parse('blob:https://www.mediavida.com/abc'),
          isMainFrame: true,
        ),
        WebNavAction.stay,
      );
    });

    test('hands mailto/tel/intent to an external app even as a subframe', () {
      expect(
        classifyNavigation(
          Uri.parse('mailto:mods@mediavida.com'),
          isMainFrame: true,
        ),
        WebNavAction.openExternal,
      );
      expect(
        classifyNavigation(Uri.parse('tel:+34911000000'), isMainFrame: false),
        WebNavAction.openExternal,
      );
    });
  });
}
