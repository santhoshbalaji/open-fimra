import 'dart:convert';

import 'package:flutter_web_auth_2/flutter_web_auth_2.dart';
import 'package:http/http.dart' as http;

import '../config.dart';
import 'pkce.dart';
import 'tokens.dart';

/// A sign-in problem with a message the user can read.
class SignInException implements Exception {
  const SignInException(this.message, {this.cancelled = false});
  final String message;
  final bool cancelled;

  @override
  String toString() => message;
}

/// The sign-in server's endpoints from OpenID Connect discovery.
class OidcEndpoints {
  const OidcEndpoints({required this.authorization, required this.token, this.endSession});

  factory OidcEndpoints.fromDiscovery(Map<String, dynamic> json) {
    Uri required(String key) {
      final value = json[key];
      if (value is! String || value.isEmpty) {
        throw SignInException('The sign-in server did not list its $key.');
      }
      return Uri.parse(value);
    }

    final end = json['end_session_endpoint'];
    return OidcEndpoints(
      authorization: required('authorization_endpoint'),
      token: required('token_endpoint'),
      endSession: end is String ? Uri.parse(end) : null,
    );
  }

  final Uri authorization;
  final Uri token;
  final Uri? endSession;
}

/// The state of one sign-in attempt, kept until the browser returns.
class AuthorizationRequest {
  AuthorizationRequest({required this.url, required this.pkce, required this.state, required this.nonce});
  final Uri url;
  final Pkce pkce;
  final String state;
  final String nonce;
}

/// Opens the sign-in page and waits for the return address.
abstract interface class AuthBrowser {
  /// Returns the full address the sign-in server sent the browser back to.
  Future<String> authenticate(Uri url, String callbackScheme);
}

/// Uses a Chrome Custom Tab on Android and a pop-up window in a browser.
class SystemAuthBrowser implements AuthBrowser {
  const SystemAuthBrowser();

  @override
  Future<String> authenticate(Uri url, String callbackScheme) async {
    try {
      return await FlutterWebAuth2.authenticate(url: url.toString(), callbackUrlScheme: callbackScheme);
    } on Exception catch (error) {
      final text = error.toString();
      if (text.contains('CANCELED') || text.contains('CANCELLED')) {
        throw const SignInException('Sign-in was cancelled.', cancelled: true);
      }
      throw SignInException('The sign-in page could not open. $text');
    }
  }
}

/// OpenID Connect authorisation code flow with PKCE for a public client.
///
/// The app never sees the user's password, and the client has no secret.
class OidcClient {
  OidcClient({required this.config, required http.Client httpClient, DateTime Function()? clock})
    : _http = httpClient,
      _clock = clock ?? DateTime.now;

  final AppConfig config;
  final http.Client _http;
  final DateTime Function() _clock;
  OidcEndpoints? _endpoints;

  static const scope = 'openid profile email';

  Future<OidcEndpoints> endpoints() async {
    final cached = _endpoints;
    if (cached != null) return cached;
    final url = Uri.parse('${config.issuer}/.well-known/openid-configuration');
    final http.Response response;
    try {
      response = await _http.get(url).timeout(const Duration(seconds: 15));
    } on Exception {
      throw SignInException(
        'Could not reach the sign-in server at ${config.issuer}. Check the server settings and your connection.',
      );
    }
    if (response.statusCode != 200) {
      throw SignInException(
        'The sign-in server answered ${response.statusCode}. Check the sign-in address in the server settings.',
      );
    }
    return _endpoints = OidcEndpoints.fromDiscovery(jsonDecode(response.body) as Map<String, dynamic>);
  }

  /// Builds the address of the sign-in page.
  Future<AuthorizationRequest> authorizationRequest({Pkce? pkce, String? state, String? nonce}) async {
    final endpoint = (await endpoints()).authorization;
    final proof = pkce ?? Pkce.generate();
    final attempt = state ?? randomToken();
    final once = nonce ?? randomToken();
    return AuthorizationRequest(
      pkce: proof,
      state: attempt,
      nonce: once,
      url: endpoint.replace(
        queryParameters: {
          ...endpoint.queryParameters,
          'response_type': 'code',
          'client_id': config.clientId,
          'redirect_uri': config.redirectUri,
          'scope': scope,
          'code_challenge': proof.challenge,
          'code_challenge_method': Pkce.method,
          'state': attempt,
          'nonce': once,
        },
      ),
    );
  }

