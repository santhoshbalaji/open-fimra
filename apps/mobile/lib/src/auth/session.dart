import 'dart:async';

import 'package:flutter/foundation.dart';

import 'oidc.dart';
import 'tokens.dart';

enum SessionStatus { starting, signedOut, signingIn, signedIn }

/// The signed-in user's session: tokens, refresh and sign-out.
class Session extends ChangeNotifier {
  Session({
    required OidcClient oidc,
    required TokenStore store,
    required AuthBrowser browser,
    DateTime Function()? clock,
  }) : _client = oidc,
       _tokenStore = store,
       _authBrowser = browser,
       _clock = clock ?? DateTime.now;

  final OidcClient _client;
  final TokenStore _tokenStore;
  final AuthBrowser _authBrowser;
  final DateTime Function() _clock;

  TokenSet? _tokens;
  Future<TokenSet?>? _refreshing;
  SessionStatus _status = SessionStatus.starting;
  String? _message;

  SessionStatus get status => _status;

  /// The latest sign-in problem, for the sign-in screen.
  String? get message => _message;

  /// The username from the latest token, for display only.
  String? get username {
    final token = _tokens?.idToken ?? _tokens?.accessToken;
    if (token == null) return null;
    return TokenSet.unverifiedClaims(token)['preferred_username'] as String?;
  }

  /// Reads saved tokens. A saved session is used until the server refuses it.
  Future<void> restore() async {
    _tokens = await _tokenStore.read();
    _set(_tokens == null ? SessionStatus.signedOut : SessionStatus.signedIn);
  }

  Future<void> signIn() async {
    if (_status == SessionStatus.signingIn) return;
    _message = null;
    _set(SessionStatus.signingIn);
    try {
      final tokens = await _client.signIn(_authBrowser);
      await _tokenStore.write(tokens);
      _tokens = tokens;
      _set(SessionStatus.signedIn);
    } on SignInException catch (error) {
      _message = error.cancelled ? null : error.message;
      _set(SessionStatus.signedOut);
    }
  }

  /// Ends the session on the phone and on the sign-in server. [message] is
  /// shown on the sign-in screen, for example after a wipe.
  Future<void> signOut({String? message}) async {
    final tokens = _tokens;
    _tokens = null;
    _message = message;
    await _tokenStore.clear();
    _set(SessionStatus.signedOut);
    if (tokens != null) await _client.signOut(tokens);
  }

  /// Returns a current access token, refreshing it first when it is about to
  /// expire. Throws [SessionEndedException] when the user must sign in again.
  Future<String> accessToken({bool forceRefresh = false}) async {
    var tokens = _tokens;
    if (tokens == null) throw const SessionEndedException();
    if (forceRefresh || tokens.expiresSoon(_clock())) {
      tokens = await (_refreshing ??= _refresh(tokens).whenComplete(() => _refreshing = null));
      if (tokens == null) throw const SessionEndedException();
    }
    return tokens.accessToken;
  }

  Future<TokenSet?> _refresh(TokenSet current) async {
    final next = await _client.refresh(current);
    if (next == null) {
      _tokens = null;
      await _tokenStore.clear();
      _message = 'Your session has ended. Sign in again.';
      _set(SessionStatus.signedOut);
      return null;
    }
    _tokens = next;
    await _tokenStore.write(next);
    return next;
  }

  void _set(SessionStatus status) {
    _status = status;
    notifyListeners();
  }
}
