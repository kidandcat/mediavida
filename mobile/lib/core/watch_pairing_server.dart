import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';

import '../api/mv_api.dart';

/// Localhost handshake server for pairing an Amazfit watchface.
///
/// While pairing is active the Flutter app runs this tiny HTTP server bound to
/// loopback. The watch's Zepp side-service fetches `GET /pair` once (with the
/// shared app-key header) to receive a watch token minted by the backend. After
/// that the watch talks to the backend directly and autonomously.
///
/// The contract here is matched exactly by the watch side — do not change the
/// port, path, header name or app key without updating the watch app too.
class WatchPairingServer {
  WatchPairingServer(this._api, {this.onPaired});

  /// Loopback port the watch's side-service fetches from.
  static const int port = 28590;

  /// Path the watch hits to receive its token.
  static const String pairPath = '/pair';

  /// Header the watch must send (shared secret between this app and the watch).
  static const String appKeyHeader = 'X-Watch-App-Key';

  /// Shared key the watch presents. Must equal what the watch side is built with.
  static const String appKey = '70f54fb484323ae9c7fcaff542bcfda8';

  /// Label applied to watches paired through this handshake.
  static const String watchLabel = 'Amazfit';

  final MvApi _api;

  /// Invoked after a successful `/pair` handshake, so the UI can refresh the
  /// paired-watches list and show a confirmation.
  final void Function()? onPaired;

  HttpServer? _server;

  bool get isRunning => _server != null;

  /// Starts the loopback server. Rethrows the [SocketException] if the port is
  /// already in use (e.g. pairing was started twice) so the caller can surface it.
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
      final result = await _api.pairWatch(label: watchLabel);
      debugPrint('[WatchPairing] paired ✓ token=${result.token.substring(0, 8)}…');
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

/// Status of the app-scoped pairing window.
enum PairingPhase { idle, waiting, paired, error }

class PairingState {
  const PairingState(this.phase, {this.until, this.message});
  final PairingPhase phase;
  final DateTime? until; // when the active window auto-closes
  final String? message;

  static const idle = PairingState(PairingPhase.idle);
}

/// App-scoped owner of the pairing server, so it survives leaving the Relojes
/// screen and the app being backgrounded (the Dart isolate keeps running while
/// the user switches to the Zepp app to open the watch mini-app). The server is
/// only torn down when the pairing window elapses, on success, or explicitly —
/// NOT when the screen is disposed. This is what makes the loopback handshake
/// reachable during the window the watch actually fetches.
class WatchPairingHub {
  WatchPairingHub._();
  static final WatchPairingHub instance = WatchPairingHub._();

  WatchPairingServer? _server;
  Timer? _autoStop;

  /// Observable state for the UI.
  final ValueNotifier<PairingState> state = ValueNotifier(PairingState.idle);

  bool get active => _server != null;

  /// Opens a pairing window: binds the loopback server and keeps it up for
  /// [window]. Safe to call repeatedly (re-arms the window).
  Future<void> start(MvApi api,
      {Duration window = const Duration(minutes: 5)}) async {
    _autoStop?.cancel();
    if (_server == null) {
      final server = WatchPairingServer(api, onPaired: _onPaired);
      try {
        await server.start();
      } on SocketException catch (e) {
        state.value = PairingState(PairingPhase.error,
            message: 'No se pudo abrir el emparejamiento (puerto en uso).');
        debugPrint('[WatchPairing] bind failed: $e');
        return;
      }
      _server = server;
    }
    final until = DateTime.now().add(window);
    state.value = PairingState(PairingPhase.waiting, until: until);
    _autoStop = Timer(window, stop);
  }

  void _onPaired() {
    state.value = const PairingState(PairingPhase.paired);
    // Keep the server up briefly in case the watch retries, then close it.
    _autoStop?.cancel();
    _autoStop = Timer(const Duration(seconds: 30), stop);
  }

  Future<void> stop() async {
    _autoStop?.cancel();
    _autoStop = null;
    final s = _server;
    _server = null;
    await s?.stop();
    if (state.value.phase != PairingPhase.paired) {
      state.value = PairingState.idle;
    }
  }
}