  /// Reads the code from the return address and checks the state.
  static String codeFromCallback(String callback, AuthorizationRequest request) {
    final uri = Uri.parse(callback);
    final params = {...uri.queryParameters, ...Uri.splitQueryString(uri.fragment)};
    if (params['state'] != request.state) {
      throw const SignInException('The sign-in reply did not match this attempt. Try again.');
    }
    final error = params['error'];
    if (error != null) {
      final description = params['error_description'];
      throw SignInException(
        error == 'access_denied' ? 'Sign-in was refused.' : 'Sign-in failed: ${description ?? error}.',
      );
    }
    final code = params['code'];
    if (code == null || code.isEmpty) {
      throw const SignInException('The sign-in reply had no code. Try again.');
    }
    return code;
  }

  /// Opens the sign-in page in [browser] and returns the user's tokens.
  Future<TokenSet> signIn(AuthBrowser browser) async {
    final request = await authorizationRequest();
    final callback = await browser.authenticate(request.url, config.callbackScheme);
    final code = codeFromCallback(callback, request);
    final tokens = await exchangeCode(code, request.pkce);
    final idToken = tokens.idToken;
    if (idToken != null && TokenSet.unverifiedClaims(idToken)['nonce'] != request.nonce) {
      throw const SignInException('The sign-in reply did not match this attempt. Try again.');
    }
    return tokens;
  }

  Future<TokenSet> exchangeCode(String code, Pkce pkce) => _token({
    'grant_type': 'authorization_code',
    'code': code,
    'redirect_uri': config.redirectUri,
    'client_id': config.clientId,
    'code_verifier': pkce.verifier,
  });

  /// Returns fresh tokens, or null when the session has ended and the user
  /// must sign in again.
  Future<TokenSet?> refresh(TokenSet current) async {
    final refreshToken = current.refreshToken;
    if (refreshToken == null) return null;
    try {
      return current.mergeRefresh(
        await _token({'grant_type': 'refresh_token', 'refresh_token': refreshToken, 'client_id': config.clientId}),
      );
    } on SessionEndedException {
      return null;
    }
  }

  /// Ends the session on the sign-in server. Failure is not an error: the
  /// app forgets the tokens either way.
  Future<void> signOut(TokenSet tokens) async {
    final refreshToken = tokens.refreshToken;
    if (refreshToken == null) return;
    try {
      final endSession = (await endpoints()).endSession;
      if (endSession == null) return;
      await _http
          .post(endSession, body: {'client_id': config.clientId, 'refresh_token': refreshToken})
          .timeout(const Duration(seconds: 10));
    } on Exception {
      // Offline or unreachable: the server session expires by itself.
    }
  }

  Future<TokenSet> _token(Map<String, String> form) async {
    final endpoint = (await endpoints()).token;
    final http.Response response;
    try {
      response = await _http.post(endpoint, body: form).timeout(const Duration(seconds: 20));
    } on Exception {
      throw const SignInException('Could not reach the sign-in server. Check your connection.');
    }
    Map<String, dynamic> body;
    try {
      body = jsonDecode(response.body) as Map<String, dynamic>;
    } on FormatException {
      body = const {};
    }
    if (response.statusCode == 400 && body['error'] == 'invalid_grant') {
      throw const SessionEndedException();
    }
    if (response.statusCode != 200) {
      throw SignInException('The sign-in server refused the request (${body['error'] ?? response.statusCode}).');
    }
    return TokenSet.fromTokenResponse(body, _clock());
  }
}

/// The sign-in server no longer accepts the refresh token or code.
class SessionEndedException extends SignInException {
  const SessionEndedException() : super('Your session has ended. Sign in again.');
}
