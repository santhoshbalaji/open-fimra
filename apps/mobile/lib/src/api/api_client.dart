import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:http/http.dart' as http;

import '../auth/oidc.dart';
import 'models.dart';

/// A failed API call with the server's plain-English message.
class ApiException implements Exception {
  const ApiException(this.status, this.message, {this.problems = const []});

  /// The HTTP status, or 0 when the server could not be reached.
  final int status;
  final String message;

  final List<String> problems;

  bool get offline => status == 0;

  @override
  String toString() => message;
}

/// The API refused a call because an administrator wiped this phone or it
/// was removed from the user's account. The app has been told through
/// [DeviceActionHandler] before this is thrown.
class DeviceActionException extends ApiException {
  const DeviceActionException(this.action, String message) : super(401, message);

  /// [DeviceAction.wipe] or [DeviceAction.removed].
  final DeviceAction action;
}

enum DeviceAction { wipe, removed }

/// Gives the API client a current access token.
typedef TokenProvider = Future<String> Function({bool forceRefresh});

/// Called when the API says this phone must wipe itself or was removed.
typedef DeviceActionHandler = Future<void> Function(DeviceAction action, String message);

/// The device calls the field app makes. [FimraApi] implements it; tests use fakes.
abstract interface class DeviceApi {
  Future<DeviceReceipt> registerDevice(String id, DeviceRegistration registration);
  Future<List<Device>> devices();
  Future<void> revokeDevice(String id, {bool signOut = false});
  Future<void> confirmWiped(String id);
}

/// Calls the core API with the signed-in user's token. The API applies the
/// user's grants to every answer, so the app only shows what it receives.
class FimraApi implements DeviceApi {
  FimraApi({
    required String baseUrl,
    required TokenProvider token,
    required http.Client httpClient,
    this.deviceId,
    this.onDeviceAction,
  }) : _base = baseUrl,
       _tokenFor = token,
       _http = httpClient;

  final String _base;
  final TokenProvider _tokenFor;
  final http.Client _http;

  /// This phone's registration ID, sent with every call so the API can tell
  /// a wiped phone to wipe. Null before the phone is registered.
  String? Function()? deviceId;

  /// Told when the API says this phone was wiped or removed.
  DeviceActionHandler? onDeviceAction;
  static const _timeout = Duration(seconds: 20);

  String get baseUrl => _base;

  String workspacePath(String slug) => '/api/v1/workspaces/${Uri.encodeComponent(slug)}';

  /// Tile address templates with `{z}/{x}/{y}` left for the map to fill.
  String referenceTiles(String slug) => '$_base${workspacePath(slug)}/tiles/reference/{z}/{x}/{y}.mvt';

  String detectionTiles(String slug, {int hours = 168}) =>
      '$_base${workspacePath(slug)}/tiles/detections/{z}/{x}/{y}.mvt?hours=$hours'
      '&classes=inside,near_edge,outside,fixed_heat_source';

  Future<Map<String, dynamic>> profileData() => _get('/api/v1/me');
  Future<Profile> me() async => Profile.fromJson(await profileData());

  Future<Map<String, dynamic>> workspaceData(String slug) => _get(workspacePath(slug));
  Future<WorkspaceMap> workspace(String slug) async => WorkspaceMap.fromJson(await workspaceData(slug));

  Future<Map<String, dynamic>> alertsData(
    String slug, {
    String? queue,
    String? status,
    int? hours,
    String? detection,
    String? cursor,
    int? limit,
  }) => _get('${workspacePath(slug)}/alerts', {
    'queue': ?queue,
    'status': ?status,
    if (hours != null) 'hours': '$hours',
    'detection': ?detection,
    'cursor': ?cursor,
    if (limit != null) 'limit': '$limit',
  });

  Future<AlertPage> alerts(
    String slug, {
    String? queue,
    String? status,
    int? hours,
    String? detection,
    String? cursor,
    int? limit,
  }) async => AlertPage.fromJson(
    await alertsData(
      slug,
      queue: queue,
      status: status,
      hours: hours,
      detection: detection,
      cursor: cursor,
      limit: limit,
    ),
  );

  String _alertPath(String slug, String id) => '${workspacePath(slug)}/alerts/${Uri.encodeComponent(id)}';

  Future<Map<String, dynamic>> alertData(String slug, String id) => _get(_alertPath(slug, id));
  Future<Alert> alert(String slug, String id) async => Alert.fromJson(await alertData(slug, id));

  Future<Map<String, dynamic>> historyData(String slug, String id) => _get('${_alertPath(slug, id)}/history');
  Future<AlertHistory> history(String slug, String id) async => AlertHistory.fromJson(await historyData(slug, id));

  Future<Map<String, dynamic>> offlineReference(String slug) => _get('${workspacePath(slug)}/offline-reference');

  Future<Map<String, dynamic>> assignedForms(String slug, String id) =>
      _get('${_alertPath(slug, id)}/forms', const {'attachmentMetadata': '1'});

  Future<Map<String, dynamic>> formVersion(String slug, String form, int version) =>
      _get('${workspacePath(slug)}/forms/${Uri.encodeComponent(form)}/versions/$version');

  Future<List<Map<String, dynamic>>> submissions(String slug, String id) async {
    final result = await _send('GET', '${_alertPath(slug, id)}/submissions', null, list: true);
    return (result['items'] as List).cast<Map<String, dynamic>>();
  }

