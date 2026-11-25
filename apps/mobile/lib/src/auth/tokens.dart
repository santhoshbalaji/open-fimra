import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// The tokens from one sign-in.
class TokenSet {
  const TokenSet({required this.accessToken, required this.expiresAt, this.refreshToken, this.idToken});

  factory TokenSet.fromTokenResponse(Map<String, dynamic> json, DateTime now) {
    final access = json['access_token'];
    if (access is! String || access.isEmpty) {
      throw const FormatException('The sign-in server sent no access token.');
    }
    final seconds = (json['expires_in'] as num?)?.toInt() ?? 300;
    return TokenSet(
      accessToken: access,
      expiresAt: now.add(Duration(seconds: seconds)),
      refreshToken: json['refresh_token'] as String?,
      idToken: json['id_token'] as String?,
    );
  }

  factory TokenSet.fromJson(Map<String, dynamic> json) => TokenSet(
    accessToken: json['accessToken'] as String,
    expiresAt: DateTime.parse(json['expiresAt'] as String),
    refreshToken: json['refreshToken'] as String?,
    idToken: json['idToken'] as String?,
  );

  final String accessToken;
  final DateTime expiresAt;
  final String? refreshToken;
  final String? idToken;

  /// True when the access token expires within [margin].
  bool expiresSoon(DateTime now, [Duration margin = const Duration(seconds: 30)]) =>
      !now.add(margin).isBefore(expiresAt);

  /// Keeps the refresh and ID tokens when a refresh response leaves them out.
  TokenSet mergeRefresh(TokenSet next) => TokenSet(
    accessToken: next.accessToken,
    expiresAt: next.expiresAt,
    refreshToken: next.refreshToken ?? refreshToken,
    idToken: next.idToken ?? idToken,
  );

  Map<String, dynamic> toJson() => {
    'accessToken': accessToken,
    'expiresAt': expiresAt.toUtc().toIso8601String(),
    if (refreshToken != null) 'refreshToken': refreshToken,
    if (idToken != null) 'idToken': idToken,
  };

  /// Reads the claims of the ID or access token without checking the
  /// signature. Only for display; the API checks every token itself.
  static Map<String, dynamic> unverifiedClaims(String jwt) {
    final parts = jwt.split('.');
    if (parts.length != 3) return const {};
    try {
      final body = utf8.decode(base64Url.decode(base64Url.normalize(parts[1])));
      return jsonDecode(body) as Map<String, dynamic>;
    } on FormatException {
      return const {};
    }
  }
}

/// Where the tokens are kept between app starts.
abstract interface class TokenStore {
  Future<TokenSet?> read();
  Future<void> write(TokenSet tokens);
  Future<void> clear();
}

/// Keeps tokens in the Android Keystore through flutter_secure_storage.
class SecureTokenStore implements TokenStore {
  SecureTokenStore([FlutterSecureStorage? storage]) : _storage = storage ?? const FlutterSecureStorage();

  final FlutterSecureStorage _storage;
  static const _key = 'session.tokens';

  @override
  Future<TokenSet?> read() async {
    try {
      final value = await _storage.read(key: _key);
      if (value == null) return null;
      return TokenSet.fromJson(jsonDecode(value) as Map<String, dynamic>);
    } on Exception {
      // A damaged or unreadable entry means the user signs in again.
      await clear();
      return null;
    }
  }

  @override
  Future<void> write(TokenSet tokens) => _storage.write(key: _key, value: jsonEncode(tokens.toJson()));

  @override
  Future<void> clear() => _storage.delete(key: _key);
}

/// Keeps tokens in memory only, for tests.
class MemoryTokenStore implements TokenStore {
  TokenSet? tokens;

  @override
  Future<TokenSet?> read() async => tokens;

  @override
  Future<void> write(TokenSet value) async => tokens = value;

  @override
  Future<void> clear() async => tokens = null;
}
