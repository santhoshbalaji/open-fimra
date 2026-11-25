import 'package:maplibre_gl/maplibre_gl.dart';

/// Nothing to set up on Android.
void configureMapLibrary() {}

/// Tile addresses are used as they are.
String mapTileUrl(String url) => url;

/// Sends the token with every map request. The style only loads tiles from
/// the core API, so the token goes nowhere else.
Future<void> setMapToken(String? token) =>
    setHttpHeaders(token == null ? const {} : {'Authorization': 'Bearer $token'});