  Future<Map<String, dynamic>> submitReport(String slug, String id, Map<String, dynamic> report) =>
      _send('POST', '${_alertPath(slug, id)}/submissions', report);

  Future<Map<String, dynamic>?> attachmentReceipt(String slug, String id, String attachment) async {
    try {
      return await _get('${_alertPath(slug, id)}/attachments/${Uri.encodeComponent(attachment)}');
    } on ApiException catch (error) {
      if (error.status == 404) return null;
      rethrow;
    }
  }

  Future<Map<String, dynamic>> uploadAttachment(
    String slug,
    String id,
    String attachment,
    String name,
    String type,
    Uint8List bytes,
  ) => _send(
    'PUT',
    '${_alertPath(slug, id)}/attachments/${Uri.encodeComponent(attachment)}',
    bytes,
    query: {'name': name},
    contentType: type,
    timeout: const Duration(minutes: 4),
  );

  Future<StepReceipt> recordStep(String slug, String id, StepRequest request) async => StepReceipt.fromJson(
    await _send('POST', '${workspacePath(slug)}/alerts/${Uri.encodeComponent(id)}/steps', request.toJson()),
  );

  @override
  /// Registers this phone, or records its latest push address and contact.
  Future<DeviceReceipt> registerDevice(String id, DeviceRegistration registration) async =>
      DeviceReceipt.fromJson(await _send('PUT', '/api/v1/devices/${Uri.encodeComponent(id)}', registration.toJson()));

  @override
  /// The user's phones, newest contact first.
  Future<List<Device>> devices() async {
    final json = await _get('/api/v1/devices');
    return (json['items'] as List? ?? const []).whereType<Map<String, dynamic>>().map(Device.fromJson).toList();
  }

  @override
  /// Removes one of the user's phones. [signOut] records that this phone signed out.
  Future<void> revokeDevice(String id, {bool signOut = false}) async {
    await _send(
      'DELETE',
      '/api/v1/devices/${Uri.encodeComponent(id)}',
      null,
      query: signOut ? const {'reason': 'sign_out'} : null,
      allowEmpty: true,
    );
  }

  @override
  /// Tells the API this phone deleted its data after a wipe request.
  Future<void> confirmWiped(String id) async {
    await _send('POST', '/api/v1/devices/${Uri.encodeComponent(id)}/wiped', null, allowEmpty: true);
  }

  Future<Map<String, dynamic>> _get(String path, [Map<String, String>? query]) =>
      _send('GET', path, null, query: query);

  Future<Map<String, dynamic>> _send(
    String method,
    String path,
    Object? body, {
    Map<String, String>? query,
    bool allowEmpty = false,
    bool list = false,
    String contentType = 'application/json',
    Duration timeout = _timeout,
  }) async {
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
      final device = deviceId?.call();
      if (device != null) request.headers['Fimra-Device'] = device;
      if (body != null) {
        request.headers['Content-Type'] = contentType;
        if (body is Uint8List) {
          request.bodyBytes = body;
        } else {
          request.body = jsonEncode(body);
        }
      }
      try {
        return await _http.send(request).then(http.Response.fromStream).timeout(timeout);
      } on TimeoutException {
        throw const ApiException(0, 'The server took too long to answer. Try again.');
      } on Exception {
        throw ApiException(0, 'Could not reach the server at $_base. Check your connection and the server settings.');
      }
    }

    Map<String, dynamic>? decode(http.Response response) {
      try {
        final decoded = response.body.isEmpty ? null : jsonDecode(response.body);
        if (list && decoded is List) return {'items': decoded};
        return decoded is Map<String, dynamic> ? decoded : null;
      } on FormatException {
        return null;
      }
    }

    var response = await attempt(false);
    var json = decode(response);
    // A token can be revoked or expire early; refresh once and retry. A wiped
    // or removed phone is told so instead, and a new token would not help.
    if (response.statusCode == 401 && _deviceAction(json) == null) {
      response = await attempt(true);
      json = decode(response);
    }
    if (response.statusCode >= 200 && response.statusCode < 300) {
      if (json == null && allowEmpty && response.body.isEmpty) return const {};
      if (json == null) throw ApiException(response.statusCode, 'The server sent an answer the app cannot read.');
      return json;
    }
    final message = json?['error'];
    final action = response.statusCode == 401 ? _deviceAction(json) : null;
    if (action != null) {
      final text = message is String && message.isNotEmpty ? message : 'Sign in again.';
      await onDeviceAction?.call(action, text);
      throw DeviceActionException(action, text);
    }
    throw ApiException(
      response.statusCode,
      message is String && message.isNotEmpty ? message : _fallback(response.statusCode),
      problems: (json?['problems'] as List? ?? const []).whereType<String>().toList(),
    );
  }

  static DeviceAction? _deviceAction(Map<String, dynamic>? json) => switch (json?['code']) {
    'device_wiped' => DeviceAction.wipe,
    'device_revoked' => DeviceAction.removed,
    _ => null,
  };

  static String _fallback(int status) => switch (status) {
    401 => 'Your session has ended. Sign in again.',
    403 => 'You do not have access to this.',
    404 => 'This was not found. It may be outside your area.',
    409 => 'Someone else changed this first. Check the timeline and try again.',
    _ => 'The server could not answer (error $status). Try again later.',
  };
}
