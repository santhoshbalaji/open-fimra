import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// This app's version, as in pubspec.yaml. The phone sends it when it
/// registers, so administrators can see which version each phone runs.
const fieldAppVersion = '0.2.0+2';

/// Where the app finds the core API and the sign-in server.
///
/// Build-time defaults come from `--dart-define` values. A user can replace
/// them on the phone under **Server settings**, for example to reach a
/// development machine through a tunnel or a test server.
@immutable
class AppConfig {
  const AppConfig({required this.apiBaseUrl, required this.issuer, required this.clientId, required this.redirectUri});

  /// The defaults built into this copy of the app.
  factory AppConfig.fromEnvironment() => AppConfig(
    apiBaseUrl: _withoutTrailingSlash(
      const String.fromEnvironment('FIMRA_API_URL', defaultValue: 'http://127.0.0.1:18090'),
    ),
    issuer: _withoutTrailingSlash(
      const String.fromEnvironment('FIMRA_OIDC_ISSUER', defaultValue: 'http://127.0.0.1:18180/realms/open-fimra'),
    ),
    clientId: const String.fromEnvironment('FIMRA_OIDC_CLIENT_ID', defaultValue: 'open-fimra-mobile'),
    redirectUri: const String.fromEnvironment('FIMRA_OIDC_REDIRECT_URI', defaultValue: kIsWeb ? '' : mobileRedirectUri),
  );

  /// The custom-scheme address the sign-in server returns to on a phone.
  /// It must match the scheme in android/app/build.gradle.kts.
  static const mobileRedirectUri = 'org.openfimra.field:/callback';

  final String apiBaseUrl;
  final String issuer;
  final String clientId;

  /// Empty means "work it out at run time". The browser test build uses
  /// `auth.html` on its own address.
  final String redirectUri;

  /// The scheme of [redirectUri], which the sign-in browser listens for.
  String get callbackScheme => Uri.parse(redirectUri).scheme;

  AppConfig copyWith({String? apiBaseUrl, String? issuer, String? clientId, String? redirectUri}) => AppConfig(
    apiBaseUrl: _withoutTrailingSlash(apiBaseUrl ?? this.apiBaseUrl),
    issuer: _withoutTrailingSlash(issuer ?? this.issuer),
    clientId: clientId ?? this.clientId,
    redirectUri: redirectUri ?? this.redirectUri,
  );

  /// Returns a plain-English problem with an address, or null when it is fine.
  static String? checkAddress(String value) {
    final uri = Uri.tryParse(value.trim());
    if (uri == null || !uri.hasScheme || uri.host.isEmpty) {
      return 'Enter a full address that starts with http:// or https://';
    }
    if (uri.scheme != 'http' && uri.scheme != 'https') {
      return 'The address must start with http:// or https://';
    }
    if (uri.hasQuery || uri.hasFragment) {
      return 'Leave out anything after ? or #';
    }
    return null;
  }

  @override
  bool operator ==(Object other) =>
      other is AppConfig &&
      other.apiBaseUrl == apiBaseUrl &&
      other.issuer == issuer &&
      other.clientId == clientId &&
      other.redirectUri == redirectUri;

  @override
  int get hashCode => Object.hash(apiBaseUrl, issuer, clientId, redirectUri);
}

String _withoutTrailingSlash(String value) {
  var result = value.trim();
  while (result.endsWith('/')) {
    result = result.substring(0, result.length - 1);
  }
  return result;
}

/// Keeps the server settings a user chose on this phone.
class ConfigStore {
  ConfigStore(this.defaults);

  final AppConfig defaults;
  static const _api = 'server.apiBaseUrl';
  static const _issuer = 'server.issuer';
  static const _client = 'server.clientId';

  Future<AppConfig> load() async {
    final prefs = await SharedPreferences.getInstance();
    return defaults.copyWith(
      apiBaseUrl: prefs.getString(_api),
      issuer: prefs.getString(_issuer),
      clientId: prefs.getString(_client),
    );
  }

  Future<void> save(AppConfig config) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(_api, config.apiBaseUrl);
    await prefs.setString(_issuer, config.issuer);
    await prefs.setString(_client, config.clientId);
  }

  Future<void> reset() async {
    final prefs = await SharedPreferences.getInstance();
    await Future.wait([prefs.remove(_api), prefs.remove(_issuer), prefs.remove(_client)]);
  }
}
