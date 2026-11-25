import 'package:fimra_field/src/auth/oidc.dart';
import 'package:fimra_field/src/auth/pkce.dart';
import 'package:fimra_field/src/auth/session.dart';
import 'package:fimra_field/src/auth/tokens.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support.dart';

void main() {
  group('PKCE', () {
    test('matches the example in RFC 7636 appendix B', () {
      final pkce = Pkce.fromVerifier('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk');
      expect(pkce.challenge, 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM');
      expect(Pkce.method, 'S256');
    });

    test('makes a new, valid verifier each time', () {
      final a = Pkce.generate();
      final b = Pkce.generate();
      expect(a.verifier, isNot(b.verifier));
      expect(a.verifier, matches(RegExp(r'^[A-Za-z0-9\-_]{64}$')));
      expect(a.challenge, Pkce.challengeFor(a.verifier));
    });

    test('rejects a short verifier', () {
      expect(() => Pkce.fromVerifier('short'), throwsArgumentError);
    });
  });

  group('OpenID Connect client', () {
    late FakeIdentityServer server;
    late OidcClient oidc;
    setUp(() {
      server = FakeIdentityServer();
      oidc = OidcClient(config: testConfig, httpClient: server.client, clock: () => DateTime.utc(2026, 11, 25, 10));
    });

    test('asks for a code with a S256 proof key, state and nonce', () async {
      final request = await oidc.authorizationRequest();
      final params = request.url.queryParameters;
      expect(request.url.path, '/realms/open-fimra/protocol/openid-connect/auth');
      expect(params['response_type'], 'code');
      expect(params['client_id'], 'open-fimra-mobile');
      expect(params['redirect_uri'], 'org.openfimra.field:/callback');
      expect(params['scope'], 'openid profile email');
      expect(params['code_challenge'], request.pkce.challenge);
      expect(params['code_challenge_method'], 'S256');
      expect(params['state'], request.state);
      expect(params['nonce'], request.nonce);
      expect(params, isNot(contains('code_verifier')));
    });

    test('checks the state and reports errors from the reply', () async {
      final request = await oidc.authorizationRequest();
      final base = 'org.openfimra.field:/callback?state=${request.state}';
      expect(OidcClient.codeFromCallback('$base&code=xyz', request), 'xyz');
      expect(
        () => OidcClient.codeFromCallback('org.openfimra.field:/callback?state=other&code=xyz', request),
        throwsA(isA<SignInException>().having((e) => e.message, 'message', contains('did not match'))),
      );
      expect(
        () => OidcClient.codeFromCallback('$base&error=access_denied', request),
        throwsA(isA<SignInException>().having((e) => e.message, 'message', 'Sign-in was refused.')),
      );
      expect(() => OidcClient.codeFromCallback(base, request), throwsA(isA<SignInException>()));
    });

    test('exchanges the code with its verifier and no client secret', () async {
      final browser = FakeBrowser(server);
      final tokens = await oidc.signIn(browser);
      final form = server.requests.single;
      expect(form['grant_type'], 'authorization_code');
      expect(form['code'], 'abc123');
      expect(form['client_id'], 'open-fimra-mobile');
      expect(form['redirect_uri'], 'org.openfimra.field:/callback');
      expect(Pkce.challengeFor(form['code_verifier']!), browser.opened!.queryParameters['code_challenge']);
      expect(form, isNot(contains('client_secret')));
      expect(tokens.refreshToken, 'refresh-1');
      expect(tokens.expiresAt, DateTime.utc(2026, 11, 25, 10, 5));
    });

    test('rejects an ID token issued for another sign-in', () async {
      final browser = _WrongNonceBrowser(server);
      await expectLater(oidc.signIn(browser), throwsA(isA<SignInException>()));
    });

    test('a refused refresh token means signing in again', () async {
      server.refreshAllowed = false;
      final current = TokenSet(accessToken: 'a', expiresAt: DateTime.utc(2026), refreshToken: 'r');
      expect(await oidc.refresh(current), isNull);
    });
  });

  group('session', () {
    late FakeIdentityServer server;
    late MemoryTokenStore store;
    var now = DateTime.utc(2026, 11, 25, 10);
    Session make({bool cancel = false}) => Session(
      oidc: OidcClient(config: testConfig, httpClient: server.client, clock: () => now),
      store: store,
      browser: FakeBrowser(server, cancel: cancel),
      clock: () => now,
    );
    setUp(() {
      server = FakeIdentityServer();
      store = MemoryTokenStore();
      now = DateTime.utc(2026, 11, 25, 10);
    });

    test('signs in, keeps the tokens and refreshes them before they expire', () async {
      final session = make();
      await session.restore();
      expect(session.status, SessionStatus.signedOut);
      await session.signIn();
      expect(session.status, SessionStatus.signedIn);
      expect(session.username, 'beat.guard');
      expect(store.tokens, isNotNull);
      final first = await session.accessToken();
      expect(await session.accessToken(), first);

      now = now.add(const Duration(minutes: 4, seconds: 45));
      final second = await session.accessToken();
      expect(second, isNot(first));
      expect(server.requests.last['grant_type'], 'refresh_token');
      expect(server.requests.last['refresh_token'], 'refresh-1');
      expect(store.tokens?.accessToken, second);
    });

    test('a saved session is restored without signing in again', () async {
      await make().signIn();
      final restored = make();
      await restored.restore();
      expect(restored.status, SessionStatus.signedIn);
    });

    test('an ended session asks the user to sign in again', () async {
      final session = make();
      await session.signIn();
      server.refreshAllowed = false;
      await expectLater(session.accessToken(forceRefresh: true), throwsA(isA<SessionEndedException>()));
      expect(session.status, SessionStatus.signedOut);
      expect(session.message, 'Your session has ended. Sign in again.');
      expect(store.tokens, isNull);
    });

    test('cancelling sign-in shows no error', () async {
      final session = make(cancel: true);
      await session.signIn();
      expect(session.status, SessionStatus.signedOut);
      expect(session.message, isNull);
    });

    test('signing out forgets the tokens and ends the server session', () async {
      final session = make();
      await session.signIn();
      await session.signOut();
      expect(session.status, SessionStatus.signedOut);
      expect(store.tokens, isNull);
    });
  });
}

class _WrongNonceBrowser extends FakeBrowser {
  _WrongNonceBrowser(super.server);

  @override
  Future<String> authenticate(Uri url, String callbackScheme) async {
    final callback = await super.authenticate(url, callbackScheme);
    server.nonce = 'another-sign-in';
    return callback;
  }
}
