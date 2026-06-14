import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';

import '../api/mv_api.dart';

/// Localhost handshake server for pairing an Amazfit watchface.
///
/// The Flutter app runs this tiny HTTP server bound to loopback whenever it is
/// in the foreground (see [WatchPairingHub.ensureRunning]). The watch's Zepp
/// side-service fetches `GET /pair` (with the shared app-key header) to receive
/// a watch token minted by the backend — on first pairing AND as a silent
/// self-heal whenever its stored token gets a 401. After that the watch talks
/// to the backend directly and autonomously.
///
/// The contract here is matched exactly by the watch side — do not change the
/// port, path, header names or app key without updating the watch app too.
class WatchPairingServer {
  WatchPairingServer(this._api, {this.onPaired});

  /// Loopback port the watch's side-service fetches from.
  static const int port = 28590;

  /// Path the watch hits to receive its token.
  static const String pairPath = '/pair';

  /// Header the watch must send (shared secret between this app and the watch).
  static const String appKeyHeader = 'X-Watch-App-Key';

  /// Optional header carrying the token this pair replaces (watch self-heal
  /// after a 401). Forwarded to the backend so it revokes the old token and the
  /// paired-watches list keeps one entry per physical watch.
  static const String oldTokenHeader = 'X-Watch-Old-Token';

  /// Shared key the watch presents. Must equal what the watch side is built with.
  static const String appKey = '70f54fb484323ae9c7fcaff542bcfda8';

  /// Label applied to watches paired through this handshake.
  static const String watchLabel = 'Amazfit';

  /// Getter, not an instance: the server is long-lived (ambient) and must
  /// always use the current API client.
  final MvApi? Function() _api;

  /// Invoked after a successful `/pair` handshake, so the UI can refresh the
  /// paired-watches list and show a confirmation.
  final void Function()? onPaired;

  HttpServer? _server;

  bool get isRunning => _server != null;

  /// Starts the loopback server. Rethrows the [SocketException] if the port is
  /// already in use (e.g. another app instance) so the caller can surface it.
  Future<void> start() async {
    if (_server != null) return;
    final server =
        await HttpServer.bind(InternetAddress.loopbackIPv4, port, shared: false);
    _server = server;
    debugPrint('[WatchPairing] server listening on 127.0.0.1:$port');
    server.listen(_handle, onError: (e) {
      debugPrint('[WatchPairing] server error: $e');
    });
  }

  Future<void> stop() async {
    final server = _server;
    _server = null;
    if (server != null) {
      await server.close(force: true);
      debugPrint('[WatchPairing] server stopped');
    }
  }

  Future<void> _handle(HttpRequest request) async {
    final response = request.response;
    response.headers.contentType = ContentType.json;
    debugPrint('[WatchPairing] request ${request.method} ${request.uri.path}');
    try {
      if (request.method != 'GET' || request.uri.path != pairPath) {
        response.statusCode = HttpStatus.notFound;
        response.write(jsonEncode({'error': 'not found'}));
        return;
      }
      if (request.headers.value(appKeyHeader) != appKey) {
        debugPrint('[WatchPairing] rejected: bad/missing app key');
        response.statusCode = HttpStatus.forbidden;
        response.write(jsonEncode({'error': 'forbidden'}));
        return;
      }
      final api = _api();
      if (api == null) {
        response.statusCode = HttpStatus.serviceUnavailable;
        response.write(jsonEncode({'error': 'app not logged in'}));
        return;
      }
      final replaces = request.headers.value(oldTokenHeader);
      final result = await api.pairWatch(label: watchLabel, replaces: replaces);
      debugPrint('[WatchPairing] paired ✓ token=${result.token.substring(0, 8)}…'
          '${replaces != null ? ' (replaces ${replaces.substring(0, 8)}…)' : ''}');
      response.statusCode = HttpStatus.ok;
      response.write(jsonEncode({
        'token': result.token,
        'base_url': result.baseUrl,
      }));
      onPaired?.call();
    } catch (e) {
      debugPrint('[WatchPairing] pair failed: $e');
      response.statusCode = HttpStatus.internalServerError;
      response.write(jsonEncode({'error': e.toString()}));
    } finally {
      await response.close();
    }
  }
}

/// Status of the UI pairing window (purely informative — the server itself is
/// ambient and outlives the window).
enum PairingPhase { idle, waiting, paired, error }

class PairingState {
  const PairingState(this.phase, {this.until, this.message});
  final PairingPhase phase;
  final DateTime? until; // when the active UI window auto-closes
  final String? message;

  static const idle = PairingState(PairingPhase.idle);
}

/// App-scoped owner of the pairing server.
///
/// The server is AMBIENT: [ensureRunning] binds it as soon as the app has a
/// logged-in API client (home screen init/resume) and it stays up while the
/// app lives. This is what lets a watch whose token got rejected self-heal
/// silently — "open the Mediavida app" is all the user has to do; no manual
/// un-pair/re-pair dance. Android freezes the isolate in the background, so in
/// practice the loopback endpoint is reachable while the app is foregrounded
/// (exactly when the user follows the watch's instruction to open the app).
///
/// The "pairing window" ([openWindow]/[closeWindow]) is ONLY UI state for the
/// Relojes screen's first-pairing flow (countdown + confirmation); it does not
/// start or stop the server.
class WatchPairingHub {
  WatchPairingHub._();
  static final WatchPairingHub instance = WatchPairingHub._();

  WatchPairingServer? _server;
  Timer? _windowTimer;
  MvApi? _currentApi;

  /// Observable state for the UI.
  final ValueNotifier<PairingState> state = ValueNotifier(PairingState.idle);

  bool get active => _server != null;

  /// Binds the ambient loopback server (idempotent) and refreshes the API
  /// client it mints tokens with. Call whenever a logged-in screen appears or
  /// the app resumes.
  Future<void> ensureRunning(MvApi api) async {
    _currentApi = api;
    if (_server != null) return;
    final server = WatchPairingServer(() => _currentApi, onPaired: _onPaired);
    try {
      await server.start();
      _server = server;
    } on SocketException catch (e) {
      // Port taken (another instance?) — not fatal: explicit pairing will
      // surface the error via openWindow, and we retry on the next resume.
      debugPrint('[WatchPairing] ambient bind failed: $e');
    }
  }

  /// Opens the UI pairing window: makes sure the server is up and shows the
  /// waiting/countdown state for [window]. Safe to call repeatedly (re-arms).
  Future<void> openWindow(MvApi api,
      {Duration window = const Duration(minutes: 5)}) async {
    _windowTimer?.cancel();
    await ensureRunning(api);
    if (_server == null) {
      state.value = const PairingState(PairingPhase.error,
          message: 'No se pudo abrir el emparejamiento (puerto en uso).');
      return;
    }
    final until = DateTime.now().add(window);
    state.value = PairingState(PairingPhase.waiting, until: until);
    _windowTimer = Timer(window, closeWindow);
  }

  /// Closes the UI window (countdown). The ambient server keeps running.
  void closeWindow() {
    _windowTimer?.cancel();
    _windowTimer = null;
    if (state.value.phase != PairingPhase.paired) {
      state.value = PairingState.idle;
    }
  }

  void _onPaired() {
    _windowTimer?.cancel();
    _windowTimer = null;
    state.value = const PairingState(PairingPhase.paired);
  }

  /// Full teardown (logout). Ambient pairing stops until the next
  /// [ensureRunning].
  Future<void> stop() async {
    _windowTimer?.cancel();
    _windowTimer = null;
    _currentApi = null;
    final s = _server;
    _server = null;
    await s?.stop();
    state.value = PairingState.idle;
  }
}
