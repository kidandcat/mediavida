import 'dart:io' show Platform;

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:webview_flutter/webview_flutter.dart';
import 'package:webview_flutter_android/webview_flutter_android.dart';

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

  // Neutralize the WebView's forced multiple-window support: the plugin sets
  // setSupportMultipleWindows(true) and provides no onCreateWindow handler, so
  // any `target="_blank"` link/form or `window.open()` call is silently dropped
  // — which made MV buttons (reload, send reply, …) do nothing. Re-route
  // window.open to same-tab navigation and strip target="_blank" (including on
  // nodes added later), so those interactions work. Idempotent per page.
  static const _tabFixJs = r'''
(function(){
  if (window.__mvTabFix) return; window.__mvTabFix = true;
  try {
    window.open = function(u){ try { if (u) window.location.href = u; } catch(e){} return null; };
  } catch(e){}
  function strip(root){
    try {
      var els = root.querySelectorAll ? root.querySelectorAll('a[target],form[target]') : [];
      for (var i=0;i<els.length;i++){
        var t = els[i].getAttribute('target');
        if (t && t.toLowerCase() === '_blank') els[i].removeAttribute('target');
      }
    } catch(e){}
  }
  function boot(){
    strip(document);
    try {
      new MutationObserver(function(){ strip(document); })
        .observe(document.documentElement, {childList:true, subtree:true});
    } catch(e){}
  }
  if (document.documentElement) boot();
  else document.addEventListener('DOMContentLoaded', boot);
})();
''';

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
      ..setOnConsoleMessage((m) => debugPrint('[web:${m.level.name}] ${m.message}'))
      ..setNavigationDelegate(NavigationDelegate(
        onPageStarted: (_) {
          if (mounted) setState(() => _loading = true);
          // Apply the window.open/target fix as early as possible, before the
          // page's own scripts cache a reference to window.open.
          _controller.runJavaScript(_tabFixJs).catchError((_) {});
        },
        onPageFinished: (url) {
          if (mounted) setState(() => _loading = false);
          _controller.runJavaScript(_tabFixJs).catchError((_) {});
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

    // Android: wire the file chooser so `<input type="file">` (image uploads,
    // attachments) opens a native picker — the WebView does nothing on its own.
    // iOS/WKWebView handles file inputs natively, so this is Android-only.
    // Android: also render the browser's native JS dialogs — alert/confirm/
    // prompt. webview_flutter shows none of these by default, so a confirm()
    // silently returns "cancel" and an alert()/prompt() never appears, which
    // makes MV actions gated on a confirmation do nothing. iOS/WKWebView shows
    // them natively.
    if (Platform.isAndroid) {
      final platform = _controller.platform;
      if (platform is AndroidWebViewController) {
        platform.setOnShowFileSelector(_onShowFileSelector);
        platform.setOnJavaScriptAlertDialog(_onJsAlert);
        platform.setOnJavaScriptConfirmDialog(_onJsConfirm);
        platform.setOnJavaScriptTextInputDialog(_onJsPrompt);
      }
    }

    // React to a notification tapped while the app is already running.
    NotificationService().pendingUrl.addListener(_onPendingUrl);
    _boot();
  }

  @override
  void dispose() {
    NotificationService().pendingUrl.removeListener(_onPendingUrl);
    super.dispose();
  }

  /// Open a native picker for a WebView `<input type="file">` and hand back the
  /// chosen file URIs. Honors the accept hint (image-only inputs get the image
  /// picker) and multiple selection.
  Future<List<String>> _onShowFileSelector(FileSelectorParams params) async {
    final imagesOnly = params.acceptTypes.isNotEmpty &&
        params.acceptTypes.every((t) => t.trim().startsWith('image/'));
    try {
      final result = await FilePicker.platform.pickFiles(
        allowMultiple: params.mode == FileSelectorMode.openMultiple,
        type: imagesOnly ? FileType.image : FileType.any,
      );
      if (result == null) return const [];
      return result.files
          .where((f) => f.path != null)
          .map((f) => Uri.file(f.path!).toString())
          .toList();
    } catch (e) {
      debugPrint('[web] file picker failed: $e');
      return const [];
    }
  }

  /// Render a JS `alert()` as a native dialog and wait for dismissal.
  Future<void> _onJsAlert(JavaScriptAlertDialogRequest r) async {
    if (!mounted) return;
    await showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        content: Text(r.message),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Aceptar')),
        ],
      ),
    );
  }

  /// Render a JS `confirm()` as a native dialog; the return value is what the
  /// page's `if (confirm(...))` branch sees.
  Future<bool> _onJsConfirm(JavaScriptConfirmDialogRequest r) async {
    if (!mounted) return false;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        content: Text(r.message),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Cancelar')),
          TextButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Aceptar')),
        ],
      ),
    );
    return ok ?? false;
  }

  /// Render a JS `prompt()` as a native text dialog; returns the entered text,
  /// or an empty string when cancelled.
  Future<String> _onJsPrompt(JavaScriptTextInputDialogRequest r) async {
    if (!mounted) return '';
    final field = TextEditingController(text: r.defaultText ?? '');
    final result = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: r.message.isNotEmpty ? Text(r.message) : null,
        content: TextField(controller: field, autofocus: true),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Cancelar')),
          TextButton(onPressed: () => Navigator.pop(ctx, field.text), child: const Text('Aceptar')),
        ],
      ),
    );
    field.dispose();
    return result ?? '';
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
    final scheme = uri.scheme.toLowerCase();

    // Explicit MV logout: also drop the backend session (and thus push) and
    // return to the native login screen.
    if (uri.host.contains('mediavida.com') && uri.path.contains('/login/salir')) {
      await ref.read(configProvider.notifier).signOut();
      return NavigationDecision.prevent;
    }

    // In-page schemes the WebView must handle itself — never hand these to an
    // external app (doing so is what broke javascript:/blob: driven buttons).
    const inPageSchemes = {'about', 'data', 'blob', 'javascript', 'filesystem'};
    if (inPageSchemes.contains(scheme)) return NavigationDecision.navigate;

    // Keep mediavida.com inside the WebView.
    if ((scheme == 'http' || scheme == 'https') && uri.host.endsWith('mediavida.com')) {
      return NavigationDecision.navigate;
    }

    // Everything else (external sites, mailto/tel/intent/…) → system browser/app.
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
