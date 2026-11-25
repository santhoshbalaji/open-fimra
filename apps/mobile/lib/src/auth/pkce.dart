import 'dart:convert';
import 'dart:math';

import 'package:crypto/crypto.dart';

/// A proof key for one sign-in (RFC 7636), with the S256 method.
class Pkce {
  Pkce._(this.verifier) : challenge = challengeFor(verifier);

  /// Makes a new random verifier of 64 characters.
  factory Pkce.generate([Random? random]) => Pkce._(randomToken(48, random));

  /// For tests: a known verifier.
  factory Pkce.fromVerifier(String verifier) {
    if (!RegExp(r'^[A-Za-z0-9\-._~]{43,128}$').hasMatch(verifier)) {
      throw ArgumentError.value(verifier, 'verifier', 'must be 43 to 128 unreserved characters');
    }
    return Pkce._(verifier);
  }

  final String verifier;
  final String challenge;
  static const method = 'S256';

  static String challengeFor(String verifier) => _base64Url(sha256.convert(ascii.encode(verifier)).bytes);
}

/// A random URL-safe string from [bytes] random bytes.
String randomToken([int bytes = 32, Random? random]) {
  final source = random ?? Random.secure();
  return _base64Url(List<int>.generate(bytes, (_) => source.nextInt(256)));
}

String _base64Url(List<int> bytes) => base64Url.encode(bytes).replaceAll('=', '');
