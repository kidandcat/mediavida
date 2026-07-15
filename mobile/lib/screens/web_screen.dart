import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:webview_flutter/webview_flutter.dart';

import '../core/notification_service.dart';
import '../state/providers.dart';

/// The whole app UI: a WebView over the mobile-friendly mediavida.com. The
/// backend still holds the account's MV session (which drives native push); we
/// export its cookies and inject them here so the embedded browser is logged in
/// as the same user — one login, no re-typing credentials on the web.
class WebScreen extends ConsumerStatefulWidget {
  const WebScreen({super.key});

  @override
  ConsumerState<WebScreen> createState() => _WebScreenState();
}

class _WebScreenState extends ConsumerState<WebScreen> {
  static const _home = 'https://www.mediavida.com';

  late final WebViewController _controller;
  final _cookieManager = WebViewCookieManager();

  bool _ready = false; // cookies injected + first load kicked off
  bool _loading = true; // a page load is in flight
  bool _selfHealed = false; // guard: only auto-recover from the login page once
  String? _error;

  @override
  void initState() {
    super.initState();
    _controller = WebViewController()
      ..setJavaScriptMode(JavaScriptMode.unrestricted)
      ..setNavigationDelegate(NavigationDelegate(
        onPageStarted: (_) {
          if (mounted) setState(() => _loading = true);
        },
        onPageFinished: (url) {
          if (mounted) setState(() => _loading = false);
          _maybeSelfHeal(url);
        },
        onWebResourceError: (e) {
          // Ignore sub-resource errors; only a failed main-frame load matters.
          if (e.isForMainFrame == true && mounted) {
            setState(() => _error = 'No se pudo cargar la página');
          }
        },
        onNavigationRequest: _onNavigation,
      ));
    // React to a notification tapped while the app is already running.
    NotificationService().pendingUrl.addListener(_onPendingUrl);
    _boot();
  }

  @override
  void dispose() {
    NotificationService().pendingUrl.removeListener(_onPendingUrl);
    super.dispose();
  }

  /// Fetch the backend's MV cookies, inject them into the WebView cookie store,
  /// then load the first page (a tapped notification's thread, or the home page).
  Future<void> _boot() async {
    setState(() {
      _error = null;
      _loading = true;
    });
    await _injectCookies();
    if (!mounted) return;

    final pending = NotificationService().pendingUrl.value;
    NotificationService().pendingUrl.value = null;
    final start = (pending != null && pending.isNotEmpty) ? pending : _home;

    await _controller.loadRequest(Uri.parse(start));
    if (mounted) setState(() => _ready = true);
  }

  /// Pull the MV session cookies from the backend and set them on the WebView so
  /// the embedded browser browses mediavida.com already logged in.
  Future<bool> _injectCookies() async {
    final api = ref.read(apiProvider);
    if (api == null) return false;
    try {
      final cookies = await api.mvCookies();
      for (final c in cookies) {
        await _cookieManager.setCookie(WebViewCookie(
          name: c.name,
          value: c.value,
          domain: c.domain,
          path: c.path,
        ));
      }
      return cookies.isNotEmpty;
    } catch (_) {
      // Non-fatal: the WebView still loads; the user just may need to log in on
      // the web. The backend session (and push) is unaffected.
      return false;
    }
  }

  void _onPendingUrl() {
    final url = NotificationService().pendingUrl.value;
    if (url == null || url.isEmpty || !_ready) return;
    NotificationService().pendingUrl.value = null;
    _controller.loadRequest(Uri.parse(url));
  }

  /// If MV bounced the embedded browser to its login form (cookies rotated or
  /// expired), re-pull fresh cookies from the backend — which keeps the session
  /// alive with stored credentials — and reload once, so the user isn't asked to
  /// log in again on the web.
  Future<void> _maybeSelfHeal(String url) async {
    final uri = Uri.tryParse(url);
    if (uri == null) return;
    final onLoginPage = uri.host.contains('mediavida.com') &&
        uri.path.startsWith('/login') &&
        !uri.path.contains('/salir');
    if (!onLoginPage || _selfHealed) return;
    _selfHealed = true;
    final ok = await _injectCookies();
    if (ok && mounted) await _controller.loadRequest(Uri.parse(_home));
  }

  Future<NavigationDecision> _onNavigation(NavigationRequest req) async {
    final uri = Uri.tryParse(req.url);
    if (uri == null) return NavigationDecision.navigate;

    // Explicit MV logout: also drop the backend session (and thus push) and
    // return to the native login screen.
    if (uri.host.contains('mediavida.com') && uri.path.contains('/login/salir')) {
      await ref.read(configProvider.notifier).signOut();
      return NavigationDecision.prevent;
    }

    // Keep mediavida.com inside the WebView; send everything else (mailto,
    // external links, embedded media hosts) to the system browser/app.
    final inApp = uri.scheme == 'about' ||
        uri.scheme == 'data' ||
        uri.host.endsWith('mediavida.com');
    if (inApp) return NavigationDecision.navigate;

    if (await canLaunchUrl(uri)) {
      await launchUrl(uri, mode: LaunchMode.externalApplication);
    }
    return NavigationDecision.prevent;
  }

  @override
  Widget build(BuildContext context) {
    return PopScope(
      canPop: false,
      onPopInvokedWithResult: (didPop, _) async {
        if (didPop) return;
        if (await _controller.canGoBack()) {
          await _controller.goBack();
        }
        // Otherwise swallow the back gesture: at the root there's nowhere to go,
        // and we don't want the system back to close the app abruptly here.
      },
      child: Scaffold(
        body: SafeArea(
          child: Stack(
            children: [
              if (_ready) WebViewWidget(controller: _controller),
              if (_error != null) _ErrorView(onRetry: _boot, message: _error!),
              if (_loading && _error == null)
                const LinearProgressIndicator(minHeight: 2),
              if (!_ready && _error == null)
                const Center(child: CircularProgressIndicator()),
            ],
          ),
        ),
      ),
    );
  }
}

class _ErrorView extends StatelessWidget {
  const _ErrorView({required this.onRetry, required this.message});
  final VoidCallback onRetry;
  final String message;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.wifi_off_rounded, size: 48),
          const SizedBox(height: 12),
          Text(message),
          const SizedBox(height: 16),
          FilledButton(onPressed: onRetry, child: const Text('Reintentar')),
        ],
      ),
    );
  }
}
