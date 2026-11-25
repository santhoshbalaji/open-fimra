// Gives the map the user's token. On Android, MapLibre adds the header to
// its own requests. In the browser test build, MapLibre GL JS fetches tiles
// through a small protocol handler in web/index.html that adds the header.
export 'map_auth_io.dart' if (dart.library.js_interop) 'map_auth_web.dart';
