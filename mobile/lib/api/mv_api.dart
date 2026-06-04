import 'dart:async';
import 'dart:convert';

import 'package:dio/dio.dart';
import 'package:http/http.dart' as http;

import 'models.dart';

/// Thrown when the API rejects the request or the session is unavailable.
class MvApiException implements Exception {
  final int? statusCode;
  final String message;
  MvApiException(this.message, [this.statusCode]);
  @override
  String toString() => 'MvApiException($statusCode): $message';
}

/// Client for the reverse-engineered Mediavida backend (mediavida-api on Fly).
class MvApi {
  MvApi({required String baseUrl, required String token})
      : _baseUrl = baseUrl.replaceAll(RegExp(r'/+$'), ''),
        _token = token,
        _dio = Dio(BaseOptions(
          baseUrl: baseUrl.replaceAll(RegExp(r'/+$'), ''),
          headers: {'Authorization': 'Bearer $token'},
          connectTimeout: const Duration(seconds: 20),
          receiveTimeout: const Duration(seconds: 40),
          // We handle non-2xx ourselves to surface API error messages.
          validateStatus: (s) => s != null && s < 500,
        ));

  final String _baseUrl;
  final String _token;
  final Dio _dio;

  String get baseUrl => _baseUrl;

  Never _fail(Response r) {
    String msg = 'request failed';
    final data = r.data;
    if (data is Map && data['error'] != null) {
      msg = data['error'].toString();
    } else if (data is String && data.isNotEmpty) {
      msg = data;
    }
    throw MvApiException(msg, r.statusCode);
  }

  Map<String, dynamic> _obj(Response r) {
    if (r.statusCode != 200) _fail(r);
    final d = r.data;
    if (d is Map<String, dynamic>) return d;
    if (d is String) return jsonDecode(d) as Map<String, dynamic>;
    throw MvApiException('unexpected response shape', r.statusCode);
  }

  // --- auth / status ---

  Future<bool> isAuthenticated() async {
    final r = await _dio.get('/auth/status');
    if (r.statusCode != 200) return false;
    final d = r.data is Map ? r.data as Map : <String, dynamic>{};
    return d['status'] == 'authenticated';
  }

  Future<String?> currentUser() async {
    final r = await _dio.get('/auth/status');
    if (r.statusCode != 200) return null;
    final d = r.data is Map ? r.data as Map : <String, dynamic>{};
    return d['status'] == 'authenticated' ? d['username']?.toString() : null;
  }

  // --- forum browse ---

  Future<List<ForumCategory>> forums() async {
    final d = _obj(await _dio.get('/forums'));
    return ((d['categories'] as List?) ?? [])
        .map((e) => ForumCategory.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<ForumThreadList> forumThreads(String slug, {int page = 1}) async {
    final d = _obj(await _dio.get('/forums/$slug/threads',
        queryParameters: {'page': page}));
    return ForumThreadList.fromJson(d);
  }

  // --- threads ---

  /// [url] is the full thread URL. [page] <= 0 loads the last page.
  Future<ThreadPage> thread(String url, {int page = 0}) async {
    final qp = <String, dynamic>{'url': url};
    if (page > 0) qp['page'] = page;
    final d = _obj(await _dio.get('/threads', queryParameters: qp));
    return ThreadPage.fromJson(d);
  }

  /// Like/unlike a post in the last-read thread. Call right after [thread].
  Future<void> like(int postNum) async {
    final r = await _dio.post('/threads/like', data: {'post_num': postNum});
    if (r.statusCode != 200) _fail(r);
  }

  /// Reply to the last-read thread. Call right after [thread].
  Future<void> reply(String text, {int replyToNum = 0}) async {
    final r = await _dio.post('/threads/reply',
        data: {'text': text, if (replyToNum > 0) 'reply_to_num': replyToNum});
    if (r.statusCode != 200) _fail(r);
  }

  Future<TagsResponse> threadTags(String subforum) async {
    final d = _obj(await _dio.get('/threads/tags',
        queryParameters: {'subforum': subforum}));
    return TagsResponse.fromJson(d);
  }

  Future<void> createThread({
    required String subforum,
    required String title,
    required String body,
    int? tag,
    bool addToFavorites = false,
  }) async {
    final r = await _dio.post('/threads', data: {
      'subforum': subforum,
      'title': title,
      'body': body,
      if (tag != null) 'tag': tag,
      'add_to_favorites': addToFavorites,
    });
    if (r.statusCode != 200) _fail(r);
  }

  // --- search ---

  Future<List<SearchResult>> search(String query) async {
    final d = _obj(await _dio.get('/search', queryParameters: {'q': query}));
    return ((d['results'] as List?) ?? [])
        .map((e) => SearchResult.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  // --- private messages ---

  Future<List<InboxItem>> inbox({int page = 1}) async {
    final d = _obj(await _dio.get('/inbox', queryParameters: {'page': page}));
    return ((d['conversations'] as List?) ?? [])
        .map((e) => InboxItem.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<Conversation> conversation(String id) async {
    final d = _obj(await _dio.get('/messages/$id'));
    return Conversation.fromJson(d);
  }

  /// Send a reply into an existing private-message conversation.
  Future<void> sendMessage(String id, String text) async {
    final r = await _dio.post('/messages/$id/reply', data: {'text': text});
    if (r.statusCode != 200) _fail(r);
  }

  // --- favorites / user content ---

  Future<List<ThreadListItem>> favorites() async {
    final d = _obj(await _dio.get('/favorites'));
    return ((d['favorites'] as List?) ?? [])
        .map((e) => ThreadListItem.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<List<ThreadListItem>> userPosts(String username) async {
    final d = _obj(await _dio.get('/users/$username/posts'));
    return ((d['threads'] as List?) ?? [])
        .map((e) => ThreadListItem.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<List<Mention>> mentions(String username) async {
    final d = _obj(await _dio.get('/users/$username/mentions'));
    return ((d['mentions'] as List?) ?? [])
        .map((e) => Mention.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  // --- notifications ---

  Future<Bubbles> bubbles() async {
    final d = _obj(await _dio.get('/bubbles'));
    return Bubbles.fromJson(d);
  }

  /// Live notification stream via Server-Sent Events (`GET /events`).
  /// EventSource can't set headers, so the token goes in the query string;
  /// the backend also accepts it there.
  Stream<Bubbles> events() async* {
    final uri = Uri.parse('$_baseUrl/events').replace(queryParameters: {
      'token': _token,
    });
    final client = http.Client();
    try {
      final req = http.Request('GET', uri)
        ..headers['Authorization'] = 'Bearer $_token'
        ..headers['Accept'] = 'text/event-stream';
      final resp = await client.send(req);
      if (resp.statusCode != 200) {
        throw MvApiException('events stream failed', resp.statusCode);
      }
      String event = '';
      await for (final line in resp.stream
          .transform(utf8.decoder)
          .transform(const LineSplitter())) {
        if (line.startsWith('event:')) {
          event = line.substring(6).trim();
        } else if (line.startsWith('data:')) {
          final payload = line.substring(5).trim();
          if (event == 'bubbles' && payload.isNotEmpty) {
            try {
              yield Bubbles.fromJson(jsonDecode(payload) as Map<String, dynamic>);
            } catch (_) {}
          }
        }
      }
    } finally {
      client.close();
    }
  }
}
