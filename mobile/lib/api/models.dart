// Data models mirroring the Mediavida backend REST API (mediavida-api).
// Field names match the Go DTOs exactly.

String _s(dynamic v) => v == null ? '' : v.toString();
int _i(dynamic v) {
  if (v is int) return v;
  if (v is num) return v.toInt();
  return int.tryParse(_s(v)) ?? 0;
}

bool _b(dynamic v) => v == true;

class ForumInfo {
  final String slug;
  final String name;
  final String description;
  final String icon;

  const ForumInfo({required this.slug, required this.name, this.description = '', this.icon = ''});

  factory ForumInfo.fromJson(Map<String, dynamic> j) => ForumInfo(
        slug: _s(j['slug']),
        name: _s(j['name']),
        description: _s(j['description']),
        icon: _s(j['icon']),
      );
}

class ForumCategory {
  final String name;
  final List<ForumInfo> forums;

  const ForumCategory({required this.name, required this.forums});

  factory ForumCategory.fromJson(Map<String, dynamic> j) => ForumCategory(
        name: _s(j['name']),
        forums: ((j['forums'] as List?) ?? [])
            .map((e) => ForumInfo.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class PortadaItem {
  final String title;
  final String url;
  final String forum;
  final String intro;
  final String image;
  final String replies;
  const PortadaItem({
    required this.title,
    required this.url,
    this.forum = '',
    this.intro = '',
    this.image = '',
    this.replies = '',
  });
  factory PortadaItem.fromJson(Map<String, dynamic> j) => PortadaItem(
        title: _s(j['title']),
        url: _s(j['url']),
        forum: _s(j['forum']),
        intro: _s(j['intro']),
        image: _s(j['image']),
        replies: _s(j['replies']),
      );
}

class ForumThread {
  final String id;
  final String title;
  final String url;
  final String author;
  final String tag;
  final String replies;
  final String views;
  final String lastActivity;
  final int lastTime;
  final bool pinned;
  final bool closed;
  final String unreadCount;

  const ForumThread({
    required this.id,
    required this.title,
    required this.url,
    this.author = '',
    this.tag = '',
    this.replies = '',
    this.views = '',
    this.lastActivity = '',
    this.lastTime = 0,
    this.pinned = false,
    this.closed = false,
    this.unreadCount = '',
  });

  factory ForumThread.fromJson(Map<String, dynamic> j) => ForumThread(
        id: _s(j['id']),
        title: _s(j['title']),
        url: _s(j['url']),
        author: _s(j['author']),
        tag: _s(j['tag']),
        replies: _s(j['replies']),
        views: _s(j['views']),
        lastActivity: _s(j['last_activity']),
        lastTime: _i(j['last_time']),
        pinned: _b(j['pinned']),
        closed: _b(j['closed']),
        unreadCount: _s(j['unread_count']),
      );
}

class ForumThreadList {
  final String subforum;
  final int currentPage;
  final int totalPages;
  final List<ForumThread> threads;

  const ForumThreadList({
    required this.subforum,
    required this.currentPage,
    required this.totalPages,
    required this.threads,
  });

  factory ForumThreadList.fromJson(Map<String, dynamic> j) => ForumThreadList(
        subforum: _s(j['subforum']),
        currentPage: _i(j['current_page']),
        totalPages: _i(j['total_pages']),
        threads: ((j['threads'] as List?) ?? [])
            .map((e) => ForumThread.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class Post {
  final int num;
  final String author;
  final String body;
  final String bodyHtml;
  final String avatar;
  final String date;
  final int time;
  final bool liked;

  const Post({
    required this.num,
    required this.author,
    this.body = '',
    this.bodyHtml = '',
    this.avatar = '',
    this.date = '',
    this.time = 0,
    this.liked = false,
  });

  factory Post.fromJson(Map<String, dynamic> j) => Post(
        num: _i(j['num']),
        author: _s(j['author']),
        body: _s(j['body']),
        bodyHtml: _s(j['body_html']),
        avatar: _s(j['avatar']),
        date: _s(j['date']),
        time: _i(j['time']),
        liked: _b(j['liked']),
      );
}

class ThreadPage {
  final int currentPage;
  final int totalPages;
  final List<Post> messages;

  const ThreadPage({required this.currentPage, required this.totalPages, required this.messages});

  factory ThreadPage.fromJson(Map<String, dynamic> j) => ThreadPage(
        currentPage: _i(j['current_page']),
        totalPages: _i(j['total_pages']),
        messages: ((j['messages'] as List?) ?? [])
            .map((e) => Post.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class TagOption {
  final String id;
  final String name;
  const TagOption({required this.id, required this.name});
  factory TagOption.fromJson(Map<String, dynamic> j) =>
      TagOption(id: _s(j['id']), name: _s(j['name']));
}

class TagsResponse {
  final String fid;
  final String subforum;
  final List<TagOption> tags;
  const TagsResponse({required this.fid, required this.subforum, required this.tags});
  factory TagsResponse.fromJson(Map<String, dynamic> j) => TagsResponse(
        fid: _s(j['fid']),
        subforum: _s(j['subforum']),
        tags: ((j['tags'] as List?) ?? [])
            .map((e) => TagOption.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class SearchResult {
  final String title;
  final String url;
  final String date;
  final String forum;
  final String author;
  const SearchResult({required this.title, required this.url, this.date = '', this.forum = '', this.author = ''});
  factory SearchResult.fromJson(Map<String, dynamic> j) => SearchResult(
        title: _s(j['title']),
        url: _s(j['url']),
        date: _s(j['date']),
        forum: _s(j['forum']),
        author: _s(j['author']),
      );
}

class InboxItem {
  final String id;
  final String author;
  final String date;
  final String preview;
  final bool unread;
  const InboxItem({required this.id, required this.author, this.date = '', this.preview = '', this.unread = false});
  factory InboxItem.fromJson(Map<String, dynamic> j) => InboxItem(
        id: _s(j['id']),
        author: _s(j['author']),
        date: _s(j['date']),
        preview: _s(j['preview']),
        unread: _b(j['unread']),
      );
}

class PrivateMessage {
  final String author;
  final String date;
  final String body;
  const PrivateMessage({required this.author, this.date = '', this.body = ''});
  factory PrivateMessage.fromJson(Map<String, dynamic> j) => PrivateMessage(
        author: _s(j['author']),
        date: _s(j['date']),
        body: _s(j['body']),
      );
}

class Conversation {
  final String title;
  final List<PrivateMessage> messages;
  const Conversation({this.title = '', required this.messages});
  factory Conversation.fromJson(Map<String, dynamic> j) => Conversation(
        title: _s(j['title']),
        messages: ((j['messages'] as List?) ?? [])
            .map((e) => PrivateMessage.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class ThreadListItem {
  final String title;
  final String url;
  final String forum;
  final String replies;
  final String lastActivity;
  final String unreadCount;
  final String unreadUrl; // link to first unread post: .../<page>#<num>
  const ThreadListItem({
    required this.title,
    required this.url,
    this.forum = '',
    this.replies = '',
    this.lastActivity = '',
    this.unreadCount = '',
    this.unreadUrl = '',
  });
  factory ThreadListItem.fromJson(Map<String, dynamic> j) => ThreadListItem(
        title: _s(j['title']),
        url: _s(j['url']),
        forum: _s(j['forum']),
        replies: _s(j['replies']),
        lastActivity: _s(j['last_activity']),
        unreadCount: _s(j['unread_count']),
        unreadUrl: _s(j['unread_url']),
      );

  /// Page number embedded in [unreadUrl] (.../<page>#<num>), or 0.
  int get unreadPage {
    if (unreadUrl.isEmpty) return 0;
    final path = unreadUrl.split('#').first;
    return int.tryParse(path.split('/').last) ?? 0;
  }

  /// Post number of the first unread post (the #<num> anchor), or 0.
  int get unreadPostNum {
    final i = unreadUrl.indexOf('#');
    return i < 0 ? 0 : int.tryParse(unreadUrl.substring(i + 1)) ?? 0;
  }
}

class Mention {
  final String threadTitle;
  final String threadUrl;
  final String author;
  final int postNum;
  final String body;
  final String date;
  const Mention({
    required this.threadTitle,
    required this.threadUrl,
    required this.author,
    required this.postNum,
    this.body = '',
    this.date = '',
  });
  factory Mention.fromJson(Map<String, dynamic> j) => Mention(
        threadTitle: _s(j['thread_title']),
        threadUrl: _s(j['thread_url']),
        author: _s(j['author']),
        postNum: _i(j['post_num']),
        body: _s(j['body']),
        date: _s(j['date']),
      );
}

class Bubbles {
  final int messages;
  final int notifications;
  final int favorites;
  const Bubbles({this.messages = 0, this.notifications = 0, this.favorites = 0});
  factory Bubbles.fromJson(Map<String, dynamic> j) => Bubbles(
        messages: _i(j['messages']),
        notifications: _i(j['notifications']),
        favorites: _i(j['favorites']),
      );
  int get total => messages + notifications + favorites;
}
