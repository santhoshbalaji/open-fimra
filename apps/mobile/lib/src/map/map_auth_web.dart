import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:maplibre_gl/maplibre_gl.dart';

/// web/index.html loads a self-hosted copy of MapLibre GL JS, so the browser
/// build calls no outside service.
void configureMapLibrary() {
  MapLibreMap.webLibrarySource = const MapLibreJsSource.preloaded();
}

/// Sends tile requests through the `fimra://` handler in web/index.html.
String mapTileUrl(String url) => 'fimra://$url';

Future<void> setMapToken(String? token) async {
  globalContext.setProperty('fimraTileToken'.toJS, token?.toJS);
}
