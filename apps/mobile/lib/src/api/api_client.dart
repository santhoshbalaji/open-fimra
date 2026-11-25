import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import '../auth/oidc.dart';
import 'models.dart';

/// A failed API call with the server's plain-English message.
class ApiException implements Exception {
  const ApiException(this.status, this.message);

  /// The HTTP status, or 0 when the server could not be reached.
  final int status;
  final String message;

  bool get offline => status == 0;

  @override
  String toString() => message;
}

/// Gives the API client a current access token.
typedef TokenProvider = Future<String> Function({bool forceRefresh});

/// Calls the core API with the signed-in user's token. The API applies the
/// user's grants to every answer, so the app only shows what it receives.
class FimraApi {
  FimraApi({required String baseUrl, required TokenProvider token, required http.Client httpClient})
    : _base = baseUrl,
      _tokenFor = token,
      _http = httpClient;

  final String _base;
  final TokenProvider _tokenFor;
  final http.Client _http;
  static const _timeout = Duration(seconds: 20);

  String get baseUrl => _base;

  String workspacePath(String slug) => '/api/v1/workspaces/${Uri.encodeComponent(slug)}';

  /// Tile address templates with `{z}/{x}/{y}` left for the map to fill.
  String referenceTiles(String slug) => '$_base${workspacePath(slug)}/tiles/reference/{z}/{x}/{y}.mvt';

  String detectionTiles(String slug, {int hours = 168}) =>
      '$_base${workspacePath(slug)}/tiles/detections/{z}/{x}/{y}.mvt?hours=$hours'
      '&classes=inside,near_edge,outside,fixed_heat_source';

  Future<Profile> me() async => Profile.fromJson(await _get('/api/v1/me'));

  Future<WorkspaceMap> workspace(String slug) async => WorkspaceMap.fromJson(await _get(workspacePath(slug)));

  Future<AlertPage> alerts(
    String slug, {
    String? queue,
    String? status,
    int? hours,
    String? detection,
    String? cursor,
    int? limit,
  }) async {
    final query = {
      'queue': ?queue,
      'status': ?status,
      if (hours != null) 'hours': '$hours',
      'detection': ?detection,
      'cursor': ?cursor,
      if (limit != null) 'limit': '$limit',
    };
    return AlertPage.fromJson(await _get('${workspacePath(slug)}/alerts', query));
  }

  Future<Alert> alert(String slug, String id) async =>
      Alert.fromJson(await _get('${workspacePath(slug)}/alerts/${Uri.encodeComponent(id)}'));

  Future<AlertHistory> history(String slug, String id) async =>
      AlertHistory.fromJson(await _get('${workspacePath(slug)}/alerts/${Uri.encodeComponent(id)}/history'));

  Future<StepReceipt> recordStep(String slug, String id, StepRequest request) async => StepReceipt.fromJson(
    await _send('POST', '${workspacePath(slug)}/alerts/${Uri.encodeComponent(id)}/steps', request.toJson()),
  );

  Future<Map<String, dynamic>> _get(String path, [Map<String, String>? query]) => _send('GET', path, null, query);

  Future<Map<String, dynamic>> _send(String method, String path, Object? body, [Map<String, String>? query]) async {
    final url = Uri.parse('$_base$path').replace(queryParameters: query == null || query.isEmpty ? null : query);
    Future<http.Response> attempt(bool refresh) async {
      final String token;
      try {
        token = await _tokenFor(forceRefresh: refresh);
      } on SessionEndedException catch (error) {
        throw ApiException(401, error.message);
      } on SignInException catch (error) {
        throw ApiException(0, error.message);
      }
      final request = http.Request(method, url)
        ..headers['Authorization'] = 'Bearer $token'
        ..headers['Accept'] = 'application/json';
      if (body != null) {
        request.headers['Content-Type'] = 'application/json';
        request.body = jsonEncode(body);
      }
      try {
        return await http.Response.fromStream(await _http.send(request).timeout(_timeout));
      } on TimeoutException {
        throw const ApiException(0, 'The server took too long to answer. Try again.');
      } on Exception {
        throw ApiException(0, 'Could not reach the server at $_base. Check your connection and the server settings.');
      }
    }

    var response = await attempt(false);
    // A token can be revoked or expire early; refresh once and retry.
    if (response.statusCode == 401) response = await attempt(true);
    Map<String, dynamic>? json;
    try {
      final decoded = response.body.isEmpty ? null : jsonDecode(response.body);
      if (decoded is Map<String, dynamic>) json = decoded;
    } on FormatException {
      json = null;
    }
    if (response.statusCode >= 200 && response.statusCode < 300) {
      if (json == null) throw ApiException(response.statusCode, 'The server sent an answer the app cannot read.');
      return json;
    }
    final message = json?['error'];
    throw ApiException(
      response.statusCode,
      message is String && message.isNotEmpty ? message : _fallback(response.statusCode),
    );
  }

  static String _fallback(int status) => switch (status) {
    401 => 'Your session has ended. Sign in again.',
    403 => 'You do not have access to this.',
    404 => 'This was not found. It may be outside your area.',
    409 => 'Someone else changed this first. Check the timeline and try again.',
    _ => 'The server could not answer (error $status). Try again later.',
  };
}
