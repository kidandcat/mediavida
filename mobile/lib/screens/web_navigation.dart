/// Pure URL policy for the Mediavida WebView. Kept free of Flutter/WebView
/// types so it can be unit-tested without a bindings/platform view.
library;

/// Hosts that belong to the forum and must stay inside the in-app WebView.
bool isMediavidaHost(String host) {
  final h = host.toLowerCase();
  return h == 'mediavida.com' || h.endsWith('.mediavida.com');
}

/// Schemes the WebView must handle itself. Handing these to url_launcher
/// (or blocking them) is what broke `javascript:` / `blob:` driven buttons.
bool isInPageScheme(String scheme) {
  const inPage = {'about', 'data', 'blob', 'javascript', 'filesystem'};
  return inPage.contains(scheme.toLowerCase());
}

/// Explicit MV logout: also drop the backend session (and thus push).
bool isMediavidaLogout(Uri uri) {
  return isMediavidaHost(uri.host) && uri.path.contains('/login/salir');
}

/// What the navigation delegate should do with a request.
enum WebNavAction {
  /// Load inside the WebView.
  stay,

  /// Open in the system browser / Custom Tabs / an external app.
  openExternal,

  /// Sign out of the native session and stay put.
  signOut,
}

/// Classify a navigation. [isMainFrame] is required so iframe/embeds
/// (YouTube, etc.) are not stolen out of the page into the system browser.
WebNavAction classifyNavigation(Uri uri, {required bool isMainFrame}) {
  if (isMediavidaLogout(uri)) return WebNavAction.signOut;
  if (isInPageScheme(uri.scheme)) return WebNavAction.stay;

  final http = uri.scheme == 'http' || uri.scheme == 'https';
  if (http && isMediavidaHost(uri.host)) return WebNavAction.stay;

  // Subframes of third-party hosts are embeds, not "open this link".
  if (!isMainFrame && http) return WebNavAction.stay;

  return WebNavAction.openExternal;
}
